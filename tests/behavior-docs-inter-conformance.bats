#!/usr/bin/env bats
# INTER-evaluator mechanical coverage (bead pg2-hvlyj.15, plan item 5.3).
# Drives resolve-imports.sh (on PATH as `resolve-imports`) over a shared owner
# set and per-seam-check-type implementer fixtures, asserting the classification
# (ok / stale-name WARN / divergence FAIL / external) and exit code. Fixtures
# are inline so the test is self-contained; corpus/inter/ carries the same fixtures
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

@test "external-misclass (#1): no-UUID row with a HYPHENATED owner path -> FAIL, not external" {
  # Real repo paths contain hyphens (e.g. phillipgreenii-nix-agent-support). The
  # old external test ran `grep 'external|n/a|—|-'` on "$opath $uuidcell" and a
  # bare '-' matched ANY hyphenated path, misclassifying every no-UUID row as an
  # external contract and making the failure branch unreachable. The UUID cell here
  # is empty and the row is NOT marked external, so it MUST FAIL.
  cat > "$IMPL/interfaces.md" <<'MD'
# Interfaces — implementer

## External references

| Name | Owner set-path | Owner UUID |
| ---- | -------------- | ---------- |
| `INTF-ORPHAN` | `phillipgreenii-nix-agent-support · behavior-docs/docs/behavior` |  |
MD
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -ne 0 ]
  echo "$output" | grep -qE '^  FAIL .*no owner UUID and is not marked external'
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

@test "multi-owner (#6): the seam filter does NOT swallow a no-UUID row's failure" {
  # Placement guard for the filter: a malformed row (no owner UUID, not marked
  # external) is owner-INDEPENDENT, so it MUST FAIL even though its declared
  # set-path names a set this invocation is not resolving.
  cat > "$IMPL/interfaces.md" <<'MD'
# Interfaces — implementer

## External references

| Name | Owner set-path | Owner UUID |
| ---- | -------------- | ---------- |
| `INV-ELSEWHERE-1` | `other-repo · other/docs/behavior` |  |
MD
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -ne 0 ]
  echo "$output" | grep -qE '^  FAIL .*no owner UUID and is not marked external'
}

# --- D5's imports-table shape (bead pg2-0pjvu) ---------------------------------
# D5 inserts a `What it is` column as the SECOND visible column and turns the owner
# UUID cell into `[<uuid>](remote-url)`. The parser MUST read BOTH shapes, detected
# PER ROW, so the tables and the parser can land in either order and a table caught
# mid-migration still resolves. Before the fix every D5 row missed the fixed-index
# UUID cell, warned, and the script EXITED 0 — a gate reporting success over a table
# it resolved nothing in. The script PARSES the link and MUST NOT dereference it:
# these fixtures use `example.invalid` URLs and nothing may fetch them.

impl_table_d5() {
  # $1=name $2=what-it-is $3=uuid-cell (D5's 4-visible-column shape)
  cat > "$IMPL/interfaces.md" <<MD
# Interfaces — implementer

## External references

| Name | What it is | Owner set-path | Owner UUID |
| ---- | ---------- | -------------- | ---------- |
| $1 | $2 | \`owner/docs/behavior\` | $3 |
MD
}

@test "D5 shape: link-form UUID + matching name -> ok (not a silent no-UUID pass)" {
  impl_table_d5 '`INTF-SOURCE`' 'the owner event source' \
    '[11111111-1111-4111-8111-111111111111](https://example.invalid/owner/interfaces.md)'
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok .*INTF-SOURCE'
}

@test "D5 shape: link-form UUID + stale name -> WARN (stale name, identity intact)" {
  impl_table_d5 '`INTF-SRC`' 'renamed upstream' \
    '[11111111-1111-4111-8111-111111111111](https://example.invalid/owner/interfaces.md)'
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  WARN .*stale name'
}

@test "D5 shape: link-form UUID that resolves to nothing -> FAIL (non-zero exit)" {
  impl_table_d5 '`INTF-GHOST`' 'fabricated obligation' \
    '[99999999-9999-4999-8999-999999999999](https://example.invalid/owner/interfaces.md)'
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -ne 0 ]
  echo "$output" | grep -qE '^  FAIL .*divergence'
}

@test "D5 shape: identity is the LINK TEXT, never a UUID embedded in the remote-url" {
  # The url carries the OTHER owner element's UUID. Taking "the first UUID anywhere
  # in the cell" would still resolve, so this test only passes if the link TEXT is
  # what is read: INTF-SOURCE aligns, and INTF-HANDLER is never reported.
  impl_table_d5 '`INTF-SOURCE`' 'url carries another uuid' \
    '[11111111-1111-4111-8111-111111111111](https://example.invalid/x#22222222-2222-4222-8222-222222222222)'
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok .*INTF-SOURCE'
  ! echo "$output" | grep -q 'INTF-HANDLER'
}

@test "D5 shape: a link whose text is NOT a UUID -> FAIL, and the url's UUID is not harvested" {
  impl_table_d5 '`INTF-SOURCE`' 'malformed link' \
    '[see upstream](https://example.invalid/x/11111111-1111-4111-8111-111111111111)'
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -ne 0 ]
  echo "$output" | grep -qE '^  FAIL .*link whose text is not a UUID'
  ! echo "$output" | grep -qE '^  ok '
}

@test "D5 shape: a table MID-MIGRATION with an old-shape and a D5 row resolves BOTH" {
  # The shapes are detected per ROW, so neither the tables nor the parser has to land
  # first — which is why this row-level mixing must work, not merely a uniform table.
  cat > "$IMPL/interfaces.md" <<'MD'
# Interfaces — implementer

## External references

| Name | Owner set-path | Owner UUID |
| ---- | -------------- | ---------- |
| `INTF-SOURCE` | `owner/docs/behavior` | 11111111-1111-4111-8111-111111111111 |
| `INTF-HANDLER` | migrated to D5 | `owner/docs/behavior` | [22222222-2222-4222-8222-222222222222](https://example.invalid/x) |
| `git` | `(external)` | `(external)` |
MD
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok .*INTF-SOURCE'
  echo "$output" | grep -qE '^  ok .*INTF-HANDLER'
  echo "$output" | grep -qE '^  external .*git'
}

@test "D5 shape: the multi-owner seam filter still selects on the SHIFTED set-path cell" {
  # The owner-seam filter reads the set-path cell, which D5 shifts one column right.
  # If the filter read a fixed index it would test `What it is` and stop filtering,
  # so the other seam's row would FAIL against the wrong owner.
  cat > "$IMPL/interfaces.md" <<MD
# Interfaces — implementer

## External references

| Name | What it is | Owner set-path | Owner UUID |
| ---- | ---------- | -------------- | ---------- |
| \`INTF-SOURCE\` | this seam | \`some-repo · $(basename "$OWNER")\` | [11111111-1111-4111-8111-111111111111](https://example.invalid/x) |
| \`INV-OTHER-1\` | another owner set | \`other-repo · other/docs/behavior\` | [77777777-7777-4777-8777-777777777777](https://example.invalid/y) |
MD
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok .*INTF-SOURCE'
  ! echo "$output" | grep -qE 'INV-OTHER-1'
}

# --- Citable id families (bead pg2-rlu3m) --------------------------------------
# `IDRE` enumerates the families an imports row may cite, and `owner_name_for_uuid`
# extracts the owner's current name with that same regex. A family missing from it did
# NOT degrade gracefully: the extracting `grep -oE` matched nothing, exited 1, and
# `pipefail` + `set -e` killed the script MID-LOOP — exit 1 with NO row output at all,
# so nothing was reported about ANY row, not even the ones already resolved. `DEC-` and
# `IMPL-` (the decision-doc entry families, citable per `GOAL-5`) were the live case.
#
# The fix MUST NOT be a bare `|| true` on that grep: the function would return empty,
# which is indistinguishable from "this UUID resolves to no owner definition", so the
# caller would report a FALSE `divergence` FAIL on a row whose identity resolved. The
# third test below is the guard against that regression, and it asserts BOTH that the
# message names the offending id and that the surrounding rows are still reported.

# owner_decisions writes an owner set of decision-doc entries plus one id of a family
# no area defines, so the admitted and unadmitted cases share one owner.
owner_decisions() {
  cat > "$OWNER/decisions.md" <<'MD'
# Decisions — owner
### `DEC-SEAM-1` — the imports link points toward the more public side <!-- uuid: 33333333-3333-4333-8333-333333333333 -->
### `IMPL-1` — governance authority, captured but not settled <!-- uuid: 44444444-4444-4444-8444-444444444444 -->
### `POLICY-3` — a typed id whose family no area defines <!-- uuid: 55555555-5555-4555-8555-555555555555 -->
MD
}

@test "id-family: a DEC- decision entry cited in an imports row resolves (was a mid-loop crash)" {
  owner_decisions
  impl_table '`DEC-SEAM-1`' '33333333-3333-4333-8333-333333333333'
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok .*DEC-SEAM-1'
}

@test "id-family: an IMPL- captured entry cited in an imports row resolves" {
  owner_decisions
  impl_table '`IMPL-1`' '44444444-4444-4444-8444-444444444444'
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok .*IMPL-1'
}

@test "id-family: an UNRECOGNIZED family is a per-row FAIL naming the id — never a crash, never a false divergence" {
  owner_decisions
  # The unrecognized row sits BETWEEN two resolvable ones on purpose: a mid-loop abort
  # loses the row after it (and, buffered, the one before), so asserting all three are
  # reported is what distinguishes a per-row finding from the crash.
  cat > "$IMPL/interfaces.md" <<'MD'
# Interfaces — implementer

## External references

| Name | Owner set-path | Owner UUID |
| ---- | -------------- | ---------- |
| `DEC-SEAM-1` | `owner/docs/decisions` | 33333333-3333-4333-8333-333333333333 |
| `POLICY-3` | `owner/docs/decisions` | 55555555-5555-4555-8555-555555555555 |
| `IMPL-1` | `owner/docs/decisions` | 44444444-4444-4444-8444-444444444444 |
MD
  run resolve-imports "$OWNER" "$IMPL"
  [ "$status" -ne 0 ]
  # A per-row FAIL that NAMES the offending id and says what is wrong with it.
  echo "$output" | grep -qE '^  FAIL .*UNRECOGNIZED.*POLICY-3'
  # NOT a mid-loop abort: every other row is still classified.
  echo "$output" | grep -qE '^  ok .*DEC-SEAM-1'
  echo "$output" | grep -qE '^  ok .*IMPL-1'
  # NOT a false divergence: the UUID resolved, so NO row may be reported as resolving
  # to no owner definition — that is the `|| true` regression this guards against.
  # One TRAILING negation, per the suite's convention: SC2314 is an ERROR for any
  # earlier one, and `run !` cannot wrap a pipeline.
  ! echo "$output" | grep -q 'divergence'
}

# --- Shipped corpus is genuinely exercised (#5) --------------------------------
# The durable corpus/inter fixtures ARE the agent-facing artifact; drive the real
# evaluator over each so the corpus cannot silently rot while the gate stays
# green. Each fixture MUST classify as its directory name documents.

# The fallback branch is only taken on a direct `bats tests/…` run — under
# `nix flake check` CORPUS_INTER_DIR is always exported. A stale path there is
# therefore SILENT (every corpus test `skip`s while the gate stays green), so the
# resolution is asserted below rather than skipped over.
corpus_inter_dir() {
  if [ -n "${CORPUS_INTER_DIR:-}" ]; then
    printf '%s' "$CORPUS_INTER_DIR"
  else
    printf '%s' "$BATS_TEST_DIRNAME/../claude-marketplace/behavior-docs-conformance/skills/behavior-docs-inter-conformance/corpus/inter"
  fi
}

@test "corpus (#5): the corpus directory RESOLVES (a stale path must FAIL, never skip)" {
  C=$(corpus_inter_dir)
  [ -d "$C" ] || {
    echo "corpus directory does not resolve: $C"
    echo "if the inter skill or its corpus dir was renamed, update corpus_inter_dir() and CORPUS_INTER_DIR in flake.nix"
    false
  }
  [ -d "$C/owner" ]
  [ -d "$C/divergence" ]
}

@test "corpus inter (#5): aligned fixture classifies as ok" {
  C=$(corpus_inter_dir); [ -d "$C/owner" ] || skip "corpus not found at $C"
  run resolve-imports "$C/owner" "$C/aligned"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok '
}

@test "corpus inter (#5): stale-name fixture classifies as WARN (stale name)" {
  C=$(corpus_inter_dir); [ -d "$C/owner" ] || skip "corpus not found at $C"
  run resolve-imports "$C/owner" "$C/stale-name"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  WARN .*stale name'
}

@test "corpus inter (#5): divergence fixture classifies as FAIL (non-zero exit)" {
  C=$(corpus_inter_dir); [ -d "$C/owner" ] || skip "corpus not found at $C"
  run resolve-imports "$C/owner" "$C/divergence"
  [ "$status" -ne 0 ]
  echo "$output" | grep -qE '^  FAIL .*divergence'
}

@test "corpus inter (#5): external-contract/declared classifies as external" {
  C=$(corpus_inter_dir); [ -d "$C/owner" ] || skip "corpus not found at $C"
  run resolve-imports "$C/owner" "$C/external-contract/declared"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  external '
}

@test "corpus inter (#5): external-contract/undeclared reports no external references" {
  C=$(corpus_inter_dir); [ -d "$C/owner" ] || skip "corpus not found at $C"
  run resolve-imports "$C/owner" "$C/external-contract/undeclared"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "declares no external references"
}

# --- BIDIRECTIONAL imports reconciler (bead pg2-wr6lm.4) -----------------------
# resolve-imports.sh walks the rows that WERE declared, so it is structurally
# blind to a citation with no row (and to a row with no citation). Every fixture
# is generated in BATS_TEST_TMPDIR.

rec_owner() {
  RO="$BATS_TEST_TMPDIR/rowner"
  mkdir -p "$RO"
  cat >"$RO/interfaces.md" <<'MD'
# Interfaces — owner
- **`INTF-SOURCE`** — typed events in. <!-- uuid: 11111111-1111-4111-8111-111111111111 -->
- **`INTF-HANDLER`** — events out. <!-- uuid: 22222222-2222-4222-8222-222222222222 -->
MD
}

@test "reconcile: an owner element cited with no imports row is cited-but-undeclared (FAIL)" {
  rec_owner
  RI="$BATS_TEST_TMPDIR/rimpl"
  mkdir -p "$RI"
  cat >"$RI/interfaces.md" <<'MD'
# Interfaces — implementer
- **`INTF-ZR-SOURCE`** — implements `INTF-SOURCE`.
- **`INTF-ZR-HANDLER`** — implements `INTF-HANDLER`, declared nowhere.

## External references

| Name          | Owner set-path | Owner UUID                           |
| ------------- | -------------- | ------------------------------------ |
| `INTF-SOURCE` | `rowner`       | 11111111-1111-4111-8111-111111111111 |
MD
  run reconcile-imports "$RO" "$RI"
  [ "$status" -ne 0 ]
  echo "$output" | grep -q 'FAIL cited-but-undeclared: INTF-HANDLER'
  # And the one-directional check reports this same fixture as clean, which is the
  # whole reason this script exists.
  run resolve-imports "$RO" "$RI"
  [ "$status" -eq 0 ]
}

@test "reconcile: a row for an element cited nowhere is declared-but-uncited (WARN, FAIL under --strict)" {
  rec_owner
  RI="$BATS_TEST_TMPDIR/rimpl2"
  mkdir -p "$RI"
  cat >"$RI/interfaces.md" <<'MD'
# Interfaces — implementer
- **`INTF-ZR-SOURCE`** — implements `INTF-SOURCE`.

## External references

| Name           | Owner set-path | Owner UUID                           |
| -------------- | -------------- | ------------------------------------ |
| `INTF-SOURCE`  | `rowner`       | 11111111-1111-4111-8111-111111111111 |
| `INTF-HANDLER` | `rowner`       | 22222222-2222-4222-8222-222222222222 |
MD
  run reconcile-imports "$RO" "$RI"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'WARN declared-but-uncited: INTF-HANDLER'
  run reconcile-imports --strict "$RO" "$RI"
  [ "$status" -ne 0 ]
  echo "$output" | grep -q 'FAIL declared-but-uncited: INTF-HANDLER'
}

@test "reconcile: the imports table itself does not count as a citation" {
  # If it did, every row would satisfy itself and the declared-but-uncited
  # direction would be unreachable — the check would always pass.
  rec_owner
  RI="$BATS_TEST_TMPDIR/rimpl3"
  mkdir -p "$RI"
  cat >"$RI/interfaces.md" <<'MD'
# Interfaces — implementer
- **`INTF-ZR-SOURCE`** — a boundary this set owns.

## External references

| Name          | Owner set-path | Owner UUID                           |
| ------------- | -------------- | ------------------------------------ |
| `INTF-SOURCE` | `rowner`       | 11111111-1111-4111-8111-111111111111 |
MD
  run reconcile-imports "$RO" "$RI"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'WARN declared-but-uncited: INTF-SOURCE'
}

@test "reconcile: a row naming a DIFFERENT owner set is skipped, not judged here" {
  rec_owner
  RI="$BATS_TEST_TMPDIR/rimpl4"
  mkdir -p "$RI"
  cat >"$RI/interfaces.md" <<'MD'
# Interfaces — implementer
- **`INTF-ZR-SOURCE`** — implements `INTF-SOURCE`.

## External references

| Name          | Owner set-path                | Owner UUID                           |
| ------------- | ----------------------------- | ------------------------------------ |
| `INTF-SOURCE` | `some-repo · rowner`          | 11111111-1111-4111-8111-111111111111 |
| `INV-OTHER-1` | `other-repo · other/behavior` | 33333333-3333-4333-8333-333333333333 |
MD
  run reconcile-imports "$RO" "$RI"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -q 'INV-OTHER-1'
}

@test "reconcile: an element the implementer DEFINES itself is not cited-but-undeclared" {
  # A name the implementer owns is not a citation of the owner, even when the
  # owner happens to define the same name.
  RO2="$BATS_TEST_TMPDIR/rowner2"
  RI="$BATS_TEST_TMPDIR/rimpl5"
  mkdir -p "$RO2" "$RI"
  cat >"$RO2/interfaces.md" <<'MD'
# Interfaces — owner
- **`INTF-SOURCE`** — typed events in.
MD
  cat >"$RI/interfaces.md" <<'MD'
# Interfaces — implementer
- **`INTF-SOURCE`** — this set defines its OWN element with that name.
MD
  run reconcile-imports "$RO2" "$RI"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'clean (every cited owner element is declared)'
}

@test "reconcile: a usage error exits 2, distinct from a finding" {
  rec_owner
  run reconcile-imports "$RO"
  [ "$status" -eq 2 ]
  run reconcile-imports "$RO" "$BATS_TEST_TMPDIR/nope"
  [ "$status" -eq 2 ]
}

@test "corpus inter: undeclared-citation FAILs the reconciler but passes resolve-imports" {
  C=$(corpus_inter_dir)
  [ -d "$C/undeclared-citation" ] || skip "corpus not found at $C"
  run reconcile-imports "$C/owner" "$C/undeclared-citation"
  [ "$status" -ne 0 ]
  echo "$output" | grep -q 'FAIL cited-but-undeclared: INTF-HANDLER'
  run resolve-imports "$C/owner" "$C/undeclared-citation"
  [ "$status" -eq 0 ]
}

@test "corpus inter: uncited-row WARNs, and the aligned fixture reconciles clean" {
  C=$(corpus_inter_dir)
  [ -d "$C/uncited-row" ] || skip "corpus not found at $C"
  run reconcile-imports "$C/owner" "$C/uncited-row"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'WARN declared-but-uncited: INTF-HANDLER'
  run reconcile-imports "$C/owner" "$C/aligned"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'INTER reconciliation: OK'
}

# --- Cross-set NAME collisions (bead pg2-wr6lm.4) ------------------------------
# Matching is by UUID precisely so a RENAME cannot break a seam, which also means
# no other check in this family ever compares names across sets.

@test "collisions: the same ID name DEFINED in two sets is a FAIL (class 1)" {
  A="$BATS_TEST_TMPDIR/ca"
  Bd="$BATS_TEST_TMPDIR/cb"
  mkdir -p "$A" "$Bd"
  printf '# A\n- **`INV-1`** — a rule A owns.\n' >"$A/invariants.md"
  printf '# B\n- **`INV-1`** — a DIFFERENT rule B owns.\n' >"$Bd/invariants.md"
  run name-collisions "$A" "$Bd"
  [ "$status" -ne 0 ]
  echo "$output" | grep -q 'FAIL ambiguous ID name: INV-1'
}

@test "collisions: namespaced names in two sets are clean (class 1)" {
  A="$BATS_TEST_TMPDIR/ca2"
  Bd="$BATS_TEST_TMPDIR/cb2"
  mkdir -p "$A" "$Bd"
  printf '# A\n- **`INV-DISP-1`** — a rule A owns.\n' >"$A/invariants.md"
  printf '# B\n- **`INV-EVT-1`** — a rule B owns.\n' >"$Bd/invariants.md"
  run name-collisions "$A" "$Bd"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'clean (no ID name is defined in two sets)'
}

@test "collisions: an affordance asserted against a cited set that never uses it is a class-2 candidate" {
  # The shipped defect: the implementer asserts `owner-emit <json>` is an owner
  # subcommand; the owner calls it `source-push` and has no `owner-emit`. The
  # CITATION is valid — the UUID resolves, the name matches — so nothing that
  # matches by UUID can see this.
  A="$BATS_TEST_TMPDIR/ca3"
  Bd="$BATS_TEST_TMPDIR/cb3"
  mkdir -p "$A" "$Bd"
  cat >"$Bd/interfaces.md" <<'MD'
# Owner
- **`INTF-CLI`** — operator commands. The push affordance is `source-push <json>`.
MD
  cat >"$A/interfaces.md" <<'MD'
# Implementer
- **`INTF-ZR-CLI`** — **The push path:** `owner-emit <json>` is an `INTF-CLI` subcommand.
MD
  run name-collisions "$A" "$Bd"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'CANDIDATE .* asserts `owner-emit` beside its citation of INTF-CLI'
  run name-collisions --strict "$A" "$Bd"
  [ "$status" -ne 0 ]
}

@test "collisions: the assertion and the citation may be on DIFFERENT wrapped lines" {
  # Prose wraps. A line-scoped check sees an affordance with no citation beside it
  # and a citation with no affordance, and reports nothing; the unit is the block.
  A="$BATS_TEST_TMPDIR/ca4"
  Bd="$BATS_TEST_TMPDIR/cb4"
  mkdir -p "$A" "$Bd"
  cat >"$Bd/interfaces.md" <<'MD'
# Owner
- **`INTF-CLI`** — operator commands. The push affordance is `source-push <json>`.
MD
  cat >"$A/interfaces.md" <<'MD'
# Implementer
- **`INTF-ZR-CLI`** — the push path is `owner-emit <json>`, which submits one
  operator-supplied event through the owner's `INTF-CLI` boundary.
MD
  run name-collisions "$A" "$Bd"
  echo "$output" | grep -q 'CANDIDATE .* asserts `owner-emit`'
}

@test "collisions: an affordance name BOTH sets use is not a candidate" {
  A="$BATS_TEST_TMPDIR/ca5"
  Bd="$BATS_TEST_TMPDIR/cb5"
  mkdir -p "$A" "$Bd"
  cat >"$Bd/interfaces.md" <<'MD'
# Owner
- **`INTF-CLI`** — operator commands, including `push-inject <json>`.
MD
  cat >"$A/interfaces.md" <<'MD'
# Implementer
- **`INTF-ZR-CLI`** — binds to the owner's `push-inject` affordance (`INTF-CLI`).
MD
  run name-collisions --strict "$A" "$Bd"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q '  none'
}

@test "collisions: the <repo> half of a qualified citation is not an affordance name" {
  # Otherwise every correctly written `<repo> · <set-path> · <ID>` citation reports
  # a candidate, which is the single loudest possible false positive.
  A="$BATS_TEST_TMPDIR/ca6"
  Bd="$BATS_TEST_TMPDIR/cb6"
  mkdir -p "$A" "$Bd"
  printf '# Owner\n- **`INV-11`** — a rule the owner owns.\n' >"$Bd/invariants.md"
  cat >"$A/invariants.md" <<'MD'
# Implementer
- **`INV-ZR-1`** — satisfies `some-long-repo-name · docs/behavior · INV-11`.
MD
  run name-collisions --strict "$A" "$Bd"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -q 'some-long-repo-name'
}

@test "collisions: fewer than two sets is a usage error (exit 2)" {
  A="$BATS_TEST_TMPDIR/ca7"
  mkdir -p "$A"
  printf '# A\n- **`INV-1`** — a rule.\n' >"$A/invariants.md"
  run name-collisions "$A"
  [ "$status" -eq 2 ]
}

@test "corpus inter: the name-collision fixture reports its candidate" {
  C=$(corpus_inter_dir)
  [ -d "$C/name-collision" ] || skip "corpus not found at $C"
  run name-collisions "$C/owner" "$C/name-collision"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'CANDIDATE .* asserts `owner-emit` beside its citation of INTF-SOURCE'
}

# --- Findings must be CANONICAL where they are written -------------------------
# See the matching block in tests/behavior-docs-intra-conformance.bats.

@test "canonical findings: reconcile-imports sorts a multi-location finding by path then line NUMERICALLY" {
  rec_owner
  RI="$BATS_TEST_TMPDIR/rcanon"
  mkdir -p "$RI"
  printf '# Readme\nfiller\ncites `INTF-SOURCE`\n' >"$RI/README.md"
  {
    for i in 1 2 3 4 5 6 7 8; do echo "filler $i"; done
    echo "nine cites \`INTF-SOURCE\`"
    echo "ten cites \`INTF-SOURCE\`"
  } >"$RI/interfaces.md"
  run reconcile-imports "$RO" "$RI"
  [ "$status" -ne 0 ]
  echo "$output" | grep -qF 'INTF-SOURCE (README.md:3 interfaces.md:9 interfaces.md:10)' || {
    echo "location list is not canonical; got:"
    echo "$output" | grep 'INTF-SOURCE'
    false
  }
}

@test "canonical findings: name-collisions dedupes and sorts the cited-ID list" {
  # A block citing one owner ID twice used to emit it twice, in text order
  # ("INTF-CLI INTF-CLI INV-EVT-2"), so the finding string was not canonical.
  A="$BATS_TEST_TMPDIR/cdedupe-a"
  Bd="$BATS_TEST_TMPDIR/cdedupe-b"
  mkdir -p "$A" "$Bd"
  cat >"$Bd/interfaces.md" <<'MD'
# Owner
- **`INTF-CLI`** — operator commands; the push affordance is `source-push <json>`.
- **`INV-EVT-2`** — listeners are idempotent.
MD
  # INTF-CLI is cited TWICE and appears BEFORE INV-EVT-2 in the text, so an
  # appearance-order list would read "INTF-CLI INTF-CLI INV-EVT-2".
  cat >"$A/interfaces.md" <<'MD'
# Implementer
- **`INTF-ZR-CLI`** — the push path is `owner-emit <json>`, an `INTF-CLI`
  subcommand; it is deduped per `INV-EVT-2` like any other `INTF-CLI` event.
MD
  run name-collisions "$A" "$Bd"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qF 'citation of INTF-CLI INV-EVT-2 ' || {
    echo "cited-ID list is not deduped/sorted; got:"
    echo "$output" | grep CANDIDATE
    false
  }
}

@test "canonical findings: inter scripts are byte-identical under any ambient locale" {
  C=$(corpus_inter_dir)
  [ -d "$C/owner" ] || skip "corpus not found at $C"
  for pair in "resolve-imports $C/owner $C/aligned" "reconcile-imports $C/owner $C/undeclared-citation" "name-collisions $C/owner $C/name-collision"; do
    # shellcheck disable=SC2086  # deliberate word splitting: the tuple is the argv
    set -- $pair
    a="$BATS_TEST_TMPDIR/$1.c"
    b="$BATS_TEST_TMPDIR/$1.utf8"
    env LC_ALL=C "$@" >"$a" 2>&1 || true
    env LC_ALL=en_US.UTF-8 LC_COLLATE=en_US.UTF-8 "$@" >"$b" 2>&1 || true
    diff "$a" "$b"
  done
}
