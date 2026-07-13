# Agent Instructions

Remontoire is a standalone L2 portfolio agency. Read `MISSION.md`,
`PHILOSOPHY.md`, `docs/architecture.md`, and `docs/PRD.md` before changing its
authority model or cycle state machine.

## Tracker

Canonical work is tracked in the Sylveste Beads database on zklw. The v0.1 epic
is `Revel-6ep`. Run Beads commands from `~/projects` on zklw; do not initialize a
new tracker inside this repository.

## Required Workflow

1. Use test-driven development for behavior changes.
2. Run `gofmt`, `go test ./...`, and `go vet ./...` before committing.
3. Commit logical units directly to `main`.
4. Push every completed logical unit.
5. Preserve the production landing gate: Remontoire may prepare experiment
   changes, but must never push, merge, or deploy them.

## Boundaries

- Do not add a private state database.
- Do not duplicate Beads backlog records.
- Do not move Ockham policy into Remontoire.
- Do not embed a new agent loop; invoke an existing harness through the backend
  interface.
- Treat model output as untrusted input and validate it against deterministic
  schemas and policy.
