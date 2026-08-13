#!/usr/bin/env bash
# reconcile-imports.sh — the BIDIRECTIONAL imports reconciler for one seam
# (the INTER evaluator; bead pg2-wr6lm.4, plan item 6).
#
# resolve-imports.sh is ONE-DIRECTIONAL: it walks the rows the implementer
# DECLARED and asks whether each resolves. It therefore cannot see either of the
# two ways a table drifts from the prose around it:
#
#   cited-but-undeclared — the implementer CITES an element the owner defines
#                          without declaring a row for it. The citation resolves
#                          for a human reading the prose and resolves for nothing
#                          mechanical, so the seam is invisible to every gate,
#                          including resolve-imports.sh.
#   declared-but-uncited — a row exists for an element the implementer never
#                          mentions outside the table. Either the citation was
#                          removed and the row outlived it, or the row was
#                          speculative. Both are stale seam surface.
#
# Usage: reconcile-imports.sh [--strict] <owner-set-dir> <implementer-set-dir>
# Exit: 0 when both directions reconcile, 1 on any FAIL, 2 on a usage error.
#
# SCOPE IS ONE SEAM. An imports table MAY declare owners in several sets, so a
# row whose `<repo> · <set-path>` names a DIFFERENT owner is skipped rather than
# judged against this owner — the same rule, for the same reason, as
# resolve-imports.sh. Run the script once per seam.
#
# RELATION TO THE INTRA EVALUATOR. `trace-extract.sh` sees the
# cited-but-undeclared direction from INSIDE one set, as a reference that
# resolves to neither a local definition nor a declared row ("dangling in
# prose"). It cannot see the declared-but-uncited direction at all, and it cannot
# tell an undeclared external citation from a typo, because it has no owner set
# to check the name against. This script has the owner, so it reports the
# direction precisely. Neither check subsumes the other.
set -euo pipefail

# The typed-id family list has ONE definition, in this plugin's
# `lib/behavior-ids.bash`, and MUST NOT be re-inlined here (bead pg2-fbxdw — it was
# duplicated at eight sites across six scripts and drifted twice, which is why THIS
# script reported "owner defines 0 element(s)" for a decisions area).
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

# sort_locs — canonical order for a multi-location list: by path, then by line
# NUMERICALLY. Sorting the whole `path:line` string lexically puts `a.md:10` ahead
# of `a.md:9`, which is a second, quieter way for the same finding to serialize two
# ways depending on which line happened to be seen first.
sort_locs() { sort -t: -k1,1 -k2,2n -u; }

STRICT=0
OWNER=""
IMPL=""

show_help() {
  cat <<'HELP'
reconcile-imports.sh: reconcile a seam's imports table against the prose, in BOTH directions

Usage: reconcile-imports.sh [OPTIONS] <owner-set-dir> <implementer-set-dir>

Options:
  -h, --help    Show this help message
      --strict  Treat a declared-but-uncited row as a FAIL as well (by default it
                is reported as a WARN: a stale row misleads a reader but breaks
                no identity)

Reports, for one seam: elements the implementer cites without declaring
(cited-but-undeclared, always a FAIL) and rows it declares without citing
(declared-but-uncited).
HELP
}

while [ $# -gt 0 ]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --strict) STRICT=1 ;;
  --)
    shift
    break
    ;;
  -*)
    echo "unknown option: $1" >&2
    exit 2
    ;;
  *)
    if [ -z "$OWNER" ]; then OWNER="$1"; else IMPL="${IMPL:-$1}"; fi
    ;;
  esac
  shift
done
[ -n "$OWNER" ] && [ -n "$IMPL" ] || {
  show_help >&2
  exit 2
}
[ -d "$OWNER" ] && [ -d "$IMPL" ] || {
  echo "both arguments must be directories" >&2
  exit 2
}

# A family omitted here is a family this evaluator CANNOT SEE on either side of the
# seam: it drops out of `defined_ids` AND of `imports_rows`, so an owner set whose
# elements are all of that family reports "owner defines 0 element(s)" and BOTH
# reconciliation directions are vacuous. The awk-safe (no `\b`) shape is the one
# passed into awk with `-v idpat=`.
IDPAT="$BEHAVIOR_IDPAT"

# defined_ids <set-dir> — the IDs a set DEFINES: an ID in headword position after
# a mandatory list-bullet or ATX-heading marker. The marker is required because a
# wrapped prose line frequently opens with a code span, which would otherwise
# read as a definition.
defined_ids() {
  awk -v idpat="$IDPAT" '
    BEGIN { defpat = "^[ \t]*(([-*+][ \t]+)|(#+[ \t]+))[*_`]*" idpat }
    $0 ~ defpat { if (match($0, idpat) > 0) print substr($0, RSTART, RLENGTH) }
  ' "$1"/*.md 2>/dev/null | sort -u
}

# imports_rows <set-dir> — one line per imports-table row:
#   <cited-name-ID> <TAB> <owner-set-path-cell>
# The name is the first visible cell and the owner set-path the second-to-last,
# read from the RIGHT so both the current 3-column and the D5 4-column shapes
# parse (identical rule to resolve-imports.sh, for the same reason).
imports_rows() {
  awk '
    FNR==1 { insec=0 }
    toupper($0) ~ /^##[[:space:]]+EXTERNAL REFERENCES/ { insec=1; next }
    /^##[[:space:]]/ && insec { insec=0 }
    insec { print }
  ' "$1"/*.md 2>/dev/null |
    awk -F'|' -v idpat="$IDPAT" '
      /^\|/ {
        n = NF
        if (n > 1 && $n == "") n--
        if (n < 3) next
        name = $2
        opath = $(n - 1)
        if (match(name, idpat) == 0) next
        printf "%s\t%s\n", substr(name, RSTART, RLENGTH), opath
      }'
}

# impl_refs_outside_table <set-dir> — every ID referenced OUTSIDE the imports
# table, with its location. A row cites nothing: the table is the declaration,
# so counting it as a citation would make every row self-satisfying and the
# declared-but-uncited direction unreachable.
impl_refs_outside_table() {
  for f in "$1"/*.md; do
    awk -v fname="$(basename "$f")" -v idpat="$IDPAT" '
      FNR==1 { insec=0 }
      toupper($0) ~ /^##[[:space:]]+EXTERNAL REFERENCES/ { insec=1; next }
      /^##[[:space:]]/ && insec { insec=0 }
      insec { next }
      {
        s = $0
        while (match(s, idpat) > 0) {
          printf "%s\t%s:%d\n", substr(s, RSTART, RLENGTH), fname, FNR
          s = substr(s, RSTART + RLENGTH)
        }
      }
    ' "$f"
  done
}

owner_defined=$(defined_ids "$OWNER")
impl_defined=$(defined_ids "$IMPL")
rows=$(imports_rows "$IMPL")
refs=$(impl_refs_outside_table "$IMPL")

# Keep only the rows that name THIS owner. A `<repo> · <set-path>` cell names a
# specific owner set, so a row naming a DIFFERENT one belongs to another seam and
# MUST be skipped rather than judged here.
#
# A cell with NO `·` names no owner set, and such a row is KEPT: resolve-imports.sh
# applies its owner filter only to rows that carry a `·`, for the same reason, and
# a reconciler that silently dropped every unqualified row would report a table
# written entirely in the short form as having zero declarations — i.e. it would
# call every citation undeclared and every row uncited at once.
owner_leaf=${OWNER%/}
declared=$(
  printf '%s\n' "$rows" | while IFS=$'\t' read -r id opath; do
    [ -n "$id" ] || continue
    case "$opath" in
    *·*)
      rsp=$(printf '%s' "$opath" | tr -d '`' | sed -E 's/.*·[[:space:]]*//; s/^[[:space:]]+//; s/[[:space:]]+$//')
      case "/$owner_leaf/" in
      *"/$rsp/") printf '%s\n' "$id" ;;
      esac
      ;;
    *) printf '%s\n' "$id" ;;
    esac
  done | sort -u
)

referenced=$(printf '%s\n' "$refs" | cut -f1 | sort -u)

fail=0
printf '=== INTER imports reconciliation: %s  <->  %s ===\n' "$IMPL" "$OWNER"
printf '  owner defines %s element(s); implementer declares %s row(s) for this owner\n' \
  "$(printf '%s\n' "$owner_defined" | grep -c . || true)" \
  "$(printf '%s\n' "$declared" | grep -c . || true)"

printf '\n--- cited-but-undeclared (INV-8 / INV-3) ---\n'
# An ID is cited-but-undeclared when the implementer references it outside the
# table, the OWNER defines it, the implementer does NOT define it (so the name is
# not its own), and no row declares it.
undeclared=$(
  comm -12 \
    <(printf '%s\n' "$referenced" | { grep -v '^$' || true; }) \
    <(printf '%s\n' "$owner_defined" | { grep -v '^$' || true; }) |
    comm -23 - <(printf '%s\n' "$impl_defined" | { grep -v '^$' || true; }) |
    comm -23 - <(printf '%s\n' "$declared" | { grep -v '^$' || true; })
)
if [ -z "$undeclared" ]; then
  echo "  clean (every cited owner element is declared)"
else
  while IFS= read -r id; do
    [ -n "$id" ] || continue
    locs=$(printf '%s\n' "$refs" | awk -F'\t' -v i="$id" '$1 == i { print $2 }' | sort_locs | tr '\n' ' ')
    printf '  FAIL cited-but-undeclared: %s (%s) — the owner defines it and no imports row declares it\n' \
      "$id" "${locs% }"
    fail=1
  done <<<"$undeclared"
fi

printf '\n--- declared-but-uncited (INV-8) ---\n'
uncited=$(
  comm -23 \
    <(printf '%s\n' "$declared" | { grep -v '^$' || true; }) \
    <(printf '%s\n' "$referenced" | { grep -v '^$' || true; })
)
if [ -z "$uncited" ]; then
  echo "  clean (every declared row is cited outside the table)"
else
  lead="WARN"
  if [ "$STRICT" -eq 1 ]; then
    lead="FAIL"
    fail=1
  fi
  while IFS= read -r id; do
    [ -n "$id" ] || continue
    printf '  %s declared-but-uncited: %s is declared in the imports table but referenced nowhere outside it\n' "$lead" "$id"
  done <<<"$uncited"
fi

printf '\n'
if [ "$fail" -ne 0 ]; then
  echo "INTER reconciliation: FAIL — see the FAIL lines above"
  exit 1
fi
echo "INTER reconciliation: OK"
