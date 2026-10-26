# shellcheck shell=bash
# Core logic for escalation-watchdog (bead pg2-f9x7i) as testable functions:
# strict duration parsing, the injectable clock, the escalation-clock lookup over
# `bd history --events`, and the hand-off note. No top-level code -- mkBashScript
# sources this file before the .sh body.
#
# Why the watchdog exists: a bead that stays `escalated` is only ever offered to
# the escalation-triager once (the feed is change-based), so a triager session that
# ends with no action, or a declined dispatch, leaves the bead escalated forever.
# After a threshold the watchdog hands it to the operator (hand-to-human, which
# swaps escalated for human + attention) and marks it `stale-escalation`, the
# marker meaning "automation ran out of time; check infrastructure first". The
# vocabulary is docs/guides/bead-label-vocabulary.md (ADR 0105).
#
# Exit codes of escalation-watchdog (1 stays the generic error):
#   0  ran to completion (including "nothing stale")
#   2  usage error
#   3  at least one hand-off failed (the others were still attempted)
#   4  the tracker query failed (nothing was handed off)

# Labels that mean "a person already owns this" (HUMAN_LABELS of the vocabulary):
# a bead carrying any of them is never handed off.
# shellcheck disable=SC2034  # read by escalation-watchdog.sh after sourcing
EW_HUMAN_LABELS=(attention human-focus human-focus-required human)

# The label whose latest add-event starts the escalation clock.
EW_ESCALATED_LABEL=escalated

# The label the watchdog adds so the operator can tell a timed-out hand-off.
EW_MARKER_LABEL=stale-escalation

# ew_parse_duration DUR: print DUR as whole seconds. STRICT: <n>h, <n>m or <n>s
# with n a plain decimal number (no sign, no space, no compound like 1h30m, no
# bare number). Returns 1 on anything else.
ew_parse_duration() {
  local d="${1:-}" n unit
  [[ $d =~ ^([0-9]+)([hms])$ ]] || return 1
  n="${BASH_REMATCH[1]}"
  unit="${BASH_REMATCH[2]}"
  # Force base 10 (a leading zero would otherwise read as octal) and bound the
  # digits so the arithmetic cannot overflow.
  [[ ${#n} -le 9 ]] || return 1
  n=$((10#$n))
  case "$unit" in
  h) printf '%s\n' $((n * 3600)) ;;
  m) printf '%s\n' $((n * 60)) ;;
  s) printf '%s\n' "$n" ;;
  esac
}

# jq definition shared by the timestamp readers: bd emits RFC 3339 UTC
# (2026-10-26T01:59:48Z), sometimes with fractional seconds or a +00:00 zone.
EW_JQ_EPOCH='def ep: sub("\\.[0-9]+"; "") | sub("[+-]00:?00$"; "Z") | fromdateiso8601;'

# ew_to_epoch TS: print an RFC 3339 timestamp as epoch seconds; 1 when unparsable.
ew_to_epoch() {
  jq -nr --arg t "${1:-}" "$EW_JQ_EPOCH"' $t | ep' 2>/dev/null
}

# ew_from_epoch EPOCH: print epoch seconds as an RFC 3339 UTC timestamp.
ew_from_epoch() {
  jq -nr --argjson e "$1" '$e | todate'
}

# ew_now_epoch: the current time in epoch seconds. ESCALATION_WATCHDOG_NOW
# overrides the clock (tests only; documented in --help): an integer of epoch
# seconds or an RFC 3339 UTC timestamp. Returns 1 on an unusable override.
ew_now_epoch() {
  local o="${ESCALATION_WATCHDOG_NOW:-}"
  if [[ -z $o ]]; then
    date +%s
  elif [[ $o =~ ^[0-9]+$ ]]; then
    printf '%s\n' "$o"
  else
    ew_to_epoch "$o"
  fi
}

# ew_clock HISTORY_JSON UPDATED_AT: print "<epoch> <source>" for the escalation
# clock of one bead. The clock is the created_at of the LATEST label_added event
# whose comment is "Added label: escalated" (a re-escalation restarts it);
# source is `label_added`. Only when no such event exists does it fall back to the
# bead's updated_at (source `updated_at`). Returns 1 when neither is usable.
ew_clock() {
  local history="$1" updated_at="$2" ts
  if ts="$(jq -r "$EW_JQ_EPOCH"'
      (if type=="object" and has("data") then .data else . end)
      | map(select(.event_type == "label_added" and .comment == "Added label: '"$EW_ESCALATED_LABEL"'") | .created_at | ep)
      | if length == 0 then empty else max end' <<<"$history" 2>/dev/null)" && [[ -n $ts ]]; then
    printf '%s label_added\n' "$ts"
    return 0
  fi
  if ts="$(ew_to_epoch "$updated_at")" && [[ -n $ts ]]; then
    printf '%s updated_at\n' "$ts"
    return 0
  fi
  return 1
}

# ew_format_age SECONDS: "3h 5m", "45m" or "30s".
ew_format_age() {
  local s="$1" h m
  h=$((s / 3600))
  m=$(((s % 3600) / 60))
  if [[ $h -gt 0 ]]; then
    printf '%sh %sm\n' "$h" "$m"
  elif [[ $m -gt 0 ]]; then
    printf '%sm\n' "$m"
  else
    printf '%ss\n' "$s"
  fi
}

# ew_last_comment COMMENTS_JSON: the first line of the newest comment, cut to 160
# characters, or empty when there is none. A bead's last comment is the nearest
# thing to "the last triager outcome" the tracker records in a generic way.
ew_last_comment() {
  jq -r "$EW_JQ_EPOCH"'
    (if type=="object" and has("data") then .data else . end)
    | if length == 0 then "" else (sort_by(.created_at | ep) | last | .text // "" | split("\n")[0] | .[0:160]) end' <<<"$1" 2>/dev/null || true
}

# ew_build_note AGE_SECONDS SOURCE CLOCK_EPOCH LAST_COMMENT: the note appended to
# the bead. It leads with the fixed warning (the operator sees it first in the
# notes), then names how long the bead was escalated, which clock was used, and
# the last comment when known.
ew_build_note() {
  local age="$1" source="$2" clock="$3" last="$4" how
  case "$source" in
  label_added) how="the latest 'Added label: escalated' event" ;;
  *) how="the bead's updated_at (no 'Added label: escalated' event was found)" ;;
  esac
  printf '%s\n' "Automation timed out on this bead. The cause may be infrastructure (disk, usage limit, auth), not a decision only you can make. Check the cause first."
  printf 'Escalated for %s (since %s, measured from %s).\n' "$(ew_format_age "$age")" "$(ew_from_epoch "$clock")" "$how"
  if [[ -n $last ]]; then
    printf 'Last comment on the bead: %s\n' "$last"
  else
    printf 'No comment on the bead records a triager outcome.\n'
  fi
  printf 'Handed off by escalation-watchdog (label %s).' "$EW_MARKER_LABEL"
}
