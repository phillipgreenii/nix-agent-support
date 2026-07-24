#!/usr/bin/env bats
# V3 inter-evaluator mechanical coverage (bead pg2-hvlyj.15, plan item 5.3).
# Drives resolve-imports.sh (on PATH as `resolve-imports`) over a shared owner
# set and per-seam-check-type implementer fixtures, asserting the classification
# (ok / stale-name WARN / divergence FAIL / external) and exit code. Fixtures
# are inline so the test is self-contained; corpus/v3/ carries the same fixtures
# as the durable, agent-facing artifact.

setup() {
  OWNER="$BATS_TEST_TMPDIR/owner"
  IMPL="$BATS_TEST_TMPDIR/impl"
  mkdir -p "$OWNER" "$IMPL"
  cat > "$OWNER/interfaces.md" <<'MD'
# Interfaces — owner
- **`INTF-SOURCE`** — typed events. <!-- uuid: 11111111-1111-4111-8111-111111111111 -->
- **`INTF-HANDLER`** — events out. <!-- uuid: 22222222-2222-4222-8222-222222222222 -->
MD
}

impl_table() {
  # $1=name $2=uuidcell
  cat > "$IMPL/interfaces.md" <<MD
# Interfaces — implementer

## External references

| Name | Owner set-path | Owner UUID |
| ---- | -------------- | ---------- |
| $1 | \`owner/docs/behavior\` | $2 |
MD
}

@test "obligation-alignment: matching name + resolving UUID -> ok" {
  impl_table '`INTF-SOURCE`' '11111111-1111-4111-8111-111111111111'
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok .*INTF-SOURCE'
}

@test "stale-name: old name but resolving UUID -> WARN (not a failure)" {
  impl_table '`INTF-SRC`' '11111111-1111-4111-8111-111111111111'
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  WARN .*stale name'
}

@test "genuine-divergence: unresolved owner UUID -> FAIL (non-zero exit)" {
  impl_table '`INTF-GHOST`' '99999999-9999-4999-8999-999999999999'
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -ne 0 ]
  echo "$output" | grep -qE '^  FAIL .*divergence'
}

@test "external-contract declared: no owner UUID, marked external -> external" {
  impl_table '`git`' '(external)'
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  external .*git'
}

@test "external-contract undeclared: empty imports table -> no external references" {
  cat > "$IMPL/interfaces.md" <<'MD'
# Interfaces — implementer
The core commits to a git branch and pushes it (used but undeclared).

## External references

| Name | Owner set-path | Owner UUID |
| ---- | -------------- | ---------- |
MD
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "declares no external references"
}
