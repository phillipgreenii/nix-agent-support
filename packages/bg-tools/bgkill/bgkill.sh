# shellcheck shell=bash

show_help() {
  cat <<'HELP'
bgkill: Stop a background job launched with bgrun, by NAME.

Usage: bgkill [OPTIONS] NAME

Sends SIGTERM (never SIGKILL) to the job's recorded launcher AND every process
beneath it, deepest first, so the payload and its children stop and the launcher
can still write the payload's true exit code (143 for a SIGTERM death) to
NAME.exit. A payload that ignores SIGTERM keeps running (exit 6); bgkill never
escalates to SIGKILL. It touches nothing but the process tree bgrun recorded for
NAME: no pattern matching, no process groups, no other jobs, nothing it did not
launch. It refuses (exit 5) when the recorded pid is no longer the launcher that
bgrun started (pid reused, or not a bgrun process).

Options:
  -h, --help       Show this help message
  -v, --version    Show version information
  -g, --grace SEC  Seconds to wait for the job to stop (default: 5)
  -d, --dir DIR    State directory (default: $BG_DIR, else TMPDIR/pg-bg-$USER)

Output: one line, `STOPPED NAME ...` or a refusal reason on stderr.

Exit status:
  0  the job was running and has stopped
  1  usage error
  3  no job recorded under NAME
  4  the job is not running (already DONE or EXITED); nothing to stop
  5  refused: the recorded pid is not the bgrun launcher, or bgkill is inside the job
  6  signalled, but the job is still running after the grace period (SIGTERM ignored)

Example:
  bgrun slow-tests -- go test ./...
  bgkill slow-tests
  bgcheck slow-tests

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

die() {
  echo "bgkill: error: $1" >&2
  exit "${2:-1}"
}

BG_NAME=""
GRACE=5

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  -g | --grace)
    [[ $# -ge 2 ]] || die "option $1 requires a value"
    [[ $2 =~ ^[0-9]+$ ]] || die "not a number of seconds: $2"
    GRACE="$2"
    shift 2
    ;;
  -d | --dir)
    [[ $# -ge 2 ]] || die "option $1 requires a value"
    # shellcheck disable=SC2034 # read by bg_state_dir in bg-tools-lib.bash, sourced ahead of this file at build time
    BG_DIR="$2"
    shift 2
    ;;
  --)
    shift
    ;;
  -*)
    die "unknown option: $1"
    ;;
  *)
    [[ -z $BG_NAME ]] || die "unexpected argument: $1"
    BG_NAME="$1"
    shift
    ;;
  esac
done

[[ -n $BG_NAME ]] || die "missing NAME (usage: bgkill [--grace SEC] NAME)"
bg_validate_name "$BG_NAME" || die "invalid name '$BG_NAME' (allowed: alphanumerics then . _ -)"
GRACE=$((10#$GRACE))

if ! STATUS="$(bg_status "$BG_NAME")"; then
  echo "bgkill: no job named '$BG_NAME' in $(bg_state_dir)" >&2
  exit 3
fi
case "$STATUS" in
RUNNING*) ;;
*)
  echo "bgkill: job '$BG_NAME' is not running ($STATUS); nothing to stop" >&2
  exit 4
  ;;
esac

PID="$(<"$(bg_pid_path "$BG_NAME")")"
[[ $PID =~ ^[1-9][0-9]*$ ]] || die "job '$BG_NAME' has a malformed pid record '$PID'" 5

# Identity: the pid must still be the launcher bgrun forked (a bgrun subshell
# carries its parent's command line), and — when bgrun could record it — must
# have the start time recorded for it. A pid reused by an unrelated process, or
# a forged record pointing at one, fails here and nothing is signalled.
COMMAND="$(bg_proc_command "$PID")"
if [[ $COMMAND != *bgrun* ]]; then
  echo "bgkill: refusing: pid $PID is not a bgrun launcher (command: ${COMMAND:-<none>})" >&2
  exit 5
fi
RECORDED_START="$(bg_meta_get "$BG_NAME" pidstart || true)"
if [[ -n $RECORDED_START && $RECORDED_START != "$(bg_proc_start "$PID")" ]]; then
  echo "bgkill: refusing: pid $PID no longer has the start time bgrun recorded (pid reused)" >&2
  exit 5
fi

mapfile -t DESCENDANTS < <(bg_descendants "$PID")

# A job that stops itself would kill this very process mid-way; say so instead.
for d in "${DESCENDANTS[@]}"; do
  if [[ $d == "$$" ]]; then
    echo "bgkill: refusing: bgkill is running inside job '$BG_NAME'" >&2
    exit 5
  fi
done

# Wait up to GRACE seconds (0.1 s steps) for $1 (a predicate command) to hold.
wait_until() {
  local tries=$((GRACE * 10))
  while ! "$@"; do
    [[ $tries -gt 0 ]] || return 1
    sleep 0.1
    tries=$((tries - 1))
  done
}
# shellcheck disable=SC2329 # invoked indirectly: wait_until runs its arguments
launcher_gone() { ! kill -0 "$PID" 2>/dev/null; }

# Payload tree first: the launcher then sees its payload die, writes the true
# exit code (143) and exits on its own. The launcher is signalled directly only
# when it has no payload below it yet; never as a fallback, because killing the
# launcher of a payload that ignored SIGTERM would orphan that payload while
# reporting the job stopped.
for d in "${DESCENDANTS[@]}"; do
  kill -TERM "$d" 2>/dev/null || true
done
if [[ ${#DESCENDANTS[@]} -eq 0 ]]; then
  kill -TERM "$PID" 2>/dev/null || true
fi

if wait_until launcher_gone; then
  echo "STOPPED $BG_NAME pid=$PID descendants=${#DESCENDANTS[@]} $(bg_status "$BG_NAME" || true)"
  exit 0
fi
echo "bgkill: job '$BG_NAME' (pid $PID) is still running ${GRACE}s after SIGTERM" >&2
exit 6
