#!/bin/sh
# Doctor check: pr-watcher's recent terminal events.
#
# pr-watcher is the only source of feedback/triage beads for the pr-* agent
# suite — if it's wedged, nothing downstream sees PR activity. Surface a hard
# fail when the trailing run of cycles is failures. Counts only the
# consecutive failures at the head of the (newest-first) event stream; an
# isolated transient failure followed by a success doesn't alert.
#
# Tunable via env (overridable from gc doctor invocation):
#   PR_WATCHER_DOCTOR_WINDOW    — events lookback duration (default 2h)
#   PR_WATCHER_DOCTOR_THRESHOLD — min consecutive fails to alert (default 3)

WINDOW="${PR_WATCHER_DOCTOR_WINDOW:-2h}"
THRESHOLD="${PR_WATCHER_DOCTOR_THRESHOLD:-3}"

# Pull both terminal-state event streams to a temp file, then filter with jq.
# Some event messages contain literal newlines in their .message field
# (notably multi-line dolt error stacks), which is technically invalid JSON
# and trips strict parsers. We tolerate that: jq's parse errors stream to
# /dev/null and we keep whatever it successfully extracted. Strict-mode
# (set -e) is intentionally NOT used so transient gc/jq errors don't crash
# the check; missing-data is treated as "no signal", not "FAIL".
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT INT TERM

{
  gc events --since "$WINDOW" --type order.completed 2>/dev/null
  gc events --since "$WINDOW" --type order.failed 2>/dev/null
} >"$TMP"

EVENTS_FILE="$TMP.events"
jq -c 'select(.subject == "pr-watcher")' <"$TMP" >"$EVENTS_FILE" 2>/dev/null || true

if [ ! -s "$EVENTS_FILE" ]; then
  echo "pr-watcher: no terminal events in last $WINDOW (order disabled? just enabled?)"
  exit 0
fi

# Walk newest-first, count leading order.failed entries until we hit a
# success or run out. jq emits one .type per line in sort order; awk does
# the run-length count.
trailing_fails=$(
  jq -rs 'sort_by(.ts) | reverse | .[].type' <"$EVENTS_FILE" 2>/dev/null |
    awk '
      /^order\.failed$/ { count++; next }
      { saw_success=1; print count+0; exit }
      END { if (!saw_success) print count+0 }
    '
)

total=$(jq -rs 'length' <"$EVENTS_FILE" 2>/dev/null)
total_fails=$(jq -rs '[.[] | select(.type == "order.failed")] | length' <"$EVENTS_FILE" 2>/dev/null)

if [ "$trailing_fails" -ge "$THRESHOLD" ]; then
  last_msg=$(jq -rs 'sort_by(.ts) | reverse | .[0] | .message // "(no message)"' <"$EVENTS_FILE" 2>/dev/null)
  echo "pr-watcher: last $trailing_fails consecutive runs FAILED in $WINDOW (threshold=$THRESHOLD, total seen=$total)"
  echo "  most recent: $last_msg"
  echo "Fix: tail ~/gc/.cache/pgii-pr-support/pr-watcher/run-*.log for the cause;"
  echo "     review timeout/interval in the pgii-pr-support pack (orders/pr-watcher.toml.template before nix build);"
  echo "     for transient dolt errors, check gc doctor's dolt-health output."
  exit 2
fi

echo "pr-watcher: $total_fails/$total failed in $WINDOW, $trailing_fails trailing (under threshold $THRESHOLD)"
exit 0
