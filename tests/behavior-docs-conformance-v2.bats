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

@test "uuid-carriers (INV-3): dual identity — one ID bearing two carriers is FAILed" {
  # A UUID is minted ONCE, at the definition (INV-3). Here INTF-X is defined with
  # its minted carrier, but a REFERENCE wrongly carries a SECOND, different UUID.
  # The two VALUES differ, so the old value-only dup check (sort | uniq -d) stays
  # clean; the new per-ID check MUST flag the dual identity.
  cat > "$SET/interfaces.md" <<'MD'
# Interfaces
## `INTF-X` — a boundary <!-- uuid: 11111111-1111-4111-8111-111111111111 -->
MD
  cat > "$SET/journeys.md" <<'MD'
# Journeys
- **`INTF-X`** <!-- uuid: 22222222-2222-4222-8222-222222222222 --> — a reference that must NOT carry a UUID.
MD
  run_selfchecks
  [ "$status" -eq 0 ]
  echo "$output" | grep -qi "dual identity"
  echo "$output" | grep -q "INTF-X"
}

@test "uuid-carriers (INV-3): orphan carrier — a carrier line with no ID is FAILed" {
  # JOURNEY-Y carries its minted UUID on the heading; a stray SECOND carrier sits
  # on its own line with no ID token (an orphan). The two VALUES differ, so the
  # old value-only dup check stays clean; the new orphan check MUST flag it.
  cat > "$SET/journeys.md" <<'MD'
# Journeys
### `JOURNEY-Y` — a journey <!-- uuid: 33333333-3333-4333-8333-333333333333 -->

<!-- uuid: 44444444-4444-4444-8444-444444444444 -->

Body text.
MD
  run_selfchecks
  [ "$status" -eq 0 ]
  echo "$output" | grep -qi "orphan carrier"
  echo "$output" | grep -q "44444444-4444-4444-8444-444444444444"
}

@test "uuid-carriers (INV-3): well-formed set stays clean (one carrier per ID, no orphan)" {
  # Each ID bears exactly one carrier and references carry none: the new checks
  # MUST NOT fire, and the section still reports clean.
  cat > "$SET/interfaces.md" <<'MD'
# Interfaces
## `INTF-X` — a boundary <!-- uuid: 11111111-1111-4111-8111-111111111111 -->
MD
  cat > "$SET/journeys.md" <<'MD'
# Journeys
### `JOURNEY-Y` — a journey <!-- uuid: 33333333-3333-4333-8333-333333333333 -->

A reference to `INTF-X` that correctly carries NO uuid.
MD
  run_selfchecks
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "clean ("
  # Neither new defect fires on a well-formed set. A single trailing negation
  # (the test's last command) is functionally correct in Bats and stays at
  # SC2314 style-level, which the shellcheck hook's --severity=warning tolerates.
  ! echo "$output" | grep -qiE "dual identity|orphan carrier"
}

@test "self-checks fails on an empty set (no .md files)" {
  run self-checks "$BATS_TEST_TMPDIR/empty"
  mkdir -p "$BATS_TEST_TMPDIR/empty"
  run self-checks "$BATS_TEST_TMPDIR/empty"
  [ "$status" -ne 0 ]
}
