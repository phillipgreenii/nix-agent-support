#!/usr/bin/env bash
# Deterministic self-checks for a single behavior-docs set.
# Usage: self-checks.sh <set-dir>        (a directory of .md files, usually .../docs/behavior)
#
# Prints one section per check, each mapped to the method invariant it enforces. These are the
# mechanical checks only — cross-set resolution and the substitution test
# need the agent's judgment (see SKILL.md steps 1 and 3). The >=2x pass is a HEURISTIC (literal
# term counting); confirm any borderline FAIL by hand.
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
DIR="${1:?usage: self-checks.sh <behavior-docs-set-dir>}"
cd "$DIR"
shopt -s nullglob
mds=(*.md)
[ ${#mds[@]} -gt 0 ] || {
  echo "no .md files in $DIR" >&2
  exit 1
}
# The typed-name families INV-3 enumerates. `USECASE` is one of them: without it a
# `USECASE-<n>` definition line matches no ID, so its UUID carrier reads as an ORPHAN
# (a carrier with no ID on its line) and the UUID section FAILs on a conformant set.
IDRE='\b(INV|GOAL|STORY|USECASE|JOURNEY|INTF|ACTOR|OQ)-[A-Za-z0-9]+(-[A-Za-z0-9]+)*\b'
sec() { printf '\n=== %s ===\n' "$1"; }

sec "Files"
printf '%s\n' ./*.md | sed 's#^\./##'

sec "IDs present (INV-3) — by family, then full list"
# `|| true` so a set with no IDs (e.g. a minimal fixture) does not trip
# `set -o pipefail` and abort the whole run.
{ grep -rhoE "$IDRE" ./*.md || true; } | sed -E 's/-[0-9]+$//' | sort | uniq -c
# Joined onto one line with NO trailing space: a trailing space is invisible in a
# terminal but makes this report compare unequal to itself across transports that
# trim it (a nix build log does, a file does not).
ids_line=$({ grep -rhoE "$IDRE" ./*.md || true; } | sort -uV | tr '\n' ' ')
printf '%s\n' "${ids_line% }"

sec "UUID carriers (INV-3) — well-formed, intra-set-unique, one-per-ID, no orphans; expect clean"
# Identity is a UUID minted at a definition (INV-3), carried in an HTML comment
# '<!-- uuid: XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX -->'. Retrofit is lazy, so most definitions
# carry no UUID yet; this validates only the carriers that ARE present: each MUST be a canonical
# RFC-4122 UUID and no two definitions in this set may share one. Because a UUID is minted ONCE at
# its definition, two further intra-set rules hold: each leading-ID token MUST bear at most one
# carrier (no DUAL IDENTITY — e.g. a reference re-minting the owner's UUID), and every carrier MUST
# sit on a line that carries an ID token (no ORPHAN carrier). Cross-set resolution (owner-UUID
# resolves to exactly one owner definition) is inherently multi-set and is checked by the INTER
# evaluator, not here.
UUIDRE='^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
carriers=$({ grep -rhoE '<!--[[:space:]]*uuid:[[:space:]]*[^[:space:]]+[[:space:]]*-->' ./*.md || true; } |
  sed -E 's/<!--[[:space:]]*uuid:[[:space:]]*//; s/[[:space:]]*-->$//')
if [ -z "$carriers" ]; then
  echo "  none present (lazy retrofit; nothing to validate)"
else
  uuid_bad=0
  while IFS= read -r u; do
    [ -n "$u" ] || continue
    printf '%s\n' "$u" | grep -qE "$UUIDRE" || {
      printf '  FAIL malformed UUID: %s\n' "$u"
      uuid_bad=1
    }
  done <<<"$carriers"
  dups=$(printf '%s\n' "$carriers" | sort | uniq -d)
  if [ -n "$dups" ]; then
    while IFS= read -r d; do [ -n "$d" ] && printf '  FAIL duplicate UUID within set: %s\n' "$d"; done <<<"$dups"
    uuid_bad=1
  fi
  # Pair each carrier line with the FIRST ID token appearing BEFORE its marker (the
  # definition the carrier identifies). Field 1 is that ID (empty for an orphan
  # line); field 2 is the UUID. Space-separated — IDs and UUIDs contain no spaces.
  pairs=$(
    { grep -rhE '<!--[[:space:]]*uuid:[[:space:]]*[^[:space:]]+[[:space:]]*-->' ./*.md || true; } |
      while IFS= read -r line; do
        u=$(printf '%s\n' "$line" |
          grep -oE '<!--[[:space:]]*uuid:[[:space:]]*[^[:space:]]+[[:space:]]*-->' |
          sed -E 's/<!--[[:space:]]*uuid:[[:space:]]*//; s/[[:space:]]*-->$//' | head -n1)
        prefix=${line%%<!--*}
        id=$({ printf '%s\n' "$prefix" | grep -oE "$IDRE" || true; } | head -n1)
        printf '%s %s\n' "$id" "$u"
      done
  )
  # ORPHAN carrier: a carrier line with no leading ID token (empty field 1 → a
  # leading space). Each such carrier has no definition to identify.
  orphans=$(printf '%s\n' "$pairs" | { grep '^ ' || true; } | sed -E 's/^ //')
  if [ -n "$orphans" ]; then
    while IFS= read -r o; do
      [ -n "$o" ] && printf '  FAIL orphan carrier (no ID token on its line): %s\n' "$o"
    done <<<"$orphans"
    uuid_bad=1
  fi
  # DUAL IDENTITY: an ID token appearing with more than one DISTINCT carrier. Dedup
  # identical (id,uuid) pairs first (a verbatim repeat is a value-dup, caught above),
  # then a leading ID seen twice bears two carriers.
  dual=$(printf '%s\n' "$pairs" | { grep -v '^ ' || true; } | sort -u | cut -d' ' -f1 | sort | uniq -d)
  if [ -n "$dual" ]; then
    while IFS= read -r id; do
      [ -n "$id" ] && printf '  FAIL dual identity: %s bears more than one carrier\n' "$id"
    done <<<"$dual"
    uuid_bad=1
  fi
  [ "$uuid_bad" -eq 0 ] && printf '  clean (%s carrier(s): well-formed, unique, one per ID, none orphaned)\n' "$(printf '%s\n' "$carriers" | grep -c .)"
fi

sec "Status headers (INV-4) — expect none"
grep -rniE '(^|[[:space:]])(status|state)[[:space:]]*[:=]|\(draft\)|\(partial\)|forward-looking|\bTODO\b|\bTBD\b' ./*.md |
  grep -viE 'state (diagram|machine)|status-projection|handler.?state|session.?state|failure|lifecycle|per-state|session-status|state[[:space:]]*[:=][[:space:]]*(running|completed|paused|failed|healthy|degraded|unavailable|starting|started|stopping|stopped|crashing)' ||
  echo "  clean"

sec "Inline status framing (INV-4 / intra #15) — expect none"
# A behavior-docs set is LIVING by default (INV-4): it states the intended behavior as-if-true,
# never annotating a rule with its current implementation status. #15's flagship catch is inline
# status framing in PROSE — "unmet by the current implementation", "not yet implemented" — which the
# per-doc status-header check above does NOT catch (it is prose, not a `status:` header). Flag it.
# NOTE the last alternative is anchored to a status-framing lead-in
# (yet/still/remain(s)) so it flags "yet to be implemented" (a status claim) but
# NOT contract prose like "the interface to be implemented by an implementer"
# (INV-8/INV-18) — the bare "to be implemented" substring was a latent false positive.
grep -rniE 'unmet by the current implementation|not[[:space:]]+yet[[:space:]]+implemented|currently[[:space:]]+unimplemented|no[[:space:]]+current[[:space:]]+implementation|does[[:space:]]+not[[:space:]]+yet[[:space:]]+(exist|support|implement)|planned[[:space:]]+but[[:space:]]+not[[:space:]]+(yet[[:space:]]+)?(built|implemented)|(yet|still|remains?)[[:space:]]+to[[:space:]]+be[[:space:]]+implemented' ./*.md ||
  echo "  clean"

sec "Realization-gap register (INV-23) — one '## Realization gaps' section; no gap inside an OQ-"
# INV-23 fixes the CARRIER a realization gap (INV-15) is tracked in: exactly one set-level section
# named '## Realization gaps', keyed by element id, sitting OUTSIDE every element definition, and
# NEVER an open question. The section NAME is normative and the FILE is not — the same calibration
# INV-3 uses for '## External references' — so the heading is looked for anywhere in the set.
#
# TWO FINDINGS AT TWO STRENGTHS, and the split is mechanical rather than editorial:
#
#   OQ- MISUSE is a FAIL. An OQ- says the INTENT is unsettled; a gap says the intent is settled and
#   the build has not caught up. Recording one as the other puts implementation-status prose inside
#   an element definition AND mints a citable identity (INV-3) whose later deletion strands every
#   reference to it. It is precise, and no set shipped here trips it, so it costs nothing.
#
#   MISSING PRESENCE is an ADVISORY. tests/behavior-docs-real-corpus.sh treats ANY FAIL from this
#   script as a hard failure with NO baseline escape (it never calls `record` on this output), so a
#   hard presence check would red the build for every set not yet retrofitted. Promote it to FAIL
#   once every set the real-corpus runner reads carries the section
#   (`behavior-docs/docs/decisions · DEC-CONFORM-2`).
reg_hits=$({ grep -rhoiE '^##[[:space:]]+Realization[[:space:]]+gaps[[:space:]]*$' ./*.md || true; } | wc -l | tr -d ' ')
if [ "$reg_hits" -eq 0 ]; then
  echo "  ADVISORY: no '## Realization gaps' section — INV-23 requires one in EVERY set, even with"
  echo "  nothing to record, so that an absent section means omitted rather than converged"
elif [ "$reg_hits" -gt 1 ]; then
  printf '  FAIL %s register sections — a set carries exactly one (INV-23)\n' "$reg_hits"
else
  echo "  register section present"
fi
# A gap recorded inside an OQ- element. Track the CURRENT definition the same way
# trace-extract.sh does (a bullet or heading marker is MANDATORY, so a wrapped prose line opening
# with a code span is not mistaken for a definition), and flag any line in an OQ- block that reads
# as a gap record.
oq_gap=$(
  for f in ./*.md; do
    awk -v fname="${f#./}" '
      BEGIN {
        IDPAT = "(INV|GOAL|STORY|USECASE|JOURNEY|INTF|ACTOR|OQ)-[A-Za-z0-9]+(-[A-Za-z0-9]+)*"
        DEFPAT = "^[ \t]*(([-*+][ \t]+)|(#+[ \t]+))[*_`]*" IDPAT
        cur = ""
      }
      {
        if ($0 ~ DEFPAT) {
          match($0, IDPAT)
          cur = substr($0, RSTART, RLENGTH)
        } else if ($0 ~ /^[ \t]*$/) {
          cur = ""
        }
        if (cur ~ /^OQ-/ && tolower($0) ~ /realization gap/) printf "%s:%d %s\n", fname, FNR, cur
      }
    ' "$f"
  done
)
if [ -n "$oq_gap" ]; then
  while IFS= read -r l; do
    [ -n "$l" ] && printf '  FAIL realization gap recorded inside an open question: %s (INV-23)\n' "$l"
  done <<<"$oq_gap"
else
  echo "  no gap recorded inside an OQ-"
fi

sec "Cross-set relative links (INV-8) — expect none; use textual '<repo> · <path> · <ID>'"
grep -rnoE '\]\(\.\.?/[^)]*\)' ./*.md || echo "  none"

sec "Floor-leakage candidates (INV-2/10) — judge each with the substitution test"
grep -rniE '[A-Za-z_]+\.(go|ts|py|rs|java|rb):[0-9]+|[A-Za-z_]+\([^)]*\)[[:space:]]+(returns|validates|does)|retry[[:space:]]+[0-9]+|[0-9]+(ms|s)[[:space:]]+(backoff|timeout|poll)' ./*.md ||
  echo "  none obvious"

sec "Named concept used >=2x beyond its glossary definition (INV-14) — HEURISTIC"
gloss=""
for f in ./*.md; do case "${f##*/}" in *[Gg]loss*)
  gloss="$f"
  break
  ;;
esac done
if [ -n "${gloss:-}" ]; then
  # bold headwords from glossary bullets, minus any trailing "(...)" qualifier
  # (`|| true` so a glossary with no bold-bullet headwords does not abort the run)
  headwords=$({ grep -oE '^[[:space:]]*[-*][[:space:]]+\*\*[^*]+\*\*' "$gloss" || true; } |
    sed -E 's/.*\*\*(.+)\*\*/\1/; s/[[:space:]]*\(.*\)//')
  if [ -z "$headwords" ]; then
    # No headwords => the >=2x heuristic never runs; report that instead of
    # passing vacuously (the glossary may not use "- **term** —" bullets).
    printf '  NOTICE: no bold headwords extracted from %s — heuristic did not run (glossary not using "- **term** —" bullets)\n' "${gloss#./}"
  else
    printf '%s\n' "$headwords" |
      while IFS= read -r term; do
        [ -n "$term" ] || continue
        total=$({ grep -rhoiF "$term" ./*.md || true; } | wc -l | tr -d ' ')
        g=$({ grep -hoiF "$term" "$gloss" || true; } | wc -l | tr -d ' ')
        outside=$((total - g))
        if [ "$outside" -lt 2 ]; then
          printf '  FAIL (%s uses outside definition)  %s\n' "$outside" "$term"
        fi
      done
    echo "  (terms not listed are used >=2x beyond their definition; confirm FAILs by hand — see note)"
  fi
else
  echo "  no glossary file found (looked for *gloss*.md)"
fi

sec "Mermaid fences balanced"
# `|| true` so a set with no mermaid fences (or no code fences at all) does not
# trip `set -o pipefail` and abort the run.
opens=$({ grep -rho '```mermaid' ./*.md || true; } | wc -l | tr -d ' ')
fences=$({ grep -rho '```' ./*.md || true; } | wc -l | tr -d ' ')
printf '  mermaid opens: %s ; total code fences: %s (%s)\n' "$opens" "$fences" \
  "$([ $((fences % 2)) -eq 0 ] && echo even || echo ODD-unbalanced)"
