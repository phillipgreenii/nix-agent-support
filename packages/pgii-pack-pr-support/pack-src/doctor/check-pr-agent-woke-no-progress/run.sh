#!/bin/sh
# Doctor check: pr-* agents woken but making no progress.
#
# Faster than the throughput check: this fires inside the wake-up loop
# itself. If an agent woke N+ times in the last hour, has a non-empty
# work queue, but closed zero of its target beads in that window — it's
# stuck on something downstream of wake-on-work (typically an infra
# block like missing step CLI for ZR-Private SSH, or a credential
# expiry). Catches the pattern faster than waiting on the throughput
# window to elapse.
#
# Only triggers when ALL three conditions hold:
#   - wake count for the agent >= MIN_WOKES
#   - open queue for that agent > 0 (otherwise wakes are vestigial)
#   - closes in window == 0
#
# Tunable via env:
#   PR_WOKE_WINDOW    — observation window (default 1h)
#   PR_WOKE_MIN_WOKES — min wake events to qualify (default 3)
#
# Exit 0 = healthy, 2 = alert. set -e intentionally NOT used.

WINDOW="${PR_WOKE_WINDOW:-1h}"
MIN_WOKES="${PR_WOKE_MIN_WOKES:-3}"

WINDOW_SECONDS=$(echo "$WINDOW" | awk '
  /^[0-9]+s$/ { sub(/s$/,""); print; exit }
  /^[0-9]+m$/ { sub(/m$/,""); print $0*60; exit }
  /^[0-9]+h$/ { sub(/h$/,""); print $0*3600; exit }
  /^[0-9]+d$/ { sub(/d$/,""); print $0*86400; exit }
  { print 0 }
')
if [ "${WINDOW_SECONDS:-0}" -le 0 ]; then
  echo "pr-agent-woke-no-progress: bad PR_WOKE_WINDOW=$WINDOW — skipping"
  exit 0
fi

NOW_EPOCH=$(date +%s)
CUTOFF_EPOCH=$((NOW_EPOCH - WINDOW_SECONDS))

TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT INT TERM

# Wake events in window for pgii-pr-support.pr-* agents.
WAKES_FILE="$TMP.wakes"
gc events --since "$WINDOW" --type session.woke >"$WAKES_FILE" 2>/dev/null
if [ ! -s "$WAKES_FILE" ]; then
  echo "pr-agent-woke-no-progress: no session.woke events in $WINDOW — skipping"
  exit 0
fi

# Per-agent wake count.
WAKE_COUNTS=$(jq -rs '
  [.[] | select(.subject | startswith("pgii-pr-support.pr-"))]
  | group_by(.subject)
  | map({agent: .[0].subject, wakes: length})
  | .[] | "\(.agent)\t\(.wakes)"
' <"$WAKES_FILE" 2>/dev/null)

if [ -z "$WAKE_COUNTS" ]; then
  echo "pr-agent-woke-no-progress: no pgii-pr-support.pr-* wake events in $WINDOW"
  exit 0
fi

# Build queue + close snapshots once, partition per agent.
OPEN_FILE="$TMP.open"
CLOSED_FILE="$TMP.closed"
gc bd list --status=open --json --limit 0 >"$OPEN_FILE" 2>/dev/null
gc bd list --status=closed --json --limit 0 >"$CLOSED_FILE" 2>/dev/null

if ! jq -e 'type == "array"' <"$OPEN_FILE" >/dev/null 2>&1 ||
  ! jq -e 'type == "array"' <"$CLOSED_FILE" >/dev/null 2>&1; then
  echo "pr-agent-woke-no-progress: bd list returned no data — skipping"
  exit 0
fi

# Per-agent queries (mirror agent.toml work_queries).
agent_open() {
  agent="$1"
  case "$agent" in
  pgii-pr-support.pr-self-fixer)
    jq -r '[.[] | select(.issue_type == "feedback" and .metadata.actionable == "true" and .metadata.role_hint == "mine")] | length' \
      <"$OPEN_FILE" 2>/dev/null
    ;;
  pgii-pr-support.pr-triage)
    jq -r '[.[] | select(.issue_type == "triage")] | length' <"$OPEN_FILE" 2>/dev/null
    ;;
  pgii-pr-support.pr-reviewer)
    jq -r '[.[] | select(.issue_type == "action" and .metadata.kind == "review")] | length' \
      <"$OPEN_FILE" 2>/dev/null
    ;;
  *) echo 0 ;;
  esac
}
agent_closed_in_window() {
  agent="$1"
  case "$agent" in
  pgii-pr-support.pr-self-fixer) PREDICATE='.issue_type == "feedback" and .metadata.actionable == "true" and .metadata.role_hint == "mine"' ;;
  pgii-pr-support.pr-triage) PREDICATE='.issue_type == "triage"' ;;
  pgii-pr-support.pr-reviewer) PREDICATE='.issue_type == "action" and .metadata.kind == "review"' ;;
  *)
    echo 0
    return
    ;;
  esac
  jq --argjson cutoff "$CUTOFF_EPOCH" -r "
    [.[]
     | select($PREDICATE)
     | select((.closed_at // \"\") != \"\")
     | (.closed_at | sub(\"\\\\.[0-9]+Z\$\"; \"Z\") | fromdateiso8601) as \$ts
     | select(\$ts >= \$cutoff)
    ] | length
  " <"$CLOSED_FILE" 2>/dev/null
}

echo "$WAKE_COUNTS" | while IFS="$(printf '\t')" read -r AGENT WAKES; do
  [ -z "$AGENT" ] && continue
  WAKES="${WAKES:-0}"
  if [ "$WAKES" -lt "$MIN_WOKES" ]; then
    continue
  fi
  OPEN=$(agent_open "$AGENT")
  OPEN="${OPEN:-0}"
  CLOSED=$(agent_closed_in_window "$AGENT")
  CLOSED="${CLOSED:-0}"
  if [ "$OPEN" -gt 0 ] && [ "$CLOSED" -eq 0 ]; then
    echo "  $AGENT woke=$WAKES, open=$OPEN, closed=$CLOSED in $WINDOW — WEDGED"
    echo "WEDGED" >"$TMP.alert"
  else
    echo "  $AGENT woke=$WAKES, open=$OPEN, closed=$CLOSED in $WINDOW (ok)"
  fi
done

if [ -f "$TMP.alert" ]; then
  echo "pr-agent-woke-no-progress: agent(s) woken $MIN_WOKES+ times but closed nothing despite open work"
  echo "Fix: gc session peek/attach the wedged agent — what step fails?"
  echo "     typical causes: missing tool on PATH (step CLI for ZR-Private SSH),"
  echo "     gh auth expired, or LLM stuck in a refusal loop on the work."
  exit 2
fi

echo "pr-agent-woke-no-progress: no wedged agents in $WINDOW"
exit 0
