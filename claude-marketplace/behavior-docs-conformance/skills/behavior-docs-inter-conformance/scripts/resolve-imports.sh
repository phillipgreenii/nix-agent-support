#!/usr/bin/env bash
# resolve-imports.sh — the mechanical core of the INTER evaluator (bead
# pg2-hvlyj.15, plan item 5.3). Given an OWNER behavior-docs set and an
# IMPLEMENTER set, resolve every row of the implementer's `## External
# references` imports table against the owner set BY UUID (INV-3, the 1.1
# identity model), and classify each seam reference:
#
#   ok       — the owner UUID resolves and the cited NAME matches the owner's
#              current name for that UUID (obligation-alignment).
#   WARN     — the owner UUID resolves but the cited NAME differs: a stale NAME,
#              never a broken identity (the UUID model's whole point — 1.1).
#   FAIL     — either the cited owner UUID resolves to NO owner definition (a
#              genuine divergence / broken identity — the evaluator's core value),
#              or the row declares no parseable owner UUID at all and is not marked
#              external (an UNRESOLVABLE row). A row this script cannot resolve is
#              a FAILURE, never a warning: warning on it left the failure counter at
#              0, so a table whose shape this parser did not understand exited 0
#              having checked nothing.
#   external — the row declares a consumed EXTERNAL contract (a tool/system with
#              no behavior-docs set of its own, e.g. git); there is no owner set
#              to resolve, so it is recorded as declared.
#
# Two imports-table shapes are accepted, detected PER ROW (see row_cell/cell_uuid),
# so a table part-way through the migration between them still resolves every row:
#
#   | Name | Owner set-path | Owner UUID |                      (current)
#   | Name | What it is | Owner set-path | [<uuid>](remote-url) | (D5)
#
# The owner UUID is the LAST visible cell and the owner set-path the one before it
# in both, so the owner cells are read from the RIGHT rather than by fixed index.
# This script PARSES D5's link; it never DEREFERENCES the remote-url (verifying that
# the URL resolves and still carries the UUID is a separate, deliberately deferred
# item — this script makes no network calls).
#
# This is the executable reconciliation the docs name (INV-INTF-2 / method
# INV-18, implementer form): the owner's contract vs. the implementer's stated
# obligations — NOT a verbatim peer cross-check. Message-level obligation
# reconciliation is the #7 conformance suite (packages/pr-pool/conformance);
# this script is the doc-level identity/seam layer that feeds it.
#
# Usage: resolve-imports.sh <owner-set-dir> <implementer-set-dir>
# Exit: 0 if no FAIL (warnings allowed), 1 if any row failed to resolve — an
# unresolved owner UUID, or a row carrying no parseable owner UUID.
set -euo pipefail

# DETERMINISM: every sort, comm, uniq and shell glob below MUST order bytes, not
# locale-collated characters. Without this the SAME finding serializes differently
# on a UTF-8 workstation (`invariants.md:75 README.md:61`) and in the `C`-locale
# nix build sandbox (`README.md:61 invariants.md:75`) — so a gate that compares
# finding strings reports one identical finding as BOTH a new regression AND a
# no-longer-occurring entry, and is flaky rather than useful. The finding string is
# the record: it MUST be canonical where it is WRITTEN, never normalized where it
# is compared.
export LC_ALL=C

OWNER="${1:?usage: resolve-imports.sh <owner-set-dir> <implementer-set-dir>}"
IMPL="${2:?usage: resolve-imports.sh <owner-set-dir> <implementer-set-dir>}"
UUIDRE='[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}'
# The typed-name families INV-3 enumerates, including `USECASE` — an imports row MAY cite a
# use case, and `owner_name_for_uuid` extracts the owner's current name with this same regex,
# so omitting a family would report a resolvable row as a divergence.
IDRE='\b(INV|GOAL|STORY|USECASE|JOURNEY|INTF|ACTOR|OQ)-[A-Za-z0-9]+(-[A-Za-z0-9]+)*\b'

trim() { sed -E 's/^[[:space:]]+//; s/[[:space:]]+$//'; }

# row_setpath <owner-set-path-cell> — the declared `<set-path>` half of a
# `<repo> · <set-path>` cell, code-span backticks stripped.
row_setpath() { printf '%s' "$1" | tr -d '`' | sed -E 's/.*·[[:space:]]*//' | trim; }

# row_cell <gfm-row> <index> — print ONE cell of a leading-pipe GFM table row.
# A POSITIVE index counts visible cells from the LEFT (1 = the first); a NEGATIVE
# one counts from the RIGHT (-1 = the last, -2 = the one before it). awk's field 1
# is the empty string before the leading pipe, and when the row also ends with a
# pipe its LAST field is the empty string after it; both are dropped so the index
# is over VISIBLE cells. Only an exactly-empty trailing field is dropped, so a
# genuinely blank last cell (`| … | … |  |`) still counts as a cell.
#
# Reading the owner cells from the RIGHT is what makes this parser shape-agnostic.
# The imports table's owner UUID is the LAST visible cell and the owner set-path
# the one before it in BOTH live shapes: the current
# `| Name | Owner set-path | Owner UUID |` and D5's
# `| Name | What it is | Owner set-path | [<uuid>](remote-url) |`, which inserts a
# column as the SECOND visible cell and so shifts both owner cells one field right.
row_cell() {
  printf '%s\n' "$1" | awk -F'|' -v i="$2" '
    {
      n = NF
      if (n > 1 && $n == "") n--
      k = (i + 0 < 0) ? n + 1 + i : 1 + i
      if (k >= 2 && k <= n) print $k
    }'
}

# cell_uuid <owner-uuid-cell> — the owner UUID the cell DECLARES, or nothing.
# Two shapes are accepted, detected on the CELL ITSELF (never on a header or a
# per-table mode) so a table MID-MIGRATION whose rows mix the shapes still
# resolves row by row:
#   bare      `<uuid>`                — the current shape
#   D5 link   `[<uuid>](remote-url)`  — the shape D5 introduces
# For the link form the identity is the LINK TEXT and ONLY the link text: a
# remote-url may itself carry a UUID (a fragment, a permalink path), and "the
# first UUID anywhere in the cell" would let that masquerade as the declared
# identity. A cell that IS a link but whose text is not a well-formed UUID
# therefore yields NOTHING — the caller MUST treat an empty result as a failure,
# never as a pass (that silent pass is the whole defect this parser change fixes).
cell_uuid() {
  local cell u=''
  cell=$(printf '%s' "$1" | tr -d '`')
  if printf '%s' "$cell" | grep -q ']('; then
    u=$(printf '%s' "$cell" | sed -nE "s|.*\[[[:space:]]*($UUIDRE)[[:space:]]*\][[:space:]]*\(.*|\1|p" | head -1) || u=''
  else
    u=$(printf '%s' "$cell" | grep -oE "$UUIDRE" | head -1) || u=''
  fi
  printf '%s' "$u"
}

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
echo "=== INTER seam resolution: $IMPL  ->  $OWNER ==="

# Does the implementer declare an imports table at all? (case-insensitive). With
# NO `## External references` section the inter check is vacuous for this seam,
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
  # The NAME is the first visible cell; the owner set-path and owner UUID are read
  # from the RIGHT (see row_cell) so both the current and D5's shape parse per row.
  name=$(row_cell "$row" 1 | trim)
  opath=$(row_cell "$row" -2 | trim)
  uuidcell=$(row_cell "$row" -1 | trim)
  # Skip the header and the |----| separator.
  [ "$name" = "Name" ] && continue
  # Separator row: dashes, tolerating GFM alignment colons (:---, :---:, ---:).
  printf '%s' "$name" | grep -qE '^:?-+:?$' && continue
  [ -z "$name" ] && continue

  found_rows=$((found_rows + 1))

  # No owner UUID the cell could declare: either a declared EXTERNAL contract or an
  # UNRESOLVABLE row. Classify on the UUID CELL ALONE, anchored — never on the PATH,
  # because a real repo path contains hyphens and a bare '-' would spuriously read
  # as the external marker (making the failure branch below unreachable). Strip a
  # markdown code span first so `(external)` matches whether or not it is wrapped in
  # backticks.
  #
  # An unresolvable row is a FAILURE, never a warning. It used to warn and leave the
  # counter at 0, so the script EXITED 0 having resolved nothing — a gate reporting
  # success while checking nothing. That is exactly how a table-shape change (D5's
  # column shift) degrades this evaluator silently, so the shape competence above
  # and the hard failure here are one change: neither is verifiable without the other.
  u=$(cell_uuid "$uuidcell")
  if [ -z "$u" ]; then
    uuidnorm=$(printf '%s' "$uuidcell" | tr -d '`')
    if printf '%s' "$uuidnorm" | grep -qiE '^[[:space:]]*(\(?external\)?|n/a|[—-])[[:space:]]*$'; then
      printf '  external  %-22s (declared external contract: %s)\n' "$name" "$opath"
    elif printf '%s' "$uuidnorm" | grep -q ']('; then
      printf '  FAIL      %-22s (owner-UUID cell is a link whose text is not a UUID — unparseable: %s)\n' "$name" "$uuidcell"
      fail=1
    else
      printf '  FAIL      %-22s (row has no owner UUID and is not marked external — unresolvable)\n' "$name"
      fail=1
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
  # contract vs. unresolvable row -> FAIL) is owner-independent, and filtering it
  # earlier silently swallows that failure (bats fixture #1, `external-misclass`).
  if printf '%s' "$opath" | grep -q '·'; then
    rsp=$(row_setpath "$opath")
    case "/${OWNER%/}/" in
    *"/$rsp/") ;;
    *) continue ;;
    esac
  fi

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
  echo "  NOTICE: implementer declares no imports table (## External references) — the inter check is vacuous for this seam"
elif [ "$found_rows" -eq 0 ]; then
  echo "  (implementer declares no external references)"
fi
if [ "$fail" -ne 0 ]; then
  echo "INTER: FAIL — one or more rows did not resolve (unresolved owner UUID, or an unresolvable row)"
  exit 1
fi
echo "INTER: no divergence (warnings, if any, are stale NAMES — never broken identity)"
