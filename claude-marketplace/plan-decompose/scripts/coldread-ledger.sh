#!/usr/bin/env bash
# coldread-ledger.sh
#
# Bookkeeping for the cold-read round of plan-decompose (core skill step 6).
# A refused Agent dispatch (the concurrent-subagent limit) is NOT a read; this
# ledger records, per packet key, every dispatch attempt, every refusal and the
# normalized verdict of each completed read, so that
#   * the next batch is always "every key not yet read" (refused ones retry),
#   * the report states the TRUE number of packets read, and
#   * `gate` refuses (exit 3) while any packet is unread, so a set with an
#     unread packet can never be released.
#
# The ledger is a directory; one set of tiny files per key. Read-only on
# everything but its own directory; never calls bd.
set -euo pipefail

show_help() {
  cat <<'HELP'
coldread-ledger.sh: track cold-read dispatches, refusals and verdicts

Usage:
  coldread-ledger.sh init    <ledger-dir> <key>...
  coldread-ledger.sh batch   <ledger-dir> <n>
  coldread-ledger.sh dispatched <ledger-dir> <key>
  coldread-ledger.sh refused <ledger-dir> <key>
  coldread-ledger.sh read    <ledger-dir> <key> <result-file>
  coldread-ledger.sh verdict <result-file>
  coldread-ledger.sh report  <ledger-dir>
  coldread-ledger.sh gate    <ledger-dir>

init        create the ledger with every packet key unread
batch       print up to <n> keys that are not yet read (never-dispatched and
            refused keys alike), one per line; empty output means done
dispatched  count one dispatch attempt for <key>
refused     the dispatch of <key> was refused: counts as NOT run (the key stays
            unread and is returned by `batch` again)
read        record a completed read from the cold-reader's answer file; stores
            its normalized verdict (see `verdict`)
verdict     print the normalized verdict of one cold-reader answer:
              ok        executable: yes
              advisory  executable: no, but every missing item is tagged
                        [implementer-owned] (or there are none): not a blocker
              blocking  at least one missing item tagged [decision], or an
                        UNTAGGED missing item (conservative), or an answer
                        without an "executable:" line
report      one line: total, read, unread, dispatch attempts, refused
            dispatches, and the blocking and advisory counts
gate        exit 3 naming each unread key; else exit 4 naming each blocking
            key; else exit 0 (safe to proceed to the post-check and release)

Exit codes: 0 ok, 2 usage, 3 unread packets, 4 blocking verdicts, 1 other.
HELP
}

die() {
  echo "coldread-ledger.sh: error: $1" >&2
  exit "${2:-1}"
}

# Normalize one cold-reader answer into ok|advisory|blocking.
verdict_of() {
  local f="$1" exe in_missing=0 saw_decision=0 saw_untagged=0 line
  [[ -f $f ]] || die "result file not found: $f" 2
  exe="$(grep -i -m1 -E '^[[:space:]]*([0-9]+\.)?[[:space:]]*(\*\*)?`?executable`?(\*\*)?:' "$f" || true)"
  if [[ -z $exe ]]; then
    echo blocking
    return
  fi
  while IFS= read -r line || [[ -n $line ]]; do
    if [[ $line =~ ^[[:space:]]*[0-9.]*[[:space:]]*(\*\*)?\`?missing\`?(\*\*)?: ]]; then
      in_missing=1
      continue
    fi
    if [[ $line =~ ^[[:space:]]*[0-9.]*[[:space:]]*(\*\*)?\`?(implementer-owned|unpinned-assumptions|executable)\`?(\*\*)?: ]]; then
      in_missing=0
      continue
    fi
    [[ $in_missing -eq 1 ]] || continue
    [[ $line =~ ^[[:space:]]*[-*][[:space:]]+(.*)$ ]] || continue
    local item="${BASH_REMATCH[1]}"
    case "$item" in
    '[implementer-owned]'*) ;;
    '[decision]'*) saw_decision=1 ;;
    none* | None* | NONE* | '(none)'* | n/a* | N/A*) ;;
    *) saw_untagged=1 ;;
    esac
  done <"$f"
  if [[ $exe =~ [Yy][Ee][Ss] ]]; then
    echo ok
  elif [[ $saw_decision -eq 1 || $saw_untagged -eq 1 ]]; then
    echo blocking
  else
    echo advisory
  fi
}

keys_of() {
  local d="$1" f
  for f in "$d"/*.key; do
    [[ -e $f ]] || continue
    basename "$f" .key
  done
}

need_ledger() {
  [[ -d ${1:-} ]] || die "ledger directory not found: ${1:-<missing>}" 2
}

need_key() {
  [[ -n ${2:-} && -e $1/$2.key ]] || die "unknown key: ${2:-<missing>}" 2
}

bump() {
  local f="$1" n=0
  [[ -f $f ]] && n="$(cat "$f")"
  echo $((n + 1)) >"$f"
}

count_of() {
  local f="$1"
  [[ -f $f ]] && cat "$f" || echo 0
}

cmd="${1:-}"
case "$cmd" in
-h | --help | help)
  show_help
  exit 0
  ;;
init)
  [[ $# -ge 3 ]] || die "init needs a ledger dir and at least one key" 2
  d="$2"
  shift 2
  mkdir -p "$d"
  for k in "$@"; do
    [[ $k =~ ^[A-Za-z0-9._-]+$ ]] || die "invalid key: $k" 2
    : >"$d/$k.key"
  done
  ;;
batch)
  [[ $# -eq 3 ]] || die "batch needs a ledger dir and a count" 2
  need_ledger "$2"
  [[ $3 =~ ^[1-9][0-9]*$ ]] || die "batch size must be a positive integer: $3" 2
  n=0
  while IFS= read -r k; do
    [[ -e $2/$k.read ]] && continue
    echo "$k"
    n=$((n + 1))
    [[ $n -lt $3 ]] || break
  done < <(keys_of "$2")
  ;;
dispatched)
  [[ $# -eq 3 ]] || die "dispatched needs a ledger dir and a key" 2
  need_ledger "$2"
  need_key "$2" "$3"
  bump "$2/$3.attempts"
  ;;
refused)
  [[ $# -eq 3 ]] || die "refused needs a ledger dir and a key" 2
  need_ledger "$2"
  need_key "$2" "$3"
  bump "$2/$3.refused"
  rm -f "$2/$3.read" "$2/$3.verdict"
  ;;
read)
  [[ $# -eq 4 ]] || die "read needs a ledger dir, a key and a result file" 2
  need_ledger "$2"
  need_key "$2" "$3"
  v="$(verdict_of "$4")"
  : >"$2/$3.read"
  echo "$v" >"$2/$3.verdict"
  ;;
verdict)
  [[ $# -eq 2 ]] || die "verdict needs a result file" 2
  verdict_of "$2"
  ;;
report | gate)
  [[ $# -eq 2 ]] || die "$cmd needs a ledger dir" 2
  need_ledger "$2"
  total=0 nread=0 attempts=0 refusals=0 blocking=0 advisory=0
  unread=() blockers=()
  while IFS= read -r k; do
    total=$((total + 1))
    attempts=$((attempts + $(count_of "$2/$k.attempts")))
    refusals=$((refusals + $(count_of "$2/$k.refused")))
    if [[ -e $2/$k.read ]]; then
      nread=$((nread + 1))
      case "$(cat "$2/$k.verdict")" in
      blocking)
        blocking=$((blocking + 1))
        blockers+=("$k")
        ;;
      advisory) advisory=$((advisory + 1)) ;;
      esac
    else
      unread+=("$k")
    fi
  done < <(keys_of "$2")
  if [[ $cmd == report ]]; then
    echo "total=$total read=$nread unread=$((total - nread)) dispatch-attempts=$attempts refused-dispatches=$refusals blocking=$blocking advisory=$advisory"
    exit 0
  fi
  if [[ ${#unread[@]} -gt 0 ]]; then
    echo "coldread-ledger.sh: ${#unread[@]} of $total packet(s) UNREAD (refused or never dispatched): ${unread[*]}" >&2
    exit 3
  fi
  if [[ ${#blockers[@]} -gt 0 ]]; then
    echo "coldread-ledger.sh: ${#blockers[@]} packet(s) with a blocking cold-read verdict: ${blockers[*]}" >&2
    exit 4
  fi
  ;;
"") die "missing subcommand (try --help)" 2 ;;
*) die "unknown subcommand: $cmd" 2 ;;
esac
