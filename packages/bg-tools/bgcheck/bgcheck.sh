# shellcheck shell=bash

show_help() {
  cat <<'HELP'
bgcheck: Report a background job's status and recent output in one call.

Usage: bgcheck [OPTIONS] [NAME]

With NAME, prints one status line, then the last N log lines:
  RUNNING pid=<p> etime=<t>   still going
  DONE exit=<code>            finished; <code> is the payload's TRUE exit code
  EXITED unknown              process died without writing an exit record
Without NAME, lists every recorded job with its status, one per line.

Strictly read-only (ps + tail); safe to run repeatedly. Trust the DONE exit
code, never the log tail, to judge success. Jobs are launched with `bgrun`.

Options:
  -h, --help       Show this help message
  -v, --version    Show version information
  -n, --lines N    Log lines to show (default: 20)
  -d, --dir DIR    State directory (default: $BG_DIR, else TMPDIR/pg-bg-$USER)

Exit status:
  0  status reported
  1  usage error
  3  no job recorded under NAME

Example:
  bgrun flake-check -- nix flake check
  bgcheck flake-check
  bgcheck            # list all jobs

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

die() {
  echo "bgcheck: error: $1" >&2
  exit "${2:-1}"
}

BG_NAME=""
LINES=20

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  -n | --lines)
    [[ $# -ge 2 ]] || die "option $1 requires a value"
    [[ "$2" =~ ^[0-9]+$ ]] || die "not a line count: $2"
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
    [[ -z "$BG_NAME" ]] || die "unexpected argument: $1"
    BG_NAME="$1"
    shift
    ;;
  esac
done

if [[ -z "$BG_NAME" ]]; then
  FOUND=0
  while IFS= read -r n; do
    FOUND=1
    printf '%s\t%s\n' "$n" "$(bg_status "$n" || true)"
  done < <(bg_list_names)
  if [[ "$FOUND" -eq 0 ]]; then
    echo "bgcheck: no background jobs recorded in $(bg_state_dir)" >&2
  fi
  exit 0
fi

bg_validate_name "$BG_NAME" || die "invalid name '$BG_NAME' (allowed: alphanumerics then . _ -)"

if ! STATUS="$(bg_status "$BG_NAME")"; then
  echo "bgcheck: no job named '$BG_NAME' in $(bg_state_dir)" >&2
  exit 3
fi
echo "$STATUS"

LOG="$(bg_log_path "$BG_NAME")"
if [[ -f "$LOG" ]]; then
  echo "--- last $LINES line(s) of $LOG ---"
  tail -n "$LINES" "$LOG"
fi
