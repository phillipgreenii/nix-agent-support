# shellcheck shell=bash

# Hard ceiling and default stay below the 600000 ms foreground Bash cap so one
# bgwait call always returns before the harness kills it.
MAX_TIMEOUT=540
DEFAULT_TIMEOUT=480

show_help() {
  cat <<'HELP'
bgwait: Block until a background job finishes, then report it once.

Usage: bgwait [OPTIONS] NAME

Waits (cheap file/pid checks, no log reads) until job NAME has an exit record or
dies, or the timeout expires, then prints exactly what `bgcheck NAME` prints
ONCE: a status line, then the last N log lines:
  RUNNING pid=<p> etime=<t>   timed out; the job is still going
  DONE exit=<code>            finished; <code> is the payload's TRUE exit code
  EXITED unknown              process died without writing an exit record

Use this instead of calling `bgcheck` in a loop (or `sleep`/`date` polling): one
bgwait call replaces dozens of identical polls. On exit 2 simply call bgwait
again; call `bgcheck` at most once between bgwait calls. Jobs are launched with
`bgrun`. Trust the DONE exit code, never the log tail, to judge success.

Options:
  -h, --help         Show this help message
  -v, --version      Show version information
  -t, --timeout SEC  Seconds to wait (default: 480, capped at 540 so a call
                     always fits the 10-minute foreground Bash limit)
  -n, --lines N      Log lines to show (default: 20)
  -d, --dir DIR      State directory (default: $BG_DIR, else TMPDIR/pg-bg-$USER)

Exit status:
  0  job finished (DONE); read the payload's exit code from the status line
  1  usage error
  2  timed out, job still running
  3  no job recorded under NAME
  4  job died without an exit record (EXITED unknown)

Example:
  bgrun flake-check -- nix flake check
  bgwait flake-check              # blocks, then prints DONE exit=N plus the tail
  bgwait --timeout 120 flake-check

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

die() {
  echo "bgwait: error: $1" >&2
  exit "${2:-1}"
}

BG_NAME=""
LINES=20
TIMEOUT="$DEFAULT_TIMEOUT"

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  -t | --timeout)
    [[ $# -ge 2 ]] || die "option $1 requires a value"
    [[ $2 =~ ^[0-9]+$ ]] || die "not a number of seconds: $2"
    TIMEOUT="$2"
    shift 2
    ;;
  -n | --lines)
    [[ $# -ge 2 ]] || die "option $1 requires a value"
    [[ $2 =~ ^[0-9]+$ ]] || die "not a line count: $2"
    LINES="$2"
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

[[ -n $BG_NAME ]] || die "missing NAME (usage: bgwait [--timeout SEC] NAME)"
bg_validate_name "$BG_NAME" || die "invalid name '$BG_NAME' (allowed: alphanumerics then . _ -)"

# 10# forces base 10 so a value like 0540 is not read as octal.
TIMEOUT=$((10#$TIMEOUT))
if [[ $TIMEOUT -gt $MAX_TIMEOUT ]]; then
  echo "bgwait: timeout capped at ${MAX_TIMEOUT}s (requested ${TIMEOUT}s); call bgwait again if the job is still running" >&2
  TIMEOUT="$MAX_TIMEOUT"
fi

if ! STATUS="$(bg_status "$BG_NAME")"; then
  echo "bgwait: no job named '$BG_NAME' in $(bg_state_dir)" >&2
  exit 3
fi

# BGWAIT_POLL_INTERVAL: seconds between checks (fractions allowed); a test hook.
INTERVAL="${BGWAIT_POLL_INTERVAL:-1}"
DEADLINE=$((SECONDS + TIMEOUT))
while [[ $STATUS == RUNNING* && $SECONDS -lt $DEADLINE ]]; do
  sleep "$INTERVAL"
  STATUS="$(bg_status "$BG_NAME" || true)"
done

# Same report shape as bgcheck: status line, then the log tail.
echo "$STATUS"
LOG="$(bg_log_path "$BG_NAME")"
if [[ -f $LOG ]]; then
  echo "--- last $LINES line(s) of $LOG ---"
  tail -n "$LINES" "$LOG"
fi

case "$STATUS" in
RUNNING*)
  echo "bgwait: timed out after ${TIMEOUT}s; job '$BG_NAME' is still running (call bgwait again)" >&2
  exit 2
  ;;
DONE*) exit 0 ;;
*) exit 4 ;;
esac
