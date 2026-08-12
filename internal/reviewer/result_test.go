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
  "patched_findings": [],
  "findings": [
    {"severity":"blocking","body":"Fix this.","path":"main.go","line":12,"side":"RIGHT"},
    {"severity":"caution","body":"Confirm the contract.","path":"","line":0,"side":"RIGHT"}
  ]
}`))
	if err != nil {
		t.Fatal(err)
	}
	pr := state.PullRequest{HeadSHA: "abc123", BaseBranch: "main", BaseSHA: "base123"}
	submission := submission(result, "codex-auto-review", "alice", pr, 42, "")
	if submission.CommitID != "abc123" || len(submission.Comments) != 1 {
		t.Fatalf("submission = %+v", submission)
	}
	if !strings.Contains(submission.Body, "head=abc123 base_branch=main base=base123 run=42") {
		t.Fatalf("submission body lacks run marker: %s", submission.Body)
	}
	if strings.Contains(submission.Body, result.Summary) || !strings.Contains(submission.Body, "未解決の指摘事項を投稿しました") {
		t.Fatalf("submission body exposed the internal summary: %s", submission.Body)
	}
}

func TestSubmissionWithoutFindingsFocusesOnCodeConclusion(t *testing.T) {
	result := Result{
		Decision: "NO_BLOCKING_FINDINGS",
		Summary:  "独立Review役も同じ結論です。全テストが合格し、artifactへ保存してDockerを削除しました。ブラウザ確認は未実施です。",
	}
	submission := submission(result, "codex-auto-review", "alice", state.PullRequest{HeadSHA: "abc123", BaseBranch: "main", BaseSHA: "base123"}, 42, "")
	if !strings.Contains(submission.Body, "最終状態のコード差分と関連コードを確認した範囲では、未解決の指摘事項はありません。") {
		t.Fatalf("submission body lacks the code conclusion: %s", submission.Body)
	}
	for _, internalDetail := range []string{"独立Review役", "全テスト", "artifact", "Docker", "ブラウザ確認"} {
		if strings.Contains(submission.Body, internalDetail) {
			t.Fatalf("submission body contains internal detail %q: %s", internalDetail, submission.Body)
		}
	}
}

func TestSubmissionWithPatchReportsProposedUntilMerged(t *testing.T) {
	result := Result{
		Decision: "NO_BLOCKING_FINDINGS",
		Summary:  "The patched worktree has no unresolved findings.",
		PatchedFindings: []PatchedFinding{{
			Problem: "Required CI can pass when change detection fails.",
			Fix:     "Run the required job and fail it when change detection does not succeed.",
		}},
	}
	submission := submission(result, "codex-auto-review", "alice", state.PullRequest{HeadSHA: "abc123", BaseBranch: "main", BaseSHA: "base123"}, 42, "https://example.com/patch/1")
	for _, expected := range []string{
		"自動レビュー判定: PATCH_PROPOSED",
		"- blocking: 1",
		"元PRへ取り込まれるまで、対象headでは未解消です。",
		"**問題**: Required CI can pass when change detection fails.",
		"**修正**: Run the required job and fail it when change detection does not succeed.",
		"Patch PR: https://example.com/patch/1",
	} {
		if !strings.Contains(submission.Body, expected) {
			t.Fatalf("submission body does not contain %q: %s", expected, submission.Body)
		}
	}
	for _, misleading := range []string{"自動レビュー判定: NO_BLOCKING_FINDINGS", "- blocking: 0", result.Summary} {
		if strings.Contains(submission.Body, misleading) {
			t.Fatalf("submission body contains misleading result %q: %s", misleading, submission.Body)
		}
	}
}

func TestSubmissionWithPatchReportsRemainingFindings(t *testing.T) {
	result := Result{
		Decision: "CAUTION",
		Summary:  "One caution remains after patching.",
		Findings: []Finding{{Severity: "caution", Body: "Confirm this.", Path: "main.go", Line: 12, Side: "RIGHT"}},
		PatchedFindings: []PatchedFinding{{
			Problem: "A required check could be skipped.",
			Fix:     "Make the required check fail closed.",
		}},
	}
	submission := submission(result, "codex-auto-review", "alice", state.PullRequest{HeadSHA: "abc123", BaseBranch: "main", BaseSHA: "base123"}, 42, "https://example.com/patch/1")
	if strings.Contains(submission.Body, "Patch適用後も残るfinding") || !strings.Contains(submission.Body, "- caution: 1") {
		t.Fatalf("submission body lacks remaining finding counts: %s", submission.Body)
	}
}

func TestReadResultRejectsIncompletePatchedFinding(t *testing.T) {
	_, err := readResult([]byte(`{
  "decision": "NO_BLOCKING_FINDINGS",
  "summary": "The patch resolves the issue.",
  "findings": [],
  "patched_findings": [{"problem":"Required CI can be skipped.","fix":""}]
}`))
	if err == nil || !strings.Contains(err.Error(), "empty fix") {
		t.Fatalf("readResult() error = %v", err)
	}
}

func TestPatchPullRequestBodyExplainsProblemAndFix(t *testing.T) {
	body := patchPullRequestBody(state.PullRequest{
		Number: 123, URL: "https://example.com/pull/123", HeadSHA: "abc123",
	}, []PatchedFinding{{
		Problem: "Required CI can pass when change detection fails.",
		Fix:     "Fail the required job when change detection fails.",
	}})
	for _, expected := range []string{
		"元PR: https://example.com/pull/123",
		"対象head: `abc123`",
		"## 問題",
		"Required CI can pass when change detection fails.",
		"## 修正内容",
		"Fail the required job when change detection fails.",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("patch pull request body does not contain %q: %s", expected, body)
		}
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

func TestAutomaticEligibilityDoesNotTreatBaseChangeAsUpdate(t *testing.T) {
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
	if err != nil || !eligible {
		t.Fatalf("automaticEligible() eligible=%v err=%v", eligible, err)
	}
}

func TestRecoveredReviewPostedUsesPreviousRunMarker(t *testing.T) {
	cfg := config.Default()
	runner := Runner{Config: cfg, GitHub: fakeGitHub{pr: gh.PullRequest{
		Reviews: []gh.Review{{Author: gh.Actor{Login: "reviewer"}, Body: "<!-- codex-auto-review reviewer=reviewer head=head base_branch=main base=base run=41 -->"}},
	}}}
	posted, err := runner.RecoveredReviewPosted(context.Background(), cfg.Repositories[0], state.PullRequest{HeadSHA: "head", BaseBranch: "main", BaseSHA: "base", RecoveryRunID: 41})
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
	for _, expected := range []string{"review -> patchable blocking", "最大2回", "patched_findings", "problemにコード上の具体的な問題と影響", "fixに実施した修正", "/tmp/previous", "現在のheadで必ず再検証"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("Prompt() does not contain %q: %s", expected, prompt)
		}
	}
}

func TestPromptKeepsInternalExecutionDetailsOutOfPublicSummary(t *testing.T) {
	prompt := Prompt(config.ReviewConfig{}, config.PatchConfig{}, state.PullRequest{}, "/tmp/worktree", "/tmp/artifacts", "")
	for _, expected := range []string{"summaryはコード上の結論だけ", "内部実行情報は含めない", "test未実施や動作未確認だけをfindingにせず", "home directoryやその親を対象にしたrg/find/grep/ls", "Desktop/Documents/Downloadsの直接参照は禁止"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("Prompt() does not contain %q: %s", expected, prompt)
		}
	}
}

func TestReviewCommandIgnoresInteractiveUserConfiguration(t *testing.T) {
	args := reviewCommandArgs(reviewCommand{
		Model:           "review-model",
		ReasoningEffort: "high",
		Sandbox:         "workspace-write",
		ExtraArgs:       []string{"--add-dir", "/docker-socket"},
	}, "/artifacts", "/worktree", "/schema.json", "/result.json")
	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"--ignore-user-config",
		"--enable multi_agent",
		"--enable child_agents_md",
		"--model review-model",
		`model_reasoning_effort="high"`,
		"--sandbox workspace-write",
		"--add-dir /docker-socket",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("review command does not contain %q: %s", expected, joined)
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
