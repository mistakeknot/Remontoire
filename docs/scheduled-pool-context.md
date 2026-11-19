# Scheduled pool execution in a BB thread

Status: proposed deployment design; the harness change has offline regression
coverage. No scheduler, service, pool, model configuration or cycle is changed
by this patch. These choices use the installed BB automation CLI and runtime
contract; verify that contract again before deployment.

## Failure and boundary

A standalone timer invoking `remontoire cycle` has no BB thread context.
Additionally, the harness environment allowlist discarded the context even
when an operator invoked Remontoire inside a thread. Pool admission requires a
live thread with an environment on the enrolled primary host, caller cwd, and
an integer timeout between 1 and 3600 seconds. Its default of 3600 is valid;
the harness retains its shorter 29 minute client deadline.

The pool invocation now inherits only `BB_THREAD_ID`, `BB_PROJECT_ID`,
`BB_ENVIRONMENT_ID`, `BB_CLI`, `BB_SERVER_URL`, and `BB_HOST_DAEMON_PORT` in
addition to the existing safe environment. Routing variables preserve the
caller's BB instance; they do not select a new server. No credentials, arbitrary
`BB_*` variables or context from config, files, command arguments, another
thread, or model output are added. The CLI process gets the actual `os.Getwd()`
directory, independently of the provider's `--cd` workspace and shell `PWD`.
Benchmarks and direct model children retain the original sanitized environment.

An inherited ID is a claim, never authentication. Remontoire does not certify
it as genuine. BB's pool checks the live thread and environment at admission;
only that check permits execution. Missing, deleted, destroyed, or wrong-host
context remains denied. The same-user environment cannot prove process
ancestry or distinguish a deliberately borrowed live ID. This design never
borrows an ID and does not claim to add that stronger BB security boundary.

Keep pool admission, provider arguments, model selection, receipt generation,
and `poolMayHaveStarted` unchanged. An unknown pool error, including a context
denial, is terminal, with `pool-unconfirmed` transport and no replay. This patch
adds no fallback; the existing handling of known pre-start availability failures
is unchanged and must not be used as the scheduled integration's success path.

## Proposed scheduler

Use a recurring **agent** automation, with no `--target-thread`, on an explicit
unmanaged directory on the enrolled primary host. Each run gets a new genuine
thread. A script automation does not inject a thread ID and is unsuitable for
directly invoking the cycle. No BB core change is needed for the agent design.

After review, an operator can instantiate this template with the deployment's
existing BB project, portfolio cwd, installed binary, config and approved outer
agent seat. The model in Remontoire's existing config remains untouched; the
outer agent is an additional scheduling cost, not the portfolio judge.

```sh
printf -v SCHEDULE_PROMPT '%s\n%s --config=%s cycle --mode=proposal --json\n%s' \
  'Run this exact command once in the foreground, preserving inherited BB context and cwd:' \
  "$REMONTOIRE_BINARY" "$REMONTOIRE_CONFIG" \
  'Report its exit status, cycle ID, transport and receipt location. On failure stop. Do not set a thread ID, switch transport or model, approve, resume, retry, repair, or edit files or settings.'
bb automation create --project "$PROJECT_ID" \
  --name "Remontoire daily proposal" --disabled \
  --cron '10 23 * * *' --timezone UTC \
  --environment "$PORTFOLIO_CWD" \
  --provider "$OUTER_PROVIDER" --model "$OUTER_MODEL" --reasoning high \
  --permission-mode auto \
  --prompt "$SCHEDULE_PROMPT"
```

Values must be trusted deployment paths, without whitespace or shell
metacharacters; quote the command paths when adapting to other installations.
No automation is created by this patch. The example daily time is a proposed
explicit schedule, not an assertion about any existing timer's jitter.

The recommended cutover retires the old timer and direct service invocation;
there is no replacement service `ExecStart`. Create the automation paused,
inspect its environment and host, retire the old timer, then enable the
automation under separate deployment authority. Keep it paused if any required
host, path, seat or receipt prerequisite is unknown. Do not leave two daily
schedulers enabled. Rollback pauses the automation; restoring a standalone
service will still fail context admission and is not a working fallback.

BB serializes runs per automation. Recurring failures may retry after 30 and
60 seconds, each in a fresh thread. A successful agent turn can still contain a
failed cycle: BB's automation status is not proof of cycle success. Report the
Remontoire exit and signed receipt, and use its existing portfolio lock and
cycle idempotency rather than adding another state store. A lost agent turn
with uncertain execution must be inspected before a new cycle is authorized.
Strict no-retry semantics across BB agent failures require a separate scheduler
change; the prompt alone cannot enforce them.

## Verification and release gate

Offline tests capture the pool argv, allowlisted context and actual cwd; assert
that secrets and unrelated environment values stay out; simulate the pool's
missing/stale/wrong-host/destroyed-environment denials; and prove those errors
cannot run a second provider invocation. Existing tests cover confirmation
markers, timeouts, output limits, receipt metadata and pre-start handling.

Run `gofmt`, `go test ./...` and `go vet ./...` before review. Tests use fake
runners and synthetic environment fixtures; they do not contact BB or models.
They prove propagation and failure handling, not real scheduler admission.
Deployment acceptance remains outstanding: under separate authorization,
observe a scheduled thread on the primary host, its actual cwd, a confirmed
pooled invocation with the configured judge model, and the terminal signed
receipt. Never use an existing failed production cycle as a test fixture.
