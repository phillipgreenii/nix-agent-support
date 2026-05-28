#!/bin/sh
# foreman work_query — cross-rig, prints bead IDs that need foreman attention.
#
# Iterates over every rig (including the `gc` HQ bd) since the foreman is
# scope=city but we want it to triage rig-bound work too. Without this
# script, a city-scope `bd list` only sees the city bd, missing anything
# the worker pools care about.
#
# A bead needs foreman attention if EITHER:
#   - it has the `needs-foreman` label (a worker escalated it), OR
#   - its issue_type is bug/task/feature/triage AND acceptance_criteria
#     is missing (workers can't claim it as-is).
#
# Beads already escalated to mayor (label `gc:escalation`) are excluded.
#
# Output: bare bead IDs on stdout (one per line). The foreman session
# uses these via gc's work-routing machinery to discover what to work.
#
# NOTE: bead IDs are unique across the city+rigs (each rig has its own
# prefix), so the foreman can derive which rig a bead lives in by
# matching the prefix against `gc rig list`. The prompt covers that.

set -u

# Bail safely if rig list is unreachable — better to print no IDs (which
# is interpreted as "no work") than to crash the work-query.
RIGS=$(gc rig list --json 2>/dev/null | jq -r '.rigs[]?.name // empty' 2>/dev/null)
if [ -z "$RIGS" ]; then
  exit 0
fi

# Collect every rig's open beads, then filter with one jq pass.
# The temp file avoids holding all output in shell vars (some descriptions
# contain newlines that confuse `$(...)` capture).
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT INT TERM

for rig in $RIGS; do
  gc bd --rig="$rig" list --status=open --json --limit 0 2>/dev/null >>"$TMP" || true
done

# Each line in $TMP is a JSON array. Slurp and concatenate via `add`,
# then filter.
jq -rs '
  add // []
  | .[]
  | select(
      .status == "open"
      and ((.labels // []) | any(. == "gc:escalation") | not)
      and (
        ((.labels // []) | any(. == "needs-foreman"))
        or (
          (.issue_type == "bug" or .issue_type == "task" or .issue_type == "feature" or .issue_type == "triage")
          and (.acceptance_criteria == null or .acceptance_criteria == "")
        )
      )
    )
  | .id
' <"$TMP" 2>/dev/null
