#!/bin/sh
# city-foreman work_query — hq-scoped.
#
# Returns bead IDs that need city-foreman attention:
#   - newly-arrived (status=open AND not foreman-triaged), OR
#   - explicitly tagged needs-foreman (a worker raised it), OR
#   - bug/task/feature with missing acceptance_criteria
#
# Excludes:
#   - type=triage (triager and personal-foreman handle these)
#   - already escalated (gc:escalation label)
#   - status != open
#
# Output: bare bead IDs, one per line.

set -u

gc bd list --status=open --json --limit 0 2>/dev/null |
  jq -r '
    if type == "array" then
      .[]
      | select(
          .status == "open"
          and (.issue_type != "triage")
          and ((.labels // []) | any(. == "gc:escalation") | not)
          and (
            ((.labels // []) | any(startswith("foreman-triaged:")) | not)
            or ((.labels // []) | any(. == "needs-foreman"))
            or (
              ((.labels // []) | any(startswith("foreman-triaged:")) | not)
              and (.issue_type == "bug" or .issue_type == "task" or .issue_type == "feature")
              and (.acceptance_criteria == null or .acceptance_criteria == "")
            )
          )
        )
      | .id
    else empty end
  '
