#!/bin/sh
# Doctor check: open beads with no activity for N+ days.
#
# Catches forgotten or stalled work. Permissive on type by design — we want
# to see what surfaces before deciding which types deserve a long-lived
# exclusion. Likely candidates that will need exclusion in time:
#   - session (lives as long as the agent session does)
#   - pr (lives until GitHub closes the PR)
# but we're holding off until we have evidence those dominate the signal
# rather than just looking at theory.
#
# "Activity" = bd's updated_at, which bumps on any field change (title,
# labels, metadata, notes, status). That matches the operator-intent of
# "did anyone touch this lately?"
#
# Tunable via env:
#   STALE_BEADS_DAYS      — min age in days before alert (default 7)
#   STALE_BEADS_MIN_COUNT — min stale count to trigger alert (default 1)
#
# Exit 0 = healthy, 2 = alert. set -e intentionally NOT used.

STALE_DAYS="${STALE_BEADS_DAYS:-7}"
MIN_COUNT="${STALE_BEADS_MIN_COUNT:-1}"

NOW_EPOCH=$(date +%s)
CUTOFF_EPOCH=$((NOW_EPOCH - STALE_DAYS * 86400))

TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT INT TERM

gc bd list --status=open --json --limit 0 >"$TMP" 2>/dev/null

if [ ! -s "$TMP" ] || ! jq -e 'type == "array"' <"$TMP" >/dev/null 2>&1; then
  echo "stale-beads: bd list returned no data — skipping"
  exit 0
fi

STALE_FILE="$TMP.stale"
jq --argjson cutoff "$CUTOFF_EPOCH" -c '
  [.[]
   | select((.updated_at // .created_at // "") != "")
   | (.updated_at // .created_at | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601) as $ts
   | select($ts <= $cutoff)
  ]
' <"$TMP" >"$STALE_FILE" 2>/dev/null

COUNT=$(jq 'length' <"$STALE_FILE" 2>/dev/null)
COUNT="${COUNT:-0}"

if [ "$COUNT" -lt "$MIN_COUNT" ]; then
  echo "stale-beads: no beads older than $STALE_DAYS days (under threshold $MIN_COUNT)"
  exit 0
fi

echo "stale-beads: $COUNT open bead(s) untouched for $STALE_DAYS+ days"
jq -r '
  sort_by(.updated_at // .created_at)
  | .[:5]
  | .[]
  | "  \(.id) (type=\(.issue_type), updated=\(.updated_at // .created_at), \(.title[:80]))"
' <"$STALE_FILE" 2>/dev/null
if [ "$COUNT" -gt 5 ]; then
  echo "  ... and $((COUNT - 5)) more (see 'gc bd list --status=open --json | jq sort')"
fi
echo "Fix: triage each — work it, defer with 'gc bd defer --until', close if dead,"
echo "     or add 'human' label so it surfaces via 'gc bd human list'."
exit 2
