# Architecture

## Position

Remontoire is an L2 portfolio agency. It owns recurring portfolio judgment:
which uncertainty should be resolved next, what evidence would resolve it, and
how the observed result should affect later decisions.

It is deliberately not an agent runtime, task tracker, state database, policy
engine, or deployment system.

```text
principal intent
      |
      v
Ockham policy ----+                     +---- Beads backlog
                  |                     |
                  v                     v
            Remontoire portfolio cycle
                  |       |       |
                  |       |       +---- Interpath roadmap
                  |       +------------ Intercore state/events/receipts
                  +-------------------- Codex/Claude/Skaffen/Clavain harness
```

## Process Boundary

The `remontoire` binary is a deterministic Go orchestrator. External systems
are invoked as argv through narrow adapters:

- `bd`: reads canonical backlog and creates or updates one experiment bead.
- `ic`: acquires the cycle lock, persists state, records events and replay
  inputs, accepts feedback, and signs the terminal receipt.
- `ockham`: supplies intent-derived offsets and halt policy when available.
- `codex` or `claude`: performs structured portfolio judgment, bounded
  execution, and independent review.
- `sync-roadmap-json.sh`: regenerates the canonical machine-readable roadmap.

The first four adapters are required for an active proposal cycle except that
Ockham degrades to neutral weights when absent. A shadow cycle can operate with
read-only sources and emit a receipt without creating backlog work.

## Cycle State Machine

```text
new
  -> observing
  -> ranked
  -> no_op -> completed
  -> proposed -> awaiting_approval
  -> declined -> completed
  -> approved -> executing
  -> reviewing
  -> compounding
  -> completed

Any active state -> failed
Any interrupted state -> the same state on idempotent resume
```

Each transition follows this order:

1. Validate the precondition and policy locally.
2. Persist the new cycle document with `ic state set`.
3. Record the corresponding `ic events record` event.
4. Rewrite the local receipt projection atomically.

The Intercore document is canonical. The local JSON file under
`.remontoire/cycles/<cycle-id>/receipt.json` is a human-readable artifact and
recovery projection, not a second database.

Only one active cycle may exist for a configured portfolio. `ic lock acquire`
enforces this across processes. A stable idempotency key on every mutating
action prevents duplicate beads, execution attempts, feedback, and terminal
receipts after restart.

## Observation Envelope

A cycle captures bounded, size-limited JSON inputs:

- open and recently closed Beads items;
- Intercore discovery candidates and interest profile;
- recent Intercore outcome and evidence events;
- Ockham dispatch offsets and halt status;
- the current roadmap digest;
- prior Remontoire feedback summaries.

Each input is stored as an artifact, hashed with SHA-256, and registered as an
Intercore replay input. Volatile command output is never silently re-read during
replay.

## Portfolio Judgment

The judge backend receives the observation envelope as untrusted data and must
return schema-valid JSON containing:

- zero to five ranked opportunities;
- evidence-based impact, uncertainty, cost, risk, and policy-fit scores;
- source references for every material claim;
- one selected candidate or an explicit no-op reason;
- a complete evidence contract for the selected candidate.

The deterministic core rejects candidates that violate eligibility rules,
exceed budgets, duplicate existing work, lack evidence, or request prohibited
actions. The judge can rank; it cannot waive policy.

## Evidence Contract

Every proposed experiment includes:

- hypothesis and falsifier;
- repository and allowed path set;
- primary metric name, unit, direction, baseline, and target;
- deterministic benchmark or verification command;
- maximum wall time, turns, and estimated cost;
- stop conditions;
- executor backend;
- promotion and closure criteria;
- expected artifact paths.

The contract is immutable once approved. A changed contract requires a new
proposal and approval.

## Approval and Execution

`remontoire approve <cycle-id> --actor=<principal>` writes an approval record
containing the cycle ID, evidence-contract hash, actor, and timestamp. Execution
fails closed if any field is missing or the contract hash changed.

The executor runs in an isolated git worktree. Its policy is mechanically
bounded:

- workspace write permission only;
- no network permission;
- no push, merge, deploy, release, reset, clean, or credential access;
- maximum wall time and turn/cost budget;
- allowed paths checked before and after execution;
- benchmark command run by Remontoire, not trusted to model narration.

Execution produces a patch, command transcript, benchmark result, and backend
metadata. It does not land the patch.

## Review and Compounding

A reviewer backend, preferably different from the executor, receives the
immutable contract, patch, transcript, and measured result in read-only mode.
It returns a schema-valid verdict: promote, close-success, close-failure, or
inconclusive.

Remontoire validates the verdict against measured promotion criteria, then:

- promotes the experiment into a separately human-landed implementation bead;
- or closes it with the measured evidence;
- records discovery feedback and a compact outcome summary in Intercore;
- regenerates the roadmap;
- emits one signed terminal receipt containing hashes of all inputs and
  artifacts.

The next cycle consumes the outcome summary, making the feedback loop
behavioral rather than archival.

## Failure and Recovery

- Lock contention exits without starting a second cycle.
- Source failure before proposal records a failed or degraded receipt and makes
  no backlog mutation.
- Harness failure is retryable within the same cycle and attempt budget.
- Interruption after a side effect resumes from the canonical idempotency key.
- Roadmap failure does not erase reviewed evidence; the cycle remains in
  `compounding` until regeneration succeeds.
- Receipt signing failure leaves the cycle non-terminal so it can be retried.

## Authority Matrix

| Action | Shadow | Proposed | Approved execution | Human only |
|---|---:|---:|---:|---:|
| Read canonical evidence | yes | yes | yes | yes |
| Rank opportunities | yes | yes | yes | yes |
| Create one P4 experiment | no | yes | yes | yes |
| Write experiment worktree | no | no | yes | yes |
| Close experiment with evidence | no | no | yes | yes |
| Create promotion bead | no | no | yes | yes |
| Push, merge, deploy, release | no | no | no | yes |
