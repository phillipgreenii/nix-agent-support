# shellcheck shell=bash

show_help() {
  cat <<'HELP'
bgrun: Launch a command in the background with a truthful exit record.

Usage: bgrun [OPTIONS] NAME -- COMMAND [ARGS...]

Runs COMMAND detached, appending combined output to a deterministic log, and
records the command's TRUE exit code in NAME.exit when it finishes. Check on it
(from this session or any other) with `bgcheck NAME`. The exit file is the only
trustworthy completion signal — never infer success from the launcher's exit
code or the log's tail.

COMMAND is executed in argv form (no shell re-parse). For a pipeline or shell
syntax, wrap it yourself: bgrun NAME -- bash -c 'cmd1 | cmd2'.

Options:
  -h, --help      Show this help message
  -v, --version   Show version information
  -d, --dir DIR   State directory (default: $BG_DIR, else TMPDIR/pg-bg-$USER)

Output (single line on success): NAME PID LOGPATH

Exit status:
  0  launched
  1  usage error (bad name, missing --, missing command)
  2  a job named NAME is still running

Example:
  bgrun flake-check -- nix flake check
  bgcheck flake-check

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

die() {
  echo "bgrun: error: $1" >&2
  exit "${2:-1}"
}

BG_NAME=""
declare -a PAYLOAD=()
SAW_DASHDASH=0

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  -d | --dir)
    [[ $# -ge 2 ]] || die "option $1 requires a value"
    # shellcheck disable=SC2034 # read by bg_state_dir in bg-tools-lib.bash, sourced ahead of this file at build time
    BG_DIR="$2"
    shift 2
    ;;
  --)
    shift
    SAW_DASHDASH=1
    PAYLOAD=("$@")
    break
    ;;
  -*)
    die "unknown option: $1"
    ;;
  *)
    if [[ -z $BG_NAME ]]; then
      BG_NAME="$1"
      shift
    else
      die "missing -- separator before COMMAND (got '$1' after NAME '$BG_NAME')"
    fi
    ;;
  esac
done

[[ -n $BG_NAME ]] || die "missing NAME (usage: bgrun NAME -- COMMAND [ARGS...])"
bg_validate_name "$BG_NAME" || die "invalid name '$BG_NAME' (allowed: alphanumerics then . _ -)"
[[ $SAW_DASHDASH -eq 1 ]] || die "missing -- separator before COMMAND"
[[ ${#PAYLOAD[@]} -gt 0 ]] || die "missing COMMAND after --"

DIR="$(bg_state_dir)"
mkdir -p "$DIR"
LOG="$(bg_log_path "$BG_NAME")"
PIDF="$(bg_pid_path "$BG_NAME")"
EXITF="$(bg_exit_path "$BG_NAME")"
METAF="$(bg_meta_path "$BG_NAME")"

STATUS="$(bg_status "$BG_NAME" || true)"
case "$STATUS" in
RUNNING*)
  die "job '$BG_NAME' is already running ($STATUS); pick another name or wait" 2
  ;;
esac

# Replace any state a previous, finished run of the same name left behind.
rm -f "$EXITF"
{
  printf 'cmd:'
  printf ' %q' "${PAYLOAD[@]}"
  printf '\n'
  echo "cwd: $PWD"
  echo "start: $(date -u +%FT%TZ)"
} >"$METAF"
: >"$LOG"

# Detached launch. `trap '' HUP` + disown survive the launching shell's exit;
# `set +e` is required because strict mode is inherited — without it a failing
# payload would abort the subshell BEFORE the exit-code write, which is the
# whole point of this tool.
(
  trap '' HUP
  set +e
  "${PAYLOAD[@]}"
  echo $? >"$EXITF"
) </dev/null >>"$LOG" 2>&1 &
BG_PID=$!
echo "$BG_PID" >"$PIDF"
disown "$BG_PID" 2>/dev/null || true

echo "$BG_NAME $BG_PID $LOG"
