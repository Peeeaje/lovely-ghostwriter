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
	"strings"
	"syscall"
	"time"

	"github.com/Peeeaje/lovely-ghostwriter/internal/config"
	gh "github.com/Peeeaje/lovely-ghostwriter/internal/github"
	"github.com/Peeeaje/lovely-ghostwriter/internal/paths"
	"github.com/Peeeaje/lovely-ghostwriter/internal/scanner"
	"github.com/Peeeaje/lovely-ghostwriter/internal/service"
	"github.com/Peeeaje/lovely-ghostwriter/internal/state"
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
	statePath := flags.String("state", "", "path to state.db (defaults beside config)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	remaining := flags.Args()
	if len(remaining) == 0 {
		usage(stdout)
		return nil
	}
	opts := options{configPath: *configPath, statePath: *statePath}
	if opts.statePath == "" {
		opts.statePath = filepath.Join(filepath.Dir(opts.configPath), "state.db")
	}

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
	case "status":
		return status(context.Background(), opts, stdout)
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
  status               Show detected and queued pull requests
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
	return nil
}

func status(ctx context.Context, opts options, out io.Writer) error {
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
	prs, err := store.Active(ctx)
	if err != nil {
		return err
	}
	for _, pr := range prs {
		fmt.Fprintf(out, "- %s#%d [%s] %s\n  %s\n", pr.Repository, pr.Number, pr.Status, pr.Title, pr.URL)
	}
	return nil
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
	for {
		started := time.Now()
		if err := scanOnce(ctx, opts, out); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(out, "%s scan failed: %v\n", time.Now().Format(time.RFC3339), err)
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

func doctor(opts options, out io.Writer) error {
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return err
	}

	commands := []string{"git", "gh", cfg.Review.Command}
	for _, command := range commands {
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
