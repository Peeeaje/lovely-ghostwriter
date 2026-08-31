package policy

import (
	"slices"
	"sort"

	"github.com/Peeeaje/lovely-ghostwriter/internal/config"
	gh "github.com/Peeeaje/lovely-ghostwriter/internal/github"
)

func Candidate(repository config.RepositoryConfig, pr gh.PullRequest, marker, reviewer string) bool {
	if pr.State != "OPEN" || (pr.Draft && !repository.IncludeDrafts) {
		return false
	}
	if gh.HasMarker(pr, marker, pr.HeadSHA, pr.BaseBranch, reviewer) || slices.Contains(repository.ExcludeAuthors, pr.Author.Login) {
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
	return len(MatchingReviewRequestKeys(repository, pr)) > 0
}

func MatchingReviewRequestKeys(repository config.RepositoryConfig, pr gh.PullRequest) []string {
	keys := make(map[string]struct{})
	for _, request := range pr.ReviewRequests {
		if slices.Contains(repository.Reviewers, request.Login) {
			keys["user:"+request.Login] = struct{}{}
		}
		if slices.Contains(repository.Teams, request.Slug) || slices.Contains(repository.Teams, request.Name) {
			team := request.Slug
			if team == "" {
				team = request.Name
			}
			keys["team:"+team] = struct{}{}
		}
	}
	result := make([]string, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}
