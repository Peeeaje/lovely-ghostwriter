package github

import "testing"

func TestHasMarker(t *testing.T) {
	pr := PullRequest{Reviews: []Review{{Body: "review\n<!-- codex-auto-review reviewer=alice head=abc123 -->"}}}
	if !HasMarker(pr, "codex-auto-review", "abc123") {
		t.Fatal("HasMarker() = false, want true")
	}
	if HasMarker(pr, "codex-auto-review", "different") {
		t.Fatal("HasMarker() matched a different head")
	}
}

func TestHasRunMarker(t *testing.T) {
	pr := PullRequest{Reviews: []Review{{Body: "<!-- codex-auto-review reviewer=alice head=abc123 run=42 -->"}}}
	if !HasRunMarker(pr, "codex-auto-review", "abc123", 42) {
		t.Fatal("HasRunMarker() = false, want true")
	}
	if HasRunMarker(pr, "codex-auto-review", "abc123", 43) {
		t.Fatal("HasRunMarker() matched a different run")
	}
}
