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
#   FAIL     — any of: the cited owner UUID resolves to NO owner definition (a
#              genuine divergence / broken identity — the evaluator's core value);
#              the row declares no parseable owner UUID at all and is not marked
#              external (an UNRESOLVABLE row); or the owner UUID resolves to a
#              definition whose id family is UNRECOGNIZED (see `IDRE` and
#              `owner_name_for_uuid`). A row this script cannot resolve is
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
# This script PARSES D5's link; it never DEREFERENCES the remote-url — confirming
# the URL still resolves and still carries the UUID is `resolve-links.sh` (bead
# pg2-2oupw), a deliberately separate script (see ITS header for why). This
# script makes no network calls and never will.
#
# This is the executable reconciliation the docs name (INV-INTF-2 / method
# INV-18, implementer form): the owner's contract vs. the implementer's stated
# obligations — NOT a verbatim peer cross-check. Message-level obligation
# reconciliation is the #7 conformance suite (packages/pr-pool/conformance);
# this script is the doc-level identity/seam layer that feeds it.
#
# Usage: resolve-imports.sh <owner-set-dir> <implementer-set-dir>
# Exit: 0 if no FAIL (warnings allowed), 1 if any row failed to resolve — an
# unresolved owner UUID, a row carrying no parseable owner UUID, or an owner
# definition whose id family this script does not recognize. EVERY such row is
# reported on its OWN line before the exit: this script MUST NOT abort mid-loop,
# because a run that dies on row 3 reports nothing about rows 1, 2 or 4 either.
set -euo pipefail

# The typed-id family list has ONE definition, in this plugin's
# `lib/behavior-ids.bash`, and MUST NOT be re-inlined here (bead pg2-fbxdw — it was
# duplicated at eight sites across six scripts and drifted twice).
# shellcheck source=../../../lib/behavior-ids.bash
. "$(dirname "${BASH_SOURCE[0]}")/../../../lib/behavior-ids.bash"

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
# shellcheck disable=SC2034  # consumed by cell_uuid in the sourced lib/imports-row.bash
UUIDRE='[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}'
# The imports-table row/cell parser (trim/row_cell/cell_uuid/cell_url) has ONE
# definition, in `lib/imports-row.bash` — shared with `resolve-links.sh` (bead
# pg2-2oupw) so both scripts read the SAME two live table shapes the same way.
# `cell_uuid` reads `$UUIDRE`, just set above.
# shellcheck source=../../../lib/imports-row.bash
. "$(dirname "${BASH_SOURCE[0]}")/../../../lib/imports-row.bash"
# The families an imports row MAY cite. `owner_name_for_uuid` extracts the owner's current
# name with this same regex, so a family omitted from it is a family whose rows CANNOT
# RESOLVE. GOAL-5 is why the decision-doc pair belongs here and not merely in prose: an entry
# belonging to another scope MUST be declared, with its UUID, in this set's
# `## External references` table "like any other external element". The admitted set and the
# rule for extending it live with the definition in `lib/behavior-ids.bash`; an unrecognized
# family MUST reach the loud per-row FAIL below rather than be quietly admitted by a catch-all
# prefix pattern.
IDRE="$BEHAVIOR_IDRE"
# The typed-name SHAPE, family-agnostic. It is used for ONE purpose only: NAMING the offending
# token in the unrecognized-family FAIL below, so the report says which id it choked on. It
# MUST NOT be substituted for `IDRE` anywhere a name is RESOLVED or COMPARED — admitting an
# arbitrary uppercase prefix as an identity is precisely the silent widening the paragraph
# above forbids.
ANYIDRE='\b[A-Z][A-Z0-9]*-[A-Za-z0-9]+(-[A-Za-z0-9]+)*\b'

# row_setpath <owner-set-path-cell> — the declared `<set-path>` half of a
# `<repo> · <set-path>` cell, code-span backticks stripped. Local to this
# script (not in the shared `imports-row.bash`): the owner set-path filter
# below is specific to resolve-imports.sh's single-seam-per-invocation
# contract, which `resolve-links.sh` has no equivalent of.
row_setpath() { printf '%s' "$1" | tr -d '`' | sed -E 's/.*·[[:space:]]*//' | trim; }

# owner_name_for_uuid <uuid> — resolve a cited owner UUID to the owner's CURRENT name for it
# (the first ID token on its carrier line). THREE outcomes, and the caller MUST keep them
# distinct because each is a different finding:
#
#   rc 0, a name on stdout   — RESOLVED: a carrier line was found and it carries a token of a
#                              family `IDRE` admits.
#   rc 0, nothing on stdout  — the UUID appears in NO owner carrier line: a genuine divergence,
#                              reported by the caller's existing `-z` branch.
#   rc 3, a token on stdout  — a carrier line WAS found, but it carries no token of any admitted
#                              family: the id family is UNRECOGNIZED.
#
# THE rc-3 PATH IS THE POINT, and it MUST NOT be written either of the two shorter ways:
#
#   as a bare `grep -oE "$IDRE" | head -1` (this function's pre-fix form) — the grep matches
#   nothing, exits 1, and `set -o pipefail` propagates that through the caller's command
#   substitution, so `set -e` kills the run MID-LOOP: exit 1 with NO row output at all. Not one
#   row is reported, not even the rows already resolved before the offending one.
#
#   as that same grep with `|| true` — STRICTLY WORSE, and it MUST NOT be reintroduced. The
#   function would return empty, which is indistinguishable from "this UUID resolves to no owner
#   definition", so the caller reports a FALSE `divergence` FAIL against a row whose identity
#   resolved perfectly. A loud crash at least tells the reader nothing was checked; a false
#   divergence asserts a conclusion the script never reached.
#
# An unrecognized family is a real defect in exactly one of two places — an id whose family no
# area defines, or a family this evaluator has not been taught — so it MUST surface as a LOUD
# per-row FAIL that NAMES the token, leaving the reader to decide which of the two it is.
owner_name_for_uuid() {
  local u="$1" line id offender
  line=$({ grep -rhE "uuid:[[:space:]]*${u}[[:space:]]*-->" "$OWNER"/*.md || true; } | head -1)
  [ -n "$line" ] || return 0
  id=$({ printf '%s\n' "$line" | grep -oE "$IDRE" || true; } | head -1)
  if [ -n "$id" ]; then
    printf '%s\n' "$id"
    return 0
  fi
  # Name what IS on the carrier line so the report distinguishes an unlearned family
  # (`POLICY-3`) from a carrier bearing no id at all. The UUID comment is stripped first:
  # an UPPERCASE-hex UUID matches `ANYIDRE` itself, and reporting the identity back as the
  # offending name would be worse than reporting nothing.
  offender=$({ printf '%s\n' "${line%%<!--*}" | grep -oE "$ANYIDRE" || true; } | head -1)
  [ -n "$offender" ] || offender="<no id token on the carrier line>"
  printf '%s\n' "$offender"
  return 3
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

  # `|| rc=$?` is REQUIRED, not defensive: the rc-3 outcome is a per-row FINDING, so it MUST
  # NOT propagate through `set -e` and abort the loop — that is the very crash being fixed.
  # It is also NOT a blanket `|| true`: the status is CAPTURED and branched on below, so an
  # unrecognized family stays loud instead of collapsing into the divergence branch.
  rc=0
  ownername=$(owner_name_for_uuid "$u") || rc=$?
  # Normalize the cited name to its bare ID token (the cell wraps it in a
  # markdown code span, e.g. `INTF-SOURCE`), so it compares to the owner's
  # IDRE-extracted name. Here `|| true` IS correct where it is forbidden in
  # `owner_name_for_uuid`, and the difference is the NEXT line: an empty match has an
  # EXPLICIT fallback (compare the cell verbatim), so nothing is concluded from silence.
  # Unguarded, this grep is the same pipefail crash — a row whose Name cell carries no
  # typed id at all would kill the loop.
  citedid=$({ printf '%s\n' "$name" | grep -oE "$IDRE" || true; } | head -1)
  [ -n "$citedid" ] || citedid="$name"
  if [ "$rc" -eq 3 ]; then
    printf '  FAIL      %-22s (owner UUID %s resolves to a definition whose id family is UNRECOGNIZED: %s — teach IDRE the family, or fix the id)\n' "$name" "$u" "$ownername"
    fail=1
  elif [ -z "$ownername" ]; then
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
  echo "INTER: FAIL — one or more rows did not resolve (unresolved owner UUID, an unresolvable row, or an unrecognized id family)"
  exit 1
fi
echo "INTER: no divergence (warnings, if any, are stale NAMES — never broken identity)"
