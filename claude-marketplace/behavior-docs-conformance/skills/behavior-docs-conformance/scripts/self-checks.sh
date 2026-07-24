#!/usr/bin/env bash
# Deterministic self-checks for a single behavior-docs set.
# Usage: self-checks.sh <set-dir>        (a directory of .md files, usually .../docs/behavior)
#
# Prints one section per check, each mapped to the method invariant it enforces. These are the
# mechanical checks only — cross-set resolution, extent traceability, and the substitution test
# need the agent's judgment (see SKILL.md steps 1 and 3). The >=2x pass is a HEURISTIC (literal
# term counting); confirm any borderline FAIL by hand.
set -euo pipefail
DIR="${1:?usage: self-checks.sh <behavior-docs-set-dir>}"
cd "$DIR"
shopt -s nullglob
mds=(*.md)
[ ${#mds[@]} -gt 0 ] || {
  echo "no .md files in $DIR" >&2
  exit 1
}
IDRE='\b(INV|GOAL|STORY|JOURNEY|INTF|ACTOR|OQ)-[A-Za-z0-9]+(-[A-Za-z0-9]+)*\b'
sec() { printf '\n=== %s ===\n' "$1"; }

sec "Files"
printf '%s\n' ./*.md | sed 's#^\./##'

sec "IDs present (INV-3) — by family, then full list"
grep -rhoE "$IDRE" ./*.md | sed -E 's/-[0-9]+$//' | sort | uniq -c
grep -rhoE "$IDRE" ./*.md | sort -uV | tr '\n' ' '
echo

sec "UUID carriers (INV-3) — well-formed + intra-set-unique; expect clean"
# Identity is a UUID minted at a definition (INV-3), carried in an HTML comment
# '<!-- uuid: XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX -->'. Retrofit is lazy, so most definitions
# carry no UUID yet; this validates only the carriers that ARE present: each MUST be a canonical
# RFC-4122 UUID and no two definitions in this set may share one. Cross-set resolution
# (owner-UUID resolves to exactly one owner definition) is inherently multi-set and is checked by
# the V3 inter-evaluator, not here.
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
  [ "$uuid_bad" -eq 0 ] && printf '  clean (%s carrier(s), all well-formed + unique)\n' "$(printf '%s\n' "$carriers" | grep -c .)"
fi

sec "Status headers (INV-4) — expect none"
grep -rniE '(^|[[:space:]])(status|state)[[:space:]]*[:=]|\(draft\)|\(partial\)|forward-looking|\bTODO\b|\bTBD\b' ./*.md |
  grep -viE 'state (diagram|machine)|status-projection|handler.?state|session.?state|failure|lifecycle|per-state|session-status|state[[:space:]]*[:=][[:space:]]*(running|completed|paused|failed|healthy|degraded|unavailable|starting|started|stopping|stopped|crashing)' ||
  echo "  clean"

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
  grep -oE '^[[:space:]]*[-*][[:space:]]+\*\*[^*]+\*\*' "$gloss" |
    sed -E 's/.*\*\*(.+)\*\*/\1/; s/[[:space:]]*\(.*\)//' |
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
else
  echo "  no glossary file found (looked for *gloss*.md)"
fi

sec "Mermaid fences balanced"
opens=$(grep -rhoc '```mermaid' ./*.md | paste -sd+ - | bc 2>/dev/null || grep -rho '```mermaid' ./*.md | wc -l)
fences=$(grep -rho '```' ./*.md | wc -l | tr -d ' ')
printf '  mermaid opens: %s ; total code fences: %s (%s)\n' "$opens" "$fences" \
  "$([ $((fences % 2)) -eq 0 ] && echo even || echo ODD-unbalanced)"
