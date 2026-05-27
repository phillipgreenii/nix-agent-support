#!/bin/sh
# Doctor check: orphan PR-pipeline beads.
#
# Symptom: a bead is created by the PR pipeline (pr-watcher writes
# github-* sourced feedback/triage; pr-reviewer writes agent-sourced
# review-draft feedback) but no agent's work_query selects it. The bead
# then sits open forever, or — more wastefully — pr-self-fixer LLM-time
# is spent scanning, deciding "not mine", and closing it manually with a
# bookkeeping `close_reason`. Either way the routing is broken.
#
# An "orphan" here is an open PR-pipeline bead that matches NONE of the
# three pr-* agent work_queries:
#
#   pr-self-fixer:  (feedback AND actionable=true AND role_hint=mine)
#                   OR (action AND kind=fix)
#   pr-triage:      triage
#   pr-reviewer:    action AND kind=review
#
# Newly-created beads get a grace period (PR_ORPHAN_MIN_AGE_MINUTES) so
# the watcher mid-cycle isn't flagged as broken.
#
# Tunable via env:
#   PR_ORPHAN_MIN_AGE_MINUTES — grace period before counting (default 30)
#   PR_ORPHAN_COUNT_THRESHOLD — min orphans before alert (default 3)
#
# Exit 0 = healthy, 2 = alert. set -e intentionally NOT used.

MIN_AGE_MIN="${PR_ORPHAN_MIN_AGE_MINUTES:-30}"
THRESHOLD="${PR_ORPHAN_COUNT_THRESHOLD:-3}"

NOW_EPOCH=$(date +%s)
CUTOFF_EPOCH=$((NOW_EPOCH - MIN_AGE_MIN * 60))

TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT INT TERM

gc bd list --status=open --json --limit 0 >"$TMP" 2>/dev/null
if [ ! -s "$TMP" ] || ! jq -e 'type == "array"' <"$TMP" >/dev/null 2>&1; then
  echo "pr-orphan-beads: bd list returned no data — skipping"
  exit 0
fi

# A bead is in PR-pipeline scope if its source/subsource markers say so.
# pr-watcher uses source values: github-ci, github-comment, github-pr, github-review.
# pr-reviewer uses source=agent, subsource=pr-reviewer.
# This is the over-approximation; the orphan filter trims it to "actually unrouted".
#
# Three work_query patterns translated into jq predicates:
ORPHANS_FILE="$TMP.orphans"
jq --argjson cutoff "$CUTOFF_EPOCH" -c '
  def pipeline_bead:
    (.metadata.source // "") as $s
    | (.metadata.subsource // "") as $ss
    | ( $s | startswith("github-") ) or ($s == "agent" and ($ss | startswith("pr-")));

  def pr_self_fixer_match:
    (.issue_type == "feedback" and .metadata.actionable == "true" and .metadata.role_hint == "mine")
    or (.issue_type == "action" and .metadata.kind == "fix");

  def pr_triage_match:
    .issue_type == "triage";

  def pr_reviewer_match:
    .issue_type == "action" and .metadata.kind == "review";

  def any_agent_match:
    pr_self_fixer_match or pr_triage_match or pr_reviewer_match;

  # Beads carrying the canonical `human` label are routed to a human via
  # `bd human list` — not orphans, just awaiting a person.
  def routed_to_human:
    (.labels // []) | any(. == "human");

  [.[]
   | select(pipeline_bead)
   | select(any_agent_match | not)
   | select(routed_to_human | not)
   | select((.created_at // .updated_at // "") != "")
   | select(((.created_at // .updated_at) | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601) <= $cutoff)
  ]
' <"$TMP" >"$ORPHANS_FILE" 2>/dev/null

if [ ! -s "$ORPHANS_FILE" ]; then
  echo "pr-orphan-beads: no PR-pipeline beads in store (skipping)"
  exit 0
fi

COUNT=$(jq -r 'length' <"$ORPHANS_FILE" 2>/dev/null)
COUNT="${COUNT:-0}"

if [ "$COUNT" -ge "$THRESHOLD" ]; then
  echo "pr-orphan-beads: $COUNT bead(s) match no agent's work_query (older than ${MIN_AGE_MIN}m, threshold=$THRESHOLD)"
  # Sample up to 5 IDs for the operator.
  SAMPLE=$(jq -r '
    .[:5]
    | map("  \(.id) (type=\(.issue_type), source=\(.metadata.source // "-")/\(.metadata.subsource // "-"), role_hint=\(.metadata.role_hint // "-"), kind=\(.metadata.kind // "-"))")
    | join("\n")
  ' <"$ORPHANS_FILE" 2>/dev/null)
  echo "$SAMPLE"
  echo "Fix: set role_hint/kind on creation in pr-watcher.sh / pr-reviewer drafts,"
  echo "     or widen an agent's work_query to claim them."
  exit 2
fi

echo "pr-orphan-beads: $COUNT unrouted (under threshold $THRESHOLD)"
exit 0
