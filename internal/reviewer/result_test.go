package reviewer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Peeeaje/lovely-ghostwriter/internal/config"
	gh "github.com/Peeeaje/lovely-ghostwriter/internal/github"
	"github.com/Peeeaje/lovely-ghostwriter/internal/state"
)

type fakeGitHub struct {
	pr gh.PullRequest
}

func (f fakeGitHub) CurrentUser(context.Context) (string, error) { return "reviewer", nil }
func (f fakeGitHub) PullRequest(context.Context, string, int) (gh.PullRequest, error) {
	return f.pr, nil
}
func (f fakeGitHub) SubmitReview(context.Context, string, int, gh.ReviewSubmission) (gh.Review, error) {
	return gh.Review{}, nil
}

func TestReadResultAndSubmission(t *testing.T) {
	result, err := readResult([]byte(`{
  "decision": "BLOCKING",
  "summary": "One issue was found.",
  "findings": [
    {"severity":"blocking","body":"Fix this.","path":"main.go","line":12,"side":"RIGHT"},
    {"severity":"caution","body":"Confirm the contract.","path":"","line":0,"side":"RIGHT"}
  ]
}`))
	if err != nil {
		t.Fatal(err)
	}
	pr := state.PullRequest{HeadSHA: "abc123", BaseSHA: "base123"}
	submission := submission(result, "codex-auto-review", "alice", pr, 42, "")
	if submission.CommitID != "abc123" || len(submission.Comments) != 1 {
		t.Fatalf("submission = %+v", submission)
	}
	if !strings.Contains(submission.Body, "head=abc123 base=base123 run=42") {
		t.Fatalf("submission body lacks run marker: %s", submission.Body)
	}
}

func TestCurrentTarget(t *testing.T) {
	target := state.PullRequest{HeadSHA: "head", BaseSHA: "base", BaseBranch: "main"}
	if err := currentTarget(gh.PullRequest{State: "OPEN", HeadSHA: "head", BaseSHA: "base", BaseBranch: "main"}, target); err != nil {
		t.Fatal(err)
	}
	if err := currentTarget(gh.PullRequest{State: "OPEN", HeadSHA: "new-head", BaseSHA: "base", BaseBranch: "main"}, target); err == nil {
		t.Fatal("currentTarget() accepted a changed head")
	}
}

func TestEligibleAutomaticRejectsRetargetedBaseBranch(t *testing.T) {
	cfg := config.Default()
	runner := Runner{Config: cfg, GitHub: fakeGitHub{pr: gh.PullRequest{
		State: "OPEN", HeadSHA: "head", BaseSHA: "base", BaseBranch: "release",
		ReviewRequests: []gh.ReviewRequest{{Login: "reviewer"}},
	}}}
	target := state.PullRequest{Repository: "owner/repository", Number: 1, HeadSHA: "head", BaseSHA: "base", BaseBranch: "main"}
	_, eligible, err := runner.PrepareAutomatic(context.Background(), cfg.Repositories[0], target)
	if err != nil || eligible {
		t.Fatalf("PrepareAutomatic() eligible=%v err=%v", eligible, err)
	}
}

func TestPrepareAutomaticHydratesLegacyTarget(t *testing.T) {
	cfg := config.Default()
	cfg.Repositories[0].Reviewers = []string{"reviewer"}
	runner := Runner{Config: cfg, GitHub: fakeGitHub{pr: gh.PullRequest{
		State: "OPEN", HeadSHA: "head", BaseSHA: "base", BaseBranch: "main",
		ReviewRequests: []gh.ReviewRequest{{Login: "reviewer"}},
	}}}
	target := state.PullRequest{Repository: "owner/repository", Number: 1, HeadSHA: "head"}
	prepared, eligible, err := runner.PrepareAutomatic(context.Background(), cfg.Repositories[0], target)
	if err != nil || !eligible || prepared.BaseSHA != "base" || prepared.BaseBranch != "main" {
		t.Fatalf("PrepareAutomatic() target=%+v eligible=%v err=%v", prepared, eligible, err)
	}
}

func TestAutomaticEligibilityTreatsBaseChangeAsUpdate(t *testing.T) {
	cfg := config.Default()
	cfg.Repositories[0].Reviewers = []string{"reviewer"}
	cfg.Repositories[0].InitialTrigger = config.TriggerAlways
	cfg.Repositories[0].UpdateTrigger = config.TriggerManual
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.UpsertPullRequest(context.Background(), state.PullRequest{
		Repository: "owner/repository", Number: 1, HeadSHA: "old", Title: "Change", BaseBranch: "main", BaseSHA: "base", Status: state.StatusReviewed,
	}); err != nil {
		t.Fatal(err)
	}
	runner := Runner{Config: cfg, Store: store, GitHub: fakeGitHub{}}
	eligible, err := runner.automaticEligible(context.Background(), cfg.Repositories[0], gh.PullRequest{
		Number: 1, State: "OPEN", HeadSHA: "old", BaseBranch: "main", BaseSHA: "new-base", Author: gh.Actor{Login: "alice"}, ReviewRequests: []gh.ReviewRequest{{Login: "reviewer"}},
	})
	if err != nil || eligible {
		t.Fatalf("automaticEligible() eligible=%v err=%v", eligible, err)
	}
}

func TestRecoveredReviewPostedUsesPreviousRunMarker(t *testing.T) {
	cfg := config.Default()
	runner := Runner{Config: cfg, GitHub: fakeGitHub{pr: gh.PullRequest{
		Reviews: []gh.Review{{Author: gh.Actor{Login: "reviewer"}, Body: "<!-- codex-auto-review reviewer=reviewer head=head base=base run=41 -->"}},
	}}}
	posted, err := runner.RecoveredReviewPosted(context.Background(), cfg.Repositories[0], state.PullRequest{HeadSHA: "head", BaseSHA: "base", RecoveryRunID: 41})
	if err != nil || !posted {
		t.Fatalf("RecoveredReviewPosted() posted=%v err=%v", posted, err)
	}
}

func TestPromptIncludesAdditionalInstructions(t *testing.T) {
	prompt := Prompt(config.ReviewConfig{Instructions: "Prioritize API compatibility."}, config.PatchConfig{}, state.PullRequest{}, "/tmp/worktree", "/tmp/artifacts", "")
	if !strings.Contains(prompt, "Prioritize API compatibility.") {
		t.Fatalf("Prompt() = %s", prompt)
	}
}

func TestPromptExplainsOptionalPatchOrchestration(t *testing.T) {
	prompt := Prompt(config.ReviewConfig{}, config.PatchConfig{Enabled: true, MaxIterations: 2}, state.PullRequest{}, "/tmp/worktree", "/tmp/artifacts", "/tmp/previous")
	for _, expected := range []string{"review -> patchable blocking", "最大2回", "/tmp/previous", "現在のheadで必ず再検証"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("Prompt() does not contain %q: %s", expected, prompt)
		}
	}
}

func TestPatchValidationRejectsChangedWorktreeAndRemoteHead(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	worktree := filepath.Join(root, "worktree")
	runGitTest(t, root, "init", "--bare", remote)
	runGitTest(t, root, "init", worktree)
	runGitTest(t, worktree, "config", "user.name", "Reviewer")
	runGitTest(t, worktree, "config", "user.email", "reviewer@example.com")
	if err := os.WriteFile(filepath.Join(worktree, "change.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, worktree, "add", "change.txt")
	runGitTest(t, worktree, "commit", "-m", "base")
	baseHead := runGitTest(t, worktree, "rev-parse", "HEAD")
	runGitTest(t, worktree, "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(worktree, "change.txt"), []byte("patch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, worktree, "add", "change.txt")
	runGitTest(t, worktree, "commit", "-m", "patch")
	patchHead := runGitTest(t, worktree, "rev-parse", "HEAD")
	patchTree := runGitTest(t, worktree, "show", "-s", "--format=%T", patchHead)
	runGitTest(t, worktree, "push", "origin", "HEAD:refs/heads/patch")

	if err := validatePatchWorktree(context.Background(), worktree, baseHead); err == nil {
		t.Fatal("validatePatchWorktree() accepted a changed HEAD")
	}
	if err := verifyRemotePatch(context.Background(), worktree, "patch", "refs/lovely-ghostwriter/test", patchHead, patchTree, baseHead); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "change.txt"), []byte("different\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, worktree, "add", "change.txt")
	runGitTest(t, worktree, "commit", "-m", "different")
	runGitTest(t, worktree, "push", "--force", "origin", "HEAD:refs/heads/patch")
	if err := verifyRemotePatch(context.Background(), worktree, "patch", "refs/lovely-ghostwriter/test", patchHead, patchTree, baseHead); err == nil {
		t.Fatal("verifyRemotePatch() accepted an updated remote branch")
	}
}

func runGitTest(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, output, err)
	}
	return strings.TrimSpace(string(output))
}

func TestClosePatchPullRequestHonorsContextDeadline(t *testing.T) {
	command := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexec /bin/sleep 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(command))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := closePatchPullRequest(ctx, "https://example.test/pull/1"); err == nil {
		t.Fatal("closePatchPullRequest() succeeded")
	}
	if time.Since(started) > time.Second {
		t.Fatal("closePatchPullRequest() did not honor context deadline")
	}
}
