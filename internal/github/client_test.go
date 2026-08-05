package github

import "testing"

func TestHasMarker(t *testing.T) {
	pr := PullRequest{Reviews: []Review{{Author: Actor{Login: "alice"}, Body: "review\n<!-- codex-auto-review reviewer=alice head=abc123 base_branch=main base=base123 -->"}}}
	if !HasMarker(pr, "codex-auto-review", "abc123", "main", "alice") {
		t.Fatal("HasMarker() = false, want true")
	}
	if HasMarker(pr, "codex-auto-review", "different", "main", "alice") {
		t.Fatal("HasMarker() matched a different head")
	}
	if HasMarker(pr, "codex-auto-review", "abc123", "release", "alice") {
		t.Fatal("HasMarker() matched a different base branch")
	}
}

func TestHasRunMarker(t *testing.T) {
	pr := PullRequest{Reviews: []Review{{Author: Actor{Login: "alice"}, Body: "<!-- codex-auto-review reviewer=alice head=abc123 base_branch=main base=base123 run=42 -->"}}}
	if !HasRunMarker(pr, "codex-auto-review", "abc123", "main", "alice", 42) {
		t.Fatal("HasRunMarker() = false, want true")
	}
	if HasRunMarker(pr, "codex-auto-review", "abc123", "main", "alice", 43) {
		t.Fatal("HasRunMarker() matched a different run")
	}
	if HasRunMarker(pr, "codex-auto-review", "abc123", "main", "mallory", 42) {
		t.Fatal("HasRunMarker() matched a different reviewer")
	}
}
