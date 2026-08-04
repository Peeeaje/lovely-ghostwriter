package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultConfigIsValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Default().Validate() = %v", err)
	}
	interval, err := cfg.PollInterval()
	if err != nil {
		t.Fatal(err)
	}
	if interval != 3*time.Minute {
		t.Fatalf("PollInterval() = %s, want 3m", interval)
	}
}

func TestLoadAppliesDefaultsToLegacyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	legacy := `[daemon]
poll_interval = "3m"
max_concurrency = 1

[review]
command = "codex"
model = "model"
reasoning_effort = "high"

[[repository]]
name = "owner/repository"
path = "/tmp/repository"
base_branches = ["main"]
reviewers = ["reviewer"]
`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Review.Sandbox != "workspace-write" || cfg.Review.Marker != "codex-auto-review" {
		t.Fatalf("legacy defaults = %+v", cfg.Review)
	}
	if len(cfg.Repositories) != 1 || cfg.Repositories[0].Name != "owner/repository" {
		t.Fatalf("repositories = %+v", cfg.Repositories)
	}
}

func TestWriteAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repositories[0].Name != "owner/repository" {
		t.Fatalf("repository name = %q", cfg.Repositories[0].Name)
	}
}
