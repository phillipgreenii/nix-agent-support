#!/usr/bin/env bash
# name-collisions.sh — cross-set NAME collision check (the INTER evaluator; bead
# pg2-wr6lm.4, plan item 6).
#
# Identity is the UUID and the name is a mutable label (INV-3), which is what
# makes a rename harmless. It is NOT what makes a name harmless: a name is what
# every human reader and every prose citation actually uses, so two sets that
# disagree about a name disagree about the seam even when no UUID is broken. No
# other check in this family looks at names across sets — resolve-imports.sh
# matches BY UUID precisely so that names cannot affect it.
#
# Two collision classes, and the second is the one with a shipped example:
#
#   1. AMBIGUOUS ID NAME (FAIL) — the same ID NAME is DEFINED in two sets THAT
#      REFERENCE EACH OTHER. A bare name cited across that seam then resolves to
#      two different elements. INV-3 says a set that cites another SHOULD
#      namespace its own names by topic exactly so this cannot happen; this is
#      that rule, checked.
#
#      THE REFERENCE EDGE IS PART OF THE RULE, NOT A LOOSENING OF IT (bead
#      pg2-pffnw). This check used to be corpus-GLOBAL: ANY two of the sets passed
#      on the command line defining one name was a FAIL. That is stricter than the
#      method it cites, and the method says so in two places. INV-3: a name is "a
#      mutable, human-readable label that need only be consistent WITHIN ITS OWN
#      SET", and its namespacing clause is conditioned on "a set that CITES ANOTHER
#      SET", for the stated purpose "so a bare name it cites never collides with
#      one of its own". INV-20 states the exclusion outright: "Vocabulary
#      consistency across UNRELATED sets (no reference edge) is NOT required."
#      Two sets that never mention each other cannot make any citation ambiguous:
#      a cross-set citation is qualified `<repo> · <set-path> · <name>` and
#      resolves by UUID regardless, so only a BARE name inside a CITING set can be
#      ambiguous at all.
#
#      Corpus-global was not a harmless over-reach — it failed `nix flake check` on
#      main over a non-defect. pa-monitor and pg-pr each namespace by topic (INV-3
#      satisfied on BOTH sides) and neither set's docs mention the other anywhere,
#      yet both legitimately picked the topic `GATE` — pa-monitor for the busy
#      predicate, pg-pr for the approval gate — so the gate demanded a published ID
#      be renamed to satisfy a rule the method does not state. Every future topic
#      coincidence between unrelated sets would have demanded another rename with
#      nothing to cite for it.
#
#      Name reuse across UNRELATED sets is still REPORTED, as a `note`: visible, so
#      the narrowing is never silent, but not a finding.
#
#      WHAT STILL FAILS, so this is a narrowing and not a deletion:
#        - A set that cites another and fails to namespace — the genuine INV-3
#          defect — still FAILs, because the edge is exactly what that set's own
#          imports table declares.
#        - A WITHIN-set duplicate name was never this script's job and is not
#          affected: `defined_ids` has always deduped per set. It is
#          `self-checks.sh`'s DUAL IDENTITY check (one ID token bearing more than
#          one UUID carrier), which the real-corpus runner asserts clean per set.
#        - A dangling cross-set reference is `resolve-imports.sh` /
#          `reconcile-imports.sh` / `trace-extract.sh`. Untouched.
#
#   2. ASSERTED AFFORDANCE THE OWNER DOES NOT HAVE (candidate) — a set names a
#      concrete affordance (a code-span token that is not an ID) on a line that
#      also cites an element of ANOTHER set, while that token appears NOWHERE in
#      that other set. This is the shape of a real shipped defect: the ZR
#      deployment set asserts `pr-pool-emit <json>` is an `INTF-CLI` operator
#      subcommand, and pr-pool — which owns `INTF-CLI` — calls that subcommand
#      `push-inject` and has no `pr-pool-emit` at all. Nothing caught it, because
#      the CITATION resolved: `INTF-CLI` is real and its UUID matches. Only the
#      NAME beside it was wrong.
#
# Class 2 is reported as a CANDIDATE list, not a failure, because a set
# legitimately names its OWN affordances on a line that cites an owner element,
# and no grep distinguishes "the name I use for my thing" from "the name I claim
# is yours". Judge each; `--strict` makes them fail once a corpus is clean.
#
# Usage: name-collisions.sh [--strict] <set-dir> <set-dir> [<set-dir>...]
# Exit: 0 when no FAIL, 1 on any FAIL, 2 on a usage error.
set -euo pipefail

# The typed-id family list has ONE definition, in this plugin's
# `lib/behavior-ids.bash`, and MUST NOT be re-inlined here (bead pg2-fbxdw — it was
# duplicated at eight sites across six scripts and drifted twice, leaving THIS
# script blind to `DEC-`/`IMPL-` names).
# shellcheck source=../../../lib/behavior-ids.bash
. "$(dirname "${BASH_SOURCE[0]}")/../../../lib/behavior-ids.bash"

# The imports-table row/cell parser (trim/row_cell/cell_uuid) has ONE definition,
# in `lib/imports-row.bash` — shared with `resolve-imports.sh` and
# `resolve-links.sh` so all three read the SAME two live table shapes the same way.
# Class 1 needs it to derive each set's declared REFERENCE EDGES (see `cites`).
# `cell_uuid` reads `$UUIDRE`, so that MUST be set before the source.
# shellcheck disable=SC2034  # consumed by cell_uuid in the sourced lib/imports-row.bash
UUIDRE='[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}'
# shellcheck source=../../../lib/imports-row.bash
. "$(dirname "${BASH_SOURCE[0]}")/../../../lib/imports-row.bash"

# DETERMINISM: every sort, comm, uniq and shell glob below MUST order bytes, not
# locale-collated characters. Without this the SAME finding serializes differently
# on a UTF-8 workstation (`invariants.md:75 README.md:61`) and in the `C`-locale
# nix build sandbox (`README.md:61 invariants.md:75`) — so a gate that compares
# finding strings reports one identical finding as BOTH a new regression AND a
# no-longer-occurring entry, and is flaky rather than useful. The finding string is
# the record: it MUST be canonical where it is WRITTEN, never normalized where it
# is compared.
export LC_ALL=C

STRICT=0
SETS=()

show_help() {
  cat <<'HELP'
name-collisions.sh: cross-set NAME collision check for two or more behavior-docs sets

Usage: name-collisions.sh [OPTIONS] <set-dir> <set-dir> [<set-dir>...]

Options:
  -h, --help    Show this help message
      --strict  Also fail on a class-2 candidate (an affordance name asserted
                against another set that the other set never uses)

Reports: ID names defined in two sets that REFERENCE each other (a FAIL); the same
reuse between sets with NO reference edge (a `note` — INV-20 does not require
distinct names there); and affordance names a set asserts beside a citation of
another set that the cited set does not use.
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
  *) SETS+=("$1") ;;
  esac
  shift
done
[ ${#SETS[@]} -ge 2 ] || {
  show_help >&2
  exit 2
}
for d in "${SETS[@]}"; do
  [ -d "$d" ] || {
    echo "not a directory: $d" >&2
    exit 2
  }
done

# A family omitted here is a family whose cross-set collisions this evaluator CANNOT
# SEE: the name never enters `defined_ids`, so the same id defined in two sets — the
# class-1 ambiguity this script exists to catch — goes unreported for that family.
# The awk-safe (no `\b`) shape is the one passed into awk with `-v idpat=`.
IDPAT="$BEHAVIOR_IDPAT"

# defined_ids <set-dir> — IDs in headword position after a mandatory bullet or
# heading marker (same rule, and the same reason, as the sibling scripts).
defined_ids() {
  awk -v idpat="$IDPAT" '
    BEGIN { defpat = "^[ \t]*(([-*+][ \t]+)|(#+[ \t]+))[*_`]*" idpat }
    $0 ~ defpat { if (match($0, idpat) > 0) print substr($0, RSTART, RLENGTH) }
  ' "$1"/*.md 2>/dev/null | sort -u
}

# minted_uuids <set-dir> — the UUIDs this set MINTS on its OWN definitions. A UUID
# is minted once, at the definition (INV-3), so a set's carriers are exactly the
# identities it OWNS — which is what makes "does A's imports table point at B?"
# answerable without any path arithmetic.
minted_uuids() {
  { grep -rhoE '<!--[[:space:]]*uuid:[[:space:]]*[^[:space:]]+[[:space:]]*-->' "$1"/*.md 2>/dev/null || true; } |
    sed -E 's/<!--[[:space:]]*uuid:[[:space:]]*//; s/[[:space:]]*-->$//' | sort -u
}

# imports_section <set-dir> — the lines of the set's `## External references`
# section. Reset `insec` per file so a file ENDING mid-section does not leak into
# the next (same shape, and the same reason, as resolve-imports.sh's reader).
imports_section() {
  {
    awk '
      FNR==1 { insec=0 }
      toupper($0) ~ /^##[[:space:]]+EXTERNAL REFERENCES/ { insec=1; next }
      /^##[[:space:]]/ && insec { insec=0 }
      insec { print }
    ' "$1"/*.md 2>/dev/null || true
  }
}

# declared_uuids / declared_names <set-dir> — the owner UUIDs and the cited NAMES
# the set DECLARES in its imports table. INV-3 requires a set to declare there
# every external element it references, so that table IS the set's record of its
# own reference edges; a set that cites without declaring has a DIFFERENT defect,
# already caught as `reconcile-imports.sh`'s cited-but-undeclared FAIL.
declared_uuids() {
  imports_section "$1" | while IFS= read -r row; do
    case "$row" in '|'*) ;; *) continue ;; esac
    u=$(cell_uuid "$(row_cell "$row" -1 | trim)")
    if [ -n "$u" ]; then printf '%s\n' "$u"; fi
  done | sort -u
}
declared_names() {
  imports_section "$1" | while IFS= read -r row; do
    case "$row" in '|'*) ;; *) continue ;; esac
    n=$(row_cell "$row" 1 | trim | tr -d '`')
    [ -n "$n" ] || continue
    t=$({ printf '%s\n' "$n" | grep -oE "$BEHAVIOR_IDRE" || true; } | head -1)
    printf '%s\n' "${t:-$n}"
  done | sort -u
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail=0
printf '=== INTER cross-set name collisions: %s set(s) ===\n' "${#SETS[@]}"
for d in "${SETS[@]}"; do printf '  set: %s\n' "$d"; done

for i in "${!SETS[@]}"; do
  defined_ids "${SETS[$i]}" >"$tmp/ids.$i"
  minted_uuids "${SETS[$i]}" >"$tmp/minted.$i"
  declared_uuids "${SETS[$i]}" >"$tmp/decluuid.$i"
  declared_names "${SETS[$i]}" >"$tmp/declname.$i"
done

# cites <citer-idx> <owner-idx> — does the citer's imports table DECLARE an element
# the owner set owns? Two signals, because INV-3 defines two regimes and the interim
# one is still live:
#   BY UUID    — a declared owner UUID that the owner set MINTS. The authoritative
#                form, and the same mechanism resolve-imports.sh uses.
#   BY NAME    — INV-3's interim clause, verbatim: "before an external element's
#                owner-UUID is declared, cross-set references resolve BY NAME as
#                before". Without this, a seam mid-retrofit reads as no edge and a
#                genuine namespacing defect there would go unreported.
# The name signal deliberately OVER-approximates (a declared name that some third
# set also happens to define reads as an edge to that set too). Over-approximating
# the edge can only ADD class-1 FAILs, never hide one, which is the safe direction.
cites() {
  local c="$1" o="$2"
  if [ -s "$tmp/decluuid.$c" ] && [ -s "$tmp/minted.$o" ]; then
    if comm -12 "$tmp/decluuid.$c" "$tmp/minted.$o" | grep -q .; then return 0; fi
  fi
  if [ -s "$tmp/declname.$c" ] && [ -s "$tmp/ids.$o" ]; then
    if comm -12 "$tmp/declname.$c" "$tmp/ids.$o" | grep -q .; then return 0; fi
  fi
  return 1
}

printf '\n--- class 1: ID name defined in two sets that REFERENCE each other (INV-3, INV-20) ---\n'
# PAIRWISE, not corpus-wide: the rule is a property of a SEAM, so the finding must
# name the two sets whose seam it is. Both members are printed in BYTE order (see
# the LC_ALL note above) so the finding string is canonical wherever it is written.
reuse=0
collision=0
for i in "${!SETS[@]}"; do
  for j in "${!SETS[@]}"; do
    [ "$i" -lt "$j" ] || continue
    a="${SETS[$i]}"
    b="${SETS[$j]}"
    if [ "$a" \> "$b" ]; then
      lo="$b"
      hi="$a"
    else
      lo="$a"
      hi="$b"
    fi
    common=$(comm -12 "$tmp/ids.$i" "$tmp/ids.$j")
    [ -n "$common" ] || continue
    reuse=1
    if cites "$i" "$j" || cites "$j" "$i"; then
      while IFS= read -r id; do
        [ -n "$id" ] || continue
        printf '  FAIL ambiguous ID name: %s is DEFINED in %s %s — the two sets reference each other, so a bare citation resolves to more than one element\n' \
          "$id" "$lo" "$hi"
        collision=1
        fail=1
      done <<<"$common"
    else
      while IFS= read -r id; do
        [ -n "$id" ] || continue
        printf '  note unrelated-set name reuse: %s is DEFINED in %s %s — neither set declares a reference to the other, so INV-20 does not require distinct names\n' \
          "$id" "$lo" "$hi"
      done <<<"$common"
    fi
  done
done
if [ "$collision" -eq 0 ]; then
  if [ "$reuse" -eq 0 ]; then
    echo "  clean (no ID name is defined in two sets)"
  else
    echo "  clean (no ID name is defined in two sets that reference each other)"
  fi
fi

printf '\n--- class 2: affordance name asserted against another set (candidates) ---\n'
# For each ORDERED pair of sets, find lines in the asserting set that cite an ID
# the OTHER set defines, and collect the code-span tokens on those lines that are
# not IDs. A token the other set never mentions ANYWHERE is a candidate.
#
# The token filter is deliberately narrow: a lower-case kebab/underscore
# identifier of at least two segments (`push-inject`, `pr-pool-emit`,
# `ingest-event`). A single bare word is ordinary prose in a code span and a
# path-like or version-like token is not an affordance name.
#
# ONE awk PASS PER ORDERED PAIR. The obvious shape — a shell loop over every line
# forking grep for the IDs and again for the tokens — took 26s on two real sets,
# which is not a pre-commit gate, it is a gate people disable. awk reads the cited
# set once into memory and then scans the asserting set in a single process.
candidates=0
out="$tmp/candidates"
: >"$out"
for a in "${SETS[@]}"; do
  for b in "${SETS[@]}"; do
    [ "$a" != "$b" ] || continue
    defined_ids "$b" >"$tmp/b_ids"
    [ -s "$tmp/b_ids" ] || continue
    cat "$b"/*.md >"$tmp/b_text" 2>/dev/null || true
    awk -v idpat="$IDPAT" -v bids="$tmp/b_ids" -v btext="$tmp/b_text" \
      -v aname="$a" -v bdir="$b" '
      BEGIN {
        while ((getline l < bids) > 0) OWN[l] = 1
        BT = ""
        while ((getline l < btext) > 0) BT = BT "\n" l
        # A lower-case kebab/underscore identifier of at least two segments:
        # push-inject, pr-pool-emit, ingest-event. A single bare word in a code
        # span is ordinary prose, not an affordance name.
        #
        # The span need not END at the identifier. A command affordance is written
        # with its argument INSIDE the span -- `pr-pool-emit <json>` -- so a
        # pattern anchored on a closing backtick misses exactly the shipped case
        # this class exists for. Accept a backtick OR a space as the terminator
        # and take the identifier alone.
        TOKPAT = "`[a-z][a-z0-9]*([-_][a-z0-9]+)+[ `]"
      }
      # THE UNIT IS A BLOCK, NOT A LINE. Prose wraps: in the shipped defect the
      # asserted name and the citation it is attached to sat on one line, but one
      # extra word upstream would have put them on two, and a line-scoped check
      # would then have seen an affordance with no citation beside it and a
      # citation with no affordance -- and reported nothing. A block runs from a
      # bullet or heading to the next blank line, bullet, or heading, which is the
      # unit a single assertion is actually written in.
      # cited_list — the IDs of the cited set that this block names, DEDUPED and
      # BYTE-SORTED. Accumulating them in text-appearance order emitted the same
      # finding two ways (and repeated an ID cited twice in one block, e.g.
      # "INTF-CLI INTF-CLI INV-EVT-2"), which makes the finding string
      # non-canonical. awk has no portable sort, so this is an insertion sort — the
      # list is a handful of IDs, never a hot path.
      function cited_list(text,   n, i, j, t, tmp, seen, out) {
        n = 0
        while (match(text, idpat) > 0) {
          t = substr(text, RSTART, RLENGTH)
          text = substr(text, RSTART + RLENGTH)
          if (!(t in OWN)) continue
          if (t in seen) continue
          seen[t] = 1
          tmp[++n] = t
        }
        for (i = 2; i <= n; i++) {
          t = tmp[i]
          for (j = i - 1; j >= 1 && tmp[j] > t; j--) tmp[j + 1] = tmp[j]
          tmp[j + 1] = t
        }
        out = ""
        for (i = 1; i <= n; i++) out = out tmp[i] " "
        return out
      }
      function flush() {
        if (blk == "") return
        cited = cited_list(blk)
        if (cited != "") {
          s = blk
          while (match(s, TOKPAT) > 0) {
            tok = substr(s, RSTART + 1, RLENGTH - 2)  # strip the leading ` and the terminator
            s = substr(s, RSTART + RLENGTH)
            # NOT an affordance name: the <repo> half of a qualified citation
            # `<repo> · <set-path>`. It is kebab-case and lands beside the cited
            # ID by construction, so without this it is the single loudest false
            # positive -- every correctly written citation reports one.
            # Recognised by what FOLLOWS it inside the span: the separator.
            if (s ~ /^[ ]*·/) continue
            # Present anywhere in the cited set => the two sets agree on the name.
            if (index(BT, tok) > 0) continue
            if (!((tok SUBSEP cited) in SEEN)) {
              SEEN[tok SUBSEP cited] = 1
              printf "  CANDIDATE %s asserts `%s` beside its citation of %s(%s:%d) — that name appears nowhere in %s\n", \
                aname, tok, cited, fname, blkline, bdir
            }
          }
        }
        blk = ""
      }
      FNR == 1 {
        flush()
        fname = FILENAME
        sub(/.*\//, "", fname)
      }
      /^[ \t]*$/ { flush(); next }
      /^[ \t]*[-*+][ \t]/ || /^#+[ \t]/ { flush() }
      {
        if (blk == "") blkline = FNR
        blk = blk " " $0
      }
      END { flush() }
    ' "$a"/*.md >>"$out"
  done
done
if [ -s "$out" ]; then
  cat "$out"
  candidates=$(grep -c . "$out")
  [ "$STRICT" -eq 0 ] || fail=1
fi
[ "$candidates" -ne 0 ] || echo "  none"

printf '\n'
if [ "$fail" -ne 0 ]; then
  echo "INTER name collisions: FAIL — see the FAIL lines above"
  exit 1
fi
echo "INTER name collisions: OK (class-2 candidates, if any, need judgment)"
