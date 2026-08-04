package scanner

import (
	"context"
	"testing"

	"github.com/Peeeaje/lovely-ghostwriter/internal/config"
	gh "github.com/Peeeaje/lovely-ghostwriter/internal/github"
	"github.com/Peeeaje/lovely-ghostwriter/internal/state"
)

type fakeSource struct {
	prs []gh.PullRequest
}

func (f fakeSource) OpenPullRequests(context.Context, string) ([]gh.PullRequest, error) {
	return f.prs, nil
}

type fakeStore struct {
	prs []state.PullRequest
}

func (f *fakeStore) UpsertPullRequest(_ context.Context, pr state.PullRequest) (bool, error) {
	f.prs = append(f.prs, pr)
	return true, nil
}

func TestScanFiltersAndClassifiesPullRequests(t *testing.T) {
	source := fakeSource{prs: []gh.PullRequest{
		{Number: 1, State: "OPEN", HeadSHA: "one", BaseBranch: "main", Author: gh.Actor{Login: "alice"}, ReviewRequests: []gh.ReviewRequest{{Login: "reviewer"}}},
		{Number: 2, State: "OPEN", HeadSHA: "two", BaseBranch: "release", Author: gh.Actor{Login: "alice"}, ReviewRequests: []gh.ReviewRequest{{Login: "reviewer"}}},
		{Number: 3, State: "OPEN", HeadSHA: "three", BaseBranch: "main", Author: gh.Actor{Login: "bot"}, ReviewRequests: []gh.ReviewRequest{{Login: "reviewer"}}},
	}}
	store := &fakeStore{}
	cfg := config.Default()
	cfg.Repositories[0] = config.RepositoryConfig{
		Name:           "owner/repository",
		Path:           "/tmp/repository",
		BaseBranches:   []string{"main"},
		Authors:        []string{"alice", "bot"},
		Reviewers:      []string{"reviewer"},
		ExcludeAuthors: []string{"bot"},
	}

	result, err := New(source, store).Scan(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.Queued != 1 || result.Detected != 1 || result.Skipped != 1 {
		t.Fatalf("Scan() = %+v", result)
	}
	if store.prs[0].Status != state.StatusQueued || store.prs[1].Status != state.StatusDetected {
		t.Fatalf("stored statuses = %q, %q", store.prs[0].Status, store.prs[1].Status)
	}
}
