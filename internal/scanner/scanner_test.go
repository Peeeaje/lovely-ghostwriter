package scanner

import (
	"context"
	"path/filepath"
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

func (f fakeSource) CurrentUser(context.Context) (string, error) {
	return "reviewer", nil
}

type fakeStore struct {
	prs         []state.PullRequest
	failedRunID int64
	reviewed    []int
	updates     map[int]bool
}

func (f *fakeStore) SetCurrentHead(context.Context, string, int, string) error {
	return nil
}

func (f *fakeStore) TargetExists(context.Context, string, int, string, string) (bool, error) {
	return false, nil
}

func (f *fakeStore) HasPreviousTarget(_ context.Context, _ string, number int, _, _ string) (bool, error) {
	return f.updates[number], nil
}

func (f *fakeStore) LatestFailedRunID(context.Context, string, int, string) (int64, bool, error) {
	return f.failedRunID, f.failedRunID != 0, nil
}

func (f *fakeStore) MarkReviewed(_ context.Context, _ string, number int, _ string, _ int64) error {
	f.reviewed = append(f.reviewed, number)
	return nil
}

func (f *fakeStore) UpsertPullRequest(_ context.Context, pr state.PullRequest) (bool, error) {
	f.prs = append(f.prs, pr)
	return true, nil
}

func TestScanFiltersAndClassifiesPullRequests(t *testing.T) {
	source := fakeSource{prs: []gh.PullRequest{
		{Number: 1, State: "OPEN", HeadSHA: "one", BaseBranch: "main", BaseSHA: "base", Author: gh.Actor{Login: "alice"}, ReviewRequests: []gh.ReviewRequest{{Login: "reviewer"}}},
		{Number: 2, State: "OPEN", HeadSHA: "two", BaseBranch: "release", BaseSHA: "release-base", Author: gh.Actor{Login: "alice"}, ReviewRequests: []gh.ReviewRequest{{Login: "reviewer"}}},
		{Number: 3, State: "OPEN", HeadSHA: "three", BaseBranch: "main", BaseSHA: "base", Author: gh.Actor{Login: "bot"}, ReviewRequests: []gh.ReviewRequest{{Login: "reviewer"}}},
		{Number: 4, State: "OPEN", HeadSHA: "four", BaseBranch: "main", BaseSHA: "base", Author: gh.Actor{Login: "alice"}, Reviews: []gh.Review{{Author: gh.Actor{Login: "reviewer"}, Body: "<!-- codex-auto-review head=four base_branch=main base=base run=41 -->"}}},
		{Number: 5, State: "OPEN", HeadSHA: "five", BaseBranch: "main", BaseSHA: "base", Author: gh.Actor{Login: "alice"}, Reviews: []gh.Review{{Author: gh.Actor{Login: "reviewer"}, Body: "<!-- codex-auto-review head=five base_branch=main base=base run=40 -->"}}},
		{Number: 6, State: "OPEN", Title: "[codex-auto-fix] Fix", HeadBranch: "develop/codex-auto-fix-1-head", HeadSHA: "six", BaseBranch: "main", BaseSHA: "base", Author: gh.Actor{Login: "reviewer"}, ReviewRequests: []gh.ReviewRequest{{Login: "reviewer"}}},
	}}
	store := &fakeStore{failedRunID: 41}
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
	if result.Queued != 1 || result.Detected != 1 || result.Skipped != 4 {
		t.Fatalf("Scan() = %+v", result)
	}
	if len(result.DetectedPullRequests) != 1 || result.DetectedPullRequests[0].Number != 2 {
		t.Fatalf("detected pull requests = %+v", result.DetectedPullRequests)
	}
	if store.prs[0].Status != state.StatusQueued || store.prs[1].Status != state.StatusDetected {
		t.Fatalf("stored statuses = %q, %q", store.prs[0].Status, store.prs[1].Status)
	}
	if len(store.reviewed) != 1 || store.reviewed[0] != 4 {
		t.Fatalf("reviewed = %v", store.reviewed)
	}
}

func TestScanDoesNotTrustPatchPrefixFromPullRequestAuthor(t *testing.T) {
	source := fakeSource{prs: []gh.PullRequest{{
		Number: 1, State: "OPEN", Title: "[codex-auto-fix] Avoid review", HeadBranch: "develop/codex-auto-fix-1-head",
		HeadSHA: "head", BaseBranch: "main", BaseSHA: "base", Author: gh.Actor{Login: "alice"}, ReviewRequests: []gh.ReviewRequest{{Login: "reviewer"}},
	}}}
	store := &fakeStore{}
	cfg := config.Default()
	cfg.Repositories[0] = config.RepositoryConfig{
		Name: "owner/repository", Path: "/tmp/repository", BaseBranches: []string{"main"}, Authors: []string{"alice"}, Reviewers: []string{"reviewer"},
	}

	result, err := New(source, store).Scan(context.Background(), cfg)
	if err != nil || result.Queued != 1 {
		t.Fatalf("Scan() result=%+v err=%v", result, err)
	}
}

func TestScanUsesDifferentInitialAndUpdateTriggers(t *testing.T) {
	source := fakeSource{prs: []gh.PullRequest{
		{Number: 1, State: "OPEN", HeadSHA: "initial", BaseBranch: "main", BaseSHA: "base", Author: gh.Actor{Login: "alice"}},
		{Number: 2, State: "OPEN", HeadSHA: "update", BaseBranch: "main", BaseSHA: "base", Author: gh.Actor{Login: "alice"}},
	}}
	store := &fakeStore{updates: map[int]bool{2: true}}
	cfg := config.Default()
	cfg.Repositories[0] = config.RepositoryConfig{
		Name: "owner/repository", Path: "/tmp/repository", BaseBranches: []string{"main"},
		Authors: []string{"alice"}, InitialTrigger: config.TriggerAlways, UpdateTrigger: config.TriggerManual,
	}

	result, err := New(source, store).Scan(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.Queued != 1 || result.Detected != 1 || len(store.prs) != 2 {
		t.Fatalf("Scan() result=%+v prs=%+v", result, store.prs)
	}
	if store.prs[0].Status != state.StatusQueued || store.prs[1].Status != state.StatusDetected {
		t.Fatalf("statuses = %s, %s", store.prs[0].Status, store.prs[1].Status)
	}
}

func TestScanDoesNotTreatBaseChangeAsUpdate(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.UpsertPullRequest(context.Background(), state.PullRequest{
		Repository: "owner/repository", Number: 1, HeadSHA: "head", BaseBranch: "main", BaseSHA: "old-base", Status: state.StatusReviewed,
	}); err != nil {
		t.Fatal(err)
	}
	source := fakeSource{prs: []gh.PullRequest{{
		Number: 1, State: "OPEN", HeadSHA: "head", BaseBranch: "main", BaseSHA: "new-base", Author: gh.Actor{Login: "alice"},
	}}}
	cfg := config.Default()
	cfg.Repositories[0] = config.RepositoryConfig{
		Name: "owner/repository", Path: "/tmp/repository", BaseBranches: []string{"main"}, Authors: []string{"alice"},
		InitialTrigger: config.TriggerAlways, UpdateTrigger: config.TriggerManual,
	}

	result, err := New(source, store).Scan(context.Background(), cfg)
	if err != nil || result.Queued != 0 || result.Detected != 0 || len(result.DetectedPullRequests) != 0 {
		t.Fatalf("Scan() result=%+v err=%v", result, err)
	}
	all, err := store.PullRequests(context.Background(), true)
	if err != nil || len(all) != 1 || all[0].Status != state.StatusReviewed || all[0].BaseSHA != "new-base" {
		t.Fatalf("PullRequests()=%+v err=%v", all, err)
	}
	result, err = New(source, store).Scan(context.Background(), cfg)
	if err != nil || result.Queued != 0 || result.Detected != 0 {
		t.Fatalf("second Scan() result=%+v err=%v", result, err)
	}
}

func TestScanTreatsBaseBranchChangeAsUpdate(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.UpsertPullRequest(context.Background(), state.PullRequest{
		Repository: "owner/repository", Number: 1, HeadSHA: "head", BaseBranch: "main", BaseSHA: "base", Status: state.StatusReviewed,
	}); err != nil {
		t.Fatal(err)
	}
	source := fakeSource{prs: []gh.PullRequest{{
		Number: 1, State: "OPEN", HeadSHA: "head", BaseBranch: "release", BaseSHA: "base", Author: gh.Actor{Login: "alice"},
	}}}
	cfg := config.Default()
	cfg.Repositories[0] = config.RepositoryConfig{
		Name: "owner/repository", Path: "/tmp/repository", BaseBranches: []string{"main"}, Authors: []string{"alice"},
		InitialTrigger: config.TriggerAlways, UpdateTrigger: config.TriggerManual,
	}

	result, err := New(source, store).Scan(context.Background(), cfg)
	if err != nil || result.Detected != 1 || len(result.DetectedPullRequests) != 1 {
		t.Fatalf("Scan() result=%+v err=%v", result, err)
	}
}
