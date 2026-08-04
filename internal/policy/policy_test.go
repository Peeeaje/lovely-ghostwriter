package policy

import (
	"testing"

	"github.com/Peeeaje/lovely-ghostwriter/internal/config"
	gh "github.com/Peeeaje/lovely-ghostwriter/internal/github"
)

func TestAutomatic(t *testing.T) {
	repository := config.RepositoryConfig{Reviewers: []string{"reviewer"}}
	pr := gh.PullRequest{
		State: "OPEN", HeadSHA: "head", Author: gh.Actor{Login: "alice"},
		ReviewRequests: []gh.ReviewRequest{{Login: "reviewer"}},
	}
	if !Automatic(repository, pr, "marker", "reviewer", config.TriggerReviewRequest) {
		t.Fatal("Automatic() = false, want true")
	}
	pr.Reviews = []gh.Review{{Author: gh.Actor{Login: "reviewer"}, Body: "<!-- marker head=head -->"}}
	if Automatic(repository, pr, "marker", "reviewer", config.TriggerReviewRequest) {
		t.Fatal("Automatic() accepted a reviewed head")
	}
	pr.Reviews = nil
	pr.ReviewRequests = nil
	if !Automatic(repository, pr, "marker", "reviewer", config.TriggerAlways) {
		t.Fatal("Automatic() rejected always trigger")
	}
	if Automatic(repository, pr, "marker", "reviewer", config.TriggerManual) {
		t.Fatal("Automatic() accepted manual trigger")
	}
}
