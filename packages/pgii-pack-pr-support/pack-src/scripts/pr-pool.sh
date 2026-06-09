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
READY_PROMPT="${PR_POOL_READY_PROMPT:-❯}" # glyph alone; claude follows it with a non-breaking space (U+00A0), not ASCII
SEND_SETTLE="${PR_POOL_SEND_SETTLE:-1}"   # seconds between typing the nudge and pressing Enter
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
# Prints the session name. BEADS_DIR/WORKSPACE_ROOT are pinned per session so
# bd/pg-pr always resolve to the monorepo's zr .beads, regardless of tmux server age.
dispatch() {
  local cid="$1" sess
  sess="$(session_name "$cid")"
  tmux -u -L "$SOCKET" new-session -d -s "$sess" -c "$REPO_ROOT" \
    -e "BEADS_ACTOR=$ACTOR" \
    -e "BEADS_DIR=$REPO_ROOT/.beads" \
    -e "WORKSPACE_ROOT=$REPO_ROOT" \
    claude --dangerously-skip-permissions --effort max --session-id "$(uuidgen)" \
    >/dev/null ||
    {
      log "ERROR: tmux new-session failed for $cid"
      return 1
    }
  printf '%s\n' "$sess"
}

# wait_ready polls the pane until the ready prompt glyph appears, bounded by
# READY_TIMEOUT seconds. Returns nonzero on timeout so the caller can flag.
# We match the glyph alone (READY_PROMPT default "❯") because claude renders its
# prompt as ❯+U+00A0 (non-breaking space), not ❯+ASCII-space.
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

# nudge_text builds the instruction sent to the feedback processor. Points at the
# refreshed SKILL.md; the processor creates/links work beads (children of the PR
# bead) and de-duplicates against the PR's existing open work beads. It does NOT
# implement fixes, does NOT work the new beads, and does NOT exit — the
# orchestrator owns session teardown.
nudge_text() {
  local cid="$1"
  printf '%s' "Read $SKILL_MD and process process-feedback cycle $cid: claim it, read its feedback children (bd children $cid), resolve the parent PR bead and review the PR's existing open work beads (bd children <PR> --status=open). For each feedback, create a work bead (task/bug) as a child of the PR bead, discovered-from the feedback — but if that work matches an existing open work bead, link/update it instead of creating a duplicate. Do NOT apply fixes and do NOT work the new work beads. Close each feedback bead, then close the cycle with a one-line summary."
}

# send_nudge types the nudge into the pane, then submits it. The Enter MUST be
# a SEPARATE send-keys after a brief settle: if text+Enter are sent in one
# burst, claude ingests it as a paste and the trailing Enter becomes a newline
# instead of submitting (confirmed live).
send_nudge() {
  local sess="$1" cid="$2"
  [ -z "$SKILL_MD" ] && {
    log "ERROR: PR_POOL_SKILL_MD unset (path to pg-pr-process-feedback SKILL.md)"
    return 1
  }
  tmux -L "$SOCKET" send-keys -t "$sess" "$(nudge_text "$cid")" || return 1
  sleep "$SEND_SETTLE"
  tmux -L "$SOCKET" send-keys -t "$sess" Enter
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

# gated returns 0 (pause) if an optional, read-only sentinel file is present.
# Paths default to empty (disabled). The script never creates/edits/removes them.
gated() {
  [ -n "$QUOTA_PAUSED" ] && [ -f "$QUOTA_PAUSED" ] && {
    log "QUOTA_PAUSED present; pausing"
    return 0
  }
  [ -n "$CICD_DOWN" ] && [ -f "$CICD_DOWN" ] && {
    log "CICD_DOWN present; pausing"
    return 0
  }
  return 1
}

# work_one dispatches + drives one cycle to completion.
# Returns wait_done's exit code on success; 1 on any earlier failure.
work_one() {
  local cid="$1" sess
  sess="$(dispatch "$cid")" || return 1
  if ! wait_ready "$sess"; then
    unclaim "$cid"
    return 1
  fi
  send_nudge "$sess" "$cid" || {
    unclaim "$cid"
    return 1
  }
  wait_done "$cid" "$sess"
}

# drain_once works up to MAX discoverable cycles per pass (serially). Returns 0
# whether gated (paused) or after attempting up to MAX discoverable cycles.
drain_once() {
  gated && return 0
  local cid worked=0
  while read -r cid; do
    [ -z "$cid" ] && continue
    if [ "$worked" -ge "$MAX" ]; then
      log "pr-pool: reached MAX=$MAX cycle(s) this pass; stopping"
      break
    fi
    log "pr-pool: working cycle $cid"
    work_one "$cid" || log "pr-pool: cycle $cid did not complete (flagged)"
    worked=$((worked + 1))
  done < <(discover_cycles)
  log "pr-pool: drain pass complete ($worked cycle(s) attempted)"
  return 0
}

main() {
  mkdir -p "$LOG_DIR"
  precheck || exit 1
  drain_once
}

main "$@"
