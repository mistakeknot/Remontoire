# Codex usage fixtures

`executor.jsonl` is the `codex exec --json` stream of one real Remontoire
executor run (cycle of 2026-07-13, one turn). `attempt-record.json` is the
`execution` object Remontoire stored for that cycle, before usage was tracked.

Redactions, applied to a copy of the original files: `thread_id` replaced with
a zero UUID; every `aggregated_output` and the final `agent_message` text
replaced with `[redacted]`; `/home/<user>/` replaced with `/home/REDACTED/`;
the worktree path and base commit in the record replaced with placeholders.
The event structure and the `turn.completed` `usage` numbers are unchanged.

Codex reports token counts but no dollar cost. The tests therefore expect the
tokens to parse and the cost to stay unknown.
