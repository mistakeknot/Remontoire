# Conventions

## Engineering

- Go code is formatted with `gofmt` and tested with `go test ./...`.
- Domain invariants live in deterministic code, not prompts.
- External CLI integrations are behind narrow interfaces and tested with fakes.
- All timestamps use UTC RFC 3339 form.
- JSON schemas and persisted structs are versioned from their first release.
- Receipt fields are append-only within a schema version.
- Errors identify the failed stage, command, and safe recovery action.

## Safety

- Research and review harnesses run read-only.
- Execution runs only after an explicit approval and only in an isolated
  worktree with bounded writable paths.
- Network access, git push, merge, deployment, and destructive git operations
  are forbidden to execution backends.
- Secrets and raw environment values never enter prompts or receipts.
- Canonical commands are passed as argv, never interpolated through a shell.

## Repository

- Commit each logical unit directly to `main`.
- Do not stage unrelated changes in adjacent repositories.
- Work is complete only after tests pass and `git push` succeeds.
