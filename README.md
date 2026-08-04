# lovely-ghostwriter

`lovely-ghostwriter` is a local-first daemon that watches GitHub pull requests and orchestrates Codex reviews.

The project is under active development. The current implementation can:

- load repository rules from TOML
- detect matching GitHub review requests with `gh`
- classify pull requests as automatically queued or manually triggered
- persist state safely in SQLite
- run continuously as a daemon
- install itself as a macOS LaunchAgent

It does **not yet execute Codex reviews or post to GitHub**. Queued pull requests remain queued until the review worker is implemented.

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

On macOS this writes:

```text
~/Library/Application Support/lovely-ghostwriter/config.toml
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

The service is restarted by `launchd` after a crash and starts again after login.

## State

Runtime state is stored separately from configuration:

```text
~/Library/Application Support/lovely-ghostwriter/state.db
```

SQLite uses WAL mode and treats `(repository, pull request number, head SHA)` as the identity of a detected revision. This keeps repeated scans idempotent and avoids shared-file update races when review workers are added.

## Roadmap

1. Codex review worker with isolated Git worktrees
2. Explicit GitHub posting policy and review markers
3. Queue, retry, cancel, and crash recovery
4. Clickable desktop notifications
5. Homebrew distribution
6. Optional macOS menu bar UI

## License

[MIT](LICENSE)
