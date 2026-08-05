# Troubleshooting

## Docker works in the terminal but not in a review

Typical error:

```text
permission denied while trying to connect to the docker API at unix://...
```

First verify Docker outside the review:

```sh
docker version
```

If that succeeds, check `review.sandbox` and `patch.sandbox`. The default
`workspace-write` sandbox allows writes to the review artifacts and worktree,
but a host Docker socket normally lives outside both. Use
`danger-full-access` for stages that must run container-based checks:

```toml
[review]
sandbox = "danger-full-access"

[patch]
sandbox = "danger-full-access"
```

Docker socket access effectively gives the review process broad control over
the host. Only enable it for repositories and prompts you trust.

## Configuration changes do not take effect

The daemon reads `config.toml` when it starts. Restart it after changing the
configuration:

```sh
lovely-ghostwriter service uninstall
lovely-ghostwriter service install
```

If another service manager owns the LaunchAgent, restart it through that
manager instead of reinstalling it manually.

Confirm the active process and current state:

```sh
launchctl print "gui/$(id -u)/io.github.peeeaje.lovely-ghostwriter"
lovely-ghostwriter status
```

## Commands work interactively but fail under launchd

`launchd` does not inherit the full PATH from an interactive shell. Use an
absolute `command` path in `config.toml`, or ensure the installed LaunchAgent
provides a PATH containing `codex`, `gh`, `git`, and repository-specific tools.

Inspect the effective command and logs with:

```sh
launchctl print "gui/$(id -u)/io.github.peeeaje.lovely-ghostwriter"
lovely-ghostwriter logs --follow
```

## Worktrees or containers remain after a review

lovely-ghostwriter mechanically removes the Git worktree and temporary refs
after success, failure, cancellation, or head retargeting. Repository-specific
resources created inside the review, such as Docker Compose projects or
development servers, must be stopped by Codex before it returns.

Put the repository's startup and cleanup commands in its agent instructions,
and reinforce them with `review.instructions` or `patch.instructions`. Check
the current state and review log before removing anything manually:

```sh
lovely-ghostwriter status
lovely-ghostwriter logs owner/repository#123 --tail 200
```
