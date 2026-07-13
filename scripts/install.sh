#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SERVICE_SOURCE="${ROOT}/deploy/systemd/remontoire.service"
TIMER_SOURCE="${ROOT}/deploy/systemd/remontoire.timer"

DRY_RUN=false
CHECK_ONLY=false
UNINSTALL=false
ENABLE_TIMER=false
FORCE_CONFIG=false
BINARY_SOURCE=""
PROJECT_DIR="${HOME}/projects"
ROADMAP_SCRIPT=""
ROADMAP_PATH=""
INTERCORE_BINARY=""
BEADS_BINARY=""
OCKHAM_BINARY=""
GIT_BINARY=""
BASH_BINARY=""
CODEX_BINARY=""
CLAUDE_BINARY=""

usage() {
  cat <<'EOF'
Usage: scripts/install.sh [options]

Install Remontoire for the current user. Existing operator configuration and
cycle state are preserved by default.

Options:
  --check                    Validate deployment sources without writing
  --dry-run                  Print actions without writing
  --uninstall                Remove installed code and units; preserve config/state
  --enable                   Enable and start the timer after verification
  --no-enable                Do not enable the timer (default)
  --force-config             Replace an existing generated config
  --binary PATH              Install a prebuilt Remontoire binary
  --project-dir PATH         Canonical portfolio project directory
  --roadmap-script PATH      Roadmap generator script
  --roadmap-path PATH        Generated canonical roadmap JSON
  --intercore-binary PATH    Intercore CLI
  --beads-binary PATH        Beads CLI
  --ockham-binary PATH       Ockham CLI (optional at runtime)
  --git-binary PATH          Git CLI
  --bash-binary PATH         Bash CLI
  --codex-binary PATH        Codex CLI
  --claude-binary PATH       Claude CLI
  -h, --help                 Show this help
EOF
}

require_value() {
  local option=$1
  local value=${2-}
  if [[ -z "${value}" || "${value}" == -* ]]; then
    printf 'install: %s requires one path\n' "${option}" >&2
    exit 2
  fi
}

while (($# > 0)); do
  case "$1" in
    --check) CHECK_ONLY=true; shift ;;
    --dry-run) DRY_RUN=true; shift ;;
    --uninstall) UNINSTALL=true; shift ;;
    --enable) ENABLE_TIMER=true; shift ;;
    --no-enable) ENABLE_TIMER=false; shift ;;
    --force-config) FORCE_CONFIG=true; shift ;;
    --binary|--project-dir|--roadmap-script|--roadmap-path|--intercore-binary|--beads-binary|--ockham-binary|--git-binary|--bash-binary|--codex-binary|--claude-binary)
      option=$1
      require_value "${option}" "${2-}"
      value=$2
      case "${option}" in
        --binary) BINARY_SOURCE=${value} ;;
        --project-dir) PROJECT_DIR=${value} ;;
        --roadmap-script) ROADMAP_SCRIPT=${value} ;;
        --roadmap-path) ROADMAP_PATH=${value} ;;
        --intercore-binary) INTERCORE_BINARY=${value} ;;
        --beads-binary) BEADS_BINARY=${value} ;;
        --ockham-binary) OCKHAM_BINARY=${value} ;;
        --git-binary) GIT_BINARY=${value} ;;
        --bash-binary) BASH_BINARY=${value} ;;
        --codex-binary) CODEX_BINARY=${value} ;;
        --claude-binary) CLAUDE_BINARY=${value} ;;
      esac
      shift 2
      ;;
    --binary=*|--project-dir=*|--roadmap-script=*|--roadmap-path=*|--intercore-binary=*|--beads-binary=*|--ockham-binary=*|--git-binary=*|--bash-binary=*|--codex-binary=*|--claude-binary=*)
      option=${1%%=*}
      value=${1#*=}
      require_value "${option}" "${value}"
      case "${option}" in
        --binary) BINARY_SOURCE=${value} ;;
        --project-dir) PROJECT_DIR=${value} ;;
        --roadmap-script) ROADMAP_SCRIPT=${value} ;;
        --roadmap-path) ROADMAP_PATH=${value} ;;
        --intercore-binary) INTERCORE_BINARY=${value} ;;
        --beads-binary) BEADS_BINARY=${value} ;;
        --ockham-binary) OCKHAM_BINARY=${value} ;;
        --git-binary) GIT_BINARY=${value} ;;
        --bash-binary) BASH_BINARY=${value} ;;
        --codex-binary) CODEX_BINARY=${value} ;;
        --claude-binary) CLAUDE_BINARY=${value} ;;
      esac
      shift
      ;;
    -h|--help) usage; exit 0 ;;
    *) printf 'install: unknown option %s\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
done

ROADMAP_SCRIPT=${ROADMAP_SCRIPT:-"${PROJECT_DIR}/Sylveste/scripts/sync-roadmap-json.sh"}
ROADMAP_PATH=${ROADMAP_PATH:-"${PROJECT_DIR}/Sylveste/docs/roadmap.json"}

CONFIG_HOME=${XDG_CONFIG_HOME:-"${HOME}/.config"}
STATE_HOME=${XDG_STATE_HOME:-"${HOME}/.local/state"}
DATA_HOME=${XDG_DATA_HOME:-"${HOME}/.local/share"}
BIN_DIR="${HOME}/.local/bin"
BIN_DEST="${BIN_DIR}/remontoire"
CONFIG_DIR="${CONFIG_HOME}/remontoire"
CONFIG_FILE="${CONFIG_DIR}/config.json"
SYSTEMD_DIR="${CONFIG_HOME}/systemd/user"
SERVICE_DEST="${SYSTEMD_DIR}/remontoire.service"
TIMER_DEST="${SYSTEMD_DIR}/remontoire.timer"
SHARE_DIR="${DATA_HOME}/remontoire"
SCHEMA_DIR="${SHARE_DIR}/schemas"
ARTIFACT_ROOT="${STATE_HOME}/remontoire/cycles"
WORKTREE_ROOT="${PROJECT_DIR}/.worktrees/remontoire"
CODEX_RUNTIME_HOME="${STATE_HOME}/remontoire/codex"
CODEX_AUTH_PLACEHOLDER="${CODEX_RUNTIME_HOME}/auth.json"

log_action() {
  printf '%s\n' "$*"
}

install_atomic() {
  local mode=$1
  local source=$2
  local destination=$3
  local temporary
  temporary=$(mktemp "${destination}.tmp.XXXXXX")
  if ! install -m "${mode}" "${source}" "${temporary}"; then
    rm -f "${temporary}"
    return 1
  fi
  if ! mv -f "${temporary}" "${destination}"; then
    rm -f "${temporary}"
    return 1
  fi
}

source_check() {
  local file
  for file in \
    "${SERVICE_SOURCE}" \
    "${TIMER_SOURCE}" \
    "${ROOT}/scripts/wait-network.sh" \
    "${ROOT}/schemas/judgment-v1.json" \
    "${ROOT}/schemas/execution-v1.json" \
    "${ROOT}/schemas/review-v1.json"; do
    [[ -f "${file}" ]] || { printf 'install: required source is missing: %s\n' "${file}" >&2; return 1; }
  done
  bash -n "${BASH_SOURCE[0]}"
}

require_absolute_clean() {
  local name=$1
  local value=$2
  if [[ "${value}" != /* || "${value}" == */../* || "${value}" == */./* || "${value}" == */.. || "${value}" == */. || "${value}" == *//* ]]; then
    printf 'install: %s must be a clean absolute path: %s\n' "${name}" "${value}" >&2
    return 1
  fi
}

require_systemd_path() {
  local name=$1
  local value=$2
  if [[ "${value}" =~ [[:space:]] || "${value}" == *'%'* || "${value}" == *'$'* || "${value}" == *'"'* || "${value}" == *\\* ]]; then
    printf 'install: %s contains characters unsupported by the systemd unit: %s\n' "${name}" "${value}" >&2
    return 1
  fi
}

resolve_executable() {
  local label=$1
  local override=$2
  local command_name=$3
  shift 3
  local candidate=""
  if [[ -n "${override}" ]]; then
    candidate=${override}
  else
    candidate=$(type -P "${command_name}" 2>/dev/null || true)
    if [[ -z "${candidate}" ]]; then
      for candidate in "$@"; do
        [[ -x "${candidate}" ]] && break
        candidate=""
      done
    fi
  fi
  if [[ -z "${candidate}" || ! -x "${candidate}" ]]; then
    printf 'install: cannot resolve executable for %s\n' "${label}" >&2
    return 1
  fi
  require_absolute_clean "${label}" "${candidate}"
  printf '%s\n' "${candidate}"
}

resolve_optional_executable() {
  local override=$1
  local command_name=$2
  local fallback=$3
  local candidate=""
  if [[ -n "${override}" ]]; then
    candidate=${override}
  else
    candidate=$(type -P "${command_name}" 2>/dev/null || true)
    [[ -n "${candidate}" ]] || candidate=${fallback}
  fi
  require_absolute_clean "${command_name}" "${candidate}"
  printf '%s\n' "${candidate}"
}

json_escape() {
  local value=$1
  value=${value//\\/\\\\}
  value=${value//\"/\\\"}
  value=${value//$'\n'/\\n}
  value=${value//$'\r'/\\r}
  value=${value//$'\t'/\\t}
  printf '%s' "${value}"
}

render_config() {
  cat <<EOF
{
  "version": 1,
  "portfolio": "sylveste",
  "project_dir": "$(json_escape "${PROJECT_DIR}")",
  "artifact_root": "$(json_escape "${ARTIFACT_ROOT}")",
  "worktree_root": "$(json_escape "${WORKTREE_ROOT}")",
  "allowed_repository_roots": [
    "$(json_escape "${PROJECT_DIR}")"
  ],
  "judgment_schema_path": "$(json_escape "${SCHEMA_DIR}/judgment-v1.json")",
  "execution_schema_path": "$(json_escape "${SCHEMA_DIR}/execution-v1.json")",
  "review_schema_path": "$(json_escape "${SCHEMA_DIR}/review-v1.json")",
  "roadmap_script_path": "$(json_escape "${ROADMAP_SCRIPT}")",
  "roadmap_path": "$(json_escape "${ROADMAP_PATH}")",
  "default_mode": "proposal",
  "judge_backend": "codex",
  "reviewer_backend": "claude",
  "max_input_bytes": 1048576,
  "discovery_limit": 100,
  "lock_timeout": "0s",
  "intercore_binary": "$(json_escape "${INTERCORE_BINARY}")",
  "beads_binary": "$(json_escape "${BEADS_BINARY}")",
  "ockham_binary": "$(json_escape "${OCKHAM_BINARY}")",
  "git_binary": "$(json_escape "${GIT_BINARY}")",
  "bash_binary": "$(json_escape "${BASH_BINARY}")",
  "codex_binary": "$(json_escape "${CODEX_BINARY}")",
  "codex_model": "",
  "claude_binary": "$(json_escape "${CLAUDE_BINARY}")",
  "claude_model": ""
}
EOF
}

render_service() {
  local content
  content=$(<"${SERVICE_SOURCE}")
  content=${content//%h\/.config\/remontoire\/config.json/"${CONFIG_FILE}"}
  content=${content//%h\/.local\/state\/remontoire\/cycles/"${ARTIFACT_ROOT}"}
  content=${content//%h\/.local\/state\/remontoire/"${STATE_HOME}/remontoire"}
  content=${content//%h\/.local\/state/"${STATE_HOME}"}
  content=${content//%h\/.local\/share/"${DATA_HOME}"}
  content=${content//%h\/projects/"${PROJECT_DIR}"}
  printf '%s\n' "${content}"
}

remove_installation() {
  if [[ "${DRY_RUN}" == true ]]; then
    log_action "would remove ${BIN_DEST}, ${SERVICE_DEST}, ${TIMER_DEST}, and ${SHARE_DIR}"
    log_action "would preserve ${CONFIG_FILE} and ${STATE_HOME}/remontoire"
    return
  fi
  if type -P systemctl >/dev/null 2>&1; then
    systemctl --user disable --now remontoire.timer >/dev/null 2>&1 || true
  fi
  rm -f "${BIN_DEST}" "${SERVICE_DEST}" "${TIMER_DEST}"
  rm -f \
    "${SHARE_DIR}/wait-network.sh" \
    "${SCHEMA_DIR}/judgment-v1.json" \
    "${SCHEMA_DIR}/execution-v1.json" \
    "${SCHEMA_DIR}/review-v1.json"
  rmdir "${SCHEMA_DIR}" "${SHARE_DIR}" 2>/dev/null || true
  if type -P systemctl >/dev/null 2>&1; then
    systemctl --user daemon-reload
  fi
  log_action "removed Remontoire code and user units; preserved config and state"
}

require_absolute_clean HOME "${HOME}"
require_absolute_clean XDG_CONFIG_HOME "${CONFIG_HOME}"
require_absolute_clean XDG_STATE_HOME "${STATE_HOME}"
require_absolute_clean XDG_DATA_HOME "${DATA_HOME}"
require_systemd_path XDG_CONFIG_HOME "${CONFIG_HOME}"
require_systemd_path XDG_STATE_HOME "${STATE_HOME}"
require_systemd_path XDG_DATA_HOME "${DATA_HOME}"

if [[ "${UNINSTALL}" == true ]]; then
  remove_installation
  exit 0
fi

source_check
require_absolute_clean project-dir "${PROJECT_DIR}"
require_systemd_path project-dir "${PROJECT_DIR}"

if [[ "${CHECK_ONLY}" == true ]]; then
  log_action "deployment sources are valid"
  exit 0
fi

PRESERVE_CONFIG=false
if [[ -f "${CONFIG_FILE}" && "${FORCE_CONFIG}" == false ]]; then
  PRESERVE_CONFIG=true
  JQ_BINARY=$(resolve_executable jq "" jq /usr/bin/jq /opt/homebrew/bin/jq)
  PROJECT_DIR=$("${JQ_BINARY}" -er '.project_dir | select(type == "string" and length > 0)' "${CONFIG_FILE}")
  ARTIFACT_ROOT=$("${JQ_BINARY}" -er '.artifact_root | select(type == "string" and length > 0)' "${CONFIG_FILE}")
  WORKTREE_ROOT=$("${JQ_BINARY}" -er '.worktree_root | select(type == "string" and length > 0)' "${CONFIG_FILE}")
  require_absolute_clean project-dir "${PROJECT_DIR}"
  require_absolute_clean artifact-root "${ARTIFACT_ROOT}"
  require_absolute_clean worktree-root "${WORKTREE_ROOT}"
  require_systemd_path project-dir "${PROJECT_DIR}"
  require_systemd_path artifact-root "${ARTIFACT_ROOT}"
else
  require_absolute_clean roadmap-script "${ROADMAP_SCRIPT}"
  require_absolute_clean roadmap-path "${ROADMAP_PATH}"
  [[ -f "${ROADMAP_SCRIPT}" ]] || { printf 'install: roadmap script does not exist: %s\n' "${ROADMAP_SCRIPT}" >&2; exit 1; }
  [[ -f "${ROADMAP_PATH}" ]] || { printf 'install: roadmap does not exist: %s\n' "${ROADMAP_PATH}" >&2; exit 1; }
  INTERCORE_BINARY=$(resolve_executable intercore "${INTERCORE_BINARY}" ic "${HOME}/.local/bin/ic")
  BEADS_BINARY=$(resolve_executable beads "${BEADS_BINARY}" bd "${HOME}/.local/bin/bd")
  OCKHAM_BINARY=$(resolve_optional_executable "${OCKHAM_BINARY}" ockham "${PROJECT_DIR}/Sylveste/os/Ockham/ockham")
  GIT_BINARY=$(resolve_executable git "${GIT_BINARY}" git /usr/bin/git)
  BASH_BINARY=$(resolve_executable bash "${BASH_BINARY}" bash /usr/bin/bash /bin/bash)
  CODEX_BINARY=$(resolve_executable codex "${CODEX_BINARY}" codex "${HOME}/.local/bin/codex")
  CLAUDE_BINARY=$(resolve_executable claude "${CLAUDE_BINARY}" claude "${HOME}/.local/bin/claude")
fi

[[ -d "${PROJECT_DIR}" ]] || { printf 'install: project directory does not exist: %s\n' "${PROJECT_DIR}" >&2; exit 1; }

if [[ -n "${BINARY_SOURCE}" ]]; then
  [[ -x "${BINARY_SOURCE}" ]] || { printf 'install: binary is not executable: %s\n' "${BINARY_SOURCE}" >&2; exit 1; }
  require_absolute_clean binary "${BINARY_SOURCE}"
elif [[ "${DRY_RUN}" == false ]]; then
  GO_BINARY=$(resolve_executable go "" go /usr/local/go/bin/go)
fi

if [[ "${DRY_RUN}" == true ]]; then
  log_action "would install Remontoire to ${BIN_DEST}"
  if [[ -f "${CONFIG_FILE}" && "${FORCE_CONFIG}" == false ]]; then
    log_action "would preserve ${CONFIG_FILE}"
  else
    log_action "would write ${CONFIG_FILE}"
  fi
  log_action "would install user units in ${SYSTEMD_DIR}"
  [[ "${ENABLE_TIMER}" == true ]] && log_action "would enable remontoire.timer"
  exit 0
fi

TMP=$(mktemp -d "${TMPDIR:-/tmp}/remontoire-install.XXXXXX")
trap 'rm -rf "${TMP}"' EXIT
if [[ -z "${BINARY_SOURCE}" ]]; then
  BINARY_SOURCE="${TMP}/remontoire"
  (cd "${ROOT}" && "${GO_BINARY}" build -trimpath -o "${BINARY_SOURCE}" ./cmd/remontoire)
fi

mkdir -p \
  "${BIN_DIR}" \
  "${CONFIG_DIR}" \
  "${SYSTEMD_DIR}" \
  "${SCHEMA_DIR}" \
  "${ARTIFACT_ROOT}" \
  "${CODEX_RUNTIME_HOME}" \
  "${WORKTREE_ROOT}"

if [[ -e "${CODEX_AUTH_PLACEHOLDER}" || -L "${CODEX_AUTH_PLACEHOLDER}" ]]; then
  if [[ ! -f "${CODEX_AUTH_PLACEHOLDER}" || -L "${CODEX_AUTH_PLACEHOLDER}" ]]; then
    printf 'install: Codex auth bind destination must be a regular file: %s\n' "${CODEX_AUTH_PLACEHOLDER}" >&2
    exit 1
  fi
  chmod 0600 "${CODEX_AUTH_PLACEHOLDER}"
else
  install -m 0600 /dev/null "${CODEX_AUTH_PLACEHOLDER}"
fi

install_atomic 0755 "${BINARY_SOURCE}" "${BIN_DEST}"
render_service >"${TMP}/remontoire.service"
install_atomic 0644 "${TMP}/remontoire.service" "${SERVICE_DEST}"
install_atomic 0644 "${TIMER_SOURCE}" "${TIMER_DEST}"
install_atomic 0755 "${ROOT}/scripts/wait-network.sh" "${SHARE_DIR}/wait-network.sh"
install_atomic 0644 "${ROOT}/schemas/judgment-v1.json" "${SCHEMA_DIR}/judgment-v1.json"
install_atomic 0644 "${ROOT}/schemas/execution-v1.json" "${SCHEMA_DIR}/execution-v1.json"
install_atomic 0644 "${ROOT}/schemas/review-v1.json" "${SCHEMA_DIR}/review-v1.json"

if [[ "${PRESERVE_CONFIG}" == false ]]; then
  render_config >"${TMP}/config.json"
  install_atomic 0600 "${TMP}/config.json" "${CONFIG_FILE}"
else
  log_action "preserved existing operator config ${CONFIG_FILE}"
fi

"${BIN_DEST}" --config="${CONFIG_FILE}" doctor --json

SYSTEMCTL_BINARY=$(type -P systemctl 2>/dev/null || true)
if [[ -n "${SYSTEMCTL_BINARY}" ]]; then
  "${SYSTEMCTL_BINARY}" --user daemon-reload
elif [[ "${ENABLE_TIMER}" == true ]]; then
  printf 'install: systemctl is required unless --no-enable is used\n' >&2
  exit 1
fi
if [[ "${ENABLE_TIMER}" == true ]]; then
  "${SYSTEMCTL_BINARY}" --user enable --now remontoire.timer
fi

log_action "installed Remontoire; config=${CONFIG_FILE} state=${STATE_HOME}/remontoire"
