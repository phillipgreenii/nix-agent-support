#!/usr/bin/env bats
# IMPL-evaluator mechanical coverage (bead pg2-wr6lm.4) — the third parallel
# evaluator: an implementation reconciled against ITS OWN behavior-docs set.
# Drives impl-traces.sh (on PATH as `impl-traces`) over inline fixtures asserting
# each classification, then over the durable corpus/impl fixtures.
#
# Every fixture is generated in BATS_TEST_TMPDIR: no test reads or writes a real
# behavior-docs set.

setup() {
  SET="$BATS_TEST_TMPDIR/set"
  IMPL="$BATS_TEST_TMPDIR/impl"
  mkdir -p "$SET" "$IMPL"
  cat >"$SET/invariants.md" <<'MD'
# Invariants
- **`INV-1`** — Every accepted request is answered exactly once.
- **`INV-2`** — A rejected request leaves no durable trace.
MD
  cat >"$SET/README.md" <<'MD'
# Set

## External references

| Name     | What it is             | Owner set-path                | Owner UUID                           |
| -------- | ---------------------- | ----------------------------- | ------------------------------------ |
| `INV-90` | the owner rule for ids | `other-repo · docs/behavior`  | 90909090-9090-4909-8909-909090909090 |
MD
}

@test "a citation resolving to a definition in the set classifies as ok" {
  cat >"$IMPL/handler.go" <<'GO'
// Answer satisfies INV-1: answered exactly once.
GO
  run impl-traces "$SET" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok +INV-1'
}

@test "a citation resolving only through the imports table classifies as external" {
  cat >"$IMPL/identity.go" <<'GO'
// The identity rule is owned elsewhere and declared in the imports table (INV-90).
GO
  run impl-traces "$SET" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  external +INV-90'
}

@test "a citation framed as removed classifies as historical, not a failure (INV-4)" {
  # A set leaves NO tombstone when an element is removed, so the code is the only
  # place the history can live. Failing this would force the code to delete its
  # rationale or the set to resurrect a dead ID.
  cat >"$IMPL/legacy.go" <<'GO'
// RETENTION (the former INV-77, resolved 2026-07-30): the rule was deleted.
GO
  run impl-traces "$SET" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  historical +INV-77'
}

@test "a citation resolving to nothing is a FAIL with a non-zero exit" {
  cat >"$IMPL/stale.go" <<'GO'
// Reject implements INV-404, which this set does not define or declare.
GO
  run impl-traces "$SET" "$IMPL"
  [ "$status" -ne 0 ]
  echo "$output" | grep -qE '^  FAIL +INV-404'
  echo "$output" | grep -q 'stale.go:1'
}

@test "one LIVE citation of a dead ID is a FAIL even when another is historical" {
  # The historical excuse is per-ID, not per-line: a comment explaining that
  # INV-77 was removed does not license a second site that still claims to
  # implement it.
  cat >"$IMPL/legacy.go" <<'GO'
// RETENTION (the former INV-77, resolved 2026-07-30): the rule was deleted.
GO
  cat >"$IMPL/live.go" <<'GO'
// Retain enforces INV-77 on every write.
GO
  run impl-traces "$SET" "$IMPL"
  [ "$status" -ne 0 ]
  echo "$output" | grep -qE '^  FAIL +INV-77'
  echo "$output" | grep -q 'live.go:1'
}

@test "a family citation resolves against the family it names" {
  # `INV-EVT-*` tokenizes to the bare family name, which is nobody's definition.
  # It is a legitimate way for code to cite a whole family and MUST resolve.
  cat >"$SET/families.md" <<'MD'
# Families
- **`INV-EVT-1`** — delivery is at-least-once.
- **`INV-EVT-2`** — listeners are idempotent.
MD
  cat >"$IMPL/queue.go" <<'GO'
// Delivery semantics are all from INV-EVT-*.
GO
  run impl-traces "$SET" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok +INV-EVT'
}

@test "the set directory is excluded from the implementation scan" {
  # The set usually lives INSIDE the impl root. If its own definitions counted as
  # implementation citations, coverage would always read 100% and the whole
  # coverage section would be a lie.
  mkdir -p "$IMPL/docs"
  cp "$SET/invariants.md" "$IMPL/docs/invariants.md"
  run impl-traces "$IMPL/docs" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'cites no behavior-docs ID at all'
}

@test "coverage is a NOTICE by default and a FAIL under --strict" {
  cat >"$IMPL/handler.go" <<'GO'
// Answer satisfies INV-1.
GO
  run impl-traces "$SET" "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'NOTICE .* cited nowhere'
  run impl-traces --strict "$SET" "$IMPL"
  [ "$status" -ne 0 ]
  echo "$output" | grep -q 'FAIL .* cited nowhere'
}

@test "a usage error exits 2, distinct from a finding" {
  run impl-traces "$SET"
  [ "$status" -eq 2 ]
  run impl-traces "$SET" "$BATS_TEST_TMPDIR/nope"
  [ "$status" -eq 2 ]
}

# --- Shipped corpus is genuinely exercised ------------------------------------
# The fallback branch is only taken on a direct `bats tests/…` run; under
# `nix flake check` CORPUS_IMPL_DIR is always exported. A stale path there is
# SILENT (every corpus test skips while the gate stays green), so the resolution
# is asserted below rather than skipped over.
corpus_impl_dir() {
  if [ -n "${CORPUS_IMPL_DIR:-}" ]; then
    printf '%s' "$CORPUS_IMPL_DIR"
  else
    printf '%s' "$BATS_TEST_DIRNAME/../claude-marketplace/behavior-docs-conformance/skills/behavior-docs-impl-conformance/corpus/impl"
  fi
}

@test "corpus (#5): the corpus directory RESOLVES (a stale path must FAIL, never skip)" {
  C=$(corpus_impl_dir)
  [ -d "$C" ] || {
    echo "corpus directory does not resolve: $C"
    echo "if the impl skill or its corpus dir was renamed, update corpus_impl_dir() and CORPUS_IMPL_DIR in flake.nix"
    false
  }
  [ -d "$C/set" ]
  [ -d "$C/dangling" ]
}

@test "corpus impl: live / external / historical fixtures all classify and pass" {
  C=$(corpus_impl_dir)
  [ -d "$C/set" ] || skip "corpus not found at $C"
  run impl-traces "$C/set" "$C/live"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok +INV-1'
  run impl-traces "$C/set" "$C/external"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  external +INV-90'
  run impl-traces "$C/set" "$C/historical"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  historical +INV-77'
}

@test "corpus impl: the dangling fixture FAILs with a non-zero exit" {
  C=$(corpus_impl_dir)
  [ -d "$C/set" ] || skip "corpus not found at $C"
  run impl-traces "$C/set" "$C/dangling"
  [ "$status" -ne 0 ]
  echo "$output" | grep -qE '^  FAIL +INV-404'
}

# --- Findings must be CANONICAL where they are written -------------------------
# See the matching block in tests/behavior-docs-intra-conformance.bats. `find`
# walks in FILESYSTEM order and a locale-collated sort reorders paths, so a
# multi-location citation finding serialized two ways for the same input. This
# pins path BYTE order and NUMERIC line order in one expected string.

canon_impl() {
  CSET="$BATS_TEST_TMPDIR/cset"
  CIMPL="$BATS_TEST_TMPDIR/cimpl"
  mkdir -p "$CSET" "$CIMPL"
  printf '# Invariants\n- **`INV-1`** — a rule that resolves.\n' >"$CSET/invariants.md"
  # `acl.go` vs `acl_test.go`: '.' (0x2E) sorts before '_' (0x5F) in BYTE order,
  # while a UTF-8 collation that ignores punctuation orders them the other way —
  # this is the exact pair that made the real gate flaky.
  printf '// cites INV-404\n' >"$CIMPL/acl.go"
  {
    echo "// filler"
    echo "// filler"
    echo "// nine cites INV-404"
    echo "// ten cites INV-404"
  } >"$CIMPL/acl_test.go"
  # Move the citations to lines 9 and 10 so numeric ordering is exercised.
  {
    for i in 1 2 3 4 5 6 7 8; do echo "// filler $i"; done
    echo "// nine cites INV-404"
    echo "// ten cites INV-404"
  } >"$CIMPL/acl_test.go"
}

@test "canonical findings: a multi-location citation sorts by path (BYTE order) then line NUMERICALLY" {
  canon_impl
  run impl-traces "$CSET" "$CIMPL"
  [ "$status" -ne 0 ]
  echo "$output" | grep -qF '(acl.go:1 acl_test.go:9 acl_test.go:10)' || {
    echo "location list is not canonical; got:"
    echo "$output" | grep 'INV-404'
    false
  }
}

@test "canonical findings: impl-traces output is byte-identical under any ambient locale" {
  canon_impl
  a="$BATS_TEST_TMPDIR/out.c"
  b="$BATS_TEST_TMPDIR/out.utf8"
  c="$BATS_TEST_TMPDIR/out.unset"
  env LC_ALL=C impl-traces "$CSET" "$CIMPL" >"$a" 2>&1 || true
  env LC_ALL=en_US.UTF-8 LC_COLLATE=en_US.UTF-8 impl-traces "$CSET" "$CIMPL" >"$b" 2>&1 || true
  env -u LC_ALL -u LC_COLLATE LANG=en_US.UTF-8 impl-traces "$CSET" "$CIMPL" >"$c" 2>&1 || true
  diff "$a" "$b"
  diff "$a" "$c"
}
