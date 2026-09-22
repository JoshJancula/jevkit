#!/usr/bin/env bash
# Shared plugin bootstrap: probe PATH for jevkit; print (or apply) install.
set -euo pipefail

EXPECTED_COMMAND="jevkit"
PINNED_VERSION="dev"
INSTALL_COMMAND="go install github.com/OWNER/jevkit/cmd/jevkit@latest"

usage() {
  cat <<'USAGE'
Usage: jevkit-plugin-bootstrap.sh probe [--json]
       jevkit-plugin-bootstrap.sh ensure [--yes] [--json]
USAGE
}

json_escape() {
  local s=${1-}
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  s=${s//$'\n'/\\n}
  printf '%s' "$s"
}

emit_probe() {
  local want_json=$1 outcome=$2 resolved=$3 remediation=$4
  if [[ "$want_json" -eq 1 ]]; then
    printf '{'
    printf '"command":"%s",' "$(json_escape "$EXPECTED_COMMAND")"
    printf '"outcome":"%s",' "$(json_escape "$outcome")"
    printf '"pinnedVersion":"%s",' "$(json_escape "$PINNED_VERSION")"
    printf '"remediation":"%s",' "$(json_escape "$remediation")"
    printf '"resolvedCommand":"%s"' "$(json_escape "$resolved")"
    printf '}\n'
  else
    printf 'outcome: %s\n' "$outcome"
    printf 'command: %s\n' "$EXPECTED_COMMAND"
    if [[ -n "$resolved" ]]; then
      printf 'resolvedCommand: %s\n' "$resolved"
    fi
    printf 'pinnedVersion: %s\n' "$PINNED_VERSION"
    if [[ -n "$remediation" ]]; then
      printf 'remediation: %s\n' "$remediation"
    fi
  fi
}

probe_collect() {
  local resolved=""
  PROBE_OUTCOME=""
  PROBE_RESOLVED=""
  PROBE_REMEDIATION=""
  if ! resolved="$(command -v "$EXPECTED_COMMAND" 2>/dev/null)"; then
    PROBE_OUTCOME="missing"
    PROBE_REMEDIATION="$INSTALL_COMMAND"
    return 1
  fi
  PROBE_OUTCOME="usable"
  PROBE_RESOLVED="$resolved"
  return 0
}

bootstrap_pinned() {
  # Best-effort install of the pinned module version into the user Go bin.
  local dest="${GOBIN:-${GOPATH:-$HOME/go}/bin}"
  mkdir -p "$dest"
  if ! command -v go >/dev/null 2>&1; then
    return 1
  fi
  GOBIN="$dest" go install "github.com/OWNER/jevkit/cmd/jevkit@latest"
  if [[ -x "$dest/jevkit" ]]; then
    PATH="$dest:$PATH"
    export PATH
    return 0
  fi
  return 1
}

probe_cmd() {
  local want_json=$1
  probe_collect || true
  emit_probe "$want_json" "$PROBE_OUTCOME" "$PROBE_RESOLVED" "$PROBE_REMEDIATION"
  [[ "$PROBE_OUTCOME" == "usable" ]]
}

ensure_cmd() {
  local want_json=$1
  local assume_yes=$2
  probe_collect || true
  if [[ "$PROBE_OUTCOME" == "usable" ]]; then
    emit_probe "$want_json" "$PROBE_OUTCOME" "$PROBE_RESOLVED" ""
    return 0
  fi
  if [[ "$assume_yes" -eq 1 ]] || [[ "${JEVKIT_PLUGIN_AUTO_BOOTSTRAP:-}" == "1" ]]; then
    if bootstrap_pinned; then
      probe_collect || true
      emit_probe "$want_json" "$PROBE_OUTCOME" "$PROBE_RESOLVED" "$PROBE_REMEDIATION"
      [[ "$PROBE_OUTCOME" == "usable" ]]
      return $?
    fi
  fi
  # Print the install command; do not mutate without consent.
  printf '%s\n' "$INSTALL_COMMAND" >&2
  emit_probe "$want_json" "missing" "" "$INSTALL_COMMAND"
  return 1
}

cmd=""
want_json=0
assume_yes=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --json) want_json=1; shift ;;
    --yes) assume_yes=1; shift ;;
    probe|ensure)
      if [[ -n "$cmd" ]]; then usage >&2; exit 2; fi
      cmd=$1; shift
      ;;
    *) usage >&2; exit 2 ;;
  esac
done

case "$cmd" in
  probe) probe_cmd "$want_json" ;;
  ensure) ensure_cmd "$want_json" "$assume_yes" ;;
  *) usage >&2; exit 2 ;;
esac
