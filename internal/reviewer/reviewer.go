package reviewer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Peeeaje/lovely-ghostwriter/internal/config"
	gh "github.com/Peeeaje/lovely-ghostwriter/internal/github"
	"github.com/Peeeaje/lovely-ghostwriter/internal/policy"
	"github.com/Peeeaje/lovely-ghostwriter/internal/state"
	"github.com/Peeeaje/lovely-ghostwriter/internal/worktree"
)

type GitHubClient interface {
	CurrentUser(context.Context) (string, error)
	PullRequest(context.Context, string, int) (gh.PullRequest, error)
	SubmitReview(context.Context, string, int, gh.ReviewSubmission) (gh.Review, error)
}

type Runner struct {
	Config       config.Config
	Store        *state.Store
	GitHub       GitHubClient
	Worktrees    worktree.Manager
	ArtifactRoot string
}

func (r *Runner) PrepareAutomatic(ctx context.Context, repository config.RepositoryConfig, target state.PullRequest) (state.PullRequest, bool, error) {
	current, err := r.GitHub.PullRequest(ctx, target.Repository, target.Number)
	if err != nil {
		return target, false, err
	}
	reviewer, err := r.GitHub.CurrentUser(ctx)
	if err != nil {
		return target, false, err
	}
	if current.HeadSHA != target.HeadSHA || target.BaseSHA != "" && (current.BaseSHA != target.BaseSHA || current.BaseBranch != target.BaseBranch) {
		return target, false, nil
	}
	target.BaseBranch = current.BaseBranch
	target.BaseSHA = current.BaseSHA
	if !repository.AutoReviewBase(current.BaseBranch) {
		return target, false, nil
	}
	return target, policy.Automatic(repository, current, r.Config.Review.Marker, reviewer), nil
}

func (r *Runner) RecoveredReviewPosted(ctx context.Context, target state.PullRequest) (bool, error) {
	if target.RecoveryRunID == 0 {
		return false, nil
	}
	current, err := r.GitHub.PullRequest(ctx, target.Repository, target.Number)
	if err != nil {
		return false, err
	}
	reviewer, err := r.GitHub.CurrentUser(ctx)
	if err != nil {
		return false, err
	}
	return gh.HasRunMarker(current, r.Config.Review.Marker, target.HeadSHA, reviewer, target.RecoveryRunID), nil
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
	worktreePath, err := r.Worktrees.Prepare(ctx, sourcePath, pr.Repository, pr.Number, pr.BaseBranch, pr.BaseSHA, pr.HeadSHA, run.ID)
	if err != nil {
		_ = r.Store.SetRunPaths(ctx, run.ID, logPath, artifactPath, "")
		return status, err
	}
	if err := r.Store.SetRunPaths(ctx, run.ID, logPath, artifactPath, worktreePath); err != nil {
		_ = r.Worktrees.Cleanup(context.Background(), sourcePath, worktreePath, run.ID)
		return status, err
	}
	defer func() {
		cleanupErr := r.Worktrees.Cleanup(context.Background(), sourcePath, worktreePath, run.ID)
		if cleanupErr != nil && runErr == nil {
			runErr = cleanupErr
			status = state.StatusFailed
		}
	}()

	current, err := r.GitHub.PullRequest(ctx, pr.Repository, pr.Number)
	if err != nil {
		return status, err
	}
	if err := currentTarget(current, pr); err != nil {
		return status, err
	}
	contextData, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return status, fmt.Errorf("encode pull request context: %w", err)
	}
	if err := os.WriteFile(filepath.Join(artifactPath, "pr-context.json"), contextData, 0o644); err != nil {
		return status, fmt.Errorf("write pull request context: %w", err)
	}

	prompt := Prompt(r.Config, pr, worktreePath, artifactPath)
	if err := os.WriteFile(filepath.Join(artifactPath, "prompt.md"), []byte(prompt+"\n"), 0o644); err != nil {
		return status, fmt.Errorf("write prompt: %w", err)
	}
	schemaPath := filepath.Join(artifactPath, "review-result.schema.json")
	if err := os.WriteFile(schemaPath, outputSchema(), 0o644); err != nil {
		return status, fmt.Errorf("write output schema: %w", err)
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return status, fmt.Errorf("open review log: %w", err)
	}
	defer logFile.Close()

	resultPath := filepath.Join(artifactPath, "result.json")
	args := []string{
		"exec", "--ephemeral",
		"-C", artifactPath,
		"--add-dir", worktreePath,
		"--skip-git-repo-check",
		"--sandbox", r.Config.Review.Sandbox,
		"--output-schema", schemaPath,
		"--output-last-message", resultPath,
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
	command.Dir = artifactPath
	command.Stdin = strings.NewReader(prompt)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Run(); err != nil {
		return status, fmt.Errorf("Codex review failed: %w", err)
	}
	resultData, err := os.ReadFile(resultPath)
	if err != nil {
		return status, fmt.Errorf("read Codex review result: %w", err)
	}
	result, err := readResult(resultData)
	if err != nil {
		return status, err
	}
	if !r.Config.Review.PostReviews {
		return state.StatusCompleted, nil
	}

	current, err = r.GitHub.PullRequest(ctx, pr.Repository, pr.Number)
	if err != nil {
		return status, err
	}
	if err := currentTarget(current, pr); err != nil {
		return status, err
	}
	reviewer, err := r.GitHub.CurrentUser(ctx)
	if err != nil {
		return status, err
	}
	if _, err := r.GitHub.SubmitReview(ctx, pr.Repository, pr.Number, submission(result, r.Config.Review.Marker, reviewer, pr, run.ID)); err != nil {
		return status, err
	}
	posted, err := r.GitHub.PullRequest(ctx, pr.Repository, pr.Number)
	if err != nil {
		return status, err
	}
	if !gh.HasRunMarker(posted, r.Config.Review.Marker, pr.HeadSHA, reviewer, run.ID) {
		return status, fmt.Errorf("submitted review marker was not found for run %d", run.ID)
	}
	return state.StatusReviewed, nil
}

func currentTarget(current gh.PullRequest, target state.PullRequest) error {
	if current.State != "OPEN" {
		return fmt.Errorf("pull request is no longer open")
	}
	if current.HeadSHA != target.HeadSHA {
		return fmt.Errorf("pull request head changed from %s to %s", target.HeadSHA, current.HeadSHA)
	}
	if current.BaseSHA != target.BaseSHA {
		return fmt.Errorf("pull request base changed from %s to %s", target.BaseSHA, current.BaseSHA)
	}
	if current.BaseBranch != target.BaseBranch {
		return fmt.Errorf("pull request base branch changed from %s to %s", target.BaseBranch, current.BaseBranch)
	}
	return nil
}
