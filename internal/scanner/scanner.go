package scanner

import (
	"context"
	"fmt"
	"slices"

	"github.com/Peeeaje/lovely-ghostwriter/internal/config"
	gh "github.com/Peeeaje/lovely-ghostwriter/internal/github"
	"github.com/Peeeaje/lovely-ghostwriter/internal/state"
)

type PullRequestSource interface {
	OpenPullRequests(context.Context, string) ([]gh.PullRequest, error)
}

type PullRequestStore interface {
	UpsertPullRequest(context.Context, state.PullRequest) (bool, error)
}

type Result struct {
	Queued   int
	Detected int
	Skipped  int
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
	for _, repository := range cfg.Repositories {
		prs, err := s.source.OpenPullRequests(ctx, repository.Name)
		if err != nil {
			return result, err
		}
		for _, pr := range prs {
			if !eligible(repository, pr) {
				result.Skipped++
				continue
			}

			status := state.StatusDetected
			if repository.AutoReviewBase(pr.BaseBranch) {
				status = state.StatusQueued
			}
			created, err := s.store.UpsertPullRequest(ctx, state.PullRequest{
				Repository: repository.Name,
				Number:     pr.Number,
				HeadSHA:    pr.HeadSHA,
				Title:      pr.Title,
				URL:        pr.URL,
				Author:     pr.Author.Login,
				BaseBranch: pr.BaseBranch,
				Status:     status,
			})
			if err != nil {
				return result, fmt.Errorf("save %s#%d: %w", repository.Name, pr.Number, err)
			}
			if !created {
				continue
			}
			if status == state.StatusQueued {
				result.Queued++
			} else {
				result.Detected++
			}
		}
	}
	return result, nil
}

func eligible(repository config.RepositoryConfig, pr gh.PullRequest) bool {
	if pr.State != "OPEN" || (pr.Draft && !repository.IncludeDrafts) {
		return false
	}
	if slices.Contains(repository.ExcludeAuthors, pr.Author.Login) {
		return false
	}
	if len(repository.Authors) > 0 && !slices.Contains(repository.Authors, pr.Author.Login) {
		return false
	}
	for _, request := range pr.ReviewRequests {
		if slices.Contains(repository.Reviewers, request.Login) ||
			slices.Contains(repository.Teams, request.Slug) ||
			slices.Contains(repository.Teams, request.Name) {
			return true
		}
	}
	return false
}
