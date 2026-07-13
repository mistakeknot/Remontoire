#!/usr/bin/env bash
set -euo pipefail

HOST=${REMONTOIRE_NETWORK_CHECK_HOST:-api.openai.com}
DEADLINE_SECONDS=300
LOOKUP_TIMEOUT_SECONDS=5
DELAY_SECONDS=10

if [[ ! "${HOST}" =~ ^[A-Za-z0-9.-]+$ ]]; then
  printf 'remontoire-network: invalid DNS host %s\n' "${HOST}" >&2
  exit 2
fi

deadline=$((SECONDS + DEADLINE_SECONDS))
while ((SECONDS < deadline)); do
  remaining=$((deadline - SECONDS))
  lookup_timeout=${LOOKUP_TIMEOUT_SECONDS}
  ((lookup_timeout <= remaining)) || lookup_timeout=${remaining}
  if /usr/bin/timeout "${lookup_timeout}s" /usr/bin/getent ahosts "${HOST}" >/dev/null 2>&1; then
    exit 0
  fi
  remaining=$((deadline - SECONDS))
  ((remaining > 0)) || break
  delay=${DELAY_SECONDS}
  ((delay <= remaining)) || delay=${remaining}
  /usr/bin/sleep "${delay}"
done

printf 'remontoire-network: DNS lookup for %s failed within %ss\n' "${HOST}" "${DEADLINE_SECONDS}" >&2
exit 1
