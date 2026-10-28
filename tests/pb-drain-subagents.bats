#!/usr/bin/env bats
# bats file_tags=type:integration
# Conformance for bead pg2-5ggo5o (ADR 0113): pb:drain-implementer and pb:drain-lander pin model
# and effort in their frontmatter, and every drain-one dispatch site points at the one rule that
# names them. Each assertion runs once on the real tree and once on a mutated copy that must fail.
#
# Environment: PBD_PB_DIR  path to the pb plugin root (holds agents/ and skills/)

HEADING='Which agent receives a drain dispatch'

setup() {
  : "${PBD_PB_DIR:?}"
  work="$BATS_TEST_TMPDIR/pb"
  cp -R "$PBD_PB_DIR" "$work"
  chmod -R u+w "$work"
}

fm() { awk 'NR==1&&$0!="---"{exit} NR>1&&$0=="---"{exit} NR>1{print}' "$1"; }

check_agents() {
  local root="$1" a f
  for a in drain-implementer drain-lander; do
    f="$root/agents/$a.md"
    [ -f "$f" ] || { echo "missing $f"; return 1; }
    fm "$f" | grep -qx "name: $a" || { echo "$a: name"; return 1; }
    fm "$f" | grep -qE '^model: claude-sonnet-[0-9][0-9-]*$' || { echo "$a: model"; return 1; }
    fm "$f" | grep -qx 'effort: medium' || { echo "$a: effort"; return 1; }
  done
  [ "$(fm "$root/agents/drain-implementer.md" | grep '^model:')" = "$(fm "$root/agents/drain-lander.md" | grep '^model:')" ] \
    || { echo "model lines differ"; return 1; }
}

check_tools() {
  local root="$1" a
  for a in drain-implementer drain-lander; do
    [ -f "$root/agents/$a.md" ] || return 1
    if fm "$root/agents/$a.md" | grep -q '^tools:'; then echo "$a has tools:"; return 1; fi
  done
}

check_rules() {
  local f="$1/skills/drain-one/references/rules.md" sec
  sec="$(awk -v h="$HEADING" '/^#/{ins = index($0, h) > 0; next} ins' "$f")"
  [ -n "$sec" ] || { echo "no section"; return 1; }
  for t in 'pb:drain-implementer' 'pb:drain-lander' 're-dispatch'; do
    printf '%s\n' "$sec" | grep -qF -- "$t" || { echo "section lacks $t"; return 1; }
  done
}

# site FILE FIXED COUNT: FIXED occurs COUNT times (end-anchored when ANCHOR=end) and each has
# the heading within 6 lines either side.
site() {
  local file="$1" fixed="$2" count="$3"
  awk -v f="$fixed" -v h="$HEADING" -v n="$count" -v anchor="${ANCHOR:-}" '
    { line[NR] = $0 }
    END {
      hits = 0
      for (i = 1; i <= NR; i++) {
        if (anchor == "end") { ok = (length(line[i]) >= length(f) && substr(line[i], length(line[i]) - length(f) + 1) == f) }
        else ok = (index(line[i], f) > 0)
        if (!ok) continue
        hits++
        found = 0
        for (j = i - 6; j <= i + 6; j++) if (j >= 1 && j <= NR && index(line[j], h) > 0) found = 1
        if (!found) { print file ": no heading near " f; bad = 1 }
      }
      if (hits != n) { print file ": " f " occurs " hits " times, want " n; bad = 1 }
      exit bad
    }' "$file"
}

check_sites() {
  local d="$1/skills/drain-one/references"
  site "$d/delegate.md" '**UNCURATED PATH.**' 1 &&
    site "$d/delegate.md" 'Re-dispatch with guidance' 1 &&
    site "$d/land.md" 'LANDER SUBAGENT' 1 &&
    ANCHOR=end site "$d/land.md" 're-delegate' 1 &&
    site "$d/land.md" 're-dispatch the lander at most' 2
}

@test "drain agents pin model and effort" {
  run check_agents "$work"
  [ "$status" -eq 0 ]
  sed -i.bak 's/^effort: medium/effort: high/' "$work/agents/drain-lander.md"
  run check_agents "$work"
  [ "$status" -ne 0 ]
}

@test "drain agents pin one identical model" {
  sed -i.bak 's/^model: claude-sonnet-5-5/model: claude-sonnet-4-9/' "$work/agents/drain-lander.md"
  run check_agents "$work"
  [ "$status" -ne 0 ]
}

@test "drain agents inherit the full tool set" {
  run check_tools "$work"
  [ "$status" -eq 0 ]
  sed -i.bak 's/^effort: medium/effort: medium\ntools: Bash/' "$work/agents/drain-implementer.md"
  run check_tools "$work"
  [ "$status" -ne 0 ]
}

@test "rules name the drain agents" {
  run check_rules "$work"
  [ "$status" -eq 0 ]
  sed -i.bak 's/pb:drain-lander/pb:other/g' "$work/skills/drain-one/references/rules.md"
  run check_rules "$work"
  [ "$status" -ne 0 ]
}

@test "dispatch sites point at the rule" {
  run check_sites "$work"
  [ "$status" -eq 0 ]
  sed -i.bak '0,/Which agent receives a drain dispatch/s//X/' "$work/skills/drain-one/references/delegate.md"
  run check_sites "$work"
  [ "$status" -ne 0 ]
}

@test "CURATED PATH unchanged" {
  grep -qF 'plan-decompose:packet-implementer' "$work/skills/drain-one/references/delegate.md"
  sed -i.bak 's/plan-decompose:packet-implementer/x/g' "$work/skills/drain-one/references/delegate.md"
  run grep -qF 'plan-decompose:packet-implementer' "$work/skills/drain-one/references/delegate.md"
  [ "$status" -ne 0 ]
}
