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

# resolve_self prints the configured GitHub login (cached in $SELF_LOGIN).
resolve_self() {
  if [ -z "$SELF_LOGIN" ]; then
    SELF_LOGIN="$(pg-pr config show --json 2>/dev/null | jq -r '.self_login // empty')"
  fi
  printf '%s' "$SELF_LOGIN"
}

# bd_obj runs `bd show <id> --json` and normalizes bd's array-or-object shape.
bd_obj() {
  bd show "$1" --json 2>/dev/null | jq 'if type=="array" then .[0] else . end'
}

# discover_cycles prints the IDs of open process-feedback cycles whose parent
# merge-request was authored by me. One id per line.
discover_cycles() {
  local self
  self="$(resolve_self)"
  [ -z "$self" ] && {
    log "ERROR: could not resolve self_login from pg-pr config"
    return 1
  }
  bd list --type=task --status=open --json --limit 0 2>/dev/null |
    jq -r 'if type=="array" then . else [] end
             | map(select(.title | startswith("process-feedback:")))
             | .[].id' |
    while read -r cid; do
      [ -z "$cid" ] && continue
      local pid author
      pid="$(bd_obj "$cid" | jq -r '.parent // empty')"
      [ -z "$pid" ] && continue
      author="$(bd_obj "$pid" | jq -r '.metadata.author // ""')"
      [ "$author" = "$self" ] && printf '%s\n' "$cid" || true
    done
}

# session_name maps a cycle id to its tmux session name.
session_name() { printf 'pf-%s' "$1"; }

# dispatch starts a detached, interactive claude in a tmux pane for the cycle.
# Prints the session name. BEADS_DIR/WORKSPACE_ROOT were already unset at the
# top, so the pane inherits a clean env and its bd/pg-pr resolve to zr.
dispatch() {
  local cid="$1" sess
  sess="$(session_name "$cid")"
  tmux -u -L "$SOCKET" new-session -d -s "$sess" -c "$REPO_ROOT" \
    -e "BEADS_ACTOR=$ACTOR" \
    claude --dangerously-skip-permissions --effort max --session-id "$(uuidgen)" ||
    {
      log "ERROR: tmux new-session failed for $cid"
      return 1
    }
  printf '%s\n' "$sess"
}

# wait_ready polls the pane until the ready prompt appears, bounded by
# READY_TIMEOUT seconds. Returns nonzero on timeout so the caller can flag.
wait_ready() {
  local sess="$1" deadline
  deadline=$(($(date +%s) + READY_TIMEOUT))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if tmux -L "$SOCKET" capture-pane -p -t "$sess" 2>/dev/null | grep -qF "$READY_PROMPT"; then
      return 0
    fi
    sleep 1
  done
  log "wait_ready: $sess never reached the prompt within ${READY_TIMEOUT}s"
  return 1
}

# nudge_text builds the instruction sent to the worker. Points at the clean
# SKILL.md only; overrides its "implement the change" step (step 1 creates an
# action bead instead of applying a fix) and forbids picking up new beads.
nudge_text() {
  local cid="$1"
  printf '%s' "Read $SKILL_MD and process the process-feedback cycle $cid: claim it, read its feedback children (bd children $cid), and for each create an action bead (task/bug, discovered-from the feedback) describing any needed code change — do NOT apply fixes and do NOT work the new action beads. Close non-actionable feedback. Then close the cycle with a one-line summary and exit."
}

# send_nudge types the nudge into the pane and presses Enter.
send_nudge() {
  local sess="$1" cid="$2"
  [ -z "$SKILL_MD" ] && {
    log "ERROR: PR_POOL_SKILL_MD unset (path to pg-pr-process-feedback SKILL.md)"
    return 1
  }
  tmux -L "$SOCKET" send-keys -t "$sess" "$(nudge_text "$cid")" Enter
}

# cycle_status prints the cycle bead's status.
cycle_status() { bd_obj "$1" | jq -r '.status // ""'; }

# pane_alive returns 0 while the tmux session still exists.
pane_alive() { tmux -L "$SOCKET" capture-pane -p -t "$1" >/dev/null 2>&1; }

# unclaim returns a claimed-but-unfinished cycle to the open pool so the next
# run can see it (discover/list filters --status=open; a stranded in_progress
# cycle would be invisible otherwise).
unclaim() { bd update "$1" --status=open --assignee="" >/dev/null 2>&1 || true; }

# wait_done polls until the cycle closes (success) or MAX_WAIT elapses / the
# pane dies (failure). On failure it unclaims + flags; it NEVER auto-closes.
# Before unclaiming it re-checks status, so a cycle the worker closed in the
# same instant it exited is never reverted to open (which would dupe work).
wait_done() {
  local cid="$1" sess="$2" deadline
  deadline=$(($(date +%s) + MAX_WAIT))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    case "$(cycle_status "$cid")" in
    closed) return 0 ;;
    esac
    if ! pane_alive "$sess"; then
      [ "$(cycle_status "$cid")" = "closed" ] && return 0
      log "wait_done: $sess exited before closing $cid; unclaiming"
      unclaim "$cid"
      return 1
    fi
    sleep "$POLL_INTERVAL"
  done
  [ "$(cycle_status "$cid")" = "closed" ] && return 0
  log "wait_done: $cid not closed within ${MAX_WAIT}s; unclaiming + flagging"
  unclaim "$cid"
  return 1
}

main() {
  mkdir -p "$LOG_DIR"
  precheck || exit 1
  log "pr-pool: precheck passed (REPO_ROOT=$REPO_ROOT)"
}

main "$@"
