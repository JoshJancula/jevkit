#!/usr/bin/env bash
# Resolve jevkit on PATH (via bootstrap) then exec with the given args.
# Fail-open when the binary is still missing so host agents are not blocked.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BOOTSTRAP="$ROOT/jevkit-plugin-bootstrap.sh"

fail_open() {
  printf '%s\n' '{}'
  exit 0
}

if ! command -v jevkit >/dev/null 2>&1; then
  # Print install remediation (and optionally bootstrap a pinned build).
  bash "$BOOTSTRAP" ensure || true
  if ! command -v jevkit >/dev/null 2>&1; then
    fail_open "$@"
  fi
fi

exec jevkit "$@"
