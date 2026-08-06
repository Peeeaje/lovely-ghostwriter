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
- recheck a pull request when its head changes during a long review
- optionally turn patchable blocking findings into one patch pull request
- cancel or reject queued and running reviews
- retain review logs, artifacts, and stale-run evidence
- send clickable review and detection notifications
- run continuously as a daemon
- install itself as a macOS LaunchAgent

The original pull request branch is never edited. Optional patch mode edits only an isolated worktree and pushes a separate patch branch after revalidation.

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
instructions = ""
max_head_rechecks = 3

[patch]
enabled = false
command = "codex"
model = "gpt-5.6-sol"
reasoning_effort = "xhigh"
sandbox = "workspace-write"
max_iterations = 2
branch_prefix = "develop/codex-auto-fix"
title_prefix = "[codex-auto-fix]"
extra_args = []
instructions = ""

[notification]
enabled = false
command = "auto"
timeout = "5s"
started = true
finished = true
failed = true
detected = true

[[repository]]
name = "owner/repository"
path = "~/src/repository"
base_branches = ["main"]
authors = ["alice", "bob"]
reviewers = ["your-github-login"]
teams = []
exclude_authors = ["app/dependabot", "app/renovate", "dependabot[bot]", "renovate[bot]"]
include_drafts = false
initial_trigger = "review-request"
update_trigger = "review-request"
```

An empty `authors` list allows every author except `exclude_authors`. With the default trigger, a pull request must be requested from one of the configured users or teams. Pull requests targeting `base_branches` are queued; other matching pull requests are recorded as detected only.

`initial_trigger` and `update_trigger` accept `review-request`, `always`, or `manual`. The default keeps both phases gated by a matching GitHub review request.

`workspace-write` confines writes to the review artifacts and worktree. A host Docker socket outside those directories can be exposed explicitly with `extra_args` instead of granting unrestricted access. See [Troubleshooting](docs/troubleshooting.md#docker-works-in-the-terminal-but-not-in-a-review).

Append trusted review guidance with `review.instructions`. Each repository can override review settings without changing the global defaults:

```toml
[[repository]]
name = "owner/repository"
# ...

[repository.review]
model = "gpt-5.6-sol"
reasoning_effort = "high"
post_reviews = true
instructions = "Prioritize API compatibility."

[repository.patch]
enabled = true
instructions = "Run the repository's required checks and clean up resources started by the review."
```

Patch mode asks Codex to orchestrate review, patchable blocking fixes, and re-review in the dedicated worktree. The host process checks the current head before creating one patch pull request. The review remains `PATCH_PROPOSED` until that patch is incorporated into the original pull request. Cross-repository pull requests remain review-only.

On macOS, `notification.command = "auto"` prefers `terminal-notifier` and falls back to `osascript`. Started, finished, and failed notifications include the pull request title. A newly detected pull request outside `base_branches` also sends one notification; a single-item notification opens the pull request when clicked. `notification.timeout` prevents a broken notifier from blocking review workers.

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

# Include completed, stale, and rejected history
lovely-ghostwriter status --all

# Manually queue any open pull request, including a draft
lovely-ghostwriter enqueue owner/repository#123

# Stop and locally reject the current head
lovely-ghostwriter cancel owner/repository#123

# Read or follow review logs
lovely-ghostwriter logs owner/repository#123 --tail 200
lovely-ghostwriter logs --follow

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

After changing `config.toml`, restart the daemon so new review processes use the updated settings. See [Troubleshooting](docs/troubleshooting.md#configuration-changes-do-not-take-effect).

## State

Runtime state and logs are stored separately from configuration:

```text
~/.local/state/lovely-ghostwriter/state.db
~/.local/state/lovely-ghostwriter/lovely-ghostwriter.log
```

`XDG_CONFIG_HOME` and `XDG_STATE_HOME` override these base directories.

SQLite uses WAL mode and treats `(repository, pull request number, head SHA)` as the identity of a detected revision. Only one head of a pull request runs at a time. If the head advances, the existing run keeps its artifacts, retargets to the current head, and rechecks before posting. Advances to the base branch do not restart a review; the recorded base SHA remains the diff anchor for that head. A closed or merged pull request is stopped and recorded as stale.

Each review uses a dedicated Git worktree. The daemon removes the worktree and its temporary refs mechanically on success, failure, cancellation, and head retargeting. Repository-specific processes started by Codex, such as Docker Compose or development servers, must be stopped by Codex before it returns the review result.

## Roadmap

1. GitHub Releases and Homebrew distribution
2. Optional macOS menu bar UI

## License

[MIT](LICENSE)
