package reviewer

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/Peeeaje/lovely-ghostwriter/internal/config"
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

func NotifyDetected(ctx context.Context, cfg config.Config, prs []state.PullRequest) error {
	if !cfg.Notification.Enabled || !cfg.Notification.Detected || len(prs) == 0 {
		return nil
	}
	sort.Slice(prs, func(i, j int) bool {
		if prs[i].Repository == prs[j].Repository {
			return prs[i].Number < prs[j].Number
		}
		return prs[i].Repository < prs[j].Repository
	})
	lines := make([]string, 0, min(len(prs), 5))
	for _, pr := range prs[:min(len(prs), 5)] {
		lines = append(lines, fmt.Sprintf("%s#%d %s", pr.Repository, pr.Number, pr.Title))
	}
	if len(prs) > len(lines) {
		lines = append(lines, fmt.Sprintf("and %d more", len(prs)-len(lines)))
	}
	url := ""
	if len(prs) == 1 {
		url = prs[0].URL
	}
	return notify(ctx, cfg, "Pull requests detected", fmt.Sprintf("%d pull requests require manual enqueue", len(prs)), strings.Join(lines, "\n"), url, "lovely-ghostwriter-detected")
}

func (r *Runner) notify(ctx context.Context, title string, pr state.PullRequest, message string) error {
	return notify(ctx, r.Config, title, fmt.Sprintf("%s#%d", pr.Repository, pr.Number), fmt.Sprintf("%s: %s", message, pr.Title), pr.URL, fmt.Sprintf("lovely-ghostwriter-%s-%d", pr.Repository, pr.Number))
}

func notify(ctx context.Context, cfg config.Config, title, subtitle, message, url, group string) error {
	timeout, err := cfg.NotificationTimeout()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := cfg.Notification.Command
	if command == "auto" {
		if _, err := exec.LookPath("terminal-notifier"); err == nil {
			command = "terminal-notifier"
		} else if _, err := exec.LookPath("osascript"); err == nil {
			command = "osascript"
		} else {
			return fmt.Errorf("desktop notification failed: terminal-notifier and osascript were not found")
		}
	}
	if command == "osascript" {
		script := fmt.Sprintf("display notification %s with title %s subtitle %s", strconv.Quote(message), strconv.Quote(title), strconv.Quote(subtitle))
		if output, err := exec.CommandContext(ctx, command, "-e", script).CombinedOutput(); err != nil {
			return fmt.Errorf("desktop notification failed: %s: %w", output, err)
		}
		return nil
	}
	args := []string{
		"-title", title,
		"-subtitle", subtitle,
		"-message", message,
		"-group", group,
	}
	if url != "" {
		args = append(args, "-open", url)
	}
	if output, err := exec.CommandContext(ctx, command, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("desktop notification failed: %s: %w", output, err)
	}
	return nil
}
