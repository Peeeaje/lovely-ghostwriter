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
	Patch        PatchConfig        `toml:"patch"`
	Notification NotificationConfig `toml:"notification"`
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
	Instructions    string   `toml:"instructions"`
	MaxHeadRechecks int      `toml:"max_head_rechecks"`
}

type ReviewOverride struct {
	Command         string   `toml:"command,omitempty"`
	Model           string   `toml:"model,omitempty"`
	ReasoningEffort string   `toml:"reasoning_effort,omitempty"`
	Sandbox         string   `toml:"sandbox,omitempty"`
	Marker          string   `toml:"marker,omitempty"`
	PostReviews     *bool    `toml:"post_reviews,omitempty"`
	ExtraArgs       []string `toml:"extra_args,omitempty"`
	Instructions    string   `toml:"instructions,omitempty"`
	MaxHeadRechecks *int     `toml:"max_head_rechecks,omitempty"`
}

type PatchConfig struct {
	Enabled         bool     `toml:"enabled"`
	Command         string   `toml:"command"`
	Model           string   `toml:"model"`
	ReasoningEffort string   `toml:"reasoning_effort"`
	Sandbox         string   `toml:"sandbox"`
	MaxIterations   int      `toml:"max_iterations"`
	BranchPrefix    string   `toml:"branch_prefix"`
	TitlePrefix     string   `toml:"title_prefix"`
	ExtraArgs       []string `toml:"extra_args"`
	Instructions    string   `toml:"instructions"`
}

type PatchOverride struct {
	Enabled         *bool    `toml:"enabled,omitempty"`
	Command         string   `toml:"command,omitempty"`
	Model           string   `toml:"model,omitempty"`
	ReasoningEffort string   `toml:"reasoning_effort,omitempty"`
	Sandbox         string   `toml:"sandbox,omitempty"`
	MaxIterations   int      `toml:"max_iterations,omitempty"`
	BranchPrefix    string   `toml:"branch_prefix,omitempty"`
	TitlePrefix     string   `toml:"title_prefix,omitempty"`
	ExtraArgs       []string `toml:"extra_args,omitempty"`
	Instructions    string   `toml:"instructions,omitempty"`
}

type NotificationConfig struct {
	Enabled  bool   `toml:"enabled"`
	Command  string `toml:"command"`
	Timeout  string `toml:"timeout"`
	Started  bool   `toml:"started"`
	Finished bool   `toml:"finished"`
	Failed   bool   `toml:"failed"`
	Detected bool   `toml:"detected"`
}

type RepositoryConfig struct {
	Name           string         `toml:"name"`
	Path           string         `toml:"path"`
	BaseBranches   []string       `toml:"base_branches"`
	Authors        []string       `toml:"authors"`
	Reviewers      []string       `toml:"reviewers"`
	Teams          []string       `toml:"teams"`
	ExcludeAuthors []string       `toml:"exclude_authors"`
	IncludeDrafts  bool           `toml:"include_drafts"`
	InitialTrigger string         `toml:"initial_trigger"`
	UpdateTrigger  string         `toml:"update_trigger"`
	Review         ReviewOverride `toml:"review,omitempty"`
	Patch          PatchOverride  `toml:"patch,omitempty"`
}

const (
	TriggerReviewRequest = "review-request"
	TriggerAlways        = "always"
	TriggerManual        = "manual"
)

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
			MaxHeadRechecks: 3,
		},
		Patch: PatchConfig{
			Command:         "codex",
			Model:           "gpt-5.6-sol",
			ReasoningEffort: "xhigh",
			Sandbox:         "workspace-write",
			MaxIterations:   2,
			BranchPrefix:    "develop/codex-auto-fix",
			TitlePrefix:     "[codex-auto-fix]",
		},
		Notification: NotificationConfig{
			Command:  "auto",
			Timeout:  "5s",
			Started:  true,
			Finished: true,
			Failed:   true,
			Detected: true,
		},
		Repositories: []RepositoryConfig{{
			Name:           "owner/repository",
			Path:           "~/src/repository",
			BaseBranches:   []string{"main"},
			Reviewers:      []string{"your-github-login"},
			ExcludeAuthors: []string{"app/dependabot", "app/renovate", "dependabot[bot]", "renovate[bot]"},
			InitialTrigger: TriggerReviewRequest,
			UpdateTrigger:  TriggerReviewRequest,
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
	for i := range cfg.Repositories {
		if cfg.Repositories[i].InitialTrigger == "" {
			cfg.Repositories[i].InitialTrigger = TriggerReviewRequest
		}
		if cfg.Repositories[i].UpdateTrigger == "" {
			cfg.Repositories[i].UpdateTrigger = TriggerReviewRequest
		}
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
	if err := c.Patch.validate("patch"); err != nil {
		return err
	}
	if c.Notification.Enabled && strings.TrimSpace(c.Notification.Command) == "" {
		return errors.New("notification.command is required when notifications are enabled")
	}
	if _, err := c.NotificationTimeout(); err != nil {
		return err
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
		if !validTrigger(repo.InitialTrigger) {
			return fmt.Errorf("%s.initial_trigger must be review-request, always, or manual", prefix)
		}
		if !validTrigger(repo.UpdateTrigger) {
			return fmt.Errorf("%s.update_trigger must be review-request, always, or manual", prefix)
		}
		if (repo.InitialTrigger == TriggerReviewRequest || repo.UpdateTrigger == TriggerReviewRequest) && len(repo.Reviewers) == 0 && len(repo.Teams) == 0 {
			return fmt.Errorf("%s requires at least one reviewer or team", prefix)
		}
		if err := repo.EffectiveReview(c.Review).validate(prefix + ".review"); err != nil {
			return err
		}
		if err := repo.EffectivePatch(c.Patch).validate(prefix + ".patch"); err != nil {
			return err
		}
		if _, ok := seen[repo.Name]; ok {
			return fmt.Errorf("duplicate repository: %s", repo.Name)
		}
		seen[repo.Name] = struct{}{}
	}
	return nil
}

func (c Config) NotificationTimeout() (time.Duration, error) {
	timeout, err := time.ParseDuration(c.Notification.Timeout)
	if err != nil {
		return 0, fmt.Errorf("notification.timeout: %w", err)
	}
	if timeout <= 0 {
		return 0, errors.New("notification.timeout must be positive")
	}
	return timeout, nil
}

func validTrigger(trigger string) bool {
	return slices.Contains([]string{TriggerReviewRequest, TriggerAlways, TriggerManual}, trigger)
}

func (r ReviewConfig) validate(prefix string) error {
	if strings.TrimSpace(r.Command) == "" {
		return fmt.Errorf("%s.command is required", prefix)
	}
	if !slices.Contains([]string{"read-only", "workspace-write", "danger-full-access"}, r.Sandbox) {
		return fmt.Errorf("%s.sandbox must be read-only, workspace-write, or danger-full-access", prefix)
	}
	if strings.TrimSpace(r.Marker) == "" {
		return fmt.Errorf("%s.marker is required", prefix)
	}
	if r.MaxHeadRechecks < 0 {
		return fmt.Errorf("%s.max_head_rechecks must not be negative", prefix)
	}
	return nil
}

func (p PatchConfig) validate(prefix string) error {
	if strings.TrimSpace(p.Command) == "" {
		return fmt.Errorf("%s.command is required", prefix)
	}
	if !slices.Contains([]string{"workspace-write", "danger-full-access"}, p.Sandbox) {
		return fmt.Errorf("%s.sandbox must be workspace-write or danger-full-access", prefix)
	}
	if p.MaxIterations < 1 {
		return fmt.Errorf("%s.max_iterations must be at least 1", prefix)
	}
	if strings.TrimSpace(p.BranchPrefix) == "" || strings.TrimSpace(p.TitlePrefix) == "" {
		return fmt.Errorf("%s.branch_prefix and title_prefix are required", prefix)
	}
	return nil
}

func (r RepositoryConfig) EffectiveReview(global ReviewConfig) ReviewConfig {
	if r.Review.Command != "" {
		global.Command = r.Review.Command
	}
	if r.Review.Model != "" {
		global.Model = r.Review.Model
	}
	if r.Review.ReasoningEffort != "" {
		global.ReasoningEffort = r.Review.ReasoningEffort
	}
	if r.Review.Sandbox != "" {
		global.Sandbox = r.Review.Sandbox
	}
	if r.Review.Marker != "" {
		global.Marker = r.Review.Marker
	}
	if r.Review.PostReviews != nil {
		global.PostReviews = *r.Review.PostReviews
	}
	if r.Review.ExtraArgs != nil {
		global.ExtraArgs = r.Review.ExtraArgs
	}
	if r.Review.Instructions != "" {
		global.Instructions = r.Review.Instructions
	}
	if r.Review.MaxHeadRechecks != nil {
		global.MaxHeadRechecks = *r.Review.MaxHeadRechecks
	}
	return global
}

func (r RepositoryConfig) EffectivePatch(global PatchConfig) PatchConfig {
	if r.Patch.Enabled != nil {
		global.Enabled = *r.Patch.Enabled
	}
	if r.Patch.Command != "" {
		global.Command = r.Patch.Command
	}
	if r.Patch.Model != "" {
		global.Model = r.Patch.Model
	}
	if r.Patch.ReasoningEffort != "" {
		global.ReasoningEffort = r.Patch.ReasoningEffort
	}
	if r.Patch.Sandbox != "" {
		global.Sandbox = r.Patch.Sandbox
	}
	if r.Patch.MaxIterations != 0 {
		global.MaxIterations = r.Patch.MaxIterations
	}
	if r.Patch.BranchPrefix != "" {
		global.BranchPrefix = r.Patch.BranchPrefix
	}
	if r.Patch.TitlePrefix != "" {
		global.TitlePrefix = r.Patch.TitlePrefix
	}
	if r.Patch.ExtraArgs != nil {
		global.ExtraArgs = r.Patch.ExtraArgs
	}
	if r.Patch.Instructions != "" {
		global.Instructions = r.Patch.Instructions
	}
	return global
}

func (r RepositoryConfig) PatchPullRequest(global PatchConfig, title, headBranch, author, reviewer string, crossRepository bool) bool {
	patch := r.EffectivePatch(global)
	branchPrefix := strings.TrimSuffix(patch.BranchPrefix, "-")
	return patch.Enabled && !crossRepository && author == reviewer &&
		strings.HasPrefix(title, patch.TitlePrefix+" ") && strings.HasPrefix(headBranch, branchPrefix+"-")
}

func (r RepositoryConfig) Trigger(isUpdate bool) string {
	if isUpdate {
		if r.UpdateTrigger != "" {
			return r.UpdateTrigger
		}
		return TriggerReviewRequest
	}
	if r.InitialTrigger != "" {
		return r.InitialTrigger
	}
	return TriggerReviewRequest
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
