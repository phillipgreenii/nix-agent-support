#!/usr/bin/env bash
# resolve-imports.sh — the mechanical core of the V3 INTER-evaluator (bead
# pg2-hvlyj.15, plan item 5.3). Given an OWNER behavior-docs set and an
# IMPLEMENTER set, resolve every row of the implementer's `## External
# references` imports table against the owner set BY UUID (INV-3, the 1.1
# identity model), and classify each seam reference:
#
#   ok       — the owner UUID resolves and the cited NAME matches the owner's
#              current name for that UUID (obligation-alignment).
#   WARN     — the owner UUID resolves but the cited NAME differs: a stale NAME,
#              never a broken identity (the UUID model's whole point — 1.1).
#   FAIL     — the cited owner UUID resolves to NO owner definition: a genuine
#              divergence / broken identity (the evaluator's core value).
#   external — the row declares a consumed EXTERNAL contract (a tool/system with
#              no behavior-docs set of its own, e.g. git); there is no owner set
#              to resolve, so it is recorded as declared.
#
# This is the executable reconciliation the docs name (INV-INTF-2 / method
# INV-18, implementer form): the owner's contract vs. the implementer's stated
# obligations — NOT a verbatim peer cross-check. Message-level obligation
# reconciliation is the #7 conformance suite (packages/pr-pool/conformance);
# this script is the doc-level identity/seam layer that feeds it.
#
# Usage: resolve-imports.sh <owner-set-dir> <implementer-set-dir>
# Exit: 0 if no FAIL (warnings allowed), 1 if any unresolved owner UUID.
set -euo pipefail

OWNER="${1:?usage: resolve-imports.sh <owner-set-dir> <implementer-set-dir>}"
IMPL="${2:?usage: resolve-imports.sh <owner-set-dir> <implementer-set-dir>}"
UUIDRE='[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}'
IDRE='\b(INV|GOAL|STORY|JOURNEY|INTF|ACTOR|OQ)-[A-Za-z0-9]+(-[A-Za-z0-9]+)*\b'

trim() { sed -E 's/^[[:space:]]+//; s/[[:space:]]+$//'; }

# row_setpath <owner-set-path-cell> — the declared `<set-path>` half of a
# `<repo> · <set-path>` cell, code-span backticks stripped.
row_setpath() { printf '%s' "$1" | tr -d '`' | sed -E 's/.*·[[:space:]]*//' | trim; }

# owner_name_for_uuid <uuid> — print the owner's current name (first ID token on
# the carrier line) for a UUID, or empty if the UUID is not present in the owner.
owner_name_for_uuid() {
  local u="$1" line
  line=$({ grep -rhE "uuid:[[:space:]]*${u}[[:space:]]*-->" "$OWNER"/*.md || true; } | head -1)
  [ -n "$line" ] || return 0
  printf '%s\n' "$line" | grep -oE "$IDRE" | head -1
}

fail=0
found_rows=0
echo "=== V3 seam resolution: $IMPL  ->  $OWNER ==="

# Does the implementer declare an imports table at all? (case-insensitive). With
# NO `## External references` section the V3 inter-check is vacuous for this seam,
# so surface a NOTICE rather than exiting 0 silently (an empty-but-present table
# is a different, quieter case handled after the loop).
has_table=0
if grep -qiE '^##[[:space:]]+External references' "$IMPL"/*.md 2>/dev/null; then
  has_table=1
fi

# Read the implementer's imports-table rows (skip header + separator; a data row
# has a UUID OR an explicit (external) marker in its cells).
while IFS= read -r row; do
  case "$row" in
  '|'*) ;;
  *) continue ;;
  esac
  name=$(printf '%s\n' "$row" | awk -F'|' '{print $2}' | trim)
  opath=$(printf '%s\n' "$row" | awk -F'|' '{print $3}' | trim)
  uuidcell=$(printf '%s\n' "$row" | awk -F'|' '{print $4}' | trim)
  # Skip the header and the |----| separator.
  [ "$name" = "Name" ] && continue
  # Separator row: dashes, tolerating GFM alignment colons (:---, :---:, ---:).
  printf '%s' "$name" | grep -qE '^:?-+:?$' && continue
  [ -z "$name" ] && continue

  found_rows=$((found_rows + 1))

  # External contract: no owner UUID. Classify on the UUID CELL ALONE, anchored —
  # never on the PATH, because a real repo path contains hyphens and a bare '-'
  # would spuriously read as the external marker (making the WARN branch below
  # unreachable). Strip a markdown code span first so `(external)` matches whether
  # or not it is wrapped in backticks.
  if ! printf '%s' "$uuidcell" | grep -qE "$UUIDRE"; then
    uuidnorm=$(printf '%s' "$uuidcell" | tr -d '`')
    if printf '%s' "$uuidnorm" | grep -qiE '^[[:space:]]*(\(?external\)?|n/a|[—-])[[:space:]]*$'; then
      printf '  external  %-22s (declared external contract: %s)\n' "$name" "$opath"
    else
      printf '  WARN      %-22s (row has no owner UUID and is not marked external)\n' "$name"
    fi
    continue
  fi

  # An imports table MAY declare owners in MORE THAN ONE set (a deployment set that
  # implements one set's contracts AND follows the method). This script resolves ONE
  # seam per invocation, so a row naming a different owner set MUST be skipped, not
  # FAILed against the wrong owner.
  #
  # PLACEMENT IS LOAD-BEARING and MUST stay here, AFTER the no-UUID branch above.
  # A row carrying no owner UUID has nothing to resolve against any owner, so this
  # filter has no business touching it: its classification (declared external
  # contract vs. malformed row -> WARN) is owner-independent, and filtering it
  # earlier silently swallows that WARN (bats fixture #1, `external-misclass`).
  if printf '%s' "$opath" | grep -q '·'; then
    rsp=$(row_setpath "$opath")
    case "/${OWNER%/}/" in
    *"/$rsp/") ;;
    *) continue ;;
    esac
  fi

  u=$(printf '%s' "$uuidcell" | grep -oE "$UUIDRE" | head -1)
  ownername=$(owner_name_for_uuid "$u")
  # Normalize the cited name to its bare ID token (the cell wraps it in a
  # markdown code span, e.g. `INTF-SOURCE`), so it compares to the owner's
  # IDRE-extracted name.
  citedid=$(printf '%s\n' "$name" | grep -oE "$IDRE" | head -1)
  [ -n "$citedid" ] || citedid="$name"
  if [ -z "$ownername" ]; then
    printf '  FAIL      %-22s (owner UUID %s resolves to NO owner definition — divergence)\n' "$name" "$u"
    fail=1
  elif [ "$ownername" = "$citedid" ]; then
    printf '  ok        %-22s (aligned: owner UUID %s)\n' "$name" "$u"
  else
    printf '  WARN      %-22s (stale name: owner UUID %s now names %s)\n' "$name" "$u" "$ownername"
  fi
done < <(
  # Emit only the lines of the `## External references` section. Reset `insec` at
  # each file boundary (FNR==1) so a file that ENDS mid-section does not leak its
  # state into the next concatenated file. Header match is case-insensitive.
  awk '
    FNR==1 { insec=0 }
    toupper($0) ~ /^##[[:space:]]+EXTERNAL REFERENCES/ { insec=1; next }
    /^##[[:space:]]/ && insec { insec=0 }
    insec { print }
  ' "$IMPL"/*.md 2>/dev/null || true
)

if [ "$has_table" -eq 0 ]; then
  echo "  NOTICE: implementer declares no imports table (## External references) — the V3 inter-check is vacuous for this seam"
elif [ "$found_rows" -eq 0 ]; then
  echo "  (implementer declares no external references)"
fi
if [ "$fail" -ne 0 ]; then
  echo "V3: FAIL — one or more owner UUIDs did not resolve (genuine divergence)"
  exit 1
fi
echo "V3: no divergence (warnings, if any, are stale NAMES — never broken identity)"
