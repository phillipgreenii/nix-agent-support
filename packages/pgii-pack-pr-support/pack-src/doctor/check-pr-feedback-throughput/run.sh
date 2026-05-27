#!/bin/sh
# Doctor check: feedback bead throughput.
#
# Complementary to check-pr-feedback-backlog: backlog catches "queue is
# large/old"; throughput catches "queue is moving slowly". Specifically,
# when open feedback beads exist AND zero have closed inside the
# observation window, the pipeline is producing without draining. This
# is a hard failure mode (worse than slow drain) because the queue size
# may not have hit the backlog threshold yet.
#
# An empty queue is healthy (no work → no closes expected → no alert).
#
# Tunable via env:
#   PR_FEEDBACK_THROUGHPUT_WINDOW   — observation window (default 6h)
#   PR_FEEDBACK_THROUGHPUT_MIN_OPEN — min open beads for alert (default 5)
#
# Exit 0 = healthy, 2 = alert. set -e intentionally NOT used.

WINDOW="${PR_FEEDBACK_THROUGHPUT_WINDOW:-6h}"
MIN_OPEN="${PR_FEEDBACK_THROUGHPUT_MIN_OPEN:-5}"

# Translate "6h" / "30m" / "2d" to seconds for `date -d`.
WINDOW_SECONDS=$(echo "$WINDOW" | awk '
  /^[0-9]+s$/ { sub(/s$/,""); print; exit }
  /^[0-9]+m$/ { sub(/m$/,""); print $0*60; exit }
  /^[0-9]+h$/ { sub(/h$/,""); print $0*3600; exit }
  /^[0-9]+d$/ { sub(/d$/,""); print $0*86400; exit }
  { print 0 }
')
if [ "${WINDOW_SECONDS:-0}" -le 0 ]; then
  echo "pr-feedback-throughput: bad PR_FEEDBACK_THROUGHPUT_WINDOW=$WINDOW (expected e.g. 6h, 30m) — skipping"
  exit 0
fi

NOW_EPOCH=$(date +%s)
CUTOFF_EPOCH=$((NOW_EPOCH - WINDOW_SECONDS))

TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT INT TERM

# Open count from the same pr-self-fixer query.
OPEN_FILE="$TMP.open"
gc bd list --status=open --type=feedback --json --limit 0 >"$OPEN_FILE" 2>/dev/null
if [ ! -s "$OPEN_FILE" ] || ! jq -e 'type == "array"' <"$OPEN_FILE" >/dev/null 2>&1; then
  echo "pr-feedback-throughput: bd list (open) returned no data — skipping"
  exit 0
fi
OPEN_COUNT=$(jq -r '[.[] | select(.metadata.actionable == "true" and .metadata.role_hint == "mine")] | length' \
  <"$OPEN_FILE" 2>/dev/null)
OPEN_COUNT="${OPEN_COUNT:-0}"

if [ "$OPEN_COUNT" -lt "$MIN_OPEN" ]; then
  echo "pr-feedback-throughput: open=$OPEN_COUNT below alert floor $MIN_OPEN — nothing to drain"
  exit 0
fi

# Closes inside the window.
CLOSED_FILE="$TMP.closed"
gc bd list --status=closed --type=feedback --json --limit 0 >"$CLOSED_FILE" 2>/dev/null
if [ ! -s "$CLOSED_FILE" ] || ! jq -e 'type == "array"' <"$CLOSED_FILE" >/dev/null 2>&1; then
  echo "pr-feedback-throughput: bd list (closed) returned no data — skipping"
  exit 0
fi

CLOSED_IN_WINDOW=$(jq --argjson cutoff "$CUTOFF_EPOCH" -r '
  [.[]
   | select(.metadata.actionable == "true" and .metadata.role_hint == "mine")
   | select((.closed_at // "") != "")
   | (.closed_at | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601) as $ts
   | select($ts >= $cutoff)
  ] | length
' <"$CLOSED_FILE" 2>/dev/null)
CLOSED_IN_WINDOW="${CLOSED_IN_WINDOW:-0}"

if [ "$CLOSED_IN_WINDOW" -eq 0 ]; then
  echo "pr-feedback-throughput: open=$OPEN_COUNT but ZERO closed in last $WINDOW — drain stuck"
  echo "Fix: gc session peek pgii-pr-support.pr-self-fixer (is it alive?)"
  echo "     gc events --since $WINDOW --type session.woke (is wake-on-work firing?)"
  echo "     check infra blocks (step CLI, gh auth) and tail pr-self-fixer logs"
  exit 2
fi

echo "pr-feedback-throughput: open=$OPEN_COUNT, closed=$CLOSED_IN_WINDOW in $WINDOW — draining"
exit 0
