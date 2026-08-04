package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Peeeaje/lovely-ghostwriter/internal/config"
	gh "github.com/Peeeaje/lovely-ghostwriter/internal/github"
	"github.com/Peeeaje/lovely-ghostwriter/internal/lock"
	"github.com/Peeeaje/lovely-ghostwriter/internal/paths"
	"github.com/Peeeaje/lovely-ghostwriter/internal/reviewer"
	"github.com/Peeeaje/lovely-ghostwriter/internal/scanner"
	"github.com/Peeeaje/lovely-ghostwriter/internal/service"
	"github.com/Peeeaje/lovely-ghostwriter/internal/state"
	"github.com/Peeeaje/lovely-ghostwriter/internal/worktree"
)

var version = "dev"

type options struct {
	configPath string
	statePath  string
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "lovely-ghostwriter: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	defaultPaths, err := paths.Default()
	if err != nil {
		return err
	}

	flags := flag.NewFlagSet("lovely-ghostwriter", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", defaultPaths.Config, "path to config.toml")
	statePath := flags.String("state", defaultPaths.State, "path to state.db")
	if err := flags.Parse(args); err != nil {
		return err
	}
	remaining := flags.Args()
	if len(remaining) == 0 {
		usage(stdout)
		return nil
	}
	opts := options{configPath: *configPath, statePath: *statePath}

	switch remaining[0] {
	case "init":
		if err := config.WriteDefault(opts.configPath); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "created %s\n", opts.configPath)
		return nil
	case "doctor":
		return doctor(opts, stdout)
	case "scan":
		return scanOnce(context.Background(), opts, stdout)
	case "enqueue":
		return enqueue(context.Background(), opts, remaining[1:], stdout)
	case "cancel", "reject":
		return cancel(context.Background(), opts, remaining[1:], stdout)
	case "run-queue":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runQueue(ctx, opts, stdout)
	case "status", "list":
		return status(context.Background(), opts, remaining[1:], stdout)
	case "watch-running":
		cfg, store, err := openConfiguredStore(opts)
		if err != nil {
			return err
		}
		defer store.Close()
		return watchRunning(context.Background(), cfg, store, stdout)
	case "logs":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return logs(ctx, opts, remaining[1:], stdout, stderr)
	case "daemon":
		return daemon(opts, stdout)
	case "service":
		if len(remaining) != 2 {
			return errors.New("usage: lovely-ghostwriter service install|uninstall")
		}
		return manageService(remaining[1], opts, defaultPaths, stdout)
	case "version", "--version", "-version":
		fmt.Fprintln(stdout, version)
		return nil
	case "help", "--help", "-h":
		usage(stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q", remaining[0])
	}
}

func usage(out io.Writer) {
	fmt.Fprintln(out, `lovely-ghostwriter watches GitHub pull requests and queues Codex reviews.

Usage:
  lovely-ghostwriter [--config PATH] [--state PATH] COMMAND

Commands:
  init                 Create a starter TOML configuration
  doctor               Validate configuration and external dependencies
  scan                 Scan configured repositories once
  enqueue REPO#NUMBER  Queue one pull request (--force allows a rerun)
  cancel REPO#NUMBER   Stop and locally reject the current pull request head
  reject REPO#NUMBER   Alias for cancel
  run-queue            Run queued reviews and wait for completion
  status               Show detected and queued pull requests
  watch-running        Stop running reviews whose pull request is closed
  logs                 Show review logs (--follow, --tail N, --all)
  daemon               Scan continuously
  service install      Start at login using a macOS LaunchAgent
  service uninstall    Remove the macOS LaunchAgent
  version              Print the version`)
}

func openConfiguredStore(opts options) (config.Config, *state.Store, error) {
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return config.Config{}, nil, err
	}
	store, err := state.Open(opts.statePath)
	if err != nil {
		return config.Config{}, nil, err
	}
	return cfg, store, nil
}

func scanOnce(ctx context.Context, opts options, out io.Writer) error {
	cfg, store, err := openConfiguredStore(opts)
	if err != nil {
		return err
	}
	defer store.Close()

	result, err := scanner.New(gh.NewClient(gh.ExecRunner{}), store).Scan(ctx, cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "queued=%d detected=%d skipped=%d\n", result.Queued, result.Detected, result.Skipped)
	if err := reviewer.NotifyDetected(ctx, cfg, result.DetectedPullRequests); err != nil {
		fmt.Fprintf(out, "notification warning: %v\n", err)
	}
	return nil
}

func scanConfigured(ctx context.Context, cfg config.Config, store *state.Store, out io.Writer) error {
	result, err := scanner.New(gh.NewClient(gh.ExecRunner{}), store).Scan(ctx, cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "queued=%d detected=%d skipped=%d\n", result.Queued, result.Detected, result.Skipped)
	if err := reviewer.NotifyDetected(ctx, cfg, result.DetectedPullRequests); err != nil {
		fmt.Fprintf(out, "notification warning: %v\n", err)
	}
	return nil
}

func status(ctx context.Context, opts options, args []string, out io.Writer) error {
	all := len(args) == 1 && args[0] == "--all"
	if len(args) > 1 || len(args) == 1 && !all {
		return errors.New("usage: lovely-ghostwriter status [--all]")
	}
	store, err := state.Open(opts.statePath)
	if err != nil {
		return err
	}
	defer store.Close()

	counts, err := store.Counts(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "queued=%d detected=%d running=%d failed=%d\n",
		counts[state.StatusQueued], counts[state.StatusDetected], counts[state.StatusRunning], counts[state.StatusFailed])
	if all {
		fmt.Fprintf(out, "reviewed=%d completed=%d stale=%d rejected=%d\n",
			counts[state.StatusReviewed], counts[state.StatusCompleted], counts[state.StatusStale], counts[state.StatusRejected])
	}
	prs, err := store.PullRequests(ctx, all)
	if err != nil {
		return err
	}
	for _, pr := range prs {
		fmt.Fprintf(out, "- %s#%d [%s] %s\n  %s\n", pr.Repository, pr.Number, pr.Status, pr.Title, pr.URL)
	}
	runs, err := store.Runs(ctx, all)
	if err != nil {
		return err
	}
	for _, run := range runs {
		fmt.Fprintf(out, "  run=%d %s#%d [%s] attempt=%d\n", run.ID, run.Repository, run.Number, run.Status, run.Attempt)
		if run.LogPath != "" {
			fmt.Fprintf(out, "    log=%s\n", run.LogPath)
		}
		if run.ArtifactPath != "" {
			fmt.Fprintf(out, "    artifacts=%s\n", run.ArtifactPath)
		}
		if run.Error != "" {
			fmt.Fprintf(out, "    error=%s\n", run.Error)
		}
	}
	return nil
}

func cancel(ctx context.Context, opts options, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: lovely-ghostwriter cancel OWNER/REPOSITORY#NUMBER [...] | NUMBER [...]")
	}
	cfg, store, err := openConfiguredStore(opts)
	if err != nil {
		return err
	}
	defer store.Close()
	for _, arg := range args {
		if strings.Contains(arg, "#") {
			repository, number, err := parsePullRequestRef(arg)
			if err != nil {
				return err
			}
			if err := requestCancel(ctx, store, repository, number, out); err != nil {
				return err
			}
			continue
		}
		number, err := strconv.Atoi(arg)
		if err != nil || number < 1 {
			return fmt.Errorf("invalid pull request number %q", arg)
		}
		active, err := store.PullRequests(ctx, false)
		if err != nil {
			return err
		}
		repositories := activeRepositories(cfg, active, number)
		if len(repositories) == 0 {
			return fmt.Errorf("pull request #%d is not active", number)
		}
		if len(repositories) > 1 {
			return fmt.Errorf("pull request #%d is active in %s; specify OWNER/REPOSITORY#NUMBER", number, strings.Join(repositories, ", "))
		}
		if err := requestCancel(ctx, store, repositories[0], number, out); err != nil {
			return err
		}
	}
	return nil
}

func activeRepositories(cfg config.Config, pullRequests []state.PullRequest, number int) []string {
	configured := make(map[string]bool, len(cfg.Repositories))
	for _, repository := range cfg.Repositories {
		configured[repository.Name] = true
	}
	matches := map[string]bool{}
	for _, pullRequest := range pullRequests {
		if pullRequest.Number == number && configured[pullRequest.Repository] {
			matches[pullRequest.Repository] = true
		}
	}
	var repositories []string
	for repository := range matches {
		repositories = append(repositories, repository)
	}
	sort.Strings(repositories)
	return repositories
}

func requestCancel(ctx context.Context, store *state.Store, repository string, number int, out io.Writer) error {
	rows, err := store.RequestCancel(ctx, repository, number)
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("pull request %s#%d is not active", repository, number)
	}
	fmt.Fprintf(out, "cancel requested %s#%d\n", repository, number)
	return nil
}

func logs(ctx context.Context, opts options, args []string, stdout, stderr io.Writer) error {
	all, follow, tailLines := false, false, 200
	filter := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--all":
			all = true
		case "--follow":
			follow = true
		case "--tail":
			i++
			if i >= len(args) {
				return errors.New("--tail requires a positive number")
			}
			value, err := strconv.Atoi(args[i])
			if err != nil || value < 1 {
				return errors.New("--tail requires a positive number")
			}
			tailLines = value
		default:
			if filter != "" {
				return errors.New("usage: lovely-ghostwriter logs [PR] [--tail N] [--follow] [--all]")
			}
			filter = args[i]
		}
	}
	store, err := state.Open(opts.statePath)
	if err != nil {
		return err
	}
	defer store.Close()
	runs, err := store.Runs(ctx, all || filter != "")
	if err != nil {
		return err
	}
	var paths []string
	for _, run := range runs {
		if run.LogPath == "" || !matchesRun(run, filter) || !all && filter == "" && run.Status != state.StatusRunning {
			continue
		}
		paths = append(paths, run.LogPath)
	}
	if len(paths) == 0 {
		fmt.Fprintln(stdout, "no matching review logs")
		return nil
	}
	tailArgs := []string{"-n", strconv.Itoa(tailLines)}
	if follow {
		tailArgs = append(tailArgs, "-f")
	}
	tailArgs = append(tailArgs, paths...)
	command := exec.CommandContext(ctx, "tail", tailArgs...)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil && !errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("tail logs: %w", err)
	}
	return nil
}

func matchesRun(run state.Run, filter string) bool {
	if filter == "" {
		return true
	}
	if strings.Contains(filter, "#") {
		return filter == fmt.Sprintf("%s#%d", run.Repository, run.Number)
	}
	return filter == strconv.Itoa(run.Number)
}

func daemon(opts options, out io.Writer) error {
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return err
	}
	interval, err := cfg.PollInterval()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	consumerLock, err := lock.Acquire(opts.statePath + ".lock")
	if err != nil {
		return err
	}
	defer consumerLock.Close()
	store, err := state.Open(opts.statePath)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := recoverInterrupted(ctx, cfg, store, opts, out); err != nil {
		return err
	}
	pool := reviewPool(cfg, store, opts, out)
	defer pool.Wait()
	for {
		started := time.Now()
		if err := watchRunning(ctx, cfg, store, out); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(out, "%s running review check failed: %v\n", time.Now().Format(time.RFC3339), err)
		}
		if err := scanConfigured(ctx, cfg, store, out); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(out, "%s scan failed: %v\n", time.Now().Format(time.RFC3339), err)
		}
		if _, err := pool.StartAvailable(ctx); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(out, "%s queue failed: %v\n", time.Now().Format(time.RFC3339), err)
		}
		wait := interval - time.Since(started)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func watchRunning(ctx context.Context, cfg config.Config, store *state.Store, out io.Writer) error {
	runs, err := store.Runs(ctx, false)
	if err != nil {
		return err
	}
	client := gh.NewClient(gh.ExecRunner{})
	configured := make(map[string]struct{}, len(cfg.Repositories))
	for _, repository := range cfg.Repositories {
		configured[repository.Name] = struct{}{}
	}
	for _, run := range runs {
		if run.Status != state.StatusRunning {
			continue
		}
		if _, ok := configured[run.Repository]; !ok {
			continue
		}
		pr, err := client.PullRequest(ctx, run.Repository, run.Number)
		if err != nil {
			return err
		}
		if pr.State == "OPEN" {
			continue
		}
		if _, err := store.RequestStale(ctx, run.Repository, run.Number); err != nil {
			return err
		}
		fmt.Fprintf(out, "stop requested for closed pull request %s#%d run=%d\n", run.Repository, run.Number, run.ID)
	}
	return nil
}

func enqueue(ctx context.Context, opts options, args []string, out io.Writer) error {
	force := false
	var ref string
	for _, arg := range args {
		if arg == "--force" {
			force = true
			continue
		}
		if ref != "" {
			return errors.New("usage: lovely-ghostwriter enqueue [--force] OWNER/REPOSITORY#NUMBER")
		}
		ref = arg
	}
	repositoryName, number, err := parsePullRequestRef(ref)
	if err != nil {
		return err
	}
	cfg, store, err := openConfiguredStore(opts)
	if err != nil {
		return err
	}
	defer store.Close()
	var repositoryConfig config.RepositoryConfig
	for _, repository := range cfg.Repositories {
		if repository.Name == repositoryName {
			repositoryConfig = repository
			break
		}
	}
	if repositoryConfig.Name == "" {
		return fmt.Errorf("repository %s is not configured", repositoryName)
	}

	client := gh.NewClient(gh.ExecRunner{})
	pr, err := client.PullRequest(ctx, repositoryName, number)
	if err != nil {
		return err
	}
	if pr.State != "OPEN" {
		return fmt.Errorf("pull request %s is not open", ref)
	}
	reviewer, err := client.CurrentUser(ctx)
	if err != nil {
		return err
	}
	review := repositoryConfig.EffectiveReview(cfg.Review)
	if gh.HasMarker(pr, review.Marker, pr.HeadSHA, pr.BaseSHA, reviewer) && !force {
		return fmt.Errorf("pull request %s already has a review marker for head %s; use --force to rerun", ref, pr.HeadSHA)
	}
	queued, err := store.Enqueue(ctx, state.PullRequest{
		Repository: repositoryName,
		Number:     pr.Number,
		HeadSHA:    pr.HeadSHA,
		Title:      pr.Title,
		URL:        pr.URL,
		Author:     pr.Author.Login,
		BaseBranch: pr.BaseBranch,
		BaseSHA:    pr.BaseSHA,
		Manual:     true,
		Status:     state.StatusQueued,
	}, force)
	if err != nil {
		return err
	}
	if !queued {
		return fmt.Errorf("pull request %s is already queued, running, or completed for this head; use --force to rerun", ref)
	}
	fmt.Fprintf(out, "queued %s head=%s\n", ref, pr.HeadSHA)
	return nil
}

func parsePullRequestRef(ref string) (string, int, error) {
	parts := strings.Split(ref, "#")
	if len(parts) != 2 || strings.Count(parts[0], "/") != 1 {
		return "", 0, errors.New("pull request must be OWNER/REPOSITORY#NUMBER")
	}
	number, err := strconv.Atoi(parts[1])
	if err != nil || number < 1 {
		return "", 0, errors.New("pull request number must be a positive integer")
	}
	return parts[0], number, nil
}

func runQueue(ctx context.Context, opts options, out io.Writer) error {
	consumerLock, err := lock.Acquire(opts.statePath + ".lock")
	if err != nil {
		return err
	}
	defer consumerLock.Close()
	cfg, store, err := openConfiguredStore(opts)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := recoverInterrupted(ctx, cfg, store, opts, out); err != nil {
		return err
	}
	return reviewPool(cfg, store, opts, out).Drain(ctx)
}

func recoverInterrupted(ctx context.Context, cfg config.Config, store *state.Store, opts options, out io.Writer) error {
	runs, err := store.RecoverInterrupted(ctx)
	if err != nil {
		return err
	}
	manager := worktree.Manager{Root: filepath.Join(filepath.Dir(opts.statePath), "worktrees")}
	for _, run := range runs {
		for _, repository := range cfg.Repositories {
			if repository.Name != run.Repository {
				continue
			}
			sourcePath, err := repository.ExpandedPath()
			if err != nil {
				return err
			}
			if err := manager.Cleanup(context.Background(), sourcePath, run.WorktreePath, run.ID); err != nil {
				fmt.Fprintf(out, "failed to clean interrupted run=%d: %v\n", run.ID, err)
			}
			fmt.Fprintf(out, "requeued interrupted %s#%d run=%d\n", run.Repository, run.Number, run.ID)
			break
		}
	}
	return nil
}

func reviewPool(cfg config.Config, store *state.Store, opts options, out io.Writer) *reviewer.Pool {
	root := filepath.Dir(opts.statePath)
	client := gh.NewClient(gh.ExecRunner{})
	runner := &reviewer.Runner{
		Config:       cfg,
		Store:        store,
		GitHub:       client,
		Worktrees:    worktree.Manager{Root: filepath.Join(root, "worktrees")},
		ArtifactRoot: filepath.Join(root, "runs"),
	}
	return reviewer.NewPool(cfg, store, runner, out)
}

func doctor(opts options, out io.Writer) error {
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return err
	}

	commands := map[string]struct{}{"git": {}, "gh": {}, cfg.Review.Command: {}}
	for _, repository := range cfg.Repositories {
		commands[repository.EffectiveReview(cfg.Review).Command] = struct{}{}
		patch := repository.EffectivePatch(cfg.Patch)
		if patch.Enabled {
			commands[patch.Command] = struct{}{}
		}
	}
	if cfg.Notification.Enabled {
		if cfg.Notification.Command == "auto" {
			if _, terminalErr := exec.LookPath("terminal-notifier"); terminalErr != nil {
				if _, scriptErr := exec.LookPath("osascript"); scriptErr != nil {
					return errors.New("notifications require terminal-notifier or osascript")
				}
			}
		} else {
			commands[cfg.Notification.Command] = struct{}{}
		}
	}
	for command := range commands {
		path, err := exec.LookPath(command)
		if err != nil {
			return fmt.Errorf("required command %q was not found", command)
		}
		fmt.Fprintf(out, "ok command %s: %s\n", command, path)
	}
	if output, err := exec.Command("gh", "auth", "status").CombinedOutput(); err != nil {
		return fmt.Errorf("GitHub authentication failed: %s", strings.TrimSpace(string(output)))
	}
	fmt.Fprintln(out, "ok GitHub authentication")

	for _, repository := range cfg.Repositories {
		path, err := repository.ExpandedPath()
		if err != nil {
			return err
		}
		if output, err := exec.Command("git", "-C", path, "rev-parse", "--git-dir").CombinedOutput(); err != nil {
			return fmt.Errorf("repository %s path %s is not a Git checkout: %s", repository.Name, path, strings.TrimSpace(string(output)))
		}
		fmt.Fprintf(out, "ok repository %s: %s\n", repository.Name, path)
	}
	return nil
}

func manageService(action string, opts options, defaultPaths paths.Paths, out io.Writer) error {
	if runtime.GOOS != "darwin" {
		return errors.New("service management is currently supported only on macOS")
	}
	switch action {
	case "install":
		if _, err := config.Load(opts.configPath); err != nil {
			return err
		}
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve executable: %w", err)
		}
		if err := service.Install(defaultPaths.LaunchAgent, executable, opts.configPath, defaultPaths.Log); err != nil {
			return err
		}
		fmt.Fprintf(out, "installed %s\n", defaultPaths.LaunchAgent)
		return nil
	case "uninstall":
		if err := service.Uninstall(defaultPaths.LaunchAgent); err != nil {
			return err
		}
		fmt.Fprintf(out, "removed %s\n", defaultPaths.LaunchAgent)
		return nil
	default:
		return fmt.Errorf("unknown service action %q", action)
	}
}
