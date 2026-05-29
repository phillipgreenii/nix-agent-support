#!/bin/sh
# mol-triage-poll.sh — exec body of the mol-triage-poll order.
#
# Poll for open type=triage beads in hq. If any exist, mail the
# triager to wake it. Direct bead-wake is not a primitive in gc 1.1.0;
# orders are the wake mechanism (operator clarification 2026-05-29).

set -u

if [ -n "$(gc bd list --status=open --type=triage --limit 1 --json 2>/dev/null | jq -r 'if type=="array" then .[].id else empty end' | head -1)" ]; then
  gc mail send triager \
    -s "mol-triage-poll: open type=triage bead(s) waiting" \
    -m "Triager: at least one open type=triage bead exists in hq. Wake and process."
fi
