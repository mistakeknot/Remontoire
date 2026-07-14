# Remontoire

Remontoire is the portfolio agency for the Sylveste software factory. It turns
canonical backlog, discovery, operating, and policy evidence into one bounded
experiment at a time, then compounds the result into the next decision.

Remontoire is a standalone L2 project. It chooses which uncertainty is worth
resolving next. It does not replace the systems around it:

- Beads owns backlog truth.
- Intercore owns durable state, locks, events, replay inputs, and receipts.
- Ockham owns strategic intent and dispatch policy.
- Codex, Claude, Skaffen, and Clavain are execution harnesses.
- Interpath owns generated roadmap artifacts.

The v0.1 loop is:

```text
observe -> rank -> propose -> approve -> execute -> review -> compound
```

One cycle may create at most one deduplicated P4 experiment. Execution requires
explicit human approval. Production merges and pushes are outside Remontoire's
authority.

## Install

The supported service deployment is Linux with a systemd user manager. Source
moves between Clavain and zklw through GitHub.

```bash
mkdir -p ~/projects/Sylveste/os
git clone https://github.com/mistakeknot/Remontoire.git ~/projects/Sylveste/os/Remontoire
cd ~/projects/Sylveste/os/Remontoire
git pull --ff-only
scripts/install.sh --check
scripts/install.sh --no-enable
```

Skip the clone command when the checkout already exists. A normal install
preserves existing configuration and cycle evidence and leaves the daily timer
disabled until its first canary has been reviewed.

Verify the installed runtime:

```bash
~/.local/bin/remontoire doctor --json
~/.local/bin/remontoire status --json
~/.local/bin/remontoire attention --json
```

The default config is `~/.config/remontoire/config.json`. Set
`REMONTOIRE_CONFIG` or pass `--config` only when operating another deployment.

## Autonomous Use

The scheduled service is exception-driven. Agent harnesses should call only
`remontoire attention --json` at session start. That read-only projection
contains the latest canonical cycle plus ready beads labeled
`remontoire-promotion`; it does not create, approve, resume, or repair work.

Consumers stay silent for normal and terminal cycle stages. They surface an
operator command only when a cycle needs a principal approval decision, an
explicit resume, receipt recovery, or diagnosis. Promotion beads enter the
normal next-goal ranking alongside other ready work. Selecting a promotion with
`/goal` starts downstream implementation and does not alter its source cycle.

## Run A Cycle

Use shadow mode to exercise observation and ranking without creating backlog
work:

```bash
~/.local/bin/remontoire cycle --mode=shadow --json
```

Proposal mode may create one deduplicated P4 experiment and then stops at
`awaiting_approval`:

```bash
~/.local/bin/remontoire cycle --mode=proposal --json
~/.local/bin/remontoire status --json
```

Inspect the cycle's repository, allowed paths, evidence contract, budget,
benchmark, and contract hash before making a principal decision. Approval and
execution are separate commands:

```bash
~/.local/bin/remontoire approve CYCLE_ID --actor="$USER" --json
~/.local/bin/remontoire resume CYCLE_ID --json
```

Decline a proposal without executing it:

```bash
~/.local/bin/remontoire decline CYCLE_ID --actor="$USER" --reason="REASON" --json
```

Inspect and replay terminal evidence:

```bash
~/.local/bin/remontoire receipt show CYCLE_ID --json
~/.local/bin/remontoire receipt replay CYCLE_ID --json
```

After a manual canary and receipt verification, enable the daily proposal
schedule:

```bash
systemctl --user enable --now remontoire.timer
systemctl --user list-timers remontoire.timer --all --no-pager
```

The detailed deployment, recovery, and uninstall procedures remain in the
[`zklw operations runbook`](docs/operations/zklw.md).

## First-Class Agency Contract

[`agency.json`](agency.json) is Remontoire's machine-readable
`interverse.agency/v1` identity. It declares installation, runtime,
capabilities, durable contracts, and authority boundaries for Interverse,
Intercore, and Clavain consumers. Remontoire remains an L2 agency rather than an
Interverse plugin or a Clavain fleet worker.

Every cycle run records canonical producer metadata (`kind=agency`,
`name=remontoire`, `class=portfolio`). Stage transitions emit typed
`source=agency`, `type=agency.stage` events, so `ic events list-agency` provides
history and `ic situation snapshot` provides the latest operator status.

The ownership boundary is deliberate:

- Remontoire owns portfolio judgment and its CLI lifecycle.
- Interverse owns static agency discovery and installation.
- Intercore owns durable runtime state, events, replay inputs, and receipts.
- Clavain provides a thin human-facing operator adapter.
- Beads, Ockham, and Interpath retain backlog, policy, and roadmap ownership.

## Status

v0.1 is deployed on zklw and remains under active development. The product
contract is in
[`docs/PRD.md`](docs/PRD.md), the implementation plan is in
[`docs/plans/2026-07-13-remontoire-v0.1.md`](docs/plans/2026-07-13-remontoire-v0.1.md),
and the zklw deployment runbook is in
[`docs/operations/zklw.md`](docs/operations/zklw.md).

## License

MIT
