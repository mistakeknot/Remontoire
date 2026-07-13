#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SERVICE="${ROOT}/deploy/systemd/remontoire.service"
TIMER="${ROOT}/deploy/systemd/remontoire.timer"
INSTALLER="${ROOT}/scripts/install.sh"

fail() {
  printf 'deploy_test: %s\n' "$*" >&2
  exit 1
}

assert_contains() {
  local file=$1
  local text=$2
  grep -Fq -- "${text}" "${file}" || fail "${file} is missing ${text}"
}

assert_not_contains() {
  local file=$1
  local text=$2
  if grep -Fq -- "${text}" "${file}"; then
    fail "${file} unexpectedly contains ${text}"
  fi
}

[[ -f "${SERVICE}" ]] || fail "missing service unit"
[[ -f "${TIMER}" ]] || fail "missing timer unit"
[[ -x "${INSTALLER}" ]] || fail "missing executable installer"
bash -n "${INSTALLER}"

for setting in \
  'Type=oneshot' \
  'ExecStartPre=%h/.local/share/remontoire/wait-network.sh' \
  'cycle --mode=proposal --json' \
  'TimeoutStartSec=30min' \
  'NoNewPrivileges=true' \
  'PrivateTmp=true' \
  'ProtectSystem=strict' \
  'ProtectHome=read-only' \
  'ReadWritePaths=' \
  'UMask=0077' \
  'StandardOutput=journal' \
  'StandardError=journal' \
  'SyslogIdentifier=remontoire'; do
  assert_contains "${SERVICE}" "${setting}"
done
assert_not_contains "${SERVICE}" 'ReadWritePaths=%h/projects '
assert_not_contains "${SERVICE}" '%h/.codex'
assert_not_contains "${SERVICE}" '%h/.claude'
assert_not_contains "${SERVICE}" '%h/projects/Sylveste/docs'
assert_contains "${SERVICE}" 'ReadWritePaths=%h/.clavain'
[[ -x "${ROOT}/scripts/wait-network.sh" ]] || fail "missing executable network preflight"
bash -n "${ROOT}/scripts/wait-network.sh"
assert_contains "${ROOT}/scripts/wait-network.sh" 'DEADLINE_SECONDS=300'
assert_contains "${ROOT}/scripts/wait-network.sh" '/usr/bin/timeout'
if REMONTOIRE_NETWORK_CHECK_HOST='invalid host' "${ROOT}/scripts/wait-network.sh" >/dev/null 2>&1; then
  fail "network preflight accepted an invalid host"
fi
for setting in \
  'OnCalendar=' \
  'RandomizedDelaySec=' \
  'Persistent=true' \
  'WantedBy=timers.target'; do
  assert_contains "${TIMER}" "${setting}"
done

TMP=$(mktemp -d)
trap 'rm -rf "${TMP}"' EXIT
export HOME="${TMP}/home"
export XDG_CONFIG_HOME="${HOME}/.config"
export XDG_STATE_HOME="${HOME}/.local/state"
mkdir -p "${HOME}/projects" "${TMP}/bin"

FAKE="${TMP}/bin/tool"
cat >"${FAKE}" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod 0755 "${FAKE}"
REMONTOIRE_BINARY="${TMP}/bin/remontoire"
(
  cd "${ROOT}"
  env GOCACHE="${TMP}/go-cache" GOMODCACHE="${TMP}/go-mod-cache" \
    go build -trimpath -o "${REMONTOIRE_BINARY}" ./cmd/remontoire
)
SYSTEMCTL_LOG="${TMP}/systemctl.log"
export SYSTEMCTL_LOG
cat >"${TMP}/bin/systemctl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${SYSTEMCTL_LOG}"
EOF
chmod 0755 "${TMP}/bin/systemctl"
export PATH="${TMP}/bin:${PATH}"
ROADMAP_SCRIPT="${TMP}/bin/sync-roadmap-json.sh"
cp "${FAKE}" "${ROADMAP_SCRIPT}"
ROADMAP_PATH="${HOME}/projects/roadmap.json"
printf '{}\n' >"${ROADMAP_PATH}"

DEPENDENCY_ARGS=(
  --binary "${REMONTOIRE_BINARY}"
  --intercore-binary "${FAKE}"
  --beads-binary "${FAKE}"
  --ockham-binary "${FAKE}"
  --git-binary "${FAKE}"
  --bash-binary "${FAKE}"
  --codex-binary "${FAKE}"
  --claude-binary "${FAKE}"
)
INSTALL_ARGS=(
  --no-enable
  --project-dir "${HOME}/projects"
  --roadmap-script "${ROADMAP_SCRIPT}"
  --roadmap-path "${ROADMAP_PATH}"
  "${DEPENDENCY_ARGS[@]}"
)

"${INSTALLER}" "${INSTALL_ARGS[@]}" >/dev/null
[[ -x "${HOME}/.local/bin/remontoire" ]] || fail "binary was not installed"
[[ -f "${XDG_CONFIG_HOME}/remontoire/config.json" ]] || fail "config was not installed"
[[ -f "${XDG_CONFIG_HOME}/systemd/user/remontoire.service" ]] || fail "service was not installed"
[[ -f "${HOME}/.local/share/remontoire/schemas/judgment-v1.json" ]] || fail "schemas were not installed"
[[ -x "${HOME}/.local/share/remontoire/wait-network.sh" ]] || fail "network preflight was not installed"
assert_contains "${XDG_CONFIG_HOME}/systemd/user/remontoire.service" "WorkingDirectory=${HOME}/projects"
assert_contains "${XDG_CONFIG_HOME}/systemd/user/remontoire.service" "--config=${XDG_CONFIG_HOME}/remontoire/config.json"
if grep -Fq -- 'enable --now remontoire.timer' "${SYSTEMCTL_LOG}"; then
  fail "--no-enable armed the timer"
fi

find "${HOME}/.local" "${XDG_CONFIG_HOME}/remontoire" "${XDG_CONFIG_HOME}/systemd/user" -type f -exec shasum -a 256 {} \; | sort >"${TMP}/first.sha"
"${INSTALLER}" "${INSTALL_ARGS[@]}" >/dev/null
find "${HOME}/.local" "${XDG_CONFIG_HOME}/remontoire" "${XDG_CONFIG_HOME}/systemd/user" -type f -exec shasum -a 256 {} \; | sort >"${TMP}/second.sha"
cmp -s "${TMP}/first.sha" "${TMP}/second.sha" || fail "second install changed installed content"

: >"${SYSTEMCTL_LOG}"
"${INSTALLER}" \
  --project-dir "${HOME}/projects" \
  --roadmap-script "${ROADMAP_SCRIPT}" \
  --roadmap-path "${ROADMAP_PATH}" \
  "${DEPENDENCY_ARGS[@]}" >/dev/null
if grep -Fq -- 'enable --now remontoire.timer' "${SYSTEMCTL_LOG}"; then
  fail "default install armed the timer"
fi
: >"${SYSTEMCTL_LOG}"
"${INSTALLER}" \
  --enable \
  --project-dir "${HOME}/projects" \
  --roadmap-script "${ROADMAP_SCRIPT}" \
  --roadmap-path "${ROADMAP_PATH}" \
  "${DEPENDENCY_ARGS[@]}" >/dev/null
assert_contains "${SYSTEMCTL_LOG}" 'enable --now remontoire.timer'

ALT_PROJECT="${HOME}/portfolio"
mkdir -p "${ALT_PROJECT}/Sylveste/scripts" "${ALT_PROJECT}/Sylveste/docs"
cp "${FAKE}" "${ALT_PROJECT}/Sylveste/scripts/sync-roadmap-json.sh"
printf '{}\n' >"${ALT_PROJECT}/Sylveste/docs/roadmap.json"
"${INSTALLER}" \
  --no-enable \
  --force-config \
  --project-dir "${ALT_PROJECT}" \
  "${DEPENDENCY_ARGS[@]}" >/dev/null
assert_contains "${XDG_CONFIG_HOME}/remontoire/config.json" "\"roadmap_path\": \"${ALT_PROJECT}/Sylveste/docs/roadmap.json\""
assert_contains "${XDG_CONFIG_HOME}/systemd/user/remontoire.service" "WorkingDirectory=${ALT_PROJECT}"
"${INSTALLER}" --no-enable "${DEPENDENCY_ARGS[@]}" >/dev/null
assert_contains "${XDG_CONFIG_HOME}/remontoire/config.json" "\"project_dir\": \"${ALT_PROJECT}\""
assert_contains "${XDG_CONFIG_HOME}/systemd/user/remontoire.service" "WorkingDirectory=${ALT_PROJECT}"

DRY_HOME="${TMP}/dry-home"
HOME="${DRY_HOME}" XDG_CONFIG_HOME="${DRY_HOME}/.config" XDG_STATE_HOME="${DRY_HOME}/.local/state" \
  "${INSTALLER}" --dry-run "${INSTALL_ARGS[@]}" >/dev/null
[[ ! -e "${DRY_HOME}/.local/bin/remontoire" ]] || fail "dry run wrote the binary"

"${INSTALLER}" --dry-run --uninstall --no-enable >/dev/null
[[ -x "${HOME}/.local/bin/remontoire" ]] || fail "uninstall dry run removed the binary"
ORPHAN_INSTALLER="${TMP}/orphan/scripts/install.sh"
mkdir -p "$(dirname "${ORPHAN_INSTALLER}")"
cp "${INSTALLER}" "${ORPHAN_INSTALLER}"
chmod 0755 "${ORPHAN_INSTALLER}"
"${ORPHAN_INSTALLER}" --uninstall --no-enable >/dev/null
[[ ! -e "${HOME}/.local/bin/remontoire" ]] || fail "uninstall left the binary"
[[ ! -e "${XDG_CONFIG_HOME}/systemd/user/remontoire.timer" ]] || fail "uninstall left the timer"
[[ ! -e "${HOME}/.local/share/remontoire/wait-network.sh" ]] || fail "uninstall left the network preflight"
[[ -f "${XDG_CONFIG_HOME}/remontoire/config.json" ]] || fail "uninstall removed operator config"

printf 'deploy_test: ok\n'
