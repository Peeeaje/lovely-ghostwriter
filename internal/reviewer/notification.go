package reviewer

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/Peeeaje/lovely-ghostwriter/internal/state"
)

func (r *Runner) NotifyStarted(ctx context.Context, pr state.PullRequest) error {
	if !r.Config.Notification.Enabled || !r.Config.Notification.Started {
		return nil
	}
	return r.notify(ctx, "PR review started", pr, "Review started")
}

func (r *Runner) NotifyFinished(ctx context.Context, pr state.PullRequest, status state.Status, runErr error) error {
	if !r.Config.Notification.Enabled {
		return nil
	}
	if runErr != nil {
		if !r.Config.Notification.Failed {
			return nil
		}
		return r.notify(ctx, "PR review failed", pr, runErr.Error())
	}
	if !r.Config.Notification.Finished {
		return nil
	}
	message := "Review completed"
	if status == state.StatusReviewed {
		message = "Review posted"
	}
	return r.notify(ctx, "PR review finished", pr, message)
}

func (r *Runner) notify(ctx context.Context, title string, pr state.PullRequest, message string) error {
	timeout, err := r.Config.NotificationTimeout()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{
		"-title", title,
		"-subtitle", fmt.Sprintf("%s#%d", pr.Repository, pr.Number),
		"-message", fmt.Sprintf("%s: %s", message, pr.Title),
		"-open", pr.URL,
		"-group", fmt.Sprintf("lovely-ghostwriter-%s-%d", pr.Repository, pr.Number),
	}
	if output, err := exec.CommandContext(ctx, r.Config.Notification.Command, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("desktop notification failed: %s: %w", output, err)
	}
	return nil
}
