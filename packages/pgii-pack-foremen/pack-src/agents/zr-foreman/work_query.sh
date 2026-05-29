#!/bin/sh
# zr-foreman work_query — zr-rig-scoped.
#
# Returns bead IDs in zr's db that need zr-foreman attention.
# See city-foreman/work_query.sh for the predicate; this script
# differs only in the rig flag.

set -u

gc --rig=ziprecruiter bd list --status=open --json --limit 0 2>/dev/null |
  jq -r '
    if type == "array" then
      .[]
      | select(
          .status == "open"
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
