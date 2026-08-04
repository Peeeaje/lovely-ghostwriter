package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Daemon       DaemonConfig       `toml:"daemon"`
	Review       ReviewConfig       `toml:"review"`
	Repositories []RepositoryConfig `toml:"repository"`
}

type DaemonConfig struct {
	PollInterval  string `toml:"poll_interval"`
	MaxConcurrent int    `toml:"max_concurrency"`
}

type ReviewConfig struct {
	Command         string   `toml:"command"`
	Model           string   `toml:"model"`
	ReasoningEffort string   `toml:"reasoning_effort"`
	Sandbox         string   `toml:"sandbox"`
	Marker          string   `toml:"marker"`
	PostReviews     bool     `toml:"post_reviews"`
	ExtraArgs       []string `toml:"extra_args"`
}

type RepositoryConfig struct {
	Name           string   `toml:"name"`
	Path           string   `toml:"path"`
	BaseBranches   []string `toml:"base_branches"`
	Authors        []string `toml:"authors"`
	Reviewers      []string `toml:"reviewers"`
	Teams          []string `toml:"teams"`
	ExcludeAuthors []string `toml:"exclude_authors"`
	IncludeDrafts  bool     `toml:"include_drafts"`
}

func Default() Config {
	return Config{
		Daemon: DaemonConfig{
			PollInterval:  "3m",
			MaxConcurrent: 3,
		},
		Review: ReviewConfig{
			Command:         "codex",
			Model:           "gpt-5.6-sol",
			ReasoningEffort: "high",
			Sandbox:         "workspace-write",
			Marker:          "codex-auto-review",
			PostReviews:     false,
		},
		Repositories: []RepositoryConfig{{
			Name:           "owner/repository",
			Path:           "~/src/repository",
			BaseBranches:   []string{"main"},
			Reviewers:      []string{"your-github-login"},
			ExcludeAuthors: []string{"app/dependabot", "app/renovate", "dependabot[bot]", "renovate[bot]"},
		}},
	}
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := Default()
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func WriteDefault(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("config already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect config %s: %w", path, err)
	}

	data, err := toml.Marshal(Default())
	if err != nil {
		return fmt.Errorf("encode default config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}

func (c Config) PollInterval() (time.Duration, error) {
	interval, err := time.ParseDuration(c.Daemon.PollInterval)
	if err != nil {
		return 0, fmt.Errorf("daemon.poll_interval: %w", err)
	}
	return interval, nil
}

func (c Config) Validate() error {
	interval, err := c.PollInterval()
	if err != nil {
		return err
	}
	if interval < 10*time.Second {
		return errors.New("daemon.poll_interval must be at least 10s")
	}
	if c.Daemon.MaxConcurrent < 1 {
		return errors.New("daemon.max_concurrency must be at least 1")
	}
	if strings.TrimSpace(c.Review.Command) == "" {
		return errors.New("review.command is required")
	}
	if !slices.Contains([]string{"read-only", "workspace-write", "danger-full-access"}, c.Review.Sandbox) {
		return errors.New("review.sandbox must be read-only, workspace-write, or danger-full-access")
	}
	if strings.TrimSpace(c.Review.Marker) == "" {
		return errors.New("review.marker is required")
	}
	if len(c.Repositories) == 0 {
		return errors.New("at least one [[repository]] is required")
	}

	seen := make(map[string]struct{}, len(c.Repositories))
	for i, repo := range c.Repositories {
		prefix := fmt.Sprintf("repository[%d]", i)
		if strings.Count(repo.Name, "/") != 1 {
			return fmt.Errorf("%s.name must be owner/repository", prefix)
		}
		if strings.TrimSpace(repo.Path) == "" {
			return fmt.Errorf("%s.path is required", prefix)
		}
		if len(repo.BaseBranches) == 0 {
			return fmt.Errorf("%s.base_branches must not be empty", prefix)
		}
		if len(repo.Reviewers) == 0 && len(repo.Teams) == 0 {
			return fmt.Errorf("%s requires at least one reviewer or team", prefix)
		}
		if _, ok := seen[repo.Name]; ok {
			return fmt.Errorf("duplicate repository: %s", repo.Name)
		}
		seen[repo.Name] = struct{}{}
	}
	return nil
}

func (r RepositoryConfig) AutoReviewBase(branch string) bool {
	return slices.Contains(r.BaseBranches, branch)
}

func (r RepositoryConfig) ExpandedPath() (string, error) {
	if r.Path == "~" || strings.HasPrefix(r.Path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if r.Path == "~" {
			return home, nil
		}
		return filepath.Join(home, strings.TrimPrefix(r.Path, "~/")), nil
	}
	return filepath.Clean(r.Path), nil
}
