#!/usr/bin/env bash
# trace-extract.sh — the mechanical INV-22 traceability extractor for ONE
# behavior-docs set (the INTRA evaluator; bead pg2-wr6lm.4, plan item 6).
#
# INV-22 makes traceability a PER-ELEMENT LISTING obligation rather than a
# coverage section, which is precisely what makes it mechanically checkable:
#
#   * every user story, use case and journey MUST carry a listing on its OWN
#     definition (what it requires, and what it includes by reference);
#   * every invariant and goal MUST appear in at least one such listing — that
#     listing is what puts it in extent (INV-11); and
#   * every name a listing carries MUST resolve to a definition in this set or
#     to a declared external reference (INV-3).
#
# self-checks.sh does NOT cover this. Its no-orphans check flags a UUID CARRIER
# with no ID on its line — a defect about identity carriers, not a defined-vs-
# referenced diff. Nothing in this repo computed that diff before this script.
#
# Usage: trace-extract.sh [--strict] <behavior-docs-set-dir>
#
# Exit: 0 when clean, 1 on any FAIL, 2 on a usage error.
#
# WHAT IS A FAIL
#
# Every dangling reference is a hard FAIL, full stop — whether the offending name
# appears in a per-element LISTING (INV-22's resolve obligation verbatim) or in
# running PROSE elsewhere in the set. There is NO location-based severity split
# (operator ruling, bead pg2-p7fnv Q1, OVERTURNING the earlier listing-FAIL /
# prose-WARN split this script used to implement): a dangling name is a defect
# regardless of where it is written, so `--strict` no longer changes
# dangling-reference fatality at all. `--strict` still has a job — see ADOPTION
# below — it is just no longer this one.
#
#   listing-dangling — a listing names an ID that resolves to nothing.
#   prose-dangling   — running prose names an ID that resolves to nothing (this
#                      is how a cited-but-undeclared external element looks from
#                      inside one set).
#
# THE ONE CARVE-OUT: STYLE-EXAMPLE LITERALS (operator ruling, bead pg2-p7fnv Q2).
# A set may legitimately print an ID-shaped literal to ILLUSTRATE the naming
# convention rather than to cite a real element (e.g. "a topic-namespaced name
# looks like `INV-DISP-1`") — hard-failing every dangling name would otherwise
# make that illustration itself a build-breaking defect. Such a literal MUST
# carry the literal prose marker "example only, not a citation" on the SAME LINE
# as the id(s) it covers. The marker is prose, not an HTML comment, so it reads
# to a human as plainly as it parses to this script (a comment-only marker was
# explicitly rejected: it would satisfy the machine constraint alone). A line
# carrying the marker contributes NO reference records at all — every id-shaped
# token on that line is skipped, not just the intended one — so a genuine
# citation MUST NOT share a physical line with a style example.
#
# ADOPTION IS DERIVED, NEVER ALLOWLISTED. A set that carries NO listing at all
# has not retrofitted INV-22 yet (a scheduled work stream, not a regression), so
# the missing-listing and untraced sections REPORT rather than fail — loudly, on
# their own line, with the count. A set that carries SOME listings has adopted
# it, and every element must then have one: PARTIAL adoption is the state that
# rots, so that is exactly where this fails. The adoption state is computed from
# the set itself; there is no per-set exemption list to forget to remove.
#
# A FAMILY REFERENCE RESOLVES. `INV-EVT-*` tokenizes to the bare family name
# `INV-EVT`, which is nobody's definition; it resolves here because some defined
# ID begins with `INV-EVT-`. Without that rule every family glob in a real set
# reads as dangling.
set -euo pipefail

# The typed-id family list has ONE definition, in this plugin's
# `lib/behavior-ids.bash`, and MUST NOT be re-inlined here (bead pg2-fbxdw — it was
# duplicated at eight sites across six scripts and drifted twice, which is why THIS
# script could not flag a dangling `DEC-` reference at all). Sourced HERE, before the
# `cd "$DIR"` below, because the path is relative to this script and the cwd is still
# the invocation cwd at this point.
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
DIR=""

show_help() {
  cat <<'HELP'
trace-extract.sh: mechanical INV-22 traceability extractor for one behavior-docs set

Usage: trace-extract.sh [OPTIONS] <behavior-docs-set-dir>

Options:
  -h, --help    Show this help message
      --strict  Also fail on a set that has not adopted per-element listings
                at all (adoption status only — every dangling reference,
                listing or prose, already FAILs unconditionally)

Reports, per set: each element's listing, INV-22 adoption, untraced invariants
and goals, and dangling references (listing and prose, both fatal). A line
marked "example only, not a citation" is a style example, not a citation, and
contributes no reference at all.
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
  *) DIR="$1" ;;
  esac
  shift
done
[ $# -eq 0 ] || DIR="${DIR:-$1}"

[ -n "$DIR" ] || {
  show_help >&2
  exit 2
}
cd "$DIR" || exit 2
shopt -s nullglob
mds=(*.md)
[ ${#mds[@]} -gt 0 ] || {
  echo "no .md files in $DIR" >&2
  exit 1
}

# One awk pass per file emits four record kinds, so the whole scan costs one
# process per file rather than a grep per line (this runs in a pre-commit hook).
#
#   D <ID>                 a DEFINITION: the ID sits in HEADWORD position — after
#                          an optional list bullet or ATX heading marker and any
#                          emphasis/code-span punctuation, it is the first thing
#                          on the line.
#   E <ID>                 that definition is a story / use case / journey, i.e.
#                          an element that OWES a listing.
#   L <ELEM> <ID>          <ID> appears in <ELEM>'s listing.
#   R <file>:<line> <ID>   a reference occurrence anywhere in the set.
#
# Two listing forms are recognised because both are in live use: the
# `_Requires:_` / `_Includes:_` lines under a use-case or journey heading, and
# the inline `_(→ <includes>; <requires>.)_` trailer a story bullet carries. Both
# wrap across lines, so a listing runs from its marker to the first blank line,
# heading, or new bullet.
records=$(
  for f in ./*.md; do
    awk -v fname="${f#./}" -v idpat="$BEHAVIOR_IDPAT" '
      BEGIN {
        # Passed in, never re-spelled — see the source line at the top of this script.
        # A family this pattern does not know is invisible to the whole extractor: it is
        # neither a definition (D), an element (E), a listing entry (L) nor a reference
        # (R), so an untraced element of that family and a DANGLING reference to one are
        # both unreportable.
        IDPAT = idpat
        # A list bullet or an ATX heading marker, then emphasis/code-span
        # punctuation, then the ID. The marker is MANDATORY, not optional. Every
        # definition in a real set is a bullet or a heading, whereas a WRAPPED
        # PROSE line often begins with a code span -- e.g. a line whose first
        # token is INV-8, continuing the sentence above it. With the marker
        # optional those wrapped lines read as definitions, which invents
        # definitions and, worse, silently reassigns the current element: a
        # Requires listing then lands on the wrong owner and the real owner is
        # reported as carrying no listing at all.
        # NOTE: no apostrophes anywhere in this awk program. It sits inside a
        # single-quoted shell string, so one apostrophe would end the quote and
        # hand the rest of the program to the shell to parse.
        DEFPAT = "^[ \t]*(([-*+][ \t]+)|(#+[ \t]+))[*_`]*" IDPAT
        # STYLEPAT — the Q2 style-example marker (bead pg2-p7fnv). A line carrying
        # this literal prose is illustrating the naming convention, not citing an
        # element, so every id-shaped token on THAT LINE is excluded from R below.
        # Case-sensitive on purpose: the marker is a fixed phrase the author types
        # deliberately, not prose a reader might phrase a dozen equivalent ways.
        STYLEPAT = "example only, not a citation"
        cur = ""
        inlist = 0
      }
      function collect(s) {
        NIDS = 0
        while (match(s, IDPAT) > 0) {
          NIDS++
          IDS[NIDS] = substr(s, RSTART, RLENGTH)
          s = substr(s, RSTART + RLENGTH)
        }
      }
      function emit_listing(s) {
        collect(s)
        for (j = 1; j <= NIDS; j++) printf "L %s %s\n", cur, IDS[j]
      }
      {
        line = $0
        # Every occurrence is a reference, wherever it sits -- UNLESS the line
        # carries the Q2 style-example marker, in which case none of its
        # id-shaped tokens are references at all (see STYLEPAT above).
        if (line !~ STYLEPAT) {
          collect(line)
          for (i = 1; i <= NIDS; i++) printf "R %s:%d %s\n", fname, FNR, IDS[i]
        }

        isdef = (line ~ DEFPAT)
        if (isdef) {
          collect(line)
          defid = IDS[1]
          printf "D %s\n", defid
          inlist = 0
          if (defid ~ /^(STORY|USECASE|JOURNEY)-/) {
            cur = defid
            printf "E %s\n", cur
          } else {
            cur = ""
          }
        } else if (line ~ /^[ \t]*$/) {
          # A blank line ends any listing.
          inlist = 0
          next
        } else if (line ~ /^[ \t]*[-*+][ \t]/ || line ~ /^#+[ \t]/) {
          # A new bullet or heading that is NOT a definition also ends it.
          inlist = 0
        }

        # A listing STARTS at its marker, and only the text after the marker is
        # part of it: the `_Primary actor:_` / `_Level:_` / `_Preconditions:_`
        # lines sit in the same block and MUST NOT be read as requirements.
        if (cur != "") {
          if (match(line, /_Requires:_|_Includes:_|\(→/) > 0) {
            inlist = 1
            emit_listing(substr(line, RSTART + RLENGTH))
          } else if (inlist) {
            emit_listing(line)
          }
        }
      }
    ' "$f"
  done
)

# Declared external references: the first visible cell of every imports-table
# row. Section extraction mirrors resolve-imports.sh (case-insensitive header,
# state reset per file so a file ending mid-section does not leak).
imported=$(
  awk '
    FNR==1 { insec=0 }
    toupper($0) ~ /^##[[:space:]]+EXTERNAL REFERENCES/ { insec=1; next }
    /^##[[:space:]]/ && insec { insec=0 }
    insec { print }
  ' ./*.md 2>/dev/null |
    awk -F'|' -v idpat="$BEHAVIOR_IDPAT" '
      # Passed in, never re-spelled. A family omitted here drops out of `imported`, so a
      # legitimate reference to a DECLARED external element of that family reads as
      # dangling — a FALSE finding, the opposite failure to the extractor above.
      BEGIN { IDPAT = idpat }
      /^\|/ {
        cell = $2
        if (match(cell, IDPAT) > 0) print substr(cell, RSTART, RLENGTH)
      }' | sort -u
)

defined=$(printf '%s\n' "$records" | { grep '^D ' || true; } | cut -d' ' -f2 | sort -u)
elements=$(printf '%s\n' "$records" | { grep '^E ' || true; } | cut -d' ' -f2 | sort -u)
listed_pairs=$(printf '%s\n' "$records" | { grep '^L ' || true; } | cut -d' ' -f2,3 | sort -u)
listed=$(printf '%s\n' "$listed_pairs" | { grep -v '^$' || true; } | cut -d' ' -f2 | sort -u)
# Entries the product's OWN sibling decision area defines. `GOAL-5` makes these
# citable by typed name with NO imports row: "this product's own decision area is the
# sibling **input** of the two-input model, not an external set, so it needs no row"
# (`invariants.md`'s Goals; `README.md`'s "Two inputs, one product" says the same, and
# fixes the area at `docs/decisions`, the sibling of `docs/behavior`).
#
# This is NOT a blanket exemption for the `DEC-`/`IMPL-` families, which would just
# reinstate the blind spot under a new name: the area's entries are READ, so a citation
# of an entry it does NOT define still dangles. That is precisely the detection widening
# the family list bought (bead pg2-fbxdw) — before it, a dangling `DEC-` reference was
# unreportable here. Without this resolution step the widening would instead report every
# CONFORMANT decision citation as dangling, which is a worse defect than the blind spot.
#
# A set with no sibling decision area is normal, not an error. The `-e` test on the first
# element makes the guard correct whether or not `nullglob` is in effect.
decided=""
if [ -d ../decisions ]; then
  dec_mds=(../decisions/*.md)
  if [ ${#dec_mds[@]} -gt 0 ] && [ -e "${dec_mds[0]}" ]; then
    decided=$(
      awk -v idpat="$BEHAVIOR_IDPAT" '
        BEGIN { defpat = "^[ \t]*(([-*+][ \t]+)|(#+[ \t]+))[*_`]*" idpat }
        $0 ~ defpat { if (match($0, idpat) > 0) print substr($0, RSTART, RLENGTH) }
      ' "${dec_mds[@]}" | sort -u
    )
  fi
fi

# `known` is everything a reference may legitimately name: defined here, declared in the
# imports table, or defined in the sibling decision area.
known=$(printf '%s\n%s\n%s\n' "$defined" "$imported" "$decided" | { grep -v '^$' || true; } | sort -u)

# resolves <id> — exact match against a known name, or a FAMILY reference: the
# token is a proper prefix of some known ID (`INV-EVT-*` tokenizes to INV-EVT).
# An ID contains only letters, digits and hyphens, so it is regex-safe.
resolves() {
  printf '%s\n' "$known" | grep -qE -- "^$1(-|\$)"
}

fail=0
printf '=== INV-22 traceability: %s ===\n' "$DIR"

printf '\n--- Elements and their listings ---\n'
n_elems=0
n_with=0
missing=""
if [ -z "$elements" ]; then
  echo "  (this set defines no story, use case or journey)"
else
  while IFS= read -r e; do
    [ -n "$e" ] || continue
    n_elems=$((n_elems + 1))
    mine=$(printf '%s\n' "$listed_pairs" | { grep -E "^$e " || true; } | cut -d' ' -f2 | sort -uV | tr '\n' ' ')
    if [ -n "${mine// /}" ]; then
      n_with=$((n_with + 1))
      printf '  %-20s lists: %s\n' "$e" "${mine% }"
    else
      missing="${missing}${e}"$'\n'
    fi
  done <<<"$elements"
fi

printf '\n--- INV-22 adoption ---\n'
adopted=0
if [ "$n_elems" -eq 0 ]; then
  echo "  n/a (no story, use case or journey defined)"
elif [ "$n_with" -eq 0 ]; then
  printf '  NOT ADOPTED: 0 of %s stories/use cases/journeys carry a listing — INV-22 is not retrofitted for this set\n' "$n_elems"
  [ "$STRICT" -eq 1 ] && fail=1
else
  adopted=1
  printf '  adopted: %s of %s stories/use cases/journeys carry a listing\n' "$n_with" "$n_elems"
fi

printf '\n--- Missing listings (INV-22) ---\n'
if [ -z "${missing//$'\n'/}" ]; then
  echo "  clean"
else
  while IFS= read -r m; do
    [ -n "$m" ] || continue
    if [ "$adopted" -eq 1 ]; then
      printf '  FAIL %s carries no listing on its definition\n' "$m"
      fail=1
    else
      printf '  (not adopted) %s carries no listing\n' "$m"
    fi
  done <<<"$missing"
fi

printf '\n--- Untraced elements (INV-22 / INV-11) ---\n'
# `comm -23` (defined-invariants/goals minus listed) instead of a grep per ID:
# this script runs in a pre-commit hook, so one fork per element is the budget.
untraced=$(
  comm -23 \
    <(printf '%s\n' "$defined" | { grep -E '^(INV|GOAL)-' || true; } | sort -u) \
    <(printf '%s\n' "$listed" | { grep -v '^$' || true; } | sort -u)
)
if [ -z "$untraced" ]; then
  echo "  clean (every defined invariant and goal appears in at least one listing)"
elif [ "$adopted" -eq 1 ]; then
  while IFS= read -r u; do
    [ -n "$u" ] && printf '  FAIL untraced: %s is defined but appears in no listing\n' "$u"
  done <<<"$untraced"
  fail=1
else
  printf '  (not adopted) %s invariants/goals appear in no listing\n' "$(printf '%s\n' "$untraced" | grep -c .)"
fi

printf '\n--- Dangling references (INV-22 / INV-3) ---\n'
dangling=0
# Listing-dangling FIRST and always fatal: this is INV-22's resolve obligation
# verbatim.
while IFS= read -r pair; do
  [ -n "$pair" ] || continue
  elem=${pair%% *}
  id=${pair##* }
  resolves "$id" || {
    printf '  FAIL dangling in a listing: %s (listed by %s) resolves to no definition here and no declared external reference\n' "$id" "$elem"
    fail=1
    dangling=1
  }
done <<<"$listed_pairs"
# Prose-dangling: reported and fatal unconditionally, same as listing-dangling
# (operator ruling, bead pg2-p7fnv Q1 — no more location-based severity split;
# `--strict` no longer has anything to do with this section). A style-example
# literal never reaches here at all: STYLEPAT in the awk pass above already
# excluded its line from every R record, so there is no id to resolve.
# Resolve each DISTINCT referenced ID once, then map the unresolved ones back to
# their locations. Resolving per OCCURRENCE forks a grep per reference (hundreds
# on a real set); per distinct ID it is a few dozen.
ref_ids=$(printf '%s\n' "$records" | { grep '^R ' || true; } | cut -d' ' -f3 | sort -u)
prose_bad=$(
  for id in $ref_ids; do
    [ -n "$id" ] || continue
    resolves "$id" || printf '%s\n' "$id"
  done
)
if [ -n "$prose_bad" ]; then
  dangling=1
  fail=1
  while IFS= read -r id; do
    [ -n "$id" ] || continue
    locs=$(printf '%s\n' "$records" | awk -v i="$id" '$1 == "R" && $3 == i { print $2 }' | sort_locs | tr '\n' ' ')
    printf '  FAIL dangling in prose: %s (%s) resolves to no definition here and no declared external reference\n' \
      "$id" "${locs% }"
  done <<<"$prose_bad"
fi
[ "$dangling" -eq 0 ] && echo "  clean (every referenced ID resolves here or via the imports table)"

printf '\n'
if [ "$fail" -ne 0 ]; then
  echo "INV-22: FAIL — see the FAIL lines above"
  exit 1
fi
echo "INV-22: OK"
