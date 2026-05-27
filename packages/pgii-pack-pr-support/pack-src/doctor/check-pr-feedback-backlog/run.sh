#!/bin/sh
# Doctor check: backlog of open actionable feedback beads.
#
# pr-watcher creates feedback beads; pr-self-fixer drains them by acting
# (or closing as "not-mine"). When the drain side stalls — agent blocked
# on infra (e.g. missing step CLI for ZR-Private SSH), agent crashing,
# wake-on-work not firing — beads pile up silently. This catches the
# pile-up in two complementary ways:
#
#   - COUNT: too many open at once (queue is growing)
#   - AGE:   oldest is too old (drain has fully stopped)
#
# Either tripping is an alert; the message reports both.
#
# Mirrors the same query pr-self-fixer uses
# (issue_type=feedback, actionable=true, role_hint=mine) so the count
# is exactly what the agent sees as its work queue.
#
# Tunable via env:
#   PR_FEEDBACK_BACKLOG_COUNT       — max open count before alert (default 15)
#   PR_FEEDBACK_BACKLOG_AGE_HOURS   — max age of oldest open in hours (default 4)
#
# Exit 0 = healthy, 2 = alert. set -e intentionally NOT used; missing
# data is treated as "no signal", not "FAIL".

MAX_COUNT="${PR_FEEDBACK_BACKLOG_COUNT:-15}"
MAX_AGE_HOURS="${PR_FEEDBACK_BACKLOG_AGE_HOURS:-4}"

TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT INT TERM

gc bd list --status=open --type=feedback --json --limit 0 >"$TMP" 2>/dev/null

if [ ! -s "$TMP" ] || ! jq -e 'type == "array"' <"$TMP" >/dev/null 2>&1; then
  echo "pr-feedback-backlog: bd list returned no data (dolt down? bd unreachable?) — skipping"
  exit 0
fi

# Filter to the exact set pr-self-fixer would pick up.
QUEUE_FILE="$TMP.queue"
jq -c '[.[] | select(.metadata.actionable == "true" and .metadata.role_hint == "mine")]' \
  <"$TMP" >"$QUEUE_FILE" 2>/dev/null

COUNT=$(jq -r 'length' <"$QUEUE_FILE" 2>/dev/null)
COUNT="${COUNT:-0}"

# Oldest bead's created_at. Fall back to updated_at if missing.
OLDEST_TS=$(jq -r '
  sort_by(.created_at // .updated_at // "")
  | .[0]
  | (.created_at // .updated_at // "")
' <"$QUEUE_FILE" 2>/dev/null)

OLDEST_HOURS=0
if [ -n "$OLDEST_TS" ] && [ "$OLDEST_TS" != "null" ] && [ "$OLDEST_TS" != "" ]; then
  OLDEST_EPOCH=$(date -d "$OLDEST_TS" "+%s" 2>/dev/null || echo "")
  if [ -n "$OLDEST_EPOCH" ]; then
    NOW_EPOCH=$(date +%s)
    OLDEST_HOURS=$(((NOW_EPOCH - OLDEST_EPOCH) / 3600))
  fi
fi

ALERT=0
REASONS=""
if [ "$COUNT" -gt "$MAX_COUNT" ]; then
  REASONS="count=$COUNT exceeds threshold $MAX_COUNT"
  ALERT=1
fi
if [ "$OLDEST_HOURS" -gt "$MAX_AGE_HOURS" ]; then
  if [ -n "$REASONS" ]; then REASONS="$REASONS; "; fi
  REASONS="${REASONS}oldest=${OLDEST_HOURS}h exceeds ${MAX_AGE_HOURS}h"
  ALERT=1
fi

if [ "$ALERT" -eq 1 ]; then
  echo "pr-feedback-backlog: $REASONS"
  echo "Fix: gc session peek pgii-pr-support.pr-self-fixer (or attach) — what is it doing?"
  echo "     gc events --since 1h --type session.woke | grep pr-self-fixer (wake-on-work firing?)"
  echo "     check for upstream infra blocks (step CLI for ZR-Private SSH, gh auth, etc.)"
  exit 2
fi

echo "pr-feedback-backlog: count=$COUNT (max $MAX_COUNT), oldest=${OLDEST_HOURS}h (max ${MAX_AGE_HOURS}h)"
exit 0
