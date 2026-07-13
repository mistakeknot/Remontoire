---
artifact_type: plan
bead: Revel-6ep
stage: design
requirements:
  - F1: Canonical observation and replay inputs
  - F2-F4: Structured ranking, eligibility, and deduplicated proposal
  - F5-F8: Approval, bounded execution, measurement, and review
  - F9-F11: Feedback, roadmap, receipts, and recovery
  - F12: Scheduling and zklw operations
---

# Remontoire v0.1 Implementation Plan

> Required workflow: execute each task test-first and commit each logical unit.

**Bead:** `Revel-6ep`

**Goal:** Deliver one scheduled, resumable, human-approved portfolio experiment
cycle using canonical Sylveste services and an existing agent harness.

**Architecture:** A Go CLI implements a deterministic state machine and invokes
Beads, Intercore, Ockham, Interpath, Codex, and Claude behind interfaces. Agent
outputs are versioned JSON. Intercore is canonical; local cycle files are atomic
receipt projections. Execution occurs in an isolated worktree and can never
land production changes.

**Tech stack:** Go 1.24+, standard library first, external CLI adapters, JSON
Schema fixtures, systemd user units.

## Must-Haves

**Truths**

- One portfolio lock and at most one experiment candidate per cycle.
- Every candidate is P4, deduplicated, source-supported, and governed by an
  immutable evidence contract.
- Execution is impossible without a matching explicit approval hash.
- Execution is bounded by worktree, paths, time, turns or cost, and prohibited
  production actions.
- Benchmark evidence and independent review determine promotion or closure.
- The result becomes ranking input, backlog state, roadmap state, and a signed
  replayable receipt.
- Interrupted stages resume without duplicating mutations.

**Artifacts**

- `cmd/remontoire/main.go`: operator CLI.
- `internal/domain`: versioned cycle, opportunity, contract, review, receipt.
- `internal/cycle`: orchestration and recovery state machine.
- `internal/adapters`: Beads, Intercore, Ockham, roadmap, git, and command runner.
- `internal/harness`: Codex and Claude structured backends.
- `schemas/*.json`: judge and reviewer output contracts.
- `deploy/systemd`: zklw user service and timer.
- `docs/operations/zklw.md`: install, approval, recovery, and rollback runbook.

**Key links**

- Domain validation gates every model response before adapter mutation.
- The evidence-contract hash links proposal, approval, executor, reviewer, and
  terminal receipt.
- Idempotency keys link Intercore state to Beads labels and harness attempts.
- Terminal completion requires feedback, roadmap digest, and receipt signature.

## Task 1 - Domain contracts and invariants

**Files:**

- Create `internal/domain/types.go`
- Create `internal/domain/validate.go`
- Create `internal/domain/validate_test.go`
- Create `schemas/judgment-v1.json`
- Create `schemas/review-v1.json`

1. Write failing table tests for incomplete contracts, non-P4 proposals,
   invalid metrics, unbounded budgets, missing evidence, multiple selections,
   stale approval hashes, and forbidden paths or commands.
2. Add versioned domain structs, canonical JSON hashing, normalization, stable
   fingerprints, and validation.
3. Verify with `go test ./internal/domain -v` and commit.

## Task 2 - Process adapters and canonical state

**Files:**

- Create `internal/adapters/runner.go` and tests.
- Create `internal/adapters/intercore.go` and tests.
- Create `internal/adapters/beads.go` and tests.
- Create `internal/adapters/ockham.go` and tests.
- Create `internal/adapters/roadmap.go` and tests.

1. Use fake executables to specify exact argv and JSON parsing before writing
   each adapter.
2. Implement context timeouts, bounded output, redacted errors, and no shell
   interpolation.
3. Cover lock, state, event, replay input, signed receipt, backlog read/create/
   close/label, neutral Ockham degradation, and roadmap digest operations.
4. Verify with `go test ./internal/adapters -v` and commit.

## Task 3 - Structured harness backends

**Files:**

- Create `internal/harness/backend.go`
- Create `internal/harness/codex.go` and tests.
- Create `internal/harness/claude.go` and tests.
- Create `internal/harness/prompt.go` and golden tests.

1. Write failing argv tests proving judgment/review are read-only and execution
   is workspace-write with bounded budgets and ephemeral sessions.
2. Implement schema-directed output capture, prompt size limits, timeouts, and
   structured backend metadata.
3. Mark canonical evidence as untrusted prompt data and forbid secrets,
   network, push, merge, deploy, and destructive git actions in execution.
4. Verify with `go test ./internal/harness -v` and commit.

## Task 4 - Observe, rank, deduplicate, and propose

**Files:**

- Create `internal/cycle/observe.go` and tests.
- Create `internal/cycle/propose.go` and tests.
- Create `internal/cycle/store.go` and tests.

1. Write an end-to-end fake-adapter test for a proposal cycle and a duplicate
   resume test before implementation.
2. Acquire the portfolio lock, snapshot bounded sources, hash and register
   replay inputs, invoke the judge, validate one selected contract, compute its
   fingerprint, and create one P4 bead only in proposal mode.
3. Persist each state transition and atomically update the receipt projection.
4. Verify the one-candidate, shadow no-mutation, no-op, and dedup properties;
   commit.

## Task 5 - Approval and bounded execution

**Files:**

- Create `internal/cycle/approve.go` and tests.
- Create `internal/cycle/execute.go` and tests.
- Create `internal/adapters/worktree.go` and tests.

1. Write tests that reject missing, stale, self-issued, or malformed approvals.
2. Persist approval actor, time, and immutable contract hash in Intercore.
3. Create an isolated worktree, invoke the approved backend once, inspect the
   diff for allowed paths and forbidden artifacts, and run the benchmark under
   its independent timeout.
4. Prove retry resumes from the recorded attempt and cannot invoke the executor
   twice; commit.

## Task 6 - Review, feedback, roadmap, and receipt

**Files:**

- Create `internal/cycle/review.go` and tests.
- Create `internal/cycle/compound.go` and tests.
- Create `internal/receipt/writer.go` and tests.

1. Write tests for reviewer disagreement, failed metrics, inconclusive results,
   promotion, closure, feedback visibility, roadmap failure, and signing retry.
2. Invoke a read-only reviewer, enforce deterministic promotion criteria, then
   mutate Beads exactly once.
3. Record outcome feedback and summary, regenerate the roadmap, finalize all
   artifact hashes, and ask Intercore to sign the terminal receipt.
4. Verify that the next observation includes the outcome summary; commit.

## Task 7 - CLI, replay, and recovery

**Files:**

- Create `cmd/remontoire/main.go` and tests.
- Create `internal/app/app.go` and tests.
- Create `config/remontoire.example.json`.

1. Specify commands in CLI tests: `cycle`, `approve`, `resume`, `decline`,
   `status`, `receipt show`, `receipt replay`, and `doctor`.
2. Wire configuration validation, structured JSON output, exit codes, signal
   handling, and stage-aware recovery.
3. Reconstruct replay from stored inputs without invoking live observation
   adapters; compare decision and contract hashes.
4. Run `go test ./...`, `go vet ./...`, and commit.

## Task 8 - Scheduling and deployment

**Files:**

- Create `deploy/systemd/remontoire.service`
- Create `deploy/systemd/remontoire.timer`
- Create `scripts/install.sh`
- Create `docs/operations/zklw.md`

1. Add static checks for unit hardening and installer idempotency.
2. Install the binary, config, and user units without embedding credentials.
3. Configure a daily randomized timer, persistent missed-run behavior, runtime
   timeout, restricted filesystem access, and structured journal logging.
4. Verify install/uninstall dry runs and commit.

## Task 9 - Live verification and release

1. Run formatting, unit, integration, race, vet, schema, and shell checks.
2. Obtain an independent code review and resolve all actionable findings.
3. Create the public GitHub repository, push `main`, and verify CI.
4. Install the exact pushed revision on Mac and zklw.
5. Run a scheduled proposal canary against a deliberately safe Remontoire
   fixture, inspect the contract, record explicit approval, resume execution,
   and verify review, feedback, roadmap, and signed receipt.
6. Prove no experiment commit was pushed or merged; retain the worktree and
   patch for human inspection.
7. Close child beads and `Revel-6ep`, push Beads state, and record the deployed
   revision and receipt ID in the handoff.

## Verification Matrix

```text
gofmt -w .
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/remontoire
scripts/install.sh --check
remontoire doctor --json
remontoire receipt replay <canary-cycle> --json
systemctl --user status remontoire.timer
systemctl --user list-timers remontoire.timer
ic receipt verify <canary-receipt-id>
git status --short --branch
```
