package reviewer

import (
	"strings"
	"testing"

	gh "github.com/Peeeaje/lovely-ghostwriter/internal/github"
	"github.com/Peeeaje/lovely-ghostwriter/internal/state"
)

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
	target := state.PullRequest{HeadSHA: "head", BaseSHA: "base"}
	if err := currentTarget(gh.PullRequest{State: "OPEN", HeadSHA: "head", BaseSHA: "base"}, target); err != nil {
		t.Fatal(err)
	}
	if err := currentTarget(gh.PullRequest{State: "OPEN", HeadSHA: "new-head", BaseSHA: "base"}, target); err == nil {
		t.Fatal("currentTarget() accepted a changed head")
	}
}
