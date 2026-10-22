#!/usr/bin/env bats
# bats file_tags=type:integration
# Conformance for bead pg2-zpj6z (ADR 0107): a drain agent that cannot finish a bead ESCALATES it
# (label `escalated`, findings in a comment, claim released in the SAME update); it never parks the
# bead for a person by writing `human` itself, and nothing but the escalation-triager's
# `hand-to-human` writes `attention`. The only direct `human` writes left in the consumers are NEW
# beads filed for the operator (planned operator work, beads-lifecycle LV-2), and each of those
# must say so within 20 lines above the write.
#
# Environment (set by the flake check, or by hand for a local run):
#   PBE_MARKETPLACE  path to a claude-marketplace tree holding pb/, plan-decompose/agents and
#                    bead-grooming/
#
# The mutants run the SAME scan function over a mutated copy, so the check is shown to bite.

CONSUMERS=(
  pb/skills/drain-stuck/SKILL.md
  pb/skills/drain-one/SKILL.md
  pb/skills/drain-one/references/rules.md
  pb/skills/drain-one/references/land.md
  pb/skills/drain-one/references/post-deploy-gate.md
  pb/skills/drain-one/references/delegate.md
  pb/skills/pb-gate-lifecycle/SKILL.md
  pb/commands/drain-beads.md
  pb/commands/unblock-human-beads.md
  pb/commands/unstick-beads.md
  pb/commands/stop-draining-beads.md
  pb/agents/unstick-batch-worker.md
  plan-decompose/agents/phase-plan-verifier.md
  bead-grooming/skills/bead-grooming/SKILL.md
  bead-grooming/skills/bead-grooming/references/criteria-examples.md
)

setup() {
  : "${PBE_MARKETPLACE:?}"
  work="$(mktemp -d)"
  cp -R "$PBE_MARKETPLACE" "$work/mp"
  chmod -R u+w "$work"
}

teardown() {
  rm -rf "$work"
}

# scan ROOT: print one `file:line: text` per direct write of `human` (create --labels / update
# --add-label) that lacks a planned-operator-work mention in the 20 lines above it, and per ANY
# write of `attention` (only hand-to-human writes it). No output means conformant.
scan() {
  local root="$1" f
  for f in "${CONSUMERS[@]}"; do
    awk -v file="$f" '
      { line[NR] = $0 }
      END {
        for (i = 1; i <= NR; i++) {
          if (!match(line[i], /(--add-label|--labels)[ =]+[^ `"\\]+/)) continue
          tok = substr(line[i], RSTART, RLENGTH)
          sub(/^(--add-label|--labels)[ =]+/, "", tok)
          n = split(tok, a, ",")
          human = 0; attention = 0
          for (j = 1; j <= n; j++) { if (a[j] == "human") human = 1; if (a[j] == "attention") attention = 1 }
          if (attention) { print file ":" i ": " line[i]; continue }
          if (!human) continue
          ok = 0
          for (k = (i > 20 ? i - 20 : 1); k <= i; k++)
            if (tolower(line[k]) ~ /planned operator work|lv-2/) ok = 1
          if (!ok) print file ":" i ": " line[i]
        }
      }' "$root/$f"
  done
}

# Literal replace of the first occurrence of $2 by $3 in file $1; fails when $2 is absent so a
# stale mutant cannot silently become a no-op.
mutate() {
  awk -v pat="$2" -v rep="$3" '
    !done && (i = index($0, pat)) {
      $0 = substr($0, 1, i - 1) rep substr($0, i + length(pat)); done = 1
    }
    { print }
    END { exit done ? 0 : 3 }
  ' "$1" >"$1.new"
  mv "$1.new" "$1"
}

stuck() { printf '%s\n' "$work/mp/pb/skills/drain-stuck/SKILL.md"; }

@test "every consumer file exists" {
  for f in "${CONSUMERS[@]}"; do
    [ -f "$work/mp/$f" ]
  done
}

@test "no consumer instructs a direct park of a stuck bead as human, and none writes attention" {
  run scan "$work/mp"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "the direct human writes that remain are exactly the planned-operator-work cases" {
  # file:count of lines that write `human` with --add-label/--labels. Each is a NEW bead filed
  # for the operator (LV-2): drain-stuck's decision bead and worktree-review follow-up,
  # the post-deploy gate fallback's verification child, pb-gate-lifecycle's verification
  # follow-up, the phase-plan-verifier's open-question bead, and unblock-human-beads'
  # worktree-review follow-up (the step, the rule and the flow diagram).
  expected="pb/skills/drain-stuck/SKILL.md:2
pb/skills/drain-one/references/post-deploy-gate.md:1
pb/skills/pb-gate-lifecycle/SKILL.md:1
pb/commands/unblock-human-beads.md:3
plan-decompose/agents/phase-plan-verifier.md:1"
  actual=""
  for f in "${CONSUMERS[@]}"; do
    n="$(grep -cE '(--add-label|--labels)[ =]+[^ `"]*\bhuman\b' "$work/mp/$f" || true)"
    [ "$n" -gt 0 ] && actual="${actual:+$actual
}$f:$n"
  done
  [ "$actual" = "$expected" ]
}

@test "drain-stuck exits by ESCALATE, CLOSE-AS-MOOT, CONVERT-TO-DEPENDENCY or DEFER-ON-EVENT and the person-park exit is gone" {
  s="$(stuck)"
  grep -q 'ESCALATE (labeled `escalated`' "$s"
  grep -q '^## CLOSE-AS-MOOT' "$s"
  grep -q '^## CONVERT-TO-DEPENDENCY' "$s"
  grep -q '^## DEFER-ON-EVENT' "$s"
  run grep -rn 'PARK (labeled `human`' "$work/mp/pb"
  [ "$status" -eq 1 ]
}

@test "ESCALATE is ONE update that adds escalated and releases the claim (B-1..B-3)" {
  s="$(stuck)"
  grep -qF 'bd update <id> --add-label escalated --status open --assignee "" --actor "ID"' "$s"
  grep -qF 'bd update <id> --add-label escalated,stale-precondition --status open --assignee "" --actor "ID"' "$s"
}

@test "the findings comment (with the FRESHNESS line) is written BEFORE the escalate update" {
  s="$(stuck)"
  c="$(grep -n '^6\. COMMENT' "$s" | head -1 | cut -d: -f1)"
  u="$(grep -n '^7\. .*ESCALATE AND RELEASE' "$s" | head -1 | cut -d: -f1)"
  [ -n "$c" ] && [ -n "$u" ] && [ "$c" -lt "$u" ]
  sed -n "${c},${u}p" "$s" | grep -q 'FRESHNESS:'
  sed -n "${c},${u}p" "$s" | grep -q 'NEEDS:'
}

@test "no consumer tells a drain agent to run hand-to-human (only the triager hands a bead on)" {
  run grep -nE '^[[:space:]]*(hand-to-human|\$ hand-to-human) ' \
    "$work/mp/pb/skills/drain-stuck/SKILL.md" "$work/mp/pb/skills/drain-one/SKILL.md" \
    "$work/mp/pb/commands/drain-beads.md"
  [ "$status" -eq 1 ]
}

@test "release policy park is documented as ESCALATE in pb:drain-one" {
  grep -q "ESCALATES it through .pb:drain-stuck" "$work/mp/pb/skills/drain-one/SKILL.md"
  grep -q 'ESCALATE the bead through `pb:drain-stuck`' "$work/mp/pb/skills/drain-one/references/land.md"
}

@test "an unblock release strips attention together with human" {
  run grep -nE -e '--remove-label human,human-focus-required' "$work/mp/pb/commands/unblock-human-beads.md"
  [ "$status" -eq 1 ]
  grep -q -e '--remove-label human,attention,human-focus-required' "$work/mp/pb/commands/unblock-human-beads.md"
}

@test "mutant: a direct --add-label human in drain-stuck is caught" {
  mutate "$(stuck)" 'bd update <id> --add-label escalated --status open --assignee "" --actor "ID"' 'bd update <id> --add-label human --status open --assignee "" --actor "ID"'
  run scan "$work/mp"
  [ -n "$output" ]
  [[ "$output" == *"drain-stuck/SKILL.md"* ]]
}

@test "mutant: attention written by hand is caught even next to a planned-operator-work mention" {
  mutate "$(stuck)" '--labels human,worktree-review' '--labels human,attention,worktree-review'
  run scan "$work/mp"
  [[ "$output" == *"drain-stuck/SKILL.md"* ]]
}

@test "mutant: a bead-grooming flag that writes human is caught" {
  mutate "$work/mp/bead-grooming/skills/bead-grooming/SKILL.md" 'bd update <id> --add-label escalated' 'bd update <id> --add-label human'
  run scan "$work/mp"
  [[ "$output" == *"bead-grooming/SKILL.md"* ]]
}

@test "mutant: a human write with no planned-operator-work mention above it is caught" {
  # Strip every mention in the phase-plan-verifier; its create line then has no justification.
  sed -i.bak -e 's/planned operator work/other work/g' -e 's/LV-2/LV-9/g' "$work/mp/plan-decompose/agents/phase-plan-verifier.md"
  run scan "$work/mp"
  [[ "$output" == *"phase-plan-verifier.md"* ]]
}

@test "mutant: splitting the escalate into two updates fails the one-call check" {
  mutate "$(stuck)" 'bd update <id> --add-label escalated --status open --assignee "" --actor "ID"' 'bd update <id> --add-label escalated --actor "ID"'
  run grep -qF 'bd update <id> --add-label escalated --status open --assignee "" --actor "ID"' "$(stuck)"
  [ "$status" -ne 0 ]
}
