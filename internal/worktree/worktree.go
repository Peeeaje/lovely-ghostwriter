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

func (m Manager) Prepare(ctx context.Context, sourcePath, repository string, number int, baseBranch, baseSHA, headSHA string, runID int64) (string, error) {
	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		return "", fmt.Errorf("create worktree root: %w", err)
	}
	shortHead := headSHA
	if len(shortHead) > 12 {
		shortHead = shortHead[:12]
	}
	slug := strings.NewReplacer("/", "-", "\\", "-").Replace(repository)
	path := filepath.Join(m.Root, fmt.Sprintf("%s-%d-%s-run%d", slug, number, shortHead, runID))
	refRoot := fmt.Sprintf("refs/lovely-ghostwriter/run-%d", runID)
	baseRef := refRoot + "/base"
	headRef := refRoot + "/head"
	prepared := false
	defer func() {
		if !prepared {
			m.deleteRefs(context.Background(), sourcePath, baseRef, headRef)
		}
	}()

	if baseSHA == "" {
		return "", fmt.Errorf("pull request base SHA is missing")
	}
	if output, err := command(ctx, sourcePath, "fetch", "--no-write-fetch-head", "--no-tags", "origin", "+refs/heads/"+baseBranch+":"+baseRef); err != nil {
		return "", fmt.Errorf("fetch base branch: %s: %w", output, err)
	}
	fetchedBase, err := command(ctx, sourcePath, "rev-parse", baseRef)
	if err != nil {
		return "", fmt.Errorf("resolve fetched base: %w", err)
	}
	if strings.TrimSpace(fetchedBase) != baseSHA {
		return "", fmt.Errorf("pull request base changed from %s to %s", baseSHA, strings.TrimSpace(fetchedBase))
	}
	if output, err := command(ctx, sourcePath, "fetch", "--no-write-fetch-head", "--no-tags", "origin", fmt.Sprintf("+pull/%d/head:%s", number, headRef)); err != nil {
		return "", fmt.Errorf("fetch pull request: %s: %w", output, err)
	}
	current, err := command(ctx, sourcePath, "rev-parse", headRef)
	if err != nil {
		return "", fmt.Errorf("resolve fetched head: %w", err)
	}
	current = strings.TrimSpace(current)
	if current != headSHA {
		return "", HeadChangedError{Expected: headSHA, Current: current}
	}
	if output, err := command(ctx, sourcePath, "worktree", "add", "--detach", path, headRef); err != nil {
		return "", fmt.Errorf("create worktree: %s: %w", output, err)
	}
	prepared = true
	return path, nil
}

func (m Manager) Cleanup(ctx context.Context, sourcePath, path string, runID int64) error {
	var cleanupErr error
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			if output, err := command(ctx, sourcePath, "worktree", "remove", "--force", path); err != nil {
				cleanupErr = fmt.Errorf("remove worktree: %s: %w", output, err)
			}
		}
	}
	_, _ = command(ctx, sourcePath, "worktree", "prune")
	refRoot := fmt.Sprintf("refs/lovely-ghostwriter/run-%d", runID)
	m.deleteRefs(ctx, sourcePath, refRoot+"/base", refRoot+"/head")
	return cleanupErr
}

func (m Manager) deleteRefs(ctx context.Context, sourcePath string, refs ...string) {
	for _, ref := range refs {
		_, _ = command(ctx, sourcePath, "update-ref", "-d", ref)
	}
}

func command(ctx context.Context, repositoryPath string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", repositoryPath}, args...)
	output, err := exec.CommandContext(ctx, "git", commandArgs...).CombinedOutput()
	return strings.TrimSpace(string(output)), err
}
