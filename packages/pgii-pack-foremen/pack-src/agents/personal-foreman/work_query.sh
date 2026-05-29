#!/bin/sh
# personal-foreman work_query — per-rig-db topology.
#
# Each of the 6 personal rigs has its own bd store. Iterate, union
# the results, then add type=personal-triage beads from hq.
#
# Per the spec's verified outcomes 2026-05-29: shared-db is not
# viable (gc 1.1.0 doesn't honor --rig for prefix selection when
# rigs share a dolt_database).

set -u

PERSONAL_RIGS="nix_overlay nix_personal nix_repo_base nix_ziprecruiter nix_support_apps nix_agent_support"

TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT INT TERM

for rig in $PERSONAL_RIGS; do
  gc --rig="$rig" bd list --status=open --json --limit 0 2>/dev/null >>"$TMP" || true
done

jq -rs '
  add // []
  | .[]
  | select(
      .status == "open"
      and (.issue_type != "triage")
      and (.issue_type != "personal-triage")
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
' <"$TMP"

# Also include open type=personal-triage beads in hq (the triager's
# personal-handoff target). personal-foreman picks these up, decides
# the specific rig(s), and emits work bead(s).
gc bd list --status=open --type=personal-triage --json --limit 0 2>/dev/null |
  jq -r 'if type == "array" then .[] | .id else empty end'
