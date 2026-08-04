package reviewer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Peeeaje/lovely-ghostwriter/internal/config"
	gh "github.com/Peeeaje/lovely-ghostwriter/internal/github"
	"github.com/Peeeaje/lovely-ghostwriter/internal/state"
	"github.com/Peeeaje/lovely-ghostwriter/internal/worktree"
)

type GitHubClient interface {
	CurrentUser(context.Context) (string, error)
	PullRequest(context.Context, string, int) (gh.PullRequest, error)
}

type Runner struct {
	Config       config.Config
	Store        *state.Store
	GitHub       GitHubClient
	Worktrees    worktree.Manager
	ArtifactRoot string
	Output       io.Writer
}

func (r *Runner) Run(ctx context.Context, repository config.RepositoryConfig, pr state.PullRequest, run state.Run) (status state.Status, runErr error) {
	status = state.StatusFailed
	sourcePath, err := repository.ExpandedPath()
	if err != nil {
		return status, err
	}

	artifactPath := filepath.Join(r.ArtifactRoot, fmt.Sprintf("%d", run.ID))
	if err := os.MkdirAll(artifactPath, 0o755); err != nil {
		return status, fmt.Errorf("create artifact directory: %w", err)
	}
	logPath := filepath.Join(artifactPath, "review.log")
	worktreePath, err := r.Worktrees.Prepare(ctx, sourcePath, pr.Repository, pr.Number, pr.HeadSHA, run.ID)
	if err != nil {
		_ = r.Store.SetRunPaths(ctx, run.ID, logPath, artifactPath, "")
		return status, err
	}
	if err := r.Store.SetRunPaths(ctx, run.ID, logPath, artifactPath, worktreePath); err != nil {
		_ = r.Worktrees.Cleanup(context.Background(), sourcePath, worktreePath)
		return status, err
	}
	defer func() {
		cleanupErr := r.Worktrees.Cleanup(context.Background(), sourcePath, worktreePath)
		if cleanupErr != nil && runErr == nil {
			runErr = cleanupErr
			status = state.StatusFailed
		}
	}()

	reviewer, err := r.GitHub.CurrentUser(ctx)
	if err != nil {
		return status, err
	}
	prompt := Prompt(r.Config, pr, worktreePath, artifactPath, reviewer)
	if err := os.WriteFile(filepath.Join(artifactPath, "prompt.md"), []byte(prompt+"\n"), 0o644); err != nil {
		return status, fmt.Errorf("write prompt: %w", err)
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return status, fmt.Errorf("open review log: %w", err)
	}
	defer logFile.Close()

	args := []string{
		"exec", "--ephemeral",
		"-C", worktreePath,
		"--sandbox", r.Config.Review.Sandbox,
		"--output-last-message", filepath.Join(artifactPath, "final.md"),
	}
	if r.Config.Review.ReasoningEffort != "" {
		args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(r.Config.Review.ReasoningEffort))
	}
	if r.Config.Review.Model != "" {
		args = append(args, "--model", r.Config.Review.Model)
	}
	args = append(args, r.Config.Review.ExtraArgs...)
	args = append(args, "-")

	command := exec.CommandContext(ctx, r.Config.Review.Command, args...)
	command.Dir = worktreePath
	command.Stdin = strings.NewReader(prompt)
	command.Stdout = io.MultiWriter(logFile, r.Output)
	command.Stderr = io.MultiWriter(logFile, r.Output)
	commandErr := command.Run()

	if !r.Config.Review.PostReviews {
		if commandErr != nil {
			return status, fmt.Errorf("Codex review failed: %w", commandErr)
		}
		return state.StatusCompleted, nil
	}

	current, markerErr := r.GitHub.PullRequest(ctx, pr.Repository, pr.Number)
	if markerErr != nil {
		return status, markerErr
	}
	if gh.HasMarker(current, r.Config.Review.Marker, pr.HeadSHA) {
		return state.StatusReviewed, nil
	}
	if commandErr != nil {
		return status, fmt.Errorf("Codex review failed and no review marker was found: %w", commandErr)
	}
	return status, errors.New("Codex finished without posting the expected review marker")
}
