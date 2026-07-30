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

# --- Targeted hardening (bead pg2-vybrv) — each FAILs before its fix -----------

@test "external-misclass (#1): no-UUID row with a HYPHENATED owner path -> WARN, not external" {
  # Real repo paths contain hyphens (e.g. phillipgreenii-nix-agent-support). The
  # old external test ran `grep 'external|n/a|—|-'` on "$opath $uuidcell" and a
  # bare '-' matched ANY hyphenated path, misclassifying every no-UUID row as an
  # external contract and making the WARN branch unreachable. The UUID cell here
  # is empty and the row is NOT marked external, so it MUST WARN.
  cat > "$IMPL/interfaces.md" <<'MD'
# Interfaces — implementer

## External references

| Name | Owner set-path | Owner UUID |
| ---- | -------------- | ---------- |
| `INTF-ORPHAN` | `phillipgreenii-nix-agent-support · behavior-docs/docs/behavior` |  |
MD
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  WARN .*no owner UUID and is not marked external'
  ! echo "$output" | grep -qE '^  external .*INTF-ORPHAN'
}

@test "insec-reset (#2): External-references section state does not leak across concatenated md files" {
  # awk concatenates the implementer's *.md files; if `insec` is not reset at each
  # file boundary a file that ENDS inside the section leaks its state into the next
  # file, whose top rows get parsed as bogus seam rows. Here a.md ends inside the
  # section; b.md's lone row (a ghost UUID) must NOT be parsed.
  cat > "$IMPL/a-interfaces.md" <<'MD'
# A

## External references

| Name | Owner set-path | Owner UUID |
| ---- | -------------- | ---------- |
| `INTF-SOURCE` | `owner/docs/behavior` | 11111111-1111-4111-8111-111111111111 |
MD
  cat > "$IMPL/b-leak.md" <<'MD'
| `INTF-GHOST` | `owner/docs/behavior` | 99999999-9999-4999-8999-999999999999 |
MD
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok .*INTF-SOURCE'
  ! echo "$output" | grep -qE 'INTF-GHOST'
}

@test "separator-row (#3): an alignment-colon separator (:---:) is skipped, not parsed as a data row" {
  cat > "$IMPL/interfaces.md" <<'MD'
# Interfaces — implementer

## External references

| Name | Owner set-path | Owner UUID |
| :--- | :------------: | ---------: |
| `INTF-SOURCE` | `owner/docs/behavior` | 11111111-1111-4111-8111-111111111111 |
MD
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok .*INTF-SOURCE'
  # Exactly ONE classified row (the real seam); the colon-separator must not add one.
  n=$(echo "$output" | grep -cE '^  (ok|WARN|FAIL|external) ')
  [ "$n" -eq 1 ]
}

@test "no-imports-table (#4): implementer with NO External-references section emits a NOTICE (gate not silently vacuous)" {
  cat > "$IMPL/interfaces.md" <<'MD'
# Interfaces — implementer
This set implements the owner but declares no imports table at all.
MD
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qi "no imports table"
}

@test "case-insensitive header (#4): lowercase '## external references' is still parsed" {
  cat > "$IMPL/interfaces.md" <<'MD'
# Interfaces — implementer

## external references

| Name | Owner set-path | Owner UUID |
| ---- | -------------- | ---------- |
| `INTF-SOURCE` | `owner/docs/behavior` | 11111111-1111-4111-8111-111111111111 |
MD
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok .*INTF-SOURCE'
}

# --- Multi-owner imports table (bead pg2-wr6lm.2, WS-0 item 4a) ----------------
# An imports table MAY declare owners in MORE THAN ONE set — a deployment set that
# implements one set's contracts AND follows the behavior-docs method declares rows
# into both. This script resolves ONE seam per invocation, so a row naming a
# DIFFERENT owner set MUST be skipped, not FAILed against the owner passed in.
# Before the `row_setpath` seam filter such a table could not pass in EITHER
# direction. WS-6's D5 column shift MUST preserve the filter — these tests are the
# guard.

multi_owner_table() {
  # Two rows: one into the owner set passed to the script (set-path suffix-matches
  # $OWNER), one into an unrelated set that this invocation is NOT resolving.
  cat > "$IMPL/interfaces.md" <<MD
# Interfaces — implementer

## External references

| Name | Owner set-path | Owner UUID |
| ---- | -------------- | ---------- |
| \`INTF-SOURCE\` | \`some-repo · $(basename "$OWNER")\` | 11111111-1111-4111-8111-111111111111 |
| \`INV-OTHER-1\` | \`other-repo · other/docs/behavior\` | 77777777-7777-4777-8777-777777777777 |
MD
}

@test "multi-owner (#6): a row naming ANOTHER owner set is skipped, not FAILed" {
  multi_owner_table
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok .*INTF-SOURCE'
  # The other seam's row MUST NOT be classified at all on this invocation.
  ! echo "$output" | grep -qE 'INV-OTHER-1'
}

@test "multi-owner (#6): the seam filter does NOT swallow a no-UUID row's WARN" {
  # Placement guard for the filter: a malformed row (no owner UUID, not marked
  # external) is owner-INDEPENDENT, so it MUST WARN even though its declared
  # set-path names a set this invocation is not resolving.
  cat > "$IMPL/interfaces.md" <<'MD'
# Interfaces — implementer

## External references

| Name | Owner set-path | Owner UUID |
| ---- | -------------- | ---------- |
| `INV-ELSEWHERE-1` | `other-repo · other/docs/behavior` |  |
MD
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  WARN .*no owner UUID and is not marked external'
}

# --- Shipped corpus is genuinely exercised (#5) --------------------------------
# The durable corpus/v3 fixtures ARE the agent-facing artifact; drive the real
# evaluator over each so the corpus cannot silently rot while the gate stays
# green. Each fixture MUST classify as its directory name documents.

corpus_v3_dir() {
  if [ -n "${CORPUS_V3_DIR:-}" ]; then
    printf '%s' "$CORPUS_V3_DIR"
  else
    printf '%s' "$BATS_TEST_DIRNAME/../claude-marketplace/behavior-docs-conformance/skills/behavior-docs-inter-conformance/corpus/v3"
  fi
}

@test "corpus v3 (#5): aligned fixture classifies as ok" {
  C=$(corpus_v3_dir); [ -d "$C/owner" ] || skip "corpus not found at $C"
  run resolve-imports "$C/owner" "$C/aligned"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok '
}

@test "corpus v3 (#5): stale-name fixture classifies as WARN (stale name)" {
  C=$(corpus_v3_dir); [ -d "$C/owner" ] || skip "corpus not found at $C"
  run resolve-imports "$C/owner" "$C/stale-name"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  WARN .*stale name'
}

@test "corpus v3 (#5): divergence fixture classifies as FAIL (non-zero exit)" {
  C=$(corpus_v3_dir); [ -d "$C/owner" ] || skip "corpus not found at $C"
  run resolve-imports "$C/owner" "$C/divergence"
  [ "$status" -ne 0 ]
  echo "$output" | grep -qE '^  FAIL .*divergence'
}

@test "corpus v3 (#5): external-contract/declared classifies as external" {
  C=$(corpus_v3_dir); [ -d "$C/owner" ] || skip "corpus not found at $C"
  run resolve-imports "$C/owner" "$C/external-contract/declared"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  external '
}

@test "corpus v3 (#5): external-contract/undeclared reports no external references" {
  C=$(corpus_v3_dir); [ -d "$C/owner" ] || skip "corpus not found at $C"
  run resolve-imports "$C/owner" "$C/external-contract/undeclared"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "declares no external references"
}
