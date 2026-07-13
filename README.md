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

## Status

v0.1 is under active development. The product contract is in
[`docs/PRD.md`](docs/PRD.md), and the implementation plan is in
[`docs/plans/2026-07-13-remontoire-v0.1.md`](docs/plans/2026-07-13-remontoire-v0.1.md).

## License

MIT
