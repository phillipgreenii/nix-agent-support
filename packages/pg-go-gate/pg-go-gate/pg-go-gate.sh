# shellcheck shell=bash

show_help() {
  cat <<'HELP'
pg-go-gate: Host-wide slot gate for agent go test / go build / go install.

Usage: pg-go-gate [OPTIONS] [--] COMMAND [ARGS...]

Runs COMMAND. When COMMAND is `go` followed by `test`, `build` or `install`,
it first takes one of N host-wide slots (default 3), waits for a free one for
at most a bounded time, and runs the command at nice level 10 (never higher,
and never at background QoS). Any other command runs untouched.

The wait FAILS OPEN: when no slot frees up in time the command runs anyway
with a warning, so a wedged slot can never block an agent.

Slots are symlinks slot.1..slot.N under the lock root; the link target records
the owner as "pid:process start time". A slot whose owner is dead (or whose pid
now has a different start time) is reclaimed. The cap is a soft bound.

Options:
  -h, --help       Show this help message
  -v, --version    Show version information
  -s, --slots N    Number of slots (default: $PG_GO_GATE_SLOTS, else 3)
  -w, --wait SEC   Seconds to wait for a slot (default: $PG_GO_GATE_WAIT_SECONDS, else 600)
  -n, --nice N     Nice level 0-10 (default: $PG_GO_GATE_NICE, else 10)
  -d, --dir DIR    Lock root (default: $PG_GO_GATE_LOCK_ROOT, else /tmp/pg-go-gate-slots.UID)

Exit status: the command's own exit status; 1 usage error.

Example:
  pg-go-gate go test -p 4 ./...

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

die() {
  echo "pg-go-gate: error: $1" >&2
  exit "${2:-1}"
}

SLOTS="${PG_GO_GATE_SLOTS:-3}"
WAIT_SECS="${PG_GO_GATE_WAIT_SECONDS:-600}"
NICE_LEVEL="${PG_GO_GATE_NICE:-10}"
LOCK_ROOT="${PG_GO_GATE_LOCK_ROOT:-/tmp/pg-go-gate-slots.$(id -u)}"

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  -s | --slots)
    [[ $# -ge 2 ]] || die "option $1 requires a value"
    SLOTS="$2"
    shift 2
    ;;
  -w | --wait)
    [[ $# -ge 2 ]] || die "option $1 requires a value"
    WAIT_SECS="$2"
    shift 2
    ;;
  -n | --nice)
    [[ $# -ge 2 ]] || die "option $1 requires a value"
    NICE_LEVEL="$2"
    shift 2
    ;;
  -d | --dir)
    [[ $# -ge 2 ]] || die "option $1 requires a value"
    LOCK_ROOT="$2"
    shift 2
    ;;
  --)
    shift
    break
    ;;
  -*) die "unknown option: $1" ;;
  *) break ;;
  esac
done

[[ $# -gt 0 ]] || die "missing COMMAND (usage: pg-go-gate [OPTIONS] [--] COMMAND [ARGS...])"
[[ $SLOTS =~ ^[1-9][0-9]*$ ]] || die "slots must be a positive integer: $SLOTS"
[[ $WAIT_SECS =~ ^[0-9]+$ ]] || die "wait must be a non-negative integer: $WAIT_SECS"
[[ $NICE_LEVEL =~ ^([0-9]|10)$ ]] || die "nice must be an integer 0-10 (higher starves the process): $NICE_LEVEL"

# Only go test / build / install are gated; everything else runs as given.
if [[ ${1##*/} != go ]] || [[ ${2:-} != test && ${2:-} != build && ${2:-} != install ]]; then
  exec "$@"
fi

proc_stamp() {
  local out
  out="$(ps -o lstart= -p "$1" 2>/dev/null)" || return 1
  [[ -n $out ]] || return 1
  tr -s ' ' <<<"$out"
}

owner_alive() {
  local owner="$1" pid stamp now
  pid="${owner%%:*}"
  stamp="${owner#*:}"
  [[ $pid =~ ^[0-9]+$ ]] || return 1
  now="$(proc_stamp "$pid")" || return 1
  [[ $now == "$stamp" ]]
}

SLOT_HELD=""

# Sets SLOT_HELD and returns 0 once a slot is taken; returns 1 when none freed
# within the wait budget or the lock root is unusable (callers fail open).
slot_acquire() {
  local owner stamp k link target aside tries
  mkdir -p "$LOCK_ROOT" 2>/dev/null || return 1
  stamp="$(proc_stamp "$$")" || return 1
  owner="$$:$stamp"
  tries=$((WAIT_SECS * 5))
  while :; do
    for ((k = 1; k <= SLOTS; k++)); do
      link="$LOCK_ROOT/slot.$k"
      if ln -s "$owner" "$link" 2>/dev/null; then
        SLOT_HELD="$link"
        return 0
      fi
      target="$(readlink "$link" 2>/dev/null)" || continue
      if ! owner_alive "$target"; then
        # Stale: rename aside (atomic, one reclaimer wins), re-check what was
        # moved, then take the slot.
        aside="$LOCK_ROOT/.reclaim.$$.$k"
        if mv "$link" "$aside" 2>/dev/null; then
          if owner_alive "$(readlink "$aside" 2>/dev/null)"; then
            # Lost a race: moved a live owner's fresh link; put it back.
            mv -n "$aside" "$link" 2>/dev/null
            rm -f "$aside"
            continue
          fi
          rm -f "$aside"
          if ln -s "$owner" "$link" 2>/dev/null; then
            SLOT_HELD="$link"
            return 0
          fi
        fi
      fi
    done
    [[ $tries -gt 0 ]] || return 1
    tries=$((tries - 1))
    sleep 0.2
  done
}

# Releases the slot only if it is still ours (a reclaimer may have taken it).
slot_release() {
  local target stamp
  [[ -n $SLOT_HELD ]] || return 0
  target="$(readlink "$SLOT_HELD" 2>/dev/null)" || return 0
  stamp="$(proc_stamp "$$")" || stamp=""
  [[ $target == "$$:$stamp" ]] && rm -f "$SLOT_HELD"
  SLOT_HELD=""
  return 0
}

CHILD=""
forward_signal() {
  [[ -z $CHILD ]] || kill -s "$1" "$CHILD" 2>/dev/null || true
}

if ! slot_acquire; then
  echo "pg-go-gate: warning: no slot free after ${WAIT_SECS}s (slots=$SLOTS); running without one" >&2
fi
trap slot_release EXIT
trap 'forward_signal TERM' TERM
trap 'forward_signal INT' INT
trap 'forward_signal HUP' HUP

# nice is relative, so top the current niceness up to exactly NICE_LEVEL; a
# caller already niced at or above it runs unchanged (never above NICE_LEVEL).
CUR_NICE="$(ps -o ni= -p "$$" 2>/dev/null | tr -d ' ')"
[[ $CUR_NICE =~ ^-?[0-9]+$ ]] || CUR_NICE=0
if ((CUR_NICE < NICE_LEVEL)); then
  nice -n "$((NICE_LEVEL - CUR_NICE))" "$@" &
else
  "$@" &
fi
CHILD=$!
RC=0
while :; do
  wait "$CHILD" && RC=0 || RC=$?
  # A trapped signal interrupts wait (rc > 128) while the child lives on.
  kill -0 "$CHILD" 2>/dev/null || break
done
exit "$RC"
