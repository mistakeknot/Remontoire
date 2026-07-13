# zklw Operations

Remontoire runs on zklw as the `mk` user. The systemd user timer starts one
proposal cycle per day. A scheduled cycle may observe, rank, and create at most
one deduplicated P4 experiment; it cannot approve execution or land production
changes.

## Installed Layout

| Artifact | Path |
| --- | --- |
| Binary | `~/.local/bin/remontoire` |
| Operator config | `~/.config/remontoire/config.json` |
| Schemas | `~/.local/share/remontoire/schemas/` |
| Cycle evidence | `~/.local/state/remontoire/cycles/` |
| Codex runtime home | `~/.local/state/remontoire/codex/` |
| Experiment worktrees | `~/projects/.worktrees/remontoire/` |
| User units | `~/.config/systemd/user/remontoire.{service,timer}` |

The config contains paths and backend choices only. Do not add credentials to
the config or unit. The proposal unit gives Codex a dedicated writable runtime
home and binds only `~/.codex/auth.json` into it. That one file remains writable
so Codex can persist token refreshes; the rest of `~/.codex` stays read-only.
Claude uses the existing `mk` user session during manual approved execution or
review.

## Install Without Activation

Use the pushed `main` revision. Source moves between the Mac and zklw through
GitHub, not Mutagen.

```bash
mkdir -p ~/projects/Sylveste/os
git clone https://github.com/mistakeknot/Remontoire.git ~/projects/Sylveste/os/Remontoire
cd ~/projects/Sylveste/os/Remontoire
git pull --ff-only
scripts/install.sh --check
scripts/install.sh --no-enable
```

Skip the clone command when the checkout already exists. Do not initialize a
separate Beads tracker in the repository; Remontoire uses the canonical tracker
from `~/projects`.

The installer resolves dependency binaries to absolute paths, builds
Remontoire when `--binary` is not supplied, preserves an existing config, and
reloads the user systemd manager. Use `--force-config` only after reviewing the
replacement paths.

On reinstall, the preserved config remains authoritative: the installer reads
its project, artifact, and worktree paths with `jq` before regenerating
the sandboxed unit. A plain install never enables the timer; activation requires
either `--enable` or the explicit `systemctl` command below.

## Preflight

Run every check before the first service invocation:

```bash
~/.local/bin/remontoire --config="$HOME/.config/remontoire/config.json" doctor --json
test -r "$HOME/.codex/auth.json" && test -w "$HOME/.codex/auth.json"
systemd-analyze verify ~/.config/systemd/user/remontoire.service ~/.config/systemd/user/remontoire.timer
systemctl --user status remontoire.timer --no-pager
systemctl --user is-enabled remontoire.timer
```

Before the canary, `is-enabled` must report `disabled`, and no
`remontoire.service` process should be active.

The proposal unit waits up to five minutes for external DNS before creating a
cycle. Its writable paths are limited to Intercore state, Beads state, and
Remontoire's own state/cache. It cannot write source repositories or agent
configuration; approved experiment execution is a separate manual CLI action.

## Manual Canary

Start exactly one proposal cycle through the deployed unit:

```bash
export PATH="/usr/local/go/bin:${PATH}"
systemctl --user start remontoire.service
systemctl --user status remontoire.service --no-pager
journalctl --user -u remontoire.service -n 100 --no-pager -o cat
~/.local/bin/remontoire --config="$HOME/.config/remontoire/config.json" status --json
```

A candidate cycle stops at `awaiting_approval`. A no-op or failed cycle must be
terminal and carry a signed receipt. For a terminal cycle, verify its stored
evidence offline:

```bash
~/.local/bin/remontoire --config="$HOME/.config/remontoire/config.json" receipt show CYCLE_ID --json
~/.local/bin/remontoire --config="$HOME/.config/remontoire/config.json" receipt replay CYCLE_ID --json
```

Do not approve the canary until its P4 Bead, evidence contract, repository
scope, budget, and benchmark command have been reviewed. Production push,
merge, and deployment remain outside Remontoire's authority.

## Enable The Schedule

Enable the timer only after the canary and receipt checks pass:

```bash
systemctl --user enable --now remontoire.timer
systemctl --user list-timers remontoire.timer --all --no-pager
```

The timer is persistent and may immediately run a missed occurrence when first
enabled. Confirm that no cycle is already active before enabling it.

## Recovery

Intercore holds the cycle lock and durable stage state. If a service is
interrupted, inspect the latest cycle and resume that exact ID instead of
starting a second cycle:

```bash
export PATH="/usr/local/go/bin:${PATH}"
~/.local/bin/remontoire --config="$HOME/.config/remontoire/config.json" status --json
~/.local/bin/remontoire --config="$HOME/.config/remontoire/config.json" resume CYCLE_ID --json
```

Use the journal for process failures and the cycle receipt for decision and
evidence failures:

```bash
journalctl --user -u remontoire.service --since today --no-pager
```

## Disable Or Remove

Disable scheduling without deleting evidence:

```bash
systemctl --user disable --now remontoire.timer
```

Confirm the service is inactive before uninstalling. Uninstall removes the
binary, schemas, and units, but preserves operator config and cycle state:

```bash
systemctl --user is-active remontoire.service
scripts/install.sh --uninstall
```
