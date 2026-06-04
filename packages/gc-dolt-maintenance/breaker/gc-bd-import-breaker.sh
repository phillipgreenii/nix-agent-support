# shellcheck shell=bash
# gc-bd-import-breaker — stop bd's "stale DB" auto-import spiral by pinning
# <city>/.beads/issues.jsonl as an immutable, empty file.
#
# WHY THIS EXISTS
#   bd <= 1.0.4 treats the dolt DB as "stale" when a read returns empty (which
#   happens under slow/degraded storage) and re-imports the ENTIRE issues.jsonl
#   on the spot. Each re-import is a bulk dolt commit, which deepens history,
#   which slows reads, which triggers more "empty" misreads -> a self-amplifying
#   spiral that can add thousands of commits in minutes and crash-loop the dolt
#   server. See HACKS.md "HACK 18" (durable successor to HACK 3).
#
#   There is NO bd config to disable the import (`import.auto` is rejected as an
#   unknown key). The only reliable lever is to make issues.jsonl impossible to
#   read as data and impossible to refill: a 0-byte file with the macOS immutable
#   flag (uchg). bd then imports nothing; the real data stays safe in dolt.
#
# macOS only (uses chflags). Linux equivalent: `chattr +i <file>` as root.

PROG=${0##*/}

die() {
  echo "error: $*" >&2
  exit "${2:-1}"
}

show_help() {
  cat <<'HELP'
gc-bd-import-breaker: pin <city>/.beads/issues.jsonl as a 0-byte immutable file to
stop bd's auto-import spiral (HACKS.md HACK 18; durable successor to HACK 3).

Usage:
  gc-bd-import-breaker [--city DIR]            Apply the breaker (default action)
  gc-bd-import-breaker --status [--city DIR]   Show current state, exit 0
  gc-bd-import-breaker --revert [--city DIR]   Clear immutability (undo)
  gc-bd-import-breaker -h | --help

Options:
  --city DIR   City root (the directory containing .beads/).
               Default: $GC_CITY if set, else the current directory.
  --status     Report whether the breaker is currently applied.
  --revert     Clear the immutable flag so bd/exports can manage the file again.
  -h, --help   Show this help.

Apply does:
  1. Refuses unless <city>/.beads/dolt exists (so it never strands the only copy).
  2. Backs up a non-empty issues.jsonl to issues.jsonl.breaker-backup-<UTC>.
  3. Replaces it with a 0-byte file and sets chflags uchg (immutable).
  Idempotent: re-running when already applied is a no-op.

After --revert, issues.jsonl becomes writable again; the spiral can return on a
bd build older than the 1cf8337 "auto-import blocked in dolt-native mode" fix.
Re-run apply (or upgrade bd) to be safe.
HELP
}

ACTION=apply
CITY=${GC_CITY:-}

while [[ $# -gt 0 ]]; do
  case $1 in
  -h | --help)
    show_help
    exit 0
    ;;
  --status)
    ACTION=status
    shift
    ;;
  --revert)
    ACTION=revert
    shift
    ;;
  --city)
    CITY=${2:-}
    shift 2
    ;;
  --city=*)
    CITY=${1#*=}
    shift
    ;;
  --)
    shift
    break
    ;;
  -*) die "unknown option: $1 (try --help)" ;;
  apply)
    ACTION=apply
    shift
    ;;
  *) die "unexpected argument: $1 (try --help)" ;;
  esac
done

[[ "$(uname)" == "Darwin" ]] || die "macOS only (uses chflags). Linux: 'chattr +i <file>' as root."

[[ -n $CITY ]] || CITY=$PWD
BEADS_DIR="$CITY/.beads"
JSONL="$BEADS_DIR/issues.jsonl"
[[ -d $BEADS_DIR ]] || die "no .beads directory under city '$CITY' (looked for $BEADS_DIR)"

# size in bytes, or empty string if the path does not exist
file_size() { /usr/bin/stat -f '%z' "$1" 2>/dev/null || true; }
# string flags (e.g. "uchg"), or empty
file_flags() { /usr/bin/stat -f '%Sf' "$1" 2>/dev/null || true; }
is_immutable() { file_flags "$1" | grep -q 'uchg'; }

case $ACTION in
status)
  if [[ -e $JSONL ]]; then
    printf 'issues.jsonl: size=%sB flags=[%s]\n' "$(file_size "$JSONL")" "$(file_flags "$JSONL")"
    if [[ "$(file_size "$JSONL")" == "0" ]] && is_immutable "$JSONL"; then
      echo "breaker: APPLIED"
    else
      echo "breaker: NOT applied"
    fi
  else
    echo "issues.jsonl: absent (auto-import has no source right now, but not pinned)"
  fi
  ;;

revert)
  if [[ -e $JSONL ]] && is_immutable "$JSONL"; then
    chflags nouchg "$JSONL"
    echo "breaker reverted: cleared uchg on $JSONL"
    echo "note: bd/exports may now refill it; re-apply if the spiral returns."
    command -v otlp_log >/dev/null 2>&1 && otlp_log INFO "breaker revert" city="$CITY" || true
  else
    echo "breaker not applied (nothing to revert): $JSONL"
  fi
  ;;

apply)
  # Never empty the JSONL unless we can see the durable copy in dolt.
  [[ -d "$BEADS_DIR/dolt" ]] || die "no $BEADS_DIR/dolt — refusing; cannot confirm data is safe in dolt"

  # Idempotent: already 0-byte + immutable.
  if [[ -e $JSONL && "$(file_size "$JSONL")" == "0" ]] && is_immutable "$JSONL"; then
    echo "breaker already applied (0-byte + uchg): $JSONL"
    exit 0
  fi

  # Clear any pre-existing immutability so we can replace the file.
  if [[ -e $JSONL ]] && is_immutable "$JSONL"; then
    chflags nouchg "$JSONL"
  fi

  # Back up real content (a regular non-empty file) before discarding it.
  bak=""
  if [[ -f $JSONL && -s $JSONL ]]; then
    ts=$(date -u +%Y%m%dT%H%M%SZ)
    bak="$JSONL.breaker-backup-$ts"
    cp -p "$JSONL" "$bak"
    echo "backed up $(file_size "$bak")B -> $bak"
  fi

  rm -f "$JSONL"
  : >"$JSONL"
  chflags uchg "$JSONL"

  if [[ "$(file_size "$JSONL")" == "0" ]] && is_immutable "$JSONL"; then
    echo "breaker APPLIED: $JSONL is now 0-byte + immutable (uchg)"
    echo "  verify: /bin/ls -lO '$JSONL'"
    echo "  undo:   $PROG --revert --city '$CITY'"
    command -v otlp_log >/dev/null 2>&1 && otlp_log INFO "breaker apply" city="$CITY" backed_up="${bak:+yes}" || true
  else
    die "verification failed: $JSONL is not empty+immutable"
  fi
  ;;
esac
