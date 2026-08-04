# lovely-ghostwriter

`lovely-ghostwriter` is a local-first daemon that watches GitHub pull requests and orchestrates Codex reviews.

The project is under active development. The current implementation can:

- load repository rules from TOML
- detect matching GitHub review requests with `gh`
- classify pull requests as automatically queued or manually triggered
- persist state safely in SQLite
- consume the review queue with bounded concurrency
- create an isolated Git worktree for each review
- run Codex in dry-run or GitHub posting mode
- verify a hidden review marker before recording a posted review as successful
- run continuously as a daemon
- install itself as a macOS LaunchAgent

The review worker is intentionally review-only. It does not edit the original pull request branch or create patch pull requests yet.

## Requirements

- Git
- [GitHub CLI](https://cli.github.com/) authenticated with `gh auth login`
- [Codex CLI](https://github.com/openai/codex)

Go is required only when installing from source. The repository includes a Nix development shell.

## Install from source

```sh
go install github.com/Peeeaje/lovely-ghostwriter/cmd/lovely-ghostwriter@latest
```

For development:

```sh
nix develop
go test ./...
go run ./cmd/lovely-ghostwriter version
```

## Configure

Create the default configuration:

```sh
lovely-ghostwriter init
```

By default this writes:

```text
~/.config/lovely-ghostwriter/config.toml
```

Edit the generated file:

```toml
[daemon]
poll_interval = "3m"
max_concurrency = 3

[review]
command = "codex"
model = "gpt-5.6-sol"
reasoning_effort = "high"
sandbox = "workspace-write"
marker = "codex-auto-review"
post_reviews = false # change to true after validating a dry-run
extra_args = []

[[repository]]
name = "owner/repository"
path = "~/src/repository"
base_branches = ["main"]
authors = ["alice", "bob"]
reviewers = ["your-github-login"]
teams = []
exclude_authors = ["app/dependabot", "app/renovate", "dependabot[bot]", "renovate[bot]"]
include_drafts = false
```

An empty `authors` list allows every author except `exclude_authors`. A pull request must be requested from one of the configured users or teams. Pull requests targeting `base_branches` are queued; other matching pull requests are recorded as detected only.

Validate the local environment before starting the daemon:

```sh
lovely-ghostwriter doctor
```

## Use

```sh
# Scan once
lovely-ghostwriter scan

# Inspect current state
lovely-ghostwriter status

# Manually queue any open pull request, including a draft
lovely-ghostwriter enqueue owner/repository#123

# Run the current queue in the foreground
lovely-ghostwriter run-queue

# Run in the foreground
lovely-ghostwriter daemon
```

Use `--config` and `--state` before the command to override their default paths:

```sh
lovely-ghostwriter --config ./config.toml --state ./state.db scan
```

## Start at login on macOS

```sh
lovely-ghostwriter service install
```

This creates and loads:

```text
~/Library/LaunchAgents/io.github.peeeaje.lovely-ghostwriter.plist
```

Remove it with:

```sh
lovely-ghostwriter service uninstall
```

The service is restarted by `launchd` after a crash and starts again after login. Each daemon scan also starts queued reviews up to `max_concurrency`.

## State

Runtime state and logs are stored separately from configuration:

```text
~/.local/state/lovely-ghostwriter/state.db
~/.local/state/lovely-ghostwriter/lovely-ghostwriter.log
```

`XDG_CONFIG_HOME` and `XDG_STATE_HOME` override these base directories.

SQLite uses WAL mode and treats `(repository, pull request number, head SHA)` as the identity of a detected revision. This keeps repeated scans idempotent and avoids shared-file update races between review workers.

## Roadmap

1. Patch pull request orchestration for blocking findings
2. Queue retry, cancel, and crash recovery
3. Clickable desktop notifications
4. Homebrew distribution
5. Optional macOS menu bar UI

## License

[MIT](LICENSE)
