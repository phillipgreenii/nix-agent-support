#!/usr/bin/env bash
# relocation-check.sh — the DECIDABLE half of USECASE-5 (D10)'s five-step
# relocation procedure, for ONE behavior-docs set (bead pg2-9sslr, operator
# ruling 2026-08-14).
#
# `USECASE-5` ("Relocate implementation content out of a behavior doc",
# behavior-docs/docs/behavior/journeys.md) has five steps. A prior session on
# this bead (comment 2026-08-13) split them:
#
#   1. Apply the INV-2 change test ("would this change if the implementation
#      changed while intended behavior held?")                          => JUDGMENT
#   2. Confirm via the substitution test that it is below the floor       => JUDGMENT
#   3. Move it to the owning decision area with a typed local id + UUID  => MECHANICAL
#   4. Restate the surviving behavior at the floor; cite only where
#      provenance is needed                                              => PARTLY JUDGMENT
#   5. Delete it from the behavior doc, with no tombstone (INV-4)        => MECHANICAL
#
# THIS SCRIPT CHECKS ONLY STEPS 3 AND 5. The operator ruling recorded on this
# bead (2026-08-14) is BUILD THE DECIDABLE CATEGORIES ONLY, and DOCUMENT THE
# EXCLUSIONS rather than silently drop them — this comment is that record.
#
# EXCLUDED, and why (all deliberate, none an oversight):
#
#   * Step 1 (the INV-2 change test) and step 2 (the substitution/floor test)
#     decide WHETHER a statement should move at all. There is no predicate over
#     the TEXT that settles that — only a judgment about whether intended
#     behavior survives generalization. Left to SKILL.md's Step 3 (a human).
#   * Step 4's restatement QUALITY (does the surviving floor-level statement
#     say what must hold, in the right vocabulary, and cite provenance only
#     where genuinely needed rather than "re-import the how by reference") is
#     the same kind of judgment. Nothing short of reading the prose can tell a
#     legitimate provenance citation from a smuggled implementation detail.
#   * Step 5's "status header" is already enforced by the sibling
#     self-checks.sh ("Status headers (INV-4)" section) and is NOT
#     reimplemented here.
#   * Step 5's "changelog line" has no established, detectable convention
#     anywhere in this corpus to build a mechanical rule against — inventing
#     one now would be exactly the "fuzzy predicate" this bead was originally
#     deferred to avoid. It stays unchecked, named here rather than silently
#     dropped.
#
# WHAT STEPS 3 AND 5 REDUCE TO, MECHANICALLY:
#
#   STEP 3 — every definition line in the set's sibling decision area
#   (`../decisions/*.md`, the same convention trace-extract.sh's `decided`
#   resolution uses) whose leading id is a `DEC-` or `IMPL-` family member MUST
#   carry a well-formed UUID HTML comment on THAT SAME LINE — the form
#   `docs/decisions/README.md`'s "Entry ids" fixes:
#   ``### `DEC-<TOPIC>-<n>` — summary <!-- uuid: ... --> ``. A relocated
#   statement that landed without one is invisible to every cross-set citation
#   that resolves by UUID (`GOAL-5`), so this is the mechanical core of "move
#   it ... as an entry carrying a typed local name and a stable UUID"
#   (USECASE-5 step 3). A UUID a second entry in the same area reuses is
#   flagged too — reused is not "a" (fresh) identity.
#
#   STEP 5 — no line in the TARGET set's own `*.md` files may carry a
#   NOTE-SHAPED tombstone: a parenthetical, an HTML comment, or a bold-label /
#   blockquote-led line reading "moved to ..." or "relocated to ...". This is
#   deliberately narrower than a bare `grep -i "moved to"`: plain prose
#   legitimately uses those words. Two real false positives this corpus
#   already contains prove it —
#     - `packages/pg-pr/docs/behavior/invariants.md` states, as an ACTIVE
#       behavior statement about UUID provenance, that four invariants carry a
#       UUID "relocated to its correct owner"; and
#     - USECASE-5's OWN definition illustrates the forbidden note in quotes:
#       no `"moved to …"` note.
#   Neither is a leftover tombstone, and a naive grep flags both. Scoping to
#   the NOTE SHAPES this corpus already uses for meta-annotation (a
#   parenthetical `_(...)_ `, an HTML comment, or a bold/blockquote lead) is
#   what tells the two apart; this scan is also single-line, matching every
#   such note convention already in live use here.
#
# WHAT THIS DOES NOT PROVE. A clean step 5 means no tombstone-SHAPED marker is
# left; it cannot prove the deleted content actually landed IN a decision area
# rather than vanishing entirely (that needs the specific relocation's git
# history, out of scope for a single-snapshot checker — still USECASE-5 step
# 4, a human's call). A clean step 3 means an entry carries the required
# id/UUID shape, not that its content is a faithful restatement.
#
# Usage: relocation-check.sh <behavior-docs-set-dir>
# Exit: 0 clean, 1 on any FAIL, 2 on a usage error.
set -euo pipefail

# The typed-id family list has ONE definition — see the identical note in
# trace-extract.sh / self-checks.sh (bead pg2-fbxdw).
# shellcheck source=../../../lib/behavior-ids.bash
. "$(dirname "${BASH_SOURCE[0]}")/../../../lib/behavior-ids.bash"

# DETERMINISM — see trace-extract.sh for the full rationale: every sort/comm/
# uniq/glob below MUST order bytes, not locale-collated characters, or the same
# finding serializes two different ways on a workstation vs. the C-locale nix
# build sandbox.
export LC_ALL=C
sort_locs() { sort -t: -k1,1 -k2,2n -u; }

UUIDRE='^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'

DIR="${1:-}"
[ -n "$DIR" ] || {
  echo "usage: relocation-check.sh <behavior-docs-set-dir>" >&2
  exit 2
}
cd "$DIR" 2>/dev/null || {
  echo "not a directory: $DIR" >&2
  exit 2
}
DIR=$(pwd)
shopt -s nullglob
mds=(*.md)
[ ${#mds[@]} -gt 0 ] || {
  echo "no .md files in $DIR" >&2
  exit 1
}

fail=0
printf '=== USECASE-5 relocation check (steps 3 + 5 only): %s ===\n' "$DIR"

# --- Step 3: relocated entries carry a typed id + UUID -----------------------
printf '\n--- Step 3: relocated entries carry a typed id + UUID (../decisions) ---\n'
if [ ! -d ../decisions ]; then
  echo "  (no sibling decision area — nothing to check)"
else
  dec_mds=(../decisions/*.md)
  if [ ${#dec_mds[@]} -eq 0 ] || [ ! -e "${dec_mds[0]}" ]; then
    echo "  (sibling decision area has no .md files — nothing to check)"
  else
    # One awk pass over every decisions file: DEFPAT is built the SAME way
    # trace-extract.sh/self-checks.sh build it (inside BEGIN, so awk's own
    # string-literal escaping turns "\t" into a real tab before the regex
    # engine ever sees it — building this in bash and passing it via -v would
    # not, since -v values are not re-processed that way).
    #
    # HEADING-ONLY, unlike the behavior-side DEFPAT: `docs/decisions/README.md`'s
    # "Entry ids" fixes the definition shape as an H3 heading
    # (``### `DEC-<TOPIC>-<n>` — summary <!-- uuid: ... --> ``), never a bullet.
    # Every real decision entry in this corpus is a heading; every README.md's
    # own "Entries" INDEX section restates each id as a plain BULLET with no
    # UUID (by design — "This README is the index ... nothing else is
    # authoritative about what exists here"). Admitting the bullet form here
    # (as the behavior-side DEFPAT does) would misread every index line as an
    # un-UUID'd definition — a false FAIL on every real decisions/README.md.
    pairs=$(
      awk -v idpat="$BEHAVIOR_IDPAT" '
        BEGIN {
          IDPAT = idpat
          DEFPAT = "^[ \t]*#+[ \t]+[*_`]*" IDPAT
          UUIDCOMMENT = "<!--[ \t]*uuid:[ \t]*[^ \t]+[ \t]*-->"
        }
        $0 ~ DEFPAT {
          line = $0
          if (match(line, IDPAT) > 0) {
            id = substr(line, RSTART, RLENGTH)
            if (id ~ /^(DEC|IMPL)-/) {
              u = "-"
              if (match(line, UUIDCOMMENT) > 0) {
                u = substr(line, RSTART, RLENGTH)
                gsub(/^<!--[ \t]*uuid:[ \t]*/, "", u)
                gsub(/[ \t]*-->$/, "", u)
              }
              fn = FILENAME
              sub(/^\.\.\/decisions\//, "", fn)
              printf "%s:%d %s %s\n", fn, FNR, id, u
            }
          }
        }
      ' "${dec_mds[@]}"
    )
    if [ -z "$pairs" ]; then
      echo "  (sibling decision area defines no DEC-/IMPL- entry yet — nothing to check)"
    else
      n=0
      while IFS=' ' read -r loc id u; do
        [ -n "$loc" ] || continue
        n=$((n + 1))
        if [ "$u" = "-" ]; then
          printf '  FAIL %s (%s) carries no UUID on its definition line — USECASE-5 step 3\n' "$id" "$loc"
          fail=1
        elif ! printf '%s\n' "$u" | grep -qE "$UUIDRE"; then
          printf '  FAIL %s (%s) carries a malformed UUID: %s\n' "$id" "$loc" "$u"
          fail=1
        fi
      done <<<"$pairs"
      dups=$(printf '%s\n' "$pairs" | awk '{print $3}' | { grep -v '^-$' || true; } | sort | uniq -d)
      if [ -n "$dups" ]; then
        while IFS= read -r d; do
          [ -n "$d" ] && printf '  FAIL duplicate UUID within the decision area: %s\n' "$d"
        done <<<"$dups"
        fail=1
      fi
      [ "$fail" -eq 0 ] && printf '  clean (%s entr%s: well-formed, unique UUID on its own definition line)\n' \
        "$n" "$([ "$n" -eq 1 ] && echo y || echo ies)"
    fi
  fi
fi

# --- Step 5: no tombstone left in the behavior doc (INV-4) -------------------
printf '\n--- Step 5: no tombstone left in the behavior doc (INV-4) ---\n'
# Three NOTE shapes only (see the header for why a bare phrase grep is wrong
# for this corpus): a parenthetical, an HTML comment, or a bold-label /
# blockquote lead, each reading "moved to ..." or "relocated to ...".
TOMB_RE='\([^)]*\b(moved|relocated)\b[^)]*\b(to|into)\b[^)]*\)'
TOMB_RE="$TOMB_RE"'|<!--[^>]*\b(moved|relocated)\b'
TOMB_RE="$TOMB_RE"'|^[[:space:]]*(\*\*|>[[:space:]]*)(Moved|Relocated)\b'

# `-H` (not `-r`, which does NOT reliably force it: GNU grep only shows the
# filename automatically for "more than one FILE", and a set with exactly one
# .md file — like the `relocation-tombstone` corpus fixtures themselves — has
# only one, so the filename silently drops and every downstream `file:line`
# assumption, incl. sort_locs's "path, then line" ordering, breaks). `-H`
# forces it unconditionally regardless of file count.
tomb=$({ grep -HnEi -- "$TOMB_RE" ./*.md || true; } | sed 's#^\./##' | sort_locs)
if [ -z "$tomb" ]; then
  echo "  clean (no 'moved to ...' / 'relocated to ...' note found)"
else
  fail=1
  while IFS= read -r l; do
    [ -n "$l" ] || continue
    printf '  FAIL tombstone note left in behavior doc: %s\n' "$l"
  done <<<"$tomb"
fi

printf '\n'
if [ "$fail" -ne 0 ]; then
  echo "relocation-check: FAIL — see the FAIL lines above"
  exit 1
fi
echo "relocation-check: OK"
