#!/bin/sh
# Doctor check: city bd should not hold rig-targeted beads.
#
# A rig-scope worker only sees beads in its own rig bd. A bead labeled
# `rig:ziprecruiter` (or any concrete rig name) but living in the city
# bd is invisible to the worker — it just sits there.
#
# pr-watcher legitimately uses `rig:auto` to mark PR beads it owns at
# city scope; that label is excluded from this check. Anything else
# (`rig:ziprecruiter`, `rig:nix_overlay`, etc.) in the city bd indicates
# a routing miss — typically an old import or some new producer that
# needs `--rig=` treatment.
#
# Detection only; no auto-migration. The migration pattern is recorded
# in beads gc-77nlh (closed) — use `gc bd --rig=<name> create` to mint
# a new bead from the city bd copy, then close the city bd entry with
# --reason="migrated to <new-id>".
#
# Tunable via env:
#   PR_MISPLACED_BEADS_MIN_COUNT — min count to trigger alert (default 1)
#
# Exit 0 = healthy, 2 = alert. set -e intentionally NOT used.

MIN_COUNT="${PR_MISPLACED_BEADS_MIN_COUNT:-1}"

TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT INT TERM

gc bd list --status=open --json --limit 0 >"$TMP" 2>/dev/null

if [ ! -s "$TMP" ] || ! jq -e 'type == "array"' <"$TMP" >/dev/null 2>&1; then
  echo "misplaced-beads: bd list returned no data — skipping"
  exit 0
fi

# A bead is "misplaced" if it carries a `rig:<X>` label where X is the
# name of a real rig (any rig in `gc rig list` except the city itself).
# `rig:auto` is excluded by definition.
MISPLACED_FILE="$TMP.misplaced"
jq -c --argjson rigs "$(gc rig list --json 2>/dev/null | jq '[.rigs[] | select(.name != "gc") | .name]')" '
  [.[]
   | select((.labels // []) | any(
       . as $lbl
       | ($rigs[] | select($lbl == ("rig:" + .)))
     ))]
' <"$TMP" >"$MISPLACED_FILE" 2>/dev/null

COUNT=$(jq -r 'length' <"$MISPLACED_FILE" 2>/dev/null)
COUNT="${COUNT:-0}"

if [ "$COUNT" -lt "$MIN_COUNT" ]; then
  echo "misplaced-beads: 0 city-bd beads carry a concrete rig:<name> label (under threshold $MIN_COUNT)"
  exit 0
fi

echo "misplaced-beads: $COUNT bead(s) in city bd are labeled for a specific rig — they should be in that rig's bd"
jq -r '
  .[:5]
  | .[]
  | "  \(.id) (type=\(.issue_type), labels=\((.labels // []) | map(select(startswith("rig:"))) | join(",")), title=\(.title[:70]))"
' <"$MISPLACED_FILE" 2>/dev/null
if [ "$COUNT" -gt 5 ]; then
  echo "  ... and $((COUNT - 5)) more"
fi
echo "Fix: for each, create a copy in the target rig bd via 'gc bd --rig=<name> create ...',"
echo "     then close the city-bd original with --reason='migrated to <new-id>'."
exit 2
