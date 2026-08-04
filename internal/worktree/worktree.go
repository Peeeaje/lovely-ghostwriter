package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type HeadChangedError struct {
	Expected string
	Current  string
}

func (e HeadChangedError) Error() string {
	return fmt.Sprintf("pull request head changed from %s to %s", e.Expected, e.Current)
}

type Manager struct {
	Root string
}

func (m Manager) Prepare(ctx context.Context, sourcePath, repository string, number int, headSHA string, runID int64) (string, error) {
	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		return "", fmt.Errorf("create worktree root: %w", err)
	}
	shortHead := headSHA
	if len(shortHead) > 12 {
		shortHead = shortHead[:12]
	}
	slug := strings.NewReplacer("/", "-", "\\", "-").Replace(repository)
	path := filepath.Join(m.Root, fmt.Sprintf("%s-%d-%s-run%d", slug, number, shortHead, runID))

	if output, err := command(ctx, sourcePath, "fetch", "--no-tags", "origin", fmt.Sprintf("pull/%d/head", number)); err != nil {
		return "", fmt.Errorf("fetch pull request: %s: %w", output, err)
	}
	current, err := command(ctx, sourcePath, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve fetched head: %w", err)
	}
	current = strings.TrimSpace(current)
	if current != headSHA {
		return "", HeadChangedError{Expected: headSHA, Current: current}
	}
	if output, err := command(ctx, sourcePath, "worktree", "add", "--detach", path, headSHA); err != nil {
		return "", fmt.Errorf("create worktree: %s: %w", output, err)
	}
	return path, nil
}

func (m Manager) Cleanup(ctx context.Context, sourcePath, path string) error {
	if path == "" {
		return nil
	}
	if output, err := command(ctx, sourcePath, "worktree", "remove", "--force", path); err != nil {
		return fmt.Errorf("remove worktree: %s: %w", output, err)
	}
	_, _ = command(ctx, sourcePath, "worktree", "prune")
	return nil
}

func command(ctx context.Context, repositoryPath string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", repositoryPath}, args...)
	output, err := exec.CommandContext(ctx, "git", commandArgs...).CombinedOutput()
	return strings.TrimSpace(string(output)), err
}
