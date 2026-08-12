package reviewer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

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

type reviewCommand struct {
	Command         string
	Model           string
	ReasoningEffort string
	Sandbox         string
	ExtraArgs       []string
}

type patchPullRequest struct {
	URL                 string   `json:"url"`
	HeadSHA             string   `json:"headRefOid"`
	BaseBranch          string   `json:"baseRefName"`
	CrossRepository     bool     `json:"isCrossRepository"`
	HeadRepositoryOwner gh.Actor `json:"headRepositoryOwner"`
}

func (r *Runner) PrepareAutomatic(ctx context.Context, repository config.RepositoryConfig, target state.PullRequest) (state.PullRequest, bool, error) {
	current, err := r.GitHub.PullRequest(ctx, target.Repository, target.Number)
	if err != nil {
		return target, false, err
	}
	if current.HeadSHA != target.HeadSHA || target.BaseBranch != "" && current.BaseBranch != target.BaseBranch {
		return target, false, nil
	}
	target.BaseBranch = current.BaseBranch
	target.BaseSHA = current.BaseSHA
	eligible, err := r.automaticEligible(ctx, repository, current)
	return target, eligible, err
}

func (r *Runner) automaticEligible(ctx context.Context, repository config.RepositoryConfig, current gh.PullRequest) (bool, error) {
	review := repository.EffectiveReview(r.Config.Review)
	reviewer, err := r.GitHub.CurrentUser(ctx)
	if err != nil {
		return false, err
	}
	if repository.PatchPullRequest(r.Config.Patch, current.Title, current.HeadBranch, current.Author.Login, reviewer, current.IsCrossRepository) || !repository.AutoReviewBase(current.BaseBranch) {
		return false, nil
	}
	trigger := repository.Trigger(false)
	if trigger != repository.Trigger(true) {
		isUpdate, err := r.Store.HasPreviousTarget(ctx, repository.Name, current.Number, current.HeadSHA, current.BaseBranch)
		if err != nil {
			return false, err
		}
		trigger = repository.Trigger(isUpdate)
	}
	return policy.Automatic(repository, current, review.Marker, reviewer, trigger), nil
}

func (r *Runner) RecoveredReviewPosted(ctx context.Context, repository config.RepositoryConfig, target state.PullRequest) (bool, error) {
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
	review := repository.EffectiveReview(r.Config.Review)
	return gh.HasRunMarker(current, review.Marker, target.HeadSHA, target.BaseBranch, reviewer, target.RecoveryRunID), nil
}

func (r *Runner) Run(ctx context.Context, repository config.RepositoryConfig, pr state.PullRequest, run state.Run) (finishedRun state.Run, finishedPR state.PullRequest, status state.Status, runErr error) {
	review := repository.EffectiveReview(r.Config.Review)
	patch := repository.EffectivePatch(r.Config.Patch)
	sourcePath, err := repository.ExpandedPath()
	if err != nil {
		return run, pr, state.StatusFailed, err
	}

	artifactPath := filepath.Join(r.ArtifactRoot, fmt.Sprintf("%d", run.ID))
	if err := os.MkdirAll(artifactPath, 0o755); err != nil {
		return run, pr, state.StatusFailed, fmt.Errorf("create artifact directory: %w", err)
	}
	logPath := filepath.Join(artifactPath, "review.log")
	handoff, err := r.Store.PreviousArtifact(ctx, pr.Repository, pr.Number, pr.HeadSHA)
	if err != nil {
		return run, pr, state.StatusFailed, err
	}
	previousArtifact := ""
	worktreePath := ""
	cleanupWorktree := func() error {
		if worktreePath == "" {
			return nil
		}
		saveWorktreeDiff(artifactPath, worktreePath)
		if err := r.Worktrees.Cleanup(context.Background(), sourcePath, worktreePath, run.ID); err != nil {
			return err
		}
		worktreePath = ""
		return nil
	}
	defer func() {
		if cleanupErr := cleanupWorktree(); cleanupErr != nil {
			runErr = errors.Join(runErr, cleanupErr)
			if status == state.StatusReviewed || status == state.StatusCompleted || status == state.StatusRunning || status == "" {
				status = state.StatusFailed
			}
		}
	}()

	for recheck := 0; ; recheck++ {
		if stoppedStatus, stopped, err := r.stopRequested(ctx, pr); err != nil {
			return run, pr, state.StatusFailed, err
		} else if stopped {
			writeStopArtifact(artifactPath, "stale-stop.md", fmt.Sprintf("review stopped with status %s", stoppedStatus))
			return run, pr, stoppedStatus, nil
		}
		current, err := r.GitHub.PullRequest(ctx, pr.Repository, pr.Number)
		if err != nil {
			return run, pr, state.StatusFailed, err
		}
		if current.State != "OPEN" {
			writeStopArtifact(artifactPath, "stale-stop.md", "pull request is no longer open")
			return run, pr, state.StatusStale, nil
		}
		patch.Enabled = patch.Enabled && !current.IsCrossRepository
		if current.HeadSHA != pr.HeadSHA || current.BaseBranch != pr.BaseBranch {
			if recheck >= review.MaxHeadRechecks {
				return run, pr, state.StatusFailed, fmt.Errorf("pull request changed more than %d times during review", review.MaxHeadRechecks)
			}
			if !pr.Manual {
				eligible, err := r.automaticEligible(ctx, repository, current)
				if err != nil {
					return run, pr, state.StatusFailed, err
				}
				if !eligible {
					writeStopArtifact(artifactPath, "ineligible-stop.md", "updated pull request is no longer eligible for automatic review")
					return run, pr, state.StatusCanceled, nil
				}
			}
			next := pullRequestState(pr.Repository, current, pr.Manual)
			if stoppedStatus, stopped, err := r.retargetRun(ctx, &run, pr, &next); err != nil {
				return run, pr, state.StatusFailed, err
			} else if stopped {
				writeStopArtifact(artifactPath, "stale-stop.md", fmt.Sprintf("review stopped with status %s", stoppedStatus))
				return run, pr, stoppedStatus, nil
			}
			writeStopArtifact(artifactPath, fmt.Sprintf("head-updated-recheck-%d.md", recheck+1), fmt.Sprintf("retargeted from %s to %s before review", pr.HeadSHA, next.HeadSHA))
			pr = next
		}

		worktreePath, err = r.Worktrees.Prepare(ctx, sourcePath, pr.Repository, pr.Number, pr.BaseSHA, pr.HeadSHA, run.ID)
		if err != nil {
			_ = r.Store.SetRunPaths(ctx, run.ID, logPath, artifactPath, "")
			latest, latestErr := r.GitHub.PullRequest(ctx, pr.Repository, pr.Number)
			if latestErr != nil {
				return run, pr, state.StatusFailed, errors.Join(err, latestErr)
			}
			if currentTarget(latest, pr) == nil {
				return run, pr, state.StatusFailed, err
			}
			if latest.State != "OPEN" {
				writeStopArtifact(artifactPath, "stale-stop.md", "pull request closed while preparing worktree")
				return run, pr, state.StatusStale, nil
			}
			if recheck >= review.MaxHeadRechecks {
				return run, pr, state.StatusFailed, fmt.Errorf("pull request changed more than %d times during review", review.MaxHeadRechecks)
			}
			if !pr.Manual {
				eligible, eligibilityErr := r.automaticEligible(ctx, repository, latest)
				if eligibilityErr != nil {
					return run, pr, state.StatusFailed, eligibilityErr
				}
				if !eligible {
					writeStopArtifact(artifactPath, "ineligible-stop.md", "updated pull request is no longer eligible for automatic review")
					return run, pr, state.StatusCanceled, nil
				}
			}
			next := pullRequestState(pr.Repository, latest, pr.Manual)
			if stoppedStatus, stopped, retargetErr := r.retargetRun(ctx, &run, pr, &next); retargetErr != nil {
				return run, pr, state.StatusFailed, errors.Join(err, retargetErr)
			} else if stopped {
				writeStopArtifact(artifactPath, "stale-stop.md", fmt.Sprintf("review stopped with status %s", stoppedStatus))
				return run, pr, stoppedStatus, nil
			}
			writeStopArtifact(artifactPath, fmt.Sprintf("head-updated-recheck-%d.md", recheck+1), fmt.Sprintf("retargeted from %s to %s while preparing worktree", pr.HeadSHA, next.HeadSHA))
			pr = next
			continue
		}
		if previousArtifact == "" && handoff.Path != "" && gitAncestor(ctx, sourcePath, handoff.HeadSHA, pr.HeadSHA) {
			previousArtifact = handoff.Path
		}
		if err := r.Store.SetRunPaths(ctx, run.ID, logPath, artifactPath, worktreePath); err != nil {
			return run, pr, state.StatusFailed, err
		}
		iterationPath := filepath.Join(artifactPath, fmt.Sprintf("review-%d", recheck+1))
		if err := os.MkdirAll(iterationPath, 0o755); err != nil {
			return run, pr, state.StatusFailed, err
		}
		result, status, runErr := r.reviewTarget(ctx, review, patch, pr, run, current, worktreePath, iterationPath, logPath, previousArtifact)
		if runErr != nil || status == state.StatusRejected || status == state.StatusStale {
			return run, pr, status, runErr
		}
		if status == state.StatusCompleted {
			return run, pr, status, nil
		}
		if stoppedStatus, stopped, err := r.stopRequested(ctx, pr); err != nil {
			return run, pr, state.StatusFailed, err
		} else if stopped {
			writeStopArtifact(artifactPath, "stale-stop.md", fmt.Sprintf("review stopped with status %s", stoppedStatus))
			return run, pr, stoppedStatus, nil
		}

		current, err = r.GitHub.PullRequest(ctx, pr.Repository, pr.Number)
		if err != nil {
			return run, pr, state.StatusFailed, err
		}
		if current.State != "OPEN" {
			writeStopArtifact(artifactPath, "stale-stop.md", "pull request closed before review submission")
			return run, pr, state.StatusStale, nil
		}
		if current.HeadSHA != pr.HeadSHA || current.BaseBranch != pr.BaseBranch {
			if err := cleanupWorktree(); err != nil {
				return run, pr, state.StatusFailed, err
			}
			if recheck >= review.MaxHeadRechecks {
				return run, pr, state.StatusFailed, fmt.Errorf("pull request changed more than %d times during review", review.MaxHeadRechecks)
			}
			if !pr.Manual {
				eligible, err := r.automaticEligible(ctx, repository, current)
				if err != nil {
					return run, pr, state.StatusFailed, err
				}
				if !eligible {
					writeStopArtifact(artifactPath, "ineligible-stop.md", "updated pull request is no longer eligible for automatic review")
					return run, pr, state.StatusCanceled, nil
				}
			}
			next := pullRequestState(pr.Repository, current, pr.Manual)
			if stoppedStatus, stopped, err := r.retargetRun(ctx, &run, pr, &next); err != nil {
				return run, pr, state.StatusFailed, err
			} else if stopped {
				writeStopArtifact(artifactPath, "stale-stop.md", fmt.Sprintf("review stopped with status %s", stoppedStatus))
				return run, pr, stoppedStatus, nil
			}
			writeStopArtifact(artifactPath, fmt.Sprintf("head-updated-recheck-%d.md", recheck+1), fmt.Sprintf("retargeted from %s to %s before submission", pr.HeadSHA, next.HeadSHA))
			pr = next
			previousArtifact = artifactPath
			continue
		}
		operationCtx, stopOperation, operationStopped := r.monitoredContext(ctx, pr)
		patchURL, err := r.createPatchPullRequest(operationCtx, patch, pr, current, worktreePath, artifactPath, result.PatchedFindings)
		stopOperation()
		if err != nil {
			select {
			case stoppedStatus := <-operationStopped:
				writeStopArtifact(artifactPath, "stale-stop.md", fmt.Sprintf("review stopped with status %s", stoppedStatus))
				return run, pr, stoppedStatus, nil
			default:
			}
			return run, pr, state.StatusFailed, err
		}
		latest, err := r.GitHub.PullRequest(ctx, pr.Repository, pr.Number)
		if err != nil {
			return run, pr, state.StatusFailed, err
		}
		if latest.State != "OPEN" || latest.HeadSHA != pr.HeadSHA || latest.BaseBranch != pr.BaseBranch {
			if patchURL != "" {
				if err := closePatchPullRequest(context.Background(), patchURL); err != nil {
					writeStopArtifact(artifactPath, "patch-close-error.md", err.Error())
					return run, pr, state.StatusFailed, err
				}
			}
			if err := cleanupWorktree(); err != nil {
				return run, pr, state.StatusFailed, err
			}
			if latest.State != "OPEN" {
				writeStopArtifact(artifactPath, "stale-stop.md", "pull request closed while creating patch pull request")
				return run, pr, state.StatusStale, nil
			}
			if recheck >= review.MaxHeadRechecks {
				return run, pr, state.StatusFailed, fmt.Errorf("pull request changed more than %d times during review", review.MaxHeadRechecks)
			}
			if !pr.Manual {
				eligible, err := r.automaticEligible(ctx, repository, latest)
				if err != nil {
					return run, pr, state.StatusFailed, err
				}
				if !eligible {
					writeStopArtifact(artifactPath, "ineligible-stop.md", "updated pull request is no longer eligible for automatic review")
					return run, pr, state.StatusCanceled, nil
				}
			}
			next := pullRequestState(pr.Repository, latest, pr.Manual)
			if stoppedStatus, stopped, err := r.retargetRun(ctx, &run, pr, &next); err != nil {
				return run, pr, state.StatusFailed, err
			} else if stopped {
				writeStopArtifact(artifactPath, "stale-stop.md", fmt.Sprintf("review stopped with status %s", stoppedStatus))
				return run, pr, stoppedStatus, nil
			}
			writeStopArtifact(artifactPath, fmt.Sprintf("head-updated-recheck-%d.md", recheck+1), fmt.Sprintf("retargeted from %s to %s after patch creation", pr.HeadSHA, next.HeadSHA))
			pr = next
			previousArtifact = artifactPath
			continue
		}
		reviewer, err := r.GitHub.CurrentUser(ctx)
		if err != nil {
			return run, pr, state.StatusFailed, err
		}
		if stoppedStatus, stopped, err := r.stopRequested(ctx, pr); err != nil {
			return run, pr, state.StatusFailed, err
		} else if stopped {
			if patchURL != "" {
				if err := closePatchPullRequest(context.Background(), patchURL); err != nil {
					writeStopArtifact(artifactPath, "patch-close-error.md", err.Error())
					return run, pr, stoppedStatus, err
				}
			}
			return run, pr, stoppedStatus, nil
		}
		submitCtx, stopSubmit, submitStopped := r.monitoredContext(ctx, pr)
		_, submitErr := r.GitHub.SubmitReview(submitCtx, pr.Repository, pr.Number, submission(result, review.Marker, reviewer, pr, run.ID, patchURL))
		stopSubmit()
		if submitErr != nil {
			select {
			case stoppedStatus := <-submitStopped:
				return run, pr, stoppedStatus, nil
			default:
			}
			return run, pr, state.StatusFailed, submitErr
		}
		posted, err := r.GitHub.PullRequest(ctx, pr.Repository, pr.Number)
		if err != nil {
			return run, pr, state.StatusFailed, err
		}
		if currentTarget(posted, pr) != nil {
			if patchURL != "" {
				if err := closePatchPullRequest(context.Background(), patchURL); err != nil {
					writeStopArtifact(artifactPath, "patch-close-error.md", err.Error())
					return run, pr, state.StatusFailed, err
				}
			}
			if err := cleanupWorktree(); err != nil {
				return run, pr, state.StatusFailed, err
			}
			if posted.State != "OPEN" {
				writeStopArtifact(artifactPath, "stale-stop.md", "pull request closed while submitting review")
				return run, pr, state.StatusStale, nil
			}
			if recheck >= review.MaxHeadRechecks {
				return run, pr, state.StatusFailed, fmt.Errorf("pull request changed more than %d times during review", review.MaxHeadRechecks)
			}
			if !pr.Manual {
				eligible, err := r.automaticEligible(ctx, repository, posted)
				if err != nil {
					return run, pr, state.StatusFailed, err
				}
				if !eligible {
					writeStopArtifact(artifactPath, "ineligible-stop.md", "updated pull request is no longer eligible for automatic review")
					return run, pr, state.StatusCanceled, nil
				}
			}
			next := pullRequestState(pr.Repository, posted, pr.Manual)
			if stoppedStatus, stopped, err := r.retargetRun(ctx, &run, pr, &next); err != nil {
				return run, pr, state.StatusFailed, err
			} else if stopped {
				writeStopArtifact(artifactPath, "stale-stop.md", fmt.Sprintf("review stopped with status %s", stoppedStatus))
				return run, pr, stoppedStatus, nil
			}
			writeStopArtifact(artifactPath, fmt.Sprintf("head-updated-recheck-%d.md", recheck+1), fmt.Sprintf("retargeted from %s to %s after review submission", pr.HeadSHA, next.HeadSHA))
			pr = next
			previousArtifact = artifactPath
			continue
		}
		if !gh.HasRunMarker(posted, review.Marker, pr.HeadSHA, pr.BaseBranch, reviewer, run.ID) {
			return run, pr, state.StatusFailed, fmt.Errorf("submitted review marker was not found for run %d", run.ID)
		}
		return run, pr, state.StatusReviewed, nil
	}
}

func (r *Runner) reviewTarget(ctx context.Context, review config.ReviewConfig, patch config.PatchConfig, pr state.PullRequest, run state.Run, current gh.PullRequest, worktreePath, artifactPath, logPath, previousArtifact string) (Result, state.Status, error) {
	contextData, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return Result{}, state.StatusFailed, fmt.Errorf("encode pull request context: %w", err)
	}
	if err := os.WriteFile(filepath.Join(artifactPath, "pr-context.json"), contextData, 0o644); err != nil {
		return Result{}, state.StatusFailed, fmt.Errorf("write pull request context: %w", err)
	}

	prompt := Prompt(review, patch, pr, worktreePath, artifactPath, previousArtifact)
	if err := os.WriteFile(filepath.Join(artifactPath, "prompt.md"), []byte(prompt+"\n"), 0o644); err != nil {
		return Result{}, state.StatusFailed, fmt.Errorf("write prompt: %w", err)
	}
	schemaPath := filepath.Join(artifactPath, "review-result.schema.json")
	if err := os.WriteFile(schemaPath, outputSchema(), 0o644); err != nil {
		return Result{}, state.StatusFailed, fmt.Errorf("write output schema: %w", err)
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return Result{}, state.StatusFailed, fmt.Errorf("open review log: %w", err)
	}
	defer logFile.Close()

	resultPath := filepath.Join(artifactPath, "result.json")
	commandConfig := reviewCommand{Command: review.Command, Model: review.Model, ReasoningEffort: review.ReasoningEffort, Sandbox: review.Sandbox, ExtraArgs: review.ExtraArgs}
	if patch.Enabled {
		commandConfig = reviewCommand{Command: patch.Command, Model: patch.Model, ReasoningEffort: patch.ReasoningEffort, Sandbox: patch.Sandbox, ExtraArgs: patch.ExtraArgs}
	}
	args := []string{
		"exec", "--ephemeral",
		"-C", artifactPath,
		"--add-dir", worktreePath,
		"--skip-git-repo-check",
		"--sandbox", commandConfig.Sandbox,
		"--output-schema", schemaPath,
		"--output-last-message", resultPath,
	}
	if commandConfig.ReasoningEffort != "" {
		args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(commandConfig.ReasoningEffort))
	}
	if commandConfig.Model != "" {
		args = append(args, "--model", commandConfig.Model)
	}
	args = append(args, commandConfig.ExtraArgs...)
	args = append(args, "-")

	commandCtx, stop, stopped := r.monitoredContext(ctx, pr)
	defer stop()
	command := exec.CommandContext(commandCtx, commandConfig.Command, args...)
	configureProcessGroup(command)
	command.Dir = artifactPath
	command.Stdin = strings.NewReader(prompt)
	command.Stdout = logFile
	command.Stderr = logFile
	err = command.Run()
	if command.Process != nil {
		err = errors.Join(err, waitProcessGroup(command.Process.Pid, 2*time.Second))
	}
	if err != nil {
		select {
		case status := <-stopped:
			writeStopArtifact(filepath.Dir(artifactPath), "stale-stop.md", fmt.Sprintf("review stopped with status %s", status))
			return Result{}, status, nil
		default:
		}
		return Result{}, state.StatusFailed, fmt.Errorf("Codex review failed: %w", err)
	}
	resultData, err := os.ReadFile(resultPath)
	if err != nil {
		return Result{}, state.StatusFailed, fmt.Errorf("read Codex review result: %w", err)
	}
	result, err := readResult(resultData)
	if err != nil {
		return Result{}, state.StatusFailed, err
	}
	if !review.PostReviews {
		return result, state.StatusCompleted, nil
	}
	return result, state.StatusRunning, nil
}

func configureProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		pid := command.Process.Pid
		if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		deadline := time.Now().Add(2 * time.Second)
		for syscall.Kill(-pid, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if syscall.Kill(-pid, 0) == nil {
			if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
		}
		return nil
	}
	command.WaitDelay = 3 * time.Second
}

func waitProcessGroup(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for syscall.Kill(-pid, 0) == nil {
		if time.Now().After(deadline) {
			if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
			deadline = time.Now().Add(time.Second)
			for syscall.Kill(-pid, 0) == nil && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if syscall.Kill(-pid, 0) == nil {
				return fmt.Errorf("process group %d did not stop", pid)
			}
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

func (r *Runner) monitoredContext(ctx context.Context, pr state.PullRequest) (context.Context, context.CancelFunc, <-chan state.Status) {
	monitored, cancel := context.WithCancel(ctx)
	stopped := make(chan state.Status, 1)
	go func() {
		cancelTicker := time.NewTicker(time.Second)
		stateTicker := time.NewTicker(30 * time.Second)
		defer cancelTicker.Stop()
		defer stateTicker.Stop()
		for {
			select {
			case <-monitored.Done():
				return
			case <-cancelTicker.C:
				status, requested, err := r.Store.CancellationRequested(monitored, pr.Repository, pr.Number, pr.HeadSHA)
				if err == nil && requested {
					stopped <- status
					cancel()
					return
				}
			case <-stateTicker.C:
				current, err := r.GitHub.PullRequest(monitored, pr.Repository, pr.Number)
				if err == nil && current.State != "OPEN" {
					stopped <- state.StatusStale
					cancel()
					return
				}
			}
		}
	}()
	return monitored, cancel, stopped
}

func (r *Runner) stopRequested(ctx context.Context, pr state.PullRequest) (state.Status, bool, error) {
	return r.Store.CancellationRequested(ctx, pr.Repository, pr.Number, pr.HeadSHA)
}

func (r *Runner) retargetRun(ctx context.Context, run *state.Run, previous state.PullRequest, current *state.PullRequest) (state.Status, bool, error) {
	manual, err := r.Store.RetargetRun(ctx, run, previous, *current)
	var stopped state.StopRequestedError
	if errors.As(err, &stopped) {
		return stopped.Status, true, nil
	}
	current.Manual = manual
	return "", false, err
}

func pullRequestState(repository string, pr gh.PullRequest, manual bool) state.PullRequest {
	return state.PullRequest{
		Repository: repository,
		Number:     pr.Number,
		HeadSHA:    pr.HeadSHA,
		Title:      pr.Title,
		URL:        pr.URL,
		Author:     pr.Author.Login,
		BaseBranch: pr.BaseBranch,
		BaseSHA:    pr.BaseSHA,
		Manual:     manual,
		Status:     state.StatusRunning,
	}
}

func writeStopArtifact(artifactPath, name, message string) {
	_ = os.WriteFile(filepath.Join(artifactPath, name), []byte(message+"\n"), 0o644)
}

func saveWorktreeDiff(artifactPath, worktreePath string) {
	status, err := git(context.Background(), worktreePath, "status", "--porcelain")
	if err != nil || strings.TrimSpace(status) == "" {
		return
	}
	_, _ = git(context.Background(), worktreePath, "add", "-N", ".")
	diff, err := git(context.Background(), worktreePath, "diff", "--binary", "HEAD")
	if err == nil {
		_ = os.WriteFile(filepath.Join(artifactPath, "worktree-diff.patch"), []byte(diff+"\n"), 0o644)
	}
}

func (r *Runner) createPatchPullRequest(ctx context.Context, patch config.PatchConfig, pr state.PullRequest, current gh.PullRequest, worktreePath, artifactPath string, patchedFindings []PatchedFinding) (string, error) {
	if !patch.Enabled {
		if len(patchedFindings) > 0 {
			return "", errors.New("review result has patched findings while patch mode is disabled")
		}
		return "", nil
	}
	if err := validatePatchWorktree(ctx, worktreePath, pr.HeadSHA); err != nil {
		return "", err
	}
	status, err := git(ctx, worktreePath, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(status) == "" {
		if len(patchedFindings) > 0 {
			return "", errors.New("review result has patched findings but patch worktree has no changes")
		}
		return "", nil
	}
	if len(patchedFindings) == 0 {
		return "", errors.New("patch worktree has changes but review result has no patched findings")
	}
	if _, err := git(ctx, worktreePath, "add", "-A"); err != nil {
		return "", err
	}
	generatedTree, err := git(ctx, worktreePath, "write-tree")
	if err != nil {
		return "", err
	}
	shortHead := pr.HeadSHA
	if len(shortHead) > 12 {
		shortHead = shortHead[:12]
	}
	branch := fmt.Sprintf("%s-%d-%s", strings.TrimSuffix(patch.BranchPrefix, "-"), pr.Number, shortHead)
	title := fmt.Sprintf("%s %s", patch.TitlePrefix, pr.Title)
	owner := strings.SplitN(pr.Repository, "/", 2)[0]
	patchRef := fmt.Sprintf("refs/lovely-ghostwriter/patch/%d-%s", pr.Number, shortHead)
	defer func() { _, _ = git(context.Background(), worktreePath, "update-ref", "-d", patchRef) }()
	existingPullRequest, found, err := findPatchPullRequest(ctx, pr.Repository, branch, owner, current.HeadBranch)
	if err != nil {
		return "", err
	}
	if found {
		if err := verifyRemotePatch(ctx, worktreePath, branch, patchRef, existingPullRequest.HeadSHA, generatedTree, pr.HeadSHA); err != nil {
			return "", err
		}
		return existingPullRequest.URL, nil
	}
	bodyPath := filepath.Join(artifactPath, "patch-pr-body.md")
	body := patchPullRequestBody(pr, patchedFindings)
	if err := os.WriteFile(bodyPath, []byte(body), 0o644); err != nil {
		return "", fmt.Errorf("write patch pull request body: %w", err)
	}
	if err := validatePatchWorktree(ctx, worktreePath, pr.HeadSHA); err != nil {
		return "", err
	}
	remoteBranch, err := git(ctx, worktreePath, "ls-remote", "--heads", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	if remoteBranch == "" {
		if _, err := git(ctx, worktreePath, "commit", "-m", fmt.Sprintf("%s Fix findings for #%d", patch.TitlePrefix, pr.Number)); err != nil {
			return "", err
		}
		patchHead, err := git(ctx, worktreePath, "rev-parse", "HEAD")
		if err != nil || !validPatchCommit(ctx, worktreePath, patchHead, generatedTree, pr.HeadSHA) {
			return "", fmt.Errorf("created patch commit is not based on pull request head: %w", err)
		}
		if _, err := git(ctx, worktreePath, "push", "--force-with-lease=refs/heads/"+branch+":", "origin", "HEAD:refs/heads/"+branch); err != nil {
			return "", err
		}
	} else {
		remoteBranchFields := strings.Fields(remoteBranch)
		if len(remoteBranchFields) != 2 {
			return "", fmt.Errorf("unexpected remote patch branch response: %s", remoteBranch)
		}
		if err := verifyRemotePatch(ctx, worktreePath, branch, patchRef, remoteBranchFields[0], generatedTree, pr.HeadSHA); err != nil {
			return "", err
		}
	}
	command := exec.CommandContext(ctx, "gh", "pr", "create",
		"--repo", pr.Repository,
		"--base", current.HeadBranch,
		"--head", branch,
		"--title", title,
		"--body-file", bodyPath,
	)
	output, err := command.CombinedOutput()
	lookupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	createdPullRequest, found, lookupErr := findPatchPullRequest(lookupCtx, pr.Repository, branch, owner, current.HeadBranch)
	if lookupErr != nil {
		if err != nil {
			return "", errors.Join(fmt.Errorf("create patch pull request: %s: %w", strings.TrimSpace(string(output)), err), lookupErr)
		}
		return "", lookupErr
	}
	if !found {
		if err != nil {
			return "", fmt.Errorf("create patch pull request: %s: %w", strings.TrimSpace(string(output)), err)
		}
		return "", fmt.Errorf("create patch pull request did not produce a verifiable pull request: %s", strings.TrimSpace(string(output)))
	}
	if verifyErr := verifyRemotePatch(lookupCtx, worktreePath, branch, patchRef, createdPullRequest.HeadSHA, generatedTree, pr.HeadSHA); verifyErr != nil {
		if closeErr := closePatchPullRequest(context.Background(), createdPullRequest.URL); closeErr != nil {
			return "", errors.Join(verifyErr, closeErr)
		}
		return "", verifyErr
	}
	url := createdPullRequest.URL
	_ = os.WriteFile(filepath.Join(artifactPath, "patch-pr-url.txt"), []byte(url+"\n"), 0o644)
	return url, nil
}

func patchPullRequestBody(pr state.PullRequest, findings []PatchedFinding) string {
	var body strings.Builder
	fmt.Fprintf(&body, "#%dの自動レビューで検出したblocking findingを修正します。\n\n元PR: %s\n対象head: `%s`\n\n## 問題\n", pr.Number, pr.URL, pr.HeadSHA)
	for i, finding := range findings {
		fmt.Fprintf(&body, "\n%d. %s\n", i+1, finding.Problem)
	}
	body.WriteString("\n## 修正内容\n")
	for i, finding := range findings {
		fmt.Fprintf(&body, "\n%d. %s\n", i+1, finding.Fix)
	}
	body.WriteString("\n元PRとは別に内容を確認した上で、必要に応じて取り込んでください。\n")
	return body.String()
}

func findPatchPullRequest(ctx context.Context, repository, branch, owner, baseBranch string) (patchPullRequest, bool, error) {
	command := exec.CommandContext(ctx, "gh", "pr", "list", "--repo", repository, "--state", "open", "--head", branch,
		"--json", "url,headRefOid,baseRefName,isCrossRepository,headRepositoryOwner")
	output, err := command.CombinedOutput()
	if err != nil {
		return patchPullRequest{}, false, fmt.Errorf("find patch pull request: %s: %w", strings.TrimSpace(string(output)), err)
	}
	var pullRequests []patchPullRequest
	if err := json.Unmarshal(output, &pullRequests); err != nil {
		return patchPullRequest{}, false, fmt.Errorf("decode patch pull requests: %w", err)
	}
	var sameRepository []patchPullRequest
	for _, pullRequest := range pullRequests {
		if !pullRequest.CrossRepository && pullRequest.HeadRepositoryOwner.Login == owner {
			sameRepository = append(sameRepository, pullRequest)
		}
	}
	if len(sameRepository) > 1 {
		return patchPullRequest{}, false, fmt.Errorf("multiple open patch pull requests use branch %s", branch)
	}
	if len(sameRepository) == 0 {
		return patchPullRequest{}, false, nil
	}
	if sameRepository[0].BaseBranch != baseBranch {
		return patchPullRequest{}, false, fmt.Errorf("existing patch pull request %s targets %s instead of %s", sameRepository[0].URL, sameRepository[0].BaseBranch, baseBranch)
	}
	return sameRepository[0], true, nil
}

func validatePatchWorktree(ctx context.Context, worktreePath, headSHA string) error {
	head, err := git(ctx, worktreePath, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if head != headSHA {
		return fmt.Errorf("patch worktree HEAD changed from %s to %s", headSHA, head)
	}
	return nil
}

func verifyRemotePatch(ctx context.Context, worktreePath, branch, ref, expectedHead, expectedTree, baseHead string) error {
	if _, err := git(ctx, worktreePath, "fetch", "--no-write-fetch-head", "--no-tags", "origin", "+refs/heads/"+branch+":"+ref); err != nil {
		return err
	}
	actualHead, err := git(ctx, worktreePath, "rev-parse", ref)
	if err != nil {
		return err
	}
	if actualHead != expectedHead || !validPatchCommit(ctx, worktreePath, actualHead, expectedTree, baseHead) {
		return fmt.Errorf("remote patch branch %s changed or has different changes", branch)
	}
	return nil
}

func validPatchCommit(ctx context.Context, worktreePath, head, expectedTree, baseHead string) bool {
	tree, err := git(ctx, worktreePath, "show", "-s", "--format=%T", head)
	return err == nil && tree == expectedTree && gitAncestor(ctx, worktreePath, baseHead, head)
}

func closePatchPullRequest(ctx context.Context, url string) error {
	closeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(closeCtx, "gh", "pr", "close", url).CombinedOutput()
	if err != nil {
		return fmt.Errorf("close stale patch pull request: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func git(ctx context.Context, worktreePath string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", worktreePath}, args...)
	output, err := exec.CommandContext(ctx, "git", commandArgs...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(output)), err)
	}
	return strings.TrimSpace(string(output)), nil
}

func gitAncestor(ctx context.Context, repositoryPath, ancestor, descendant string) bool {
	command := exec.CommandContext(ctx, "git", "-C", repositoryPath, "merge-base", "--is-ancestor", ancestor, descendant)
	return command.Run() == nil
}

func currentTarget(current gh.PullRequest, target state.PullRequest) error {
	if current.State != "OPEN" {
		return fmt.Errorf("pull request is no longer open")
	}
	if current.HeadSHA != target.HeadSHA {
		return fmt.Errorf("pull request head changed from %s to %s", target.HeadSHA, current.HeadSHA)
	}
	if current.BaseBranch != target.BaseBranch {
		return fmt.Errorf("pull request base branch changed from %s to %s", target.BaseBranch, current.BaseBranch)
	}
	return nil
}
