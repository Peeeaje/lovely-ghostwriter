package state

import (
	"context"
	"path/filepath"
	"testing"
)

func TestUpsertPullRequestIsIdempotent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	pr := PullRequest{
		Repository: "owner/repository",
		Number:     42,
		HeadSHA:    "abc123",
		Title:      "Change something",
		URL:        "https://github.com/owner/repository/pull/42",
		Author:     "alice",
		BaseBranch: "main",
		Status:     StatusQueued,
	}

	created, err := store.UpsertPullRequest(context.Background(), pr)
	if err != nil || !created {
		t.Fatalf("first UpsertPullRequest() = %v, %v", created, err)
	}
	created, err = store.UpsertPullRequest(context.Background(), pr)
	if err != nil || created {
		t.Fatalf("second UpsertPullRequest() = %v, %v", created, err)
	}

	counts, err := store.Counts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts[StatusQueued] != 1 {
		t.Fatalf("queued count = %d, want 1", counts[StatusQueued])
	}
}
