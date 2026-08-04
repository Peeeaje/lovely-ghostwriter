package reviewer

import (
	"context"
	"strings"
	"testing"

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
	pr := state.PullRequest{HeadSHA: "abc123"}
	submission := submission(result, "codex-auto-review", "alice", pr, 42)
	if submission.CommitID != "abc123" || len(submission.Comments) != 1 {
		t.Fatalf("submission = %+v", submission)
	}
	if !strings.Contains(submission.Body, "head=abc123 run=42") {
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
	eligible, err := runner.EligibleAutomatic(context.Background(), cfg.Repositories[0], target)
	if err != nil || eligible {
		t.Fatalf("EligibleAutomatic() eligible=%v err=%v", eligible, err)
	}
}
