package state

import (
	"context"
	"errors"
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
		BaseSHA:    "base123",
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

func TestClaimAndFinishRun(t *testing.T) {
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
		BaseSHA:    "base123",
		Status:     StatusQueued,
	}
	if _, err := store.UpsertPullRequest(context.Background(), pr); err != nil {
		t.Fatal(err)
	}

	claimed, run, ok, err := store.ClaimNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("ClaimNext() ok=%v err=%v", ok, err)
	}
	if claimed.Status != StatusRunning || run.Attempt != 1 {
		t.Fatalf("ClaimNext() pr=%+v run=%+v", claimed, run)
	}
	if _, _, ok, err := store.ClaimNext(context.Background()); err != nil || ok {
		t.Fatalf("second ClaimNext() ok=%v err=%v", ok, err)
	}
	if err := store.FinishRun(context.Background(), run, StatusCompleted, nil); err != nil {
		t.Fatal(err)
	}

	counts, err := store.Counts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts[StatusCompleted] != 1 {
		t.Fatalf("completed count = %d, want 1", counts[StatusCompleted])
	}
}

func TestEnqueuePromotesDetectedWithoutForce(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pr := PullRequest{
		Repository: "owner/repository", Number: 42, HeadSHA: "abc123", Title: "Change",
		URL: "https://github.com/owner/repository/pull/42", Author: "alice",
		BaseBranch: "release", BaseSHA: "base123", Status: StatusDetected,
	}
	if _, err := store.UpsertPullRequest(context.Background(), pr); err != nil {
		t.Fatal(err)
	}
	pr.Status = StatusQueued
	pr.Manual = true
	queued, err := store.Enqueue(context.Background(), pr, false)
	if err != nil || !queued {
		t.Fatalf("Enqueue() queued=%v err=%v", queued, err)
	}
	claimed, _, ok, err := store.ClaimNext(context.Background())
	if err != nil || !ok || claimed.Number != 42 || !claimed.Manual {
		t.Fatalf("ClaimNext() pr=%+v ok=%v err=%v", claimed, ok, err)
	}
}

func TestFailedEnqueueDoesNotMakeAutomaticQueueManual(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pr := PullRequest{
		Repository: "owner/repository", Number: 42, HeadSHA: "head", Title: "Change",
		URL: "https://github.com/owner/repository/pull/42", Author: "alice",
		BaseBranch: "main", BaseSHA: "base", Status: StatusQueued,
	}
	if _, err := store.UpsertPullRequest(context.Background(), pr); err != nil {
		t.Fatal(err)
	}
	pr.Manual = true
	if queued, err := store.Enqueue(context.Background(), pr, false); err != nil || queued {
		t.Fatalf("Enqueue() queued=%v err=%v", queued, err)
	}
	claimed, _, ok, err := store.ClaimNext(context.Background())
	if err != nil || !ok || claimed.Manual {
		t.Fatalf("ClaimNext() pr=%+v ok=%v err=%v", claimed, ok, err)
	}
}

func TestUpsertRequeuesAutomaticPullRequestWhenBaseChanges(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pr := PullRequest{
		Repository: "owner/repository", Number: 42, HeadSHA: "head", Title: "Change",
		URL: "https://github.com/owner/repository/pull/42", Author: "alice",
		BaseBranch: "main", BaseSHA: "old-base", Status: StatusQueued,
	}
	if _, err := store.UpsertPullRequest(context.Background(), pr); err != nil {
		t.Fatal(err)
	}
	claimed, run, ok, err := store.ClaimNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("ClaimNext() ok=%v err=%v", ok, err)
	}
	pr.BaseSHA = "new-base"
	if created, err := store.UpsertPullRequest(context.Background(), pr); err != nil || created {
		t.Fatalf("UpsertPullRequest() created=%v err=%v", created, err)
	}
	if err := store.FinishRun(context.Background(), run, StatusCanceled, nil); err != nil {
		t.Fatal(err)
	}
	if created, err := store.UpsertPullRequest(context.Background(), pr); err != nil || created {
		t.Fatalf("second UpsertPullRequest() created=%v err=%v", created, err)
	}
	requeued, _, ok, err := store.ClaimNext(context.Background())
	if err != nil || !ok || requeued.BaseSHA != "new-base" || claimed.HeadSHA != requeued.HeadSHA {
		t.Fatalf("requeued=%+v ok=%v err=%v", requeued, ok, err)
	}
}

func TestUpsertRequeuesAutomaticPullRequestWhenBaseBranchChanges(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pr := PullRequest{
		Repository: "owner/repository", Number: 42, HeadSHA: "head", Title: "Change",
		URL: "https://github.com/owner/repository/pull/42", Author: "alice",
		BaseBranch: "main", BaseSHA: "same-base", Status: StatusQueued,
	}
	if _, err := store.UpsertPullRequest(context.Background(), pr); err != nil {
		t.Fatal(err)
	}
	_, run, ok, err := store.ClaimNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("ClaimNext() ok=%v err=%v", ok, err)
	}
	pr.BaseBranch = "other-allowed-base"
	if _, err := store.UpsertPullRequest(context.Background(), pr); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(context.Background(), run, StatusCanceled, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertPullRequest(context.Background(), pr); err != nil {
		t.Fatal(err)
	}
	requeued, _, ok, err := store.ClaimNext(context.Background())
	if err != nil || !ok || requeued.BaseBranch != "other-allowed-base" {
		t.Fatalf("requeued=%+v ok=%v err=%v", requeued, ok, err)
	}
}

func TestUpsertQueuesDetectedPullRequestWhenBaseBecomesEligible(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pr := PullRequest{
		Repository: "owner/repository", Number: 42, HeadSHA: "head", Title: "Change",
		URL: "https://github.com/owner/repository/pull/42", Author: "alice",
		BaseBranch: "release", BaseSHA: "release-base", Status: StatusDetected,
	}
	if _, err := store.UpsertPullRequest(context.Background(), pr); err != nil {
		t.Fatal(err)
	}
	pr.BaseBranch = "main"
	pr.BaseSHA = "main-base"
	pr.Status = StatusQueued
	if _, err := store.UpsertPullRequest(context.Background(), pr); err != nil {
		t.Fatal(err)
	}
	queued, _, ok, err := store.ClaimNext(context.Background())
	if err != nil || !ok || queued.BaseBranch != "main" || queued.BaseSHA != "main-base" {
		t.Fatalf("ClaimNext() pr=%+v ok=%v err=%v", queued, ok, err)
	}
}

func TestUpsertRequeuesCanceledPullRequestWhenEligibleAgain(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pr := PullRequest{
		Repository: "owner/repository", Number: 42, HeadSHA: "head", Title: "Change",
		URL: "https://github.com/owner/repository/pull/42", Author: "alice",
		BaseBranch: "main", BaseSHA: "base", Status: StatusQueued,
	}
	if _, err := store.UpsertPullRequest(context.Background(), pr); err != nil {
		t.Fatal(err)
	}
	_, run, ok, err := store.ClaimNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("ClaimNext() ok=%v err=%v", ok, err)
	}
	if err := store.FinishRun(context.Background(), run, StatusCanceled, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertPullRequest(context.Background(), pr); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := store.ClaimNext(context.Background()); err != nil || !ok {
		t.Fatalf("requeued ClaimNext() ok=%v err=%v", ok, err)
	}
}

func TestMarkReviewedReconcilesFailedPullRequest(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pr := PullRequest{
		Repository: "owner/repository", Number: 42, HeadSHA: "head", Title: "Change",
		URL: "https://github.com/owner/repository/pull/42", Author: "alice",
		BaseBranch: "main", BaseSHA: "base", Status: StatusQueued,
	}
	if _, err := store.UpsertPullRequest(context.Background(), pr); err != nil {
		t.Fatal(err)
	}
	_, run, _, err := store.ClaimNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(context.Background(), run, StatusFailed, errors.New("confirmation failed")); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkReviewed(context.Background(), pr.Repository, pr.Number, pr.HeadSHA); err != nil {
		t.Fatal(err)
	}
	counts, err := store.Counts(context.Background())
	if err != nil || counts[StatusReviewed] != 1 || counts[StatusFailed] != 0 {
		t.Fatalf("Counts() = %v err=%v", counts, err)
	}
}

func TestRecoverInterruptedRequeuesRun(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pr := PullRequest{
		Repository: "owner/repository", Number: 42, HeadSHA: "head", Title: "Change",
		URL: "https://github.com/owner/repository/pull/42", Author: "alice",
		BaseBranch: "main", BaseSHA: "base", Status: StatusQueued,
	}
	if _, err := store.UpsertPullRequest(context.Background(), pr); err != nil {
		t.Fatal(err)
	}
	_, run, ok, err := store.ClaimNext(context.Background())
	if err != nil || !ok {
		t.Fatalf("ClaimNext() ok=%v err=%v", ok, err)
	}
	runs, err := store.RecoverInterrupted(context.Background())
	if err != nil || len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("RecoverInterrupted() runs=%+v err=%v", runs, err)
	}
	recovered, nextRun, ok, err := store.ClaimNext(context.Background())
	if err != nil || !ok || nextRun.Attempt != 2 || recovered.RecoveryRunID != run.ID {
		t.Fatalf("recovered ClaimNext() pr=%+v run=%+v ok=%v err=%v", recovered, nextRun, ok, err)
	}
}
