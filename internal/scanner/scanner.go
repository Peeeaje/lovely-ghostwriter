package scanner

import (
	"context"
	"fmt"

	"github.com/Peeeaje/lovely-ghostwriter/internal/config"
	gh "github.com/Peeeaje/lovely-ghostwriter/internal/github"
	"github.com/Peeeaje/lovely-ghostwriter/internal/policy"
	"github.com/Peeeaje/lovely-ghostwriter/internal/state"
)

type PullRequestSource interface {
	OpenPullRequests(context.Context, string) ([]gh.PullRequest, error)
	CurrentUser(context.Context) (string, error)
}

type PullRequestStore interface {
	UpsertPullRequest(context.Context, state.PullRequest) (bool, error)
	HasPreviousRevision(context.Context, string, int, string, string) (bool, error)
	RevisionExists(context.Context, string, int, string, string) (bool, error)
	LatestFailedRunID(context.Context, string, int, string) (int64, bool, error)
	MarkReviewed(context.Context, string, int, string, int64) error
}

type Result struct {
	Queued               int
	Detected             int
	Skipped              int
	DetectedPullRequests []state.PullRequest
}

type Scanner struct {
	source PullRequestSource
	store  PullRequestStore
}

func New(source PullRequestSource, store PullRequestStore) *Scanner {
	return &Scanner{source: source, store: store}
}

func (s *Scanner) Scan(ctx context.Context, cfg config.Config) (Result, error) {
	var result Result
	reviewer, err := s.source.CurrentUser(ctx)
	if err != nil {
		return result, err
	}
	for _, repository := range cfg.Repositories {
		prs, err := s.source.OpenPullRequests(ctx, repository.Name)
		if err != nil {
			return result, err
		}
		for _, pr := range prs {
			review := repository.EffectiveReview(cfg.Review)
			if repository.PatchPullRequest(cfg.Patch, pr.Title, pr.HeadBranch, pr.Author.Login, reviewer, pr.IsCrossRepository) {
				result.Skipped++
				continue
			}
			if gh.HasMarker(pr, review.Marker, pr.HeadSHA, pr.BaseSHA, reviewer) {
				runID, failed, err := s.store.LatestFailedRunID(ctx, repository.Name, pr.Number, pr.HeadSHA)
				if err != nil {
					return result, err
				}
				if failed && gh.HasRunMarker(pr, review.Marker, pr.HeadSHA, pr.BaseSHA, reviewer, runID) {
					if err := s.store.MarkReviewed(ctx, repository.Name, pr.Number, pr.HeadSHA, runID); err != nil {
						return result, err
					}
				}
				result.Skipped++
				continue
			}
			if !policy.Candidate(repository, pr, review.Marker, reviewer) {
				result.Skipped++
				continue
			}
			trigger := repository.Trigger(false)
			if trigger != repository.Trigger(true) {
				isUpdate, err := s.store.HasPreviousRevision(ctx, repository.Name, pr.Number, pr.HeadSHA, pr.BaseSHA)
				if err != nil {
					return result, err
				}
				trigger = repository.Trigger(isUpdate)
			}

			status := state.StatusDetected
			if repository.AutoReviewBase(pr.BaseBranch) && policy.Automatic(repository, pr, review.Marker, reviewer, trigger) {
				status = state.StatusQueued
			}
			candidate := state.PullRequest{
				Repository: repository.Name,
				Number:     pr.Number,
				HeadSHA:    pr.HeadSHA,
				Title:      pr.Title,
				URL:        pr.URL,
				Author:     pr.Author.Login,
				BaseBranch: pr.BaseBranch,
				BaseSHA:    pr.BaseSHA,
				Status:     status,
			}
			knownRevision, err := s.store.RevisionExists(ctx, repository.Name, pr.Number, pr.HeadSHA, pr.BaseSHA)
			if err != nil {
				return result, err
			}
			created, err := s.store.UpsertPullRequest(ctx, candidate)
			if err != nil {
				return result, fmt.Errorf("save %s#%d: %w", repository.Name, pr.Number, err)
			}
			if !created && knownRevision {
				continue
			}
			if status == state.StatusQueued {
				result.Queued++
			} else {
				result.Detected++
				result.DetectedPullRequests = append(result.DetectedPullRequests, candidate)
			}
		}
	}
	return result, nil
}
