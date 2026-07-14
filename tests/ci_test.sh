#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
WORKFLOW="${ROOT}/.github/workflows/ci.yml"
README="${ROOT}/README.md"

fail() {
  printf 'ci_test: %s\n' "$*" >&2
  exit 1
}

[[ -f "${WORKFLOW}" ]] || fail "missing CI workflow"
for gate in \
  'gofmt -w .' \
  'go test -count=1 ./...' \
  'go test -count=1 ./schemas' \
  'go test -race -count=1 ./...' \
  'go vet ./...' \
  'go build -trimpath' \
  'shellcheck scripts/install.sh scripts/wait-network.sh tests/deploy_test.sh tests/ci_test.sh' \
  'bash tests/deploy_test.sh' \
  'scripts/install.sh --check'; do
  grep -Fq -- "${gate}" "${WORKFLOW}" || fail "workflow is missing gate: ${gate}"
done

grep -Fq -- 'permissions:' "${WORKFLOW}" || fail "workflow has no explicit permissions"
grep -Fq -- 'contents: read' "${WORKFLOW}" || fail "workflow permissions are not read-only"
grep -Fq -- 'uses: actions/checkout@v7' "${WORKFLOW}" || fail "workflow checkout action is not on the supported runtime"
grep -Fq -- 'uses: actions/setup-go@v6' "${WORKFLOW}" || fail "workflow setup-go action is not on the supported runtime"
grep -Fq -- 'cache: false' "${WORKFLOW}" || fail "workflow should not cache a module with no go.sum"

for operator_command in \
  'scripts/install.sh --check' \
  'doctor --json' \
  'cycle --mode=shadow --json' \
  'cycle --mode=proposal --json' \
  'approve CYCLE_ID --actor=' \
  'decline CYCLE_ID --actor=' \
  'resume CYCLE_ID --json' \
  'receipt replay CYCLE_ID --json' \
  'systemctl --user enable --now remontoire.timer'; do
  grep -Fq -- "${operator_command}" "${README}" || fail "README is missing operator command: ${operator_command}"
done
printf 'ci_test: ok\n'
