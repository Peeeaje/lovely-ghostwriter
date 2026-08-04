package main

import (
	"reflect"
	"testing"

	"github.com/Peeeaje/lovely-ghostwriter/internal/config"
	"github.com/Peeeaje/lovely-ghostwriter/internal/state"
)

func TestActiveRepositoriesDeduplicatesHeadsAndPreservesAmbiguity(t *testing.T) {
	cfg := config.Config{Repositories: []config.RepositoryConfig{{Name: "owner/one"}, {Name: "owner/two"}}}
	pullRequests := []state.PullRequest{
		{Repository: "owner/one", Number: 42, HeadSHA: "old"},
		{Repository: "owner/one", Number: 42, HeadSHA: "new"},
		{Repository: "owner/two", Number: 42, HeadSHA: "head"},
		{Repository: "owner/removed", Number: 42, HeadSHA: "head"},
	}
	if got := activeRepositories(cfg, pullRequests, 42); !reflect.DeepEqual(got, []string{"owner/one", "owner/two"}) {
		t.Fatalf("activeRepositories() = %v", got)
	}
}
