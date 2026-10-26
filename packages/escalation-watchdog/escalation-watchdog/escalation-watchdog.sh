# shellcheck shell=bash
# escalation-watchdog - hand beads that stayed escalated too long to the operator
# (bead pg2-f9x7i, packet 2 of the stuck-escalation fix). One invocation is one
# tick against one tracker (--bd-dir); a timer in the deployment flake fires it.
#
# nix build already sources escalation-watchdog.bash ahead of this body
# (mkBashScript's hasSupportBash injection); this guard only fires for a raw
# `bash escalation-watchdog.sh` run (e.g. local bats), where nothing sourced it.
if ! declare -F ew_parse_duration >/dev/null 2>&1; then
  # shellcheck disable=SC1091  # sibling file resolved at runtime, by design
  source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/escalation-watchdog.bash"
fi

die() {
  echo "escalation-watchdog: $1" >&2
  exit "${2:-2}"
}

show_help() {
  cat <<'HELP'
escalation-watchdog: Hand beads that stayed escalated too long to the operator

Usage: escalation-watchdog --bd-dir DIR --stale-after DUR [--max-per-tick N]
                           [--quiet-for DUR] [--dry-run]

One tick: find the beads that have carried the `escalated` label for longer than
--stale-after and hand each one to the operator with `hand-to-human --also-add
stale-escalation --note ...` (labels human + attention + stale-escalation, minus
escalated, plus an explanatory note, in ONE guarded update). Use it from a timer;
it is safe to run repeatedly.

Selection: `bd ready --label escalated --unassigned` (so a bead waiting on an open
blocker, a deferred, in-progress or claimed bead is never touched), minus every
bead that already carries one of attention, human-focus, human-focus-required or
human. The clock is the time of the LATEST "Added label: escalated" event in
`bd history --events` (a re-escalation restarts it), falling back to the bead's
updated_at only when no such event exists. A bead whose updated_at is younger than
--quiet-for is skipped (a triager may be working it). Oldest escalations go first,
at most --max-per-tick per run.

The hand-off is guarded against a concurrent claim (--if-assignee "" --if-status
open). If the guard does not match, someone else got there first: the bead is
skipped, which is not an error.

Options:
  --bd-dir DIR        Tracker root to run bd against (bd -C DIR). Required.
  --stale-after DUR   Hand off beads escalated for longer than DUR. Required.
  --max-per-tick N    Hand off at most N beads per run (default 3).
  --quiet-for DUR     Skip a bead updated within the last DUR (default 2m).
  --dry-run           Print what would be handed off; write nothing.
  -h, --help          Show this help message
  -v, --version       Show version information

DUR is strictly <n>h, <n>m or <n>s (for example 12h, 90m, 30s). Anything else,
including a compound such as 1h30m or a bare number, is a usage error (exit 2).

Environment:
  ESCALATION_WATCHDOG_NOW   Override the current time (tests only): an integer of
                            epoch seconds or an RFC 3339 UTC timestamp.

Output (stdout, one line per decision): `handed-off ID age=...`,
`would-hand-off ID age=...` (dry run), `skipped-claimed ID` (guard mismatch) or
`failed ID exit=N`, then a summary line.

Exit status:
  0  ran to completion (including "nothing stale")
  1  unexpected error (for example bd, jq or hand-to-human is not installed)
  2  usage error; nothing was written
  3  at least one hand-off failed (the others were still attempted)
  4  the tracker query failed; nothing was handed off

Examples:
  escalation-watchdog --bd-dir /path/to/tracker --stale-after 12h
  escalation-watchdog --bd-dir /path/to/tracker --stale-after 12h --max-per-tick 1 --dry-run

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

BD_DIR=""
STALE_AFTER=""
QUIET_FOR="2m"
MAX_PER_TICK="3"
DRY_RUN=0

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --dry-run)
    DRY_RUN=1
    shift
    ;;
  --bd-dir | --stale-after | --quiet-for | --max-per-tick)
    [[ $# -ge 2 && -n $2 ]] || die "$1 requires a value"
    case "$1" in
    --bd-dir) BD_DIR="$2" ;;
    --stale-after) STALE_AFTER="$2" ;;
    --quiet-for) QUIET_FOR="$2" ;;
    --max-per-tick) MAX_PER_TICK="$2" ;;
    esac
    shift 2
    ;;
  *)
    die "unknown argument: $1 (see --help)"
    ;;
  esac
done

[[ -n $BD_DIR ]] || die "--bd-dir is required"
[[ -d $BD_DIR ]] || die "--bd-dir is not a directory: $BD_DIR"
[[ -n $STALE_AFTER ]] || die "--stale-after is required"
STALE_AFTER_S="$(ew_parse_duration "$STALE_AFTER")" || die "invalid --stale-after '$STALE_AFTER' (strictly <n>h, <n>m or <n>s)"
QUIET_FOR_S="$(ew_parse_duration "$QUIET_FOR")" || die "invalid --quiet-for '$QUIET_FOR' (strictly <n>h, <n>m or <n>s)"
[[ $MAX_PER_TICK =~ ^[1-9][0-9]{0,5}$ ]] || die "invalid --max-per-tick '$MAX_PER_TICK' (a positive integer)"
NOW="$(ew_now_epoch)" || die "unusable ESCALATION_WATCHDOG_NOW '${ESCALATION_WATCHDOG_NOW:-}' (epoch seconds or an RFC 3339 UTC timestamp)"
[[ -n $NOW ]] || die "unusable ESCALATION_WATCHDOG_NOW '${ESCALATION_WATCHDOG_NOW:-}' (epoch seconds or an RFC 3339 UTC timestamp)"

command -v bd >/dev/null 2>&1 || die "bd is not on PATH" 1
command -v jq >/dev/null 2>&1 || die "jq is not on PATH" 1
command -v hand-to-human >/dev/null 2>&1 || die "hand-to-human is not on PATH" 1

# --- selection ---------------------------------------------------------------
# J-1 envelope prelude: bd --json is a bare array unless BD_JSON_ENVELOPE=1.
ENVELOPE='(if type=="object" and has("data") then .data else . end)'
HUMAN_JSON="$(printf '%s\n' "${EW_HUMAN_LABELS[@]}" | jq -R . | jq -sc .)"

if ! READY_RAW="$(bd -C "$BD_DIR" ready --label "$EW_ESCALATED_LABEL" --unassigned --limit 0 --json 2>&1)"; then
  echo "escalation-watchdog: bd ready failed: $READY_RAW" >&2
  exit 4
fi
if ! CANDIDATES="$(jq -c --argjson human "$HUMAN_JSON" "$ENVELOPE"'
    | map(select(
        (.status == "open")
        and ((.assignee // "") == "")
        and (((.labels // []) | any(. as $l | $human | index($l) != null)) | not)
      ))
    | .[] | {id, updated_at}' <<<"$READY_RAW" 2>&1)"; then
  echo "escalation-watchdog: could not parse bd ready output: $CANDIDATES" >&2
  exit 4
fi

examined=0
fresh=0
quiet=0
declare -a STALE_LINES=()
while IFS= read -r cand; do
  [[ -n $cand ]] || continue
  examined=$((examined + 1))
  id="$(jq -r '.id' <<<"$cand")"
  updated_at="$(jq -r '.updated_at // ""' <<<"$cand")"
  [[ $id =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || continue
  if ! history="$(bd -C "$BD_DIR" history "$id" --events --json 2>/dev/null)"; then
    history="[]"
  fi
  if ! clock_line="$(ew_clock "$history" "$updated_at")"; then
    echo "escalation-watchdog: $id has no usable clock; skipped" >&2
    continue
  fi
  clock="${clock_line%% *}"
  source="${clock_line#* }"
  age=$((NOW - clock))
  if [[ $age -le $STALE_AFTER_S ]]; then
    fresh=$((fresh + 1))
    continue
  fi
  if upd="$(ew_to_epoch "$updated_at")" && [[ -n $upd && $((NOW - upd)) -lt $QUIET_FOR_S ]]; then
    quiet=$((quiet + 1))
    continue
  fi
  STALE_LINES+=("$age $id $source $clock")
done <<<"$CANDIDATES"

# Oldest escalation first, so the per-tick cap drains the worst backlog first.
declare -a ORDERED=()
if [[ ${#STALE_LINES[@]} -gt 0 ]]; then
  mapfile -t ORDERED < <(printf '%s\n' "${STALE_LINES[@]}" | sort -k1,1nr -k2,2)
fi

# --- action ------------------------------------------------------------------
handed=0
claimed=0
failed=0
deferred_by_cap=0
for line in ${ORDERED[@]+"${ORDERED[@]}"}; do
  read -r age id source clock <<<"$line"
  if [[ $handed -ge $MAX_PER_TICK ]]; then
    deferred_by_cap=$((deferred_by_cap + 1))
    continue
  fi
  if ! comments="$(bd -C "$BD_DIR" comments "$id" --json 2>/dev/null)"; then
    comments="[]"
  fi
  note="$(ew_build_note "$age" "$source" "$clock" "$(ew_last_comment "$comments")")"
  if [[ $DRY_RUN -eq 1 ]]; then
    printf 'would-hand-off %s age=%s clock=%s\n' "$id" "$(ew_format_age "$age")" "$source"
    handed=$((handed + 1))
    continue
  fi
  if hand-to-human "$id" --bd-dir "$BD_DIR" --also-add "$EW_MARKER_LABEL" --note "$note" --if-assignee "" --if-status open >/dev/null 2>&1; then
    rc=0
  else
    rc=$?
  fi
  case "$rc" in
  0)
    printf 'handed-off %s age=%s clock=%s\n' "$id" "$(ew_format_age "$age")" "$source"
    handed=$((handed + 1))
    ;;
  5)
    # The claim guard did not match: someone else got there first. Not an error,
    # and not counted against the cap.
    printf 'skipped-claimed %s\n' "$id"
    claimed=$((claimed + 1))
    ;;
  *)
    printf 'failed %s exit=%s\n' "$id" "$rc"
    failed=$((failed + 1))
    ;;
  esac
done

printf 'escalation-watchdog: examined %s, %s %s, skipped %s fresh / %s quiet / %s claimed, %s over the cap, %s failed\n' \
  "$examined" "$([[ $DRY_RUN -eq 1 ]] && echo "would hand off" || echo "handed off")" "$handed" "$fresh" "$quiet" "$claimed" "$deferred_by_cap" "$failed"

[[ $failed -eq 0 ]] || exit 3
