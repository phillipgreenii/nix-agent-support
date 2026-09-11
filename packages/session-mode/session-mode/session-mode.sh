# shellcheck shell=bash

show_help() {
  cat <<'HELP'
session-mode: Track which named "mode" (drain-beads, unblock-human-beads,
wrap-up-session, ...) is running in THIS Claude Code session, for display in
the status line.

Usage: session-mode <SUBCOMMAND> [ARGS...]

Subcommands:
  start KIND [--detail TEXT] [--force]
      Create or refresh this session's mode record with state=running.
      Without --force: a record already active for a DIFFERENT kind is a
      conflict (exit 3, record untouched); a record for the SAME kind is
      refreshed in place (its state and started_at are kept, unless it had
      already finished, in which case it resets to running with a fresh
      started_at). With --force: always (re)creates the record for KIND
      fresh, discarding whatever was there before.
  set-status running|stopping|finished
      Update the state of this session's existing mode record. Fails
      (exit 2) when no record exists.
  show
      Print the raw record JSON for this session. Fails (exit 2) when no
      record exists (or it is an empty file, which is treated the same).
  hook session-end
      Internal: invoked by Claude Code's SessionEnd hook. Reads
      session_id/transcript_path from stdin JSON; when a record exists in
      state running or stopping, marks it finished. Always exits 0 — a hook
      must never disrupt session teardown.

The session is identified by $CLAUDE_SESSION_ID, falling back to
$CLAUDE_CODE_SESSION_ID (start/set-status/show); the record directory is
derived from the transcript path (hook) or $PWD (everything else),
overridable via $SESSION_MODE_STATE_DIR for testing.

Options:
  -h, --help     Show this help message
  -v, --version  Show version information

Exit status:
  0  success
  1  usage error
  2  no record found (set-status / show)
  3  conflict: a different kind is already active (start, without --force)

Examples:
  session-mode start drain-beads --force --detail "P1 only"
  session-mode set-status stopping
  session-mode show
  echo '{"session_id":"abc","transcript_path":"/x/abc.jsonl"}' | session-mode hook session-end

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

die() {
  echo "session-mode: error: $1" >&2
  exit "${2:-1}"
}

resolve_session_id() {
  if [[ -n ${CLAUDE_SESSION_ID:-} ]]; then
    printf '%s' "$CLAUDE_SESSION_ID"
  elif [[ -n ${CLAUDE_CODE_SESSION_ID:-} ]]; then
    printf '%s' "$CLAUDE_CODE_SESSION_ID"
  else
    die "CLAUDE_SESSION_ID/CLAUDE_CODE_SESSION_ID is not set (run inside a Claude Code session)"
  fi
}

# resolve_file — the record path for THIS session, derived from $PWD (no
# transcript_path available outside the hook: see session_mode_state_dir).
# sid is captured into a variable on its own assignment line (not passed as
# a nested "$(resolve_session_id)" function ARGUMENT) — a failing command
# substitution used as an argument never fails the caller. That alone is
# still not enough: resolve_file() itself runs inside the subshell that
# "$(resolve_file)" spawns for its caller, and bash disables `set -e`
# inside a command-substitution subshell by default (no `inherit_errexit`
# here), so a bare `sid="$(resolve_session_id)"` would silently continue
# with sid empty rather than aborting. The explicit `|| return 1` makes the
# failure check independent of that errexit nesting quirk.
resolve_file() {
  local dir sid
  dir="$(session_mode_state_dir)"
  sid="$(resolve_session_id)" || return 1
  session_mode_file_path "$dir" "$sid"
}

cmd_start() {
  local kind="" detail="" force=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --detail)
      [[ $# -ge 2 ]] || die "option $1 requires a value"
      detail="$2"
      shift 2
      ;;
    --force)
      force=1
      shift
      ;;
    -*)
      die "unknown option: $1"
      ;;
    *)
      [[ -z $kind ]] || die "unexpected argument: $1"
      kind="$1"
      shift
      ;;
    esac
  done
  [[ -n $kind ]] || die "missing KIND (usage: session-mode start KIND [--detail TEXT] [--force])"
  session_mode_validate_kind "$kind" || die "invalid kind '$kind' (lowercase letters, digits, hyphens; must start with a letter)"

  local file now existing
  file="$(resolve_file)"
  now="$(session_mode_now_iso)"
  existing="$(session_mode_read "$file" 2>/dev/null || true)"

  if [[ -n $existing ]] && [[ $force -eq 0 ]]; then
    local existing_kind existing_state existing_started
    existing_kind="$(printf '%s' "$existing" | jq -r '.kind // empty')"
    existing_state="$(printf '%s' "$existing" | jq -r '.state // empty')"
    existing_started="$(printf '%s' "$existing" | jq -r '.started_at // empty')"

    if [[ $existing_kind != "$kind" ]]; then
      die "a different kind ('$existing_kind') is already active for this session; pass --force to override" 3
    fi

    if [[ $existing_state != "finished" ]]; then
      # Same kind, still running/stopping: refresh detail/updated_at in
      # place, keeping the existing state and started_at.
      local record
      record="$(session_mode_build_record "$kind" "$detail" "$existing_state" "$existing_started" "$now")"
      session_mode_write_atomic "$file" "$record" || die "failed to write $file"
      exit 0
    fi
    # Same kind, but previously finished: this is a genuinely new run — fall
    # through to the fresh-record write below (fresh started_at).
  fi

  local record
  record="$(session_mode_build_record "$kind" "$detail" "running" "$now" "$now")"
  session_mode_write_atomic "$file" "$record" || die "failed to write $file"
}

cmd_set_status() {
  local state="${1:-}"
  [[ -n $state ]] || die "missing STATE (usage: session-mode set-status running|stopping|finished)"
  session_mode_validate_state "$state" || die "invalid state '$state' (must be running, stopping, or finished)"

  local file existing
  file="$(resolve_file)"
  existing="$(session_mode_read "$file" 2>/dev/null || true)"
  [[ -n $existing ]] || die "no session-mode record found at $file" 2

  local kind detail started
  kind="$(printf '%s' "$existing" | jq -r '.kind // empty')"
  detail="$(printf '%s' "$existing" | jq -r '.detail // empty')"
  started="$(printf '%s' "$existing" | jq -r '.started_at // empty')"

  local record
  record="$(session_mode_build_record "$kind" "$detail" "$state" "$started" "$(session_mode_now_iso)")"
  session_mode_write_atomic "$file" "$record" || die "failed to write $file"
}

cmd_show() {
  local file existing
  file="$(resolve_file)"
  existing="$(session_mode_read "$file" 2>/dev/null || true)"
  [[ -n $existing ]] || die "no session-mode record found at $file" 2
  printf '%s\n' "$existing"
}

# cmd_hook_session_end — ALWAYS exits 0: a hook must never disrupt session
# teardown, so every failure path below is a silent no-op, not a `die`.
cmd_hook_session_end() {
  local payload sid transcript
  payload="$(cat)"
  sid="$(printf '%s' "$payload" | jq -r '.session_id // empty' 2>/dev/null || true)"
  transcript="$(printf '%s' "$payload" | jq -r '.transcript_path // empty' 2>/dev/null || true)"
  if [[ -z $sid ]]; then
    exit 0
  fi

  local dir file existing
  dir="$(session_mode_state_dir "$transcript")"
  file="$(session_mode_file_path "$dir" "$sid")"
  existing="$(session_mode_read "$file" 2>/dev/null || true)"
  if [[ -z $existing ]]; then
    exit 0
  fi

  local state
  state="$(printf '%s' "$existing" | jq -r '.state // empty' 2>/dev/null || true)"
  case "$state" in
  running | stopping)
    local kind detail started
    kind="$(printf '%s' "$existing" | jq -r '.kind // empty')"
    detail="$(printf '%s' "$existing" | jq -r '.detail // empty')"
    started="$(printf '%s' "$existing" | jq -r '.started_at // empty')"
    local record
    record="$(session_mode_build_record "$kind" "$detail" "finished" "$started" "$(session_mode_now_iso)")"
    session_mode_write_atomic "$file" "$record" 2>/dev/null || true
    ;;
  esac
  exit 0
}

main() {
  local subcommand="${1:-}"
  if [[ $# -gt 0 ]]; then
    shift
  fi

  case "$subcommand" in
  -h | --help)
    show_help
    ;;
  start)
    cmd_start "$@"
    ;;
  set-status)
    cmd_set_status "$@"
    ;;
  show)
    cmd_show
    ;;
  hook)
    local hook_event="${1:-}"
    case "$hook_event" in
    session-end)
      cmd_hook_session_end
      ;;
    *)
      die "unknown hook event: ${hook_event:-<missing>} (usage: session-mode hook session-end)"
      ;;
    esac
    ;;
  "")
    die "missing SUBCOMMAND (usage: session-mode <start|set-status|show|hook> ...)"
    ;;
  *)
    die "unknown subcommand: $subcommand"
    ;;
  esac
}

main "$@"
