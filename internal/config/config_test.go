package config

import (
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
