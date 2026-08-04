package policy

import (
	"slices"

	"github.com/Peeeaje/lovely-ghostwriter/internal/config"
	gh "github.com/Peeeaje/lovely-ghostwriter/internal/github"
)

func Candidate(repository config.RepositoryConfig, pr gh.PullRequest, marker, reviewer string) bool {
	if pr.State != "OPEN" || (pr.Draft && !repository.IncludeDrafts) {
		return false
	}
	if gh.HasMarker(pr, marker, pr.HeadSHA, pr.BaseSHA, reviewer) || slices.Contains(repository.ExcludeAuthors, pr.Author.Login) {
		return false
	}
	if len(repository.Authors) > 0 && !slices.Contains(repository.Authors, pr.Author.Login) {
		return false
	}
	return true
}

func Automatic(repository config.RepositoryConfig, pr gh.PullRequest, marker, reviewer, trigger string) bool {
	if !Candidate(repository, pr, marker, reviewer) {
		return false
	}
	if trigger == config.TriggerAlways {
		return true
	}
	if trigger == config.TriggerManual {
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
