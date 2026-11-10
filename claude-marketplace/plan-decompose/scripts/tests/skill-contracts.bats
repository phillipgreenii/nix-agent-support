# bats file_tags=type:unit
#
# The skill and agent files ARE the contract an LLM executes, so the rules the
# defect fixes added are pinned here as text checks, next to the behavior tests
# of the scripts those rules call.

PLUGIN="$(cd "$(dirname "$BATS_TEST_FILENAME")/../.." && pwd)"
SKILL="$PLUGIN/skills/plan-decompose/SKILL.md"
BINDING="$PLUGIN/skills/plan-decompose-beads/SKILL.md"

@test "the multi-repo packet label rule is stated exactly once, in the core skill" {
  [ "$(grep -c '^## Packet labels' "$SKILL")" -eq 1 ]
  # the one statement of the rule: another repo's packet does NOT carry agent-support
  run grep -rn 'and NOT `agent-support`' "$PLUGIN/skills" "$PLUGIN/agents" "$PLUGIN/README.md"
  [ "$status" -eq 0 ]
  [ "$(printf '%s\n' "$output" | wc -l | tr -d ' ')" -eq 1 ]
  [[ "$output" == *"plan-decompose/SKILL.md"* ]]
  # the binding points at it instead of restating it
  grep -q 'under$' "$BINDING" && grep -q '^"Packet labels" in the core skill' "$BINDING"
}

@test "the cold-reader agent separates decisions from implementer-owned choices" {
  agent="$PLUGIN/agents/cold-reader.md"
  grep -q '\[decision\]' "$agent"
  grep -q '\[implementer-owned\]' "$agent"
  grep -q '^3. `implementer-owned:`' "$agent"
}

@test "the skill forbids uncited preamble text written to satisfy the cold-reader" {
  grep -q 'MUST NOT add process text' "$SKILL"
}

@test "mode check requires the seam register and halts before any docket or packet exists" {
  grep -q 'check-design.sh --design' "$SKILL"
  grep -q 'An undecided seam is a BLOCKING gap, never advisory' "$SKILL"
  grep -q 'halts BEFORE step 1' "$SKILL"
}

@test "step 6 runs the cold-read round through the ledger with a stated batch size and a gate" {
  grep -q 'batch <ledger> 8' "$SKILL"
  grep -q 'A dispatch the Agent tool refuses' "$SKILL"
  grep -q '`gate <ledger>` MUST exit 0 before step 7 or any release' "$SKILL"
}

@test "step 9 wires edges through wire-edges.sh, never by hand" {
  grep -q 'scripts/wire-edges.sh --edges' "$SKILL"
  grep -q 'hand-typed `bd dep add`' "$SKILL"
  grep -q 'scripts/wire-edges.sh' "$BINDING"
}

@test "the label audit is documented as scoped to the docket" {
  grep -q 'audit-docket-label-leak.sh --docket <docket-id>' "$SKILL"
  grep -q 'audit-docket-label-leak.sh --docket <docket-id>' "$BINDING"
}

@test "every helper script the skill names exists and is executable" {
  for s in wire-edges.sh coldread-ledger.sh check-design.sh audit-docket-label-leak.sh; do
    grep -q "scripts/$s" "$SKILL"
    [ -x "$PLUGIN/scripts/$s" ]
  done
}
