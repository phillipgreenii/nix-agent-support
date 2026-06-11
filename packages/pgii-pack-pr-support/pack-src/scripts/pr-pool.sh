#!/usr/bin/env bash
# pr-pool.sh — standalone PR-feedback orchestrator (step 1).
# See docs/superpowers/specs/2026-06-08-pr-feedback-orchestrator-design.md.
set -uo pipefail

REPO_ROOT="${REPO_ROOT:-$PWD}"
SELF_LOGIN="${SELF_LOGIN:-}"
SOCKET="${PR_POOL_SOCKET:-pgpool}"
SKILL_MD="${PR_POOL_SKILL_MD:-}"
READY_TIMEOUT="${PR_POOL_READY_TIMEOUT:-60}"
MAX_WAIT="${PR_POOL_MAX_WAIT:-1800}"
POLL_INTERVAL="${PR_POOL_POLL_INTERVAL:-10}"
READY_PROMPT="${PR_POOL_READY_PROMPT:-❯}"               # glyph alone; claude follows it with a non-breaking space (U+00A0), not ASCII
SEND_SETTLE="${PR_POOL_SEND_SETTLE:-1}"                 # seconds between typing the nudge and pressing Enter
ROLE_NAME="${PR_POOL_ROLE_NAME:-PR FEEDBACK PROCESSOR}" # tmux session name = the role; monitoring keys on this
EXIT_CMD="${PR_POOL_EXIT_CMD:-/exit}"                   # graceful claude exit; kill-session is the guaranteed fallback
ACTOR="${PR_POOL_ACTOR:-pgii-pool__process-feedback}"
WORKER_SKILL_MD="${PR_POOL_WORKER_SKILL_MD:-}"                                                  # worker SKILL.md (analogue of SKILL_MD)
FEEDBACK_SESSION="${PR_POOL_FEEDBACK_SESSION:-$ROLE_NAME}"                                      # tmux session for the feedback-processor role
WORKER_SESSION="${PR_POOL_WORKER_SESSION:-WORKER}"                                              # tmux session for the worker role
FEEDBACK_ACTOR="${PR_POOL_FEEDBACK_ACTOR:-$ACTOR}"                                              # BEADS_ACTOR for feedback-processor
WORKER_ACTOR="${PR_POOL_WORKER_ACTOR:-pgii-pool__worker}"                                       # BEADS_ACTOR for worker
MAX_FEEDBACK="${PR_POOL_MAX_FEEDBACK:-1}"                                                       # per-role concurrency cap (feedback)
MAX_WORKER="${PR_POOL_MAX_WORKER:-1}"                                                           # per-role concurrency cap (worker)
WORKTREE_DIR="${PR_POOL_WORKTREE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/pr-pool/worktrees}" # passed to the worker in its nudge
ROLES="feedback-processor worker"                                                               # role list for drain + teardown
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

# discover_feedback prints "feedback-processor<TAB><cycle-id>" for each open
# process-feedback cycle (from bd ready) whose parent merge-request is mine.
discover_feedback() {
  local self
  self="$(resolve_self)"
  [ -z "$self" ] && {
    log "ERROR: could not resolve self_login from pg-pr config"
    return 1
  }
  bd ready --json --limit 0 2>/dev/null |
    jq -r 'if type=="array" then . else [] end
             | map(select(.issue_type=="task" and (.title | startswith("process-feedback:"))))
             | .[].id' |
    while read -r cid; do
      [ -z "$cid" ] && continue
      local pid author
      pid="$(bd_obj "$cid" | jq -r '.parent // empty')"
      [ -z "$pid" ] && continue
      author="$(bd_obj "$pid" | jq -r '.metadata.author // ""')"
      [ "$author" = "$self" ] && printf 'feedback-processor\t%s\n' "$cid" || true
    done
}

# discover_worker prints "worker<TAB><bead-id>" for each worker-ready bead, using
# bd ready's native label filter (the .labels field is null when unset, so a jq
# label check is avoided here).
discover_worker() {
  bd ready --label worker-ready --json --limit 0 2>/dev/null |
    jq -r 'if type=="array" then . else [] end | .[].id' |
    while read -r id; do
      [ -n "$id" ] && printf 'worker\t%s\n' "$id"
    done
}

# discover prints role<TAB>bead-id lines for every dispatchable ready bead.
discover() {
  discover_feedback
  discover_worker
}

# ensure_session creates the role's named claude session if absent (pinning
# BEADS_DIR/WORKSPACE_ROOT and the role's BEADS_ACTOR), else reuses it. The role
# defaults to feedback-processor. Waits for the prompt before returning.
ensure_session() {
  local role="${1:-feedback-processor}" sess actor
  sess="$(role_session "$role")"
  actor="$(role_actor "$role")"
  if ! tmux -L "$SOCKET" has-session -t "$sess" 2>/dev/null; then
    tmux -u -L "$SOCKET" new-session -d -s "$sess" -c "$REPO_ROOT" \
      -e "BEADS_ACTOR=$actor" \
      -e "BEADS_DIR=$REPO_ROOT/.beads" \
      -e "WORKSPACE_ROOT=$REPO_ROOT" \
      claude --dangerously-skip-permissions --effort max --session-id "$(uuidgen)" \
      >/dev/null || {
      log "ERROR: tmux new-session failed for role '$role' (session '$sess')"
      return 1
    }
  fi
  wait_ready "$sess"
}

# cycle_label builds a human-friendly claude conversation name for a cycle:
# "process-feedback <cid> PR #<n>", falling back to just the cid if the parent
# PR number can't be resolved.
cycle_label() {
  local cid="$1" pid pr
  pid="$(bd_obj "$cid" | jq -r '.parent // empty')"
  pr="$(bd_obj "$pid" | jq -r '.metadata.pr_number // empty')"
  if [ -n "$pr" ]; then
    printf 'process-feedback %s PR #%s' "$cid" "$pr"
  else
    printf 'process-feedback %s' "$cid"
  fi
}

# claude_rename names the current claude conversation in the given session.
claude_rename() { submit_line "$1" "/rename \"$2\""; }

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

# submit_line types a line into the claude pane, settles, then sends a SEPARATE
# Enter. Bundling text+Enter makes claude treat the burst as a paste and the
# trailing Enter becomes a newline instead of submitting (confirmed live). Used
# for /rename, the nudge, /clear, and exit.
submit_line() {
  local sess="$1" text="$2"
  tmux -L "$SOCKET" send-keys -t "$sess" "$text" || return 1
  sleep "$SEND_SETTLE"
  tmux -L "$SOCKET" send-keys -t "$sess" Enter
}

# clear_context resets claude's context in the given session, then waits for the
# prompt so the session is ready for the next item.
clear_context() {
  submit_line "$1" "/clear" || return 1
  wait_ready "$1"
}

# teardown_all tears down every known role's session (not only ones created this
# pass), reaping strays from crashed/earlier runs or roles no longer dispatched.
teardown_all() {
  local role
  for role in $ROLES; do
    teardown_session "$(role_session "$role")"
  done
}

# teardown_session gracefully exits claude in the given session, then kills it.
# kill-session is the guaranteed teardown. No-op if the session is absent.
teardown_session() {
  local sess="$1"
  tmux -L "$SOCKET" has-session -t "$sess" 2>/dev/null || return 0
  submit_line "$sess" "$EXIT_CMD" || true
  tmux -L "$SOCKET" kill-session -t "$sess" >/dev/null 2>&1 || true
}

# --- per-role config table (bash-3.2-safe case resolvers) ----------------
# The "*" default branch resolves to the feedback-processor role so callers
# that omit the role keep step-1 behavior.
role_session() { case "${1:-}" in worker) printf '%s' "$WORKER_SESSION" ;; *) printf '%s' "$FEEDBACK_SESSION" ;; esac }
role_actor() { case "${1:-}" in worker) printf '%s' "$WORKER_ACTOR" ;; *) printf '%s' "$FEEDBACK_ACTOR" ;; esac }
role_skill() { case "${1:-}" in worker) printf '%s' "$WORKER_SKILL_MD" ;; *) printf '%s' "$SKILL_MD" ;; esac }
role_max() { case "${1:-}" in worker) printf '%s' "$MAX_WORKER" ;; *) printf '%s' "$MAX_FEEDBACK" ;; esac }
role_nudge() { case "${1:-}" in worker) nudge_text_worker "${2:-}" ;; *) nudge_text_feedback "${2:-}" ;; esac }
role_convo_name() { case "${1:-}" in worker) worker_label "${2:-}" ;; *) cycle_label "${2:-}" ;; esac }

# nudge_text_feedback builds the instruction sent to the feedback processor. Points at the
# refreshed SKILL.md; the processor creates/links work beads (children of the PR
# bead) and de-duplicates against the PR's existing open work beads. It does NOT
# implement fixes, does NOT work the new beads, and does NOT exit — the
# orchestrator owns session teardown.
nudge_text_feedback() {
  local cid="$1"
  printf '%s' "Read $SKILL_MD and process process-feedback cycle $cid: claim it, read its feedback children (bd children $cid), resolve the parent PR bead and review the PR's existing open work beads (bd children <PR> --status=open). For each feedback, create a work bead (task/bug) as a child of the PR bead, discovered-from the feedback — but if that work matches an existing open work bead, link/update it instead of creating a duplicate. Do NOT apply fixes and do NOT work the new work beads. Close each feedback bead, then close the cycle with a one-line summary."
}

# nudge_text_worker builds the worker's instruction line. The worker does all
# git work itself (pr-pool stays git-free): resolve PR+branch bead-first, work in
# an isolated worktree, commit but never push, record then swap labels, never
# close. WORKTREE_DIR is expanded so the agent gets a concrete path.
nudge_text_worker() {
  local id="$1"
  printf '%s' "Read $WORKER_SKILL_MD and implement work bead $id. Claim it (bd update $id --claim). Resolve its PR + head branch bead-first from the parent merge-request bead's metadata (repo, pr_number, branch — no gh needed) and assert metadata.author is me; if you cannot resolve the PR or it is not mine, abort WITHOUT editing anything and leave it for worker-stuck. Create or reuse an isolated git worktree for that branch under $WORKTREE_DIR, implement the change the bead describes, and commit it (do NOT push, do NOT force). Then record the worktree path + commit SHA on the bead with bd comment, and ONLY AFTER that swap labels atomically: bd update $id --add-label needs-push --remove-label worker-ready. Leave the bead claimed/in_progress; do NOT close it."
}

# worker_label builds the claude conversation name for a work bead:
# "worker <id> PR #<n>", falling back to just the id.
worker_label() {
  local id="$1" pid pr
  pid="$(bd_obj "$id" | jq -r '.parent // empty')"
  pr="$(bd_obj "$pid" | jq -r '.metadata.pr_number // empty')"
  if [ -n "$pr" ]; then
    printf 'worker %s PR #%s' "$id" "$pr"
  else
    printf 'worker %s' "$id"
  fi
}

# send_nudge sends the role-appropriate instruction line into the session.
send_nudge() {
  local role="$1" sess="$2" id="$3"
  [ -z "$(role_skill "$role")" ] && {
    log "ERROR: SKILL.md path unset for role '$role'"
    return 1
  }
  submit_line "$sess" "$(role_nudge "$role" "$id")"
}

# bead_status prints any bead's status (empty on error).
bead_status() { bd_obj "$1" | jq -r '.status // ""'; }

# pane_alive returns 0 while the tmux session still exists.
pane_alive() { tmux -L "$SOCKET" capture-pane -p -t "$1" >/dev/null 2>&1; }

# unclaim returns a claimed-but-unfinished cycle to the open pool so the next
# run can see it (discover/list filters --status=open; a stranded in_progress
# cycle would be invisible otherwise).
unclaim() { bd update "$1" --status=open --assignee="" >/dev/null 2>&1 || true; }

# mark_human flags a bead that needs human intervention so it surfaces in
# `bd list --label human` and is excluded from discovery. Best-effort.
mark_human() { bd update "$1" --add-label human >/dev/null 2>&1 || true; }

# done_signal returns 0 when the role's completion is reached, given the bead's
# current status and (for the worker) whether it was ever seen in_progress.
#   feedback-processor -> the cycle bead is closed
#   worker             -> the bead has LEFT in_progress: closed (resolved) or, if
#                         it was seen claimed, open (handed back). seen_claimed
#                         guards the pre-claim startup race (a freshly-dispatched
#                         bead is still 'open').
done_signal() {
  local role="$1" status="$2" seen_claimed="${3:-0}"
  case "$role" in
  worker)
    [ "$status" = "closed" ] && return 0
    [ "$seen_claimed" = "1" ] && [ "$status" = "open" ] && return 0
    return 1
    ;;
  *) [ "$status" = "closed" ] ;;
  esac
}

# wait_done_fail performs the role-specific failure action:
#   feedback-processor -> unclaim (so the open pool resurfaces the cycle)
#   worker             -> add the `human` label, NEVER unclaim (a dead worker may
#                         hold a half-built worktree; blind retry is unsafe)
wait_done_fail() {
  case "$1" in
  worker)
    log "wait_done: worker $2 $3; flagging human"
    mark_human "$2"
    ;;
  *)
    log "wait_done: $2 $3; unclaiming"
    unclaim "$2"
    ;;
  esac
}

# wait_done polls until the role's completion signal fires (success) or MAX_WAIT
# elapses / the pane dies (failure). On failure it runs the role's fail action;
# it NEVER auto-closes. It reads the bead status once per poll, tracks whether the
# worker was ever seen in_progress, and re-checks after a pane death so a bead
# completed in the same instant the pane exited is not treated as a failure.
wait_done() {
  local role="$1" id="$2" sess="$3" deadline seen_claimed=0 s
  deadline=$(($(date +%s) + MAX_WAIT))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    s="$(bead_status "$id")"
    done_signal "$role" "$s" "$seen_claimed" && return 0
    [ "$role" = "worker" ] && [ "$s" = "in_progress" ] && seen_claimed=1
    if ! pane_alive "$sess"; then
      s="$(bead_status "$id")"
      done_signal "$role" "$s" "$seen_claimed" && return 0
      wait_done_fail "$role" "$id" "exited before completing"
      return 1
    fi
    sleep "$POLL_INTERVAL"
  done
  s="$(bead_status "$id")"
  done_signal "$role" "$s" "$seen_claimed" && return 0
  wait_done_fail "$role" "$id" "not complete within ${MAX_WAIT}s"
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

# work_one drives one bead to completion in its role's (reused) session: ensure
# the session, name the conversation, nudge, wait for the role's completion
# signal, then /clear for the next item. wait_done handles its own unclaim on
# failure; clear_context always runs so the session is left ready/reusable.
work_one() {
  local role="$1" id="$2" sess rc
  sess="$(role_session "$role")"
  ensure_session "$role" || return 1
  claude_rename "$sess" "$(role_convo_name "$role" "$id")"
  if ! send_nudge "$role" "$sess" "$id"; then
    [ "$role" = worker ] || unclaim "$id" # worker is never unclaimed (see wait_done_fail)
    clear_context "$sess"
    return 1
  fi
  wait_done "$role" "$id" "$sess"
  rc=$?
  clear_context "$sess"
  return "$rc"
}

# drain_once works each role's discovered beads up to that role's cap (so neither
# role can starve the other), then tears down all role sessions. Returns 0
# whether gated (paused) or after attempting the capped work.
drain_once() {
  gated && return 0
  local all role total=0
  all="$(discover)"
  for role in $ROLES; do
    local cap worked=0 r id
    cap="$(role_max "$role")"
    while IFS="$(printf '\t')" read -r r id; do
      [ "$r" = "$role" ] || continue
      [ -z "$id" ] && continue
      if [ "$worked" -ge "$cap" ]; then
        log "pr-pool: reached cap=$cap for role '$role' this pass; stopping that role"
        break
      fi
      log "pr-pool: working $role $id"
      work_one "$role" "$id" || log "pr-pool: $role $id did not complete (flagged)"
      worked=$((worked + 1))
      total=$((total + 1))
    done <<EOF
$all
EOF
  done
  teardown_all
  log "pr-pool: drain pass complete ($total item(s) attempted)"
  return 0
}

main() {
  mkdir -p "$LOG_DIR"
  precheck || exit 1
  drain_once
}

main "$@"
