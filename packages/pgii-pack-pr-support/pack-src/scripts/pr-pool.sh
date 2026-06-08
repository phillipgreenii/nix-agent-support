#!/usr/bin/env bash
# pr-pool.sh — standalone PR-feedback orchestrator (step 1).
# See docs/superpowers/specs/2026-06-08-pr-feedback-orchestrator-design.md.
set -uo pipefail

REPO_ROOT="${REPO_ROOT:-$PWD}"
SELF_LOGIN="${SELF_LOGIN:-}"
MAX="${PR_POOL_MAX:-1}"
SOCKET="${PR_POOL_SOCKET:-pgpool}"
SKILL_MD="${PR_POOL_SKILL_MD:-}"
READY_TIMEOUT="${PR_POOL_READY_TIMEOUT:-60}"
MAX_WAIT="${PR_POOL_MAX_WAIT:-1800}"
POLL_INTERVAL="${PR_POOL_POLL_INTERVAL:-10}"
READY_PROMPT="${PR_POOL_READY_PROMPT:-❯ }"
ACTOR="${PR_POOL_ACTOR:-pgii-pool__process-feedback}"
QUOTA_PAUSED="${PR_POOL_QUOTA_PAUSED:-}"
CICD_DOWN="${PR_POOL_CICD_DOWN:-}"
LOG_DIR="${PR_POOL_LOG_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/pr-pool}"

# Clean the ambient ~/phillipg_mbp/.envrc taint once, so bd/pg-pr resolve to
# the monorepo's own zr .beads — here AND in the dispatched tmux pane, which
# inherits this environment.
unset BEADS_DIR WORKSPACE_ROOT

log() { printf '[%s] %s\n' "$(date -Iseconds)" "$*" >&2; }

precheck() {
  if [ ! -d "$REPO_ROOT/.beads" ]; then
    log "ERROR: $REPO_ROOT/.beads not found — run from the monorepo root"
    return 1
  fi
  local prefix
  prefix="$(awk -F': *' '/^issue_prefix:/{print $2; exit}' "$REPO_ROOT/.beads/config.yaml" 2>/dev/null)"
  if [ "$prefix" != "zr" ]; then
    log "ERROR: bd prefix is '${prefix:-<none>}', expected 'zr' (wrong workspace?)"
    return 1
  fi
  if ! bd list --limit 1 --json >/dev/null 2>&1; then
    log "ERROR: bd/dolt not reachable (server down? it is gas-city-managed and cannot be auto-started)"
    return 1
  fi
  return 0
}

main() {
  mkdir -p "$LOG_DIR"
  precheck || exit 1
  log "pr-pool: precheck passed (REPO_ROOT=$REPO_ROOT)"
}

main "$@"
