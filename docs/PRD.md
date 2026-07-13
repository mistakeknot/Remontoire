---
artifact_type: prd
project: Remontoire
version: 0.1
epic: Revel-6ep
status: accepted
---

# Remontoire v0.1 Product Requirements

## Problem

The Sylveste portfolio has strong execution, research, tracking, and evidence
components, but no single agent owns the recurring decision loop that converts
their outputs into the next bounded portfolio experiment. Roadmaps can be
generated and work can be executed, yet reviewed outcomes do not reliably
change what the factory chooses next.

## Goal

Ship a standalone L2 portfolio agency on zklw whose scheduled cycle can research
and rank canonical opportunities, propose at most one deduplicated P4
experiment, execute it only after explicit approval through an existing harness,
review the measured outcome, compound it into future scoring and the roadmap,
and emit a replayable signed receipt. Production landing remains human-gated.

## Users

- Principal: sets intent, approves experiments, and lands production changes.
- Portfolio operator: inspects cycles, evidence contracts, receipts, and health.
- Execution harness: performs bounded work under a fixed contract.
- Downstream factory agents: consume promoted work and updated roadmaps.

## Critical User Journey

1. The scheduled service acquires the portfolio lock and snapshots canonical
   Beads, Intercore, Ockham, and roadmap evidence.
2. A read-only judge returns ranked opportunities and either a no-op or one
   evidence contract.
3. In proposal mode, Remontoire validates and deduplicates the choice, creates a
   P4 experiment bead, and pauses at `awaiting_approval`.
4. The principal inspects the contract and approves its immutable hash.
5. The cycle resumes in an isolated worktree through the approved harness,
   enforcing time, cost, path, network, and production-action limits.
6. Remontoire measures the benchmark and obtains an independent structured
   review.
7. It promotes or closes the experiment with evidence, records feedback,
   regenerates the roadmap, and emits a signed receipt.
8. The next scheduled cycle includes the prior outcome in its score context.

## Functional Requirements

### F1 - Canonical observation

Read bounded snapshots from Beads and Intercore, plus Ockham and roadmap context
when available. Hash and register every nondeterministic input for replay.

### F2 - Structured ranking

Invoke an existing harness in read-only mode with a versioned JSON schema. Rank
up to five opportunities using impact, uncertainty, cost, risk, and policy fit,
with evidence references and an explicit no-op option.

### F3 - Deterministic eligibility

Reject proposals without a complete evidence contract, source support, an
allowed repository, bounded budgets, a permitted benchmark, or a valid P4
experiment shape. Model output cannot override policy.

### F4 - One candidate and deduplication

Create no more than one experiment per cycle. Compute a stable fingerprint and
check both canonical Beads labels and prior cycle state before mutation.

### F5 - Explicit approval

Persist actor, timestamp, cycle ID, and evidence-contract hash. Execute only
when the current contract exactly matches the approved hash.

### F6 - Bounded existing-harness execution

Support Codex and Claude backends behind one interface. Run in an isolated
worktree with no network or production permissions, bounded time and turns or
cost, and pre/post allowed-path enforcement.

### F7 - Measured outcome

Run the evidence contract's benchmark independently, capture stdout, stderr,
exit status, duration, and parsed primary metric, and never substitute model
narration for measurement.

### F8 - Independent review

Review the contract, diff, transcript, and measurement in read-only mode using
a versioned schema. A deterministic policy check must agree before promotion.

### F9 - Backlog and discovery feedback

Close the experiment or create one promotion bead with evidence and dependency
links. Record discovery feedback and an outcome summary that later rankings
consume.

### F10 - Roadmap regeneration

Invoke the existing Interpath roadmap sync after backlog mutation and include
the resulting digest in the terminal receipt.

### F11 - Replayable receipt and recovery

Persist cycle state and idempotency keys in Intercore, maintain an atomic local
projection, register replay inputs, and sign one terminal receipt. Resuming an
interrupted stage must not duplicate any side effect.

### F12 - Scheduling and operations

Ship a user-level systemd service and timer for zklw, a health/status command,
structured logs, a dry-run configuration check, and an operator runbook.

## Non-Functional Requirements

- Fail closed at approval, path, budget, and production-action boundaries.
- Use no private operational database.
- Never place secrets or full environment dumps in prompts, logs, or receipts.
- Bound observation and harness payload sizes.
- Produce deterministic JSON suitable for automation.
- Keep core domain and policy tests free of external process dependencies.
- Run on macOS for development and Linux on zklw for deployment.

## Default Policy

- Portfolio: Sylveste only.
- Scheduled mode: proposal, one active cycle at a time.
- Candidate cap: one P4 experiment per cycle.
- Approval: human required before execution.
- Production landing: human required, never performed by Remontoire.
- Judge: read-only Codex or Claude backend.
- Executor: approved Codex or Claude backend in an isolated worktree.
- Reviewer: read-only backend distinct from executor when available.

## Acceptance Tests

- A fixture cycle ranks candidates and creates exactly one P4 bead despite
  repeated resume attempts.
- A duplicate fingerprint results in a no-op and no second bead.
- Execution without approval, with a stale approval hash, or outside allowed
  paths fails before the harness can mutate canonical work.
- An interrupted post-execution cycle resumes at review without a second
  executor call.
- A reviewed result changes the feedback input seen by the next ranking.
- Roadmap output changes after promotion or closure and its digest enters the
  receipt.
- Receipt replay reproduces the decision from stored input artifacts without
  live source reads.
- A deployed zklw timer starts a cycle, survives restart, and reports healthy.
- A live approved canary completes through an installed harness without any
  production push or merge.

## Out of Scope for v0.1

- Multiple simultaneous portfolio bets.
- Autonomous production landing or deployment.
- A custom model runtime or embedded agent loop.
- Replacement of Beads, Intercore, Ockham, Interpath, or Clavain.
- Automatic authority promotion beyond the explicit experiment gate.
- Cross-portfolio allocation outside Sylveste.
