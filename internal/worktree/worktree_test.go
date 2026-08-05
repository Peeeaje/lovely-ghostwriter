package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareUsesRecordedBaseAndCleanupRemovesWorktreeAndRefs(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	source := filepath.Join(root, "source")
	runGit(t, root, "init", "--bare", remote)
	runGit(t, root, "init", "-b", "main", seed)
	runGit(t, seed, "config", "user.name", "Reviewer")
	runGit(t, seed, "config", "user.email", "reviewer@example.test")
	if err := os.WriteFile(filepath.Join(seed, "file.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "file.txt")
	runGit(t, seed, "commit", "-m", "base")
	baseSHA := runGit(t, seed, "rev-parse", "HEAD")
	runGit(t, seed, "switch", "-c", "feature")
	if err := os.WriteFile(filepath.Join(seed, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "feature.txt")
	runGit(t, seed, "commit", "-m", "feature")
	headSHA := runGit(t, seed, "rev-parse", "HEAD")
	runGit(t, seed, "switch", "main")
	if err := os.WriteFile(filepath.Join(seed, "main.txt"), []byte("advanced\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "main.txt")
	runGit(t, seed, "commit", "-m", "advance main")
	runGit(t, seed, "remote", "add", "origin", remote)
	runGit(t, seed, "push", "origin", "main")
	runGit(t, seed, "push", "origin", headSHA+":refs/pull/1/head")
	runGit(t, root, "clone", remote, source)

	manager := Manager{Root: filepath.Join(root, "worktrees")}
	path, err := manager.Prepare(context.Background(), source, "owner/repository", 1, baseSHA, headSHA, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, path, "rev-parse", "HEAD"); got != headSHA {
		t.Fatalf("worktree HEAD = %s, want %s", got, headSHA)
	}
	if err := manager.Cleanup(context.Background(), source, path, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
	for _, ref := range []string{"refs/lovely-ghostwriter/run-7/base", "refs/lovely-ghostwriter/run-7/head"} {
		if exec.Command("git", "-C", source, "rev-parse", "--verify", ref).Run() == nil {
			t.Fatalf("temporary ref still exists: %s", ref)
		}
	}

	_, err = manager.Prepare(context.Background(), source, "owner/repository", 1, baseSHA, strings.Repeat("0", 40), 8)
	if err == nil {
		t.Fatal("Prepare() succeeded with a stale head")
	}
	for _, ref := range []string{"refs/lovely-ghostwriter/run-8/base", "refs/lovely-ghostwriter/run-8/head"} {
		if exec.Command("git", "-C", source, "rev-parse", "--verify", ref).Run() == nil {
			t.Fatalf("temporary ref from failed preparation still exists: %s", ref)
		}
	}
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), output, err)
	}
	return strings.TrimSpace(string(output))
}
