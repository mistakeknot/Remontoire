#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
WORKFLOW="${ROOT}/.github/workflows/ci.yml"

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
printf 'ci_test: ok\n'
