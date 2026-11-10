#!/usr/bin/env bash
# wire-edges.sh
#
# The `wire-ordering` operation of the beads binding, in bulk: for every
# planned edge it runs `bd dep add <blocked> --blocked-by <blocker>` and then
# READS THE EDGE BACK with `bd dep list <blocked> --json`, failing the run when
# the blocker is not listed as a `blocks` dependency of the blocked packet (a
# reversed edge blocks the wrong issue silently). A failed read-back is retried
# once (core skill step 9), then fails the run. After all edges it runs
# `bd dep cycles` (database-global) and fails only on a cycle that names one of
# this run's packets.
set -euo pipefail

show_help() {
  cat <<'HELP'
wire-edges.sh: wire plan-decompose ordering edges with a read-back on every edge

Usage:
  wire-edges.sh --edges <file> [--actor <id>]

--edges <file>   One edge per line: "<blocked-id> <blocker-id>" (whitespace
                 separated; blank lines and lines starting with # are skipped).
                 The blocked packet waits for the blocker.
--actor <id>     Passed to every `bd dep add` (audit trail).
-h, --help       Show this help.

Per edge: bd dep add <blocked> --blocked-by <blocker>, then
bd dep list <blocked> --json must list <blocker> with dependency_type
"blocks". One retry on a failed read-back. Prints one "ok <blocked> <- <blocker>"
line per verified edge.

Exit codes:
  0  every edge wired and read back, no cycle through this run's packets
  2  usage error / malformed edges file
  3  an edge failed its read-back after the retry (reversed edges are named)
  4  `bd dep cycles` reports a cycle naming one of this run's packets
  1  any other failure (bd or jq missing, bd itself failed)
HELP
}

die() {
  echo "wire-edges.sh: error: $1" >&2
  exit "${2:-1}"
}

EDGES=""
ACTOR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --edges)
    [[ $# -ge 2 ]] || die "--edges requires a value" 2
    EDGES="$2"
    shift 2
    ;;
  --actor)
    [[ $# -ge 2 ]] || die "--actor requires a value" 2
    ACTOR="$2"
    shift 2
    ;;
  *) die "unknown option: $1" 2 ;;
  esac
done

[[ -n $EDGES ]] || die "--edges is required" 2
[[ -f $EDGES ]] || die "--edges file not found: $EDGES" 2
command -v bd >/dev/null 2>&1 || die "bd not found on PATH"
command -v jq >/dev/null 2>&1 || die "jq not found on PATH"

BLOCKED=()
BLOCKERS=()
lineno=0
while IFS= read -r line || [[ -n $line ]]; do
  lineno=$((lineno + 1))
  [[ -z ${line//[[:space:]]/} || $line =~ ^[[:space:]]*# ]] && continue
  read -r a b extra <<<"$line"
  [[ -n ${a:-} && -n ${b:-} && -z ${extra:-} ]] || die "$EDGES:$lineno: expected '<blocked> <blocker>', got: $line" 2
  [[ $a != "$b" ]] || die "$EDGES:$lineno: self-edge on $a" 2
  BLOCKED+=("$a")
  BLOCKERS+=("$b")
done <"$EDGES"
[[ ${#BLOCKED[@]} -gt 0 ]] || die "$EDGES holds no edges" 2

ACTOR_ARGS=()
[[ -z $ACTOR ]] || ACTOR_ARGS=(--actor "$ACTOR")

# listed <issue> <other> <field>: does `bd dep list <issue>` list <other> with
# dependency_type "blocks"?
lists_blocker() {
  local raw
  raw="$(bd dep list "$1" --json)" || die "bd dep list $1 failed"
  jq -e --arg id "$2" '(if type == "array" then . else .data end) | any(.[]; .id == $id and .dependency_type == "blocks")' <<<"$raw" >/dev/null
}

for i in "${!BLOCKED[@]}"; do
  blocked="${BLOCKED[$i]}"
  blocker="${BLOCKERS[$i]}"
  verified=0
  for _ in 1 2; do
    bd dep add "$blocked" --blocked-by "$blocker" "${ACTOR_ARGS[@]}" >/dev/null ||
      die "bd dep add $blocked --blocked-by $blocker failed"
    if lists_blocker "$blocked" "$blocker"; then
      verified=1
      break
    fi
  done
  if [[ $verified -ne 1 ]]; then
    if lists_blocker "$blocker" "$blocked"; then
      die "edge $blocked <- $blocker is REVERSED: $blocker is blocked by $blocked" 3
    fi
    die "edge $blocked <- $blocker failed read-back after retry: $blocker not listed as a blocker of $blocked" 3
  fi
  echo "ok $blocked <- $blocker"
done

CYCLES="$(bd dep cycles)" || die "bd dep cycles failed"
if ! grep -q 'No dependency cycles' <<<"$CYCLES"; then
  hits=""
  for id in "${BLOCKED[@]}" "${BLOCKERS[@]}"; do
    esc="${id//./\\.}"
    # whole-id match: "x-1" must not match inside "x-10" or "x-1.5"
    hits+="$(grep -E -- "(^|[^A-Za-z0-9._-])${esc}(\$|[^A-Za-z0-9._-]|\.(\$|[^A-Za-z0-9]))" <<<"$CYCLES" || true)"$'\n'
  done
  hits="$(awk 'NF && !seen[$0]++' <<<"$hits")"
  if [[ -n $hits ]]; then
    printf '%s\n' "$hits" >&2
    die "dependency cycle through this run's packets" 4
  fi
  echo "note: bd dep cycles reported cycles, none through this run's packets (ignored)" >&2
fi
