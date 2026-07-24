#!/usr/bin/env bats
# V2 intra-evaluator mechanical coverage (bead pg2-hvlyj.14, plan item 5.2).
# Drives the bundled self-checks.sh (on PATH as `self-checks`) over per-category
# FAIL/PASS fixtures and asserts the mechanical sections flag the FAIL and stay
# clean on the PASS. Fixtures are created inline so the test is self-contained
# (the corpus/ dir under the skill carries the same fixtures as the durable,
# agent-facing artifact, incl. the judgment-only categories).

setup() {
  SET="$BATS_TEST_TMPDIR/set"
  mkdir -p "$SET"
}

# section <name> extracts the named self-checks section body from the output.
run_selfchecks() { run self-checks "$SET"; }

@test "inline-status (#15): FAIL fixture is flagged" {
  cat > "$SET/invariants.md" <<'MD'
# Invariants
- **`INV-1`** — Delivery is at-least-once. _This is unmet by the current implementation._
MD
  run_selfchecks
  [ "$status" -eq 0 ]
  # The Inline status framing section must surface the offending phrase.
  echo "$output" | grep -qi "unmet by the current implementation"
}

@test "inline-status (#15): PASS fixture is clean" {
  cat > "$SET/invariants.md" <<'MD'
# Invariants
- **`INV-1`** — Delivery is at-least-once (durable across restart).
MD
  run_selfchecks
  [ "$status" -eq 0 ]
  # No inline-status phrase present anywhere in the output.
  ! echo "$output" | grep -qi "unmet by the current implementation"
}

@test "floor-leakage (INV-2/10): FAIL fixture is flagged" {
  cat > "$SET/invariants.md" <<'MD'
# Invariants
- **`INV-2`** — The dispatcher retries 3 times with a 250ms backoff (see `orchestrator.go:142`).
MD
  run_selfchecks
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "orchestrator.go:142"
}

@test "floor-leakage (INV-2/10): PASS fixture reports none obvious" {
  cat > "$SET/invariants.md" <<'MD'
# Invariants
- **`INV-2`** — The dispatcher retries a bounded number of times before surfacing the error.
MD
  run_selfchecks
  [ "$status" -eq 0 ]
  # The floor-leakage section prints "none obvious" when clean.
  echo "$output" | grep -q "none obvious"
}

@test "judgment fixtures (substitution/extent/seam-vocab) run cleanly through the mechanical layer" {
  # A well-formed set with a glossary + invariants processes without a mechanical
  # error (these categories are agent-judgment; the corpus/ fixtures carry them).
  cat > "$SET/glossary.md" <<'MD'
# Glossary
- **event** — a typed message routed to a handler.
MD
  cat > "$SET/invariants.md" <<'MD'
# Invariants
- **`INV-1`** — The core routes each event to a handler; an event is a typed message.
MD
  run_selfchecks
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "IDs present"
}

@test "self-checks fails on an empty set (no .md files)" {
  run self-checks "$BATS_TEST_TMPDIR/empty"
  mkdir -p "$BATS_TEST_TMPDIR/empty"
  run self-checks "$BATS_TEST_TMPDIR/empty"
  [ "$status" -ne 0 ]
}
