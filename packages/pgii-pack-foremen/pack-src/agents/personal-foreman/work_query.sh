#!/bin/sh
# personal-foreman work_query — per-rig-db topology.
#
# Each of the 6 personal rigs has its own bd store. Iterate, union
# the results, then add the triager's personal-handoff beads from hq
# (type=triage + label category:personal).
#
# Per the spec's verified outcomes 2026-05-29: shared-db is not
# viable (gc 1.1.0 doesn't honor --rig for prefix selection when
# rigs share a dolt_database).
#
# Note 2026-05-29 (smoke-test fix): the triager originally emitted
# type=personal-triage beads as personal handoffs, but bd 1.0.4 does
# not accept custom types. We use the built-in type=triage with a
# category:personal label instead; this work_query filters for that
# combination in the hq lookup at the bottom.

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

# Also include the triager's personal-handoff beads from hq.
# Discriminator is the `category:personal` LABEL, not the type —
# bd 1.0.4's type registry is unstable (`triage` vs `personal-triage`
# flip between sessions due to auto-import), so we don't pin to a
# specific type. Any open bead in hq carrying `category:personal`
# is a triager handoff that wants personal-foreman attention.
gc bd list --status=open --json --limit 0 2>/dev/null |
  jq -r '
    if type == "array" then
      .[]
      | select((.labels // []) | any(. == "category:personal"))
      | .id
    else empty end
  '
