#!/usr/bin/env bats
# bats file_tags=type:integration
# INTRA-evaluator mechanical coverage (bead pg2-hvlyj.14, plan item 5.2).
# Drives the bundled self-checks.sh (on PATH as `self-checks`) over per-category
# FAIL/PASS fixtures and asserts the mechanical sections flag the FAIL and stay
# clean on the PASS. Fixtures are created inline so the test is self-contained
# (the corpus/ dir under the skill carries the same fixtures as the durable,
# agent-facing artifact, incl. the judgment-only categories).

# `run !` is used below (SC2314: a bare `!` does not fail a Bats test), and that
# form needs Bats >= 1.5.0. Declaring it makes the requirement explicit and
# silences the BW02 warning the bare usage emits.
bats_require_minimum_version 1.5.0

if [[ -n ${GFH_LIB:-} ]]; then
  # shellcheck disable=SC1090
  source "$GFH_LIB"
else
  # shellcheck disable=SC1090
  source "$(cd "$BATS_TEST_DIRNAME/support" && pwd)/git-fixture-harness.bash"
fi

setup() {
  SET="$BATS_TEST_TMPDIR/set"
  mkdir -p "$SET"
}

# _gfh_protect_fixture <suite-label>: hermetic-by-construction scrub
# (pg2-31f13/pg2-gucfd) for the capture-prefix-snapshots fixtures below,
# which git-init real repos directly under $BATS_TEST_TMPDIR -- exactly the
# pattern pg2-67h4y shows losing to a leaked GIT_DIR-family env var from a
# linked-worktree commit-hook environment. Uses gfh_reset_env + gfh_init_repo
# rather than the full gfh_setup (which creates its OWN separate repo these
# tests do not need -- they build their own fixture layout under $repo).
_gfh_protect_fixture() {
  gfh_reset_env
  GIT_CEILING_DIRECTORIES="$(cd "$BATS_TEST_TMPDIR" && pwd -P)"
  export GIT_CEILING_DIRECTORIES
  GIT_CONFIG_SYSTEM=/dev/null
  export GIT_CONFIG_SYSTEM
  HOME="$BATS_TEST_TMPDIR/home-$1"
  mkdir -p "$HOME"
  export HOME
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

@test "realization-register (INV-23): a gap recorded inside an OQ- is FAILed" {
  # The real-world shape the rule was written against: an open question used as an inline
  # realization register. An OQ- says the INTENT is unsettled; a gap says the intent is settled and
  # the build has not caught up, so this puts implementation-status prose inside an element
  # definition and mints a citable identity for a record that must later be deleted.
  cat > "$SET/journeys.md" <<'MD'
# Open questions
- **`OQ-1` — Realization tracked externally.** A **realization gap** — intended behavior the
  implementation has not yet built — is tracked against the cited ID: `INV-1`.
MD
  run_selfchecks
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'FAIL realization gap recorded inside an open question'
  echo "$output" | grep -q 'OQ-1'
}

@test "realization-register (INV-23): a missing register section is an ADVISORY, never a FAIL" {
  # Presence CANNOT be a FAIL here: tests/behavior-docs-real-corpus.sh treats any self-checks FAIL
  # as a hard failure with no baseline escape, so failing would red the build for every set not yet
  # retrofitted. This test pins the strength, not just the wording.
  cat > "$SET/invariants.md" <<'MD'
# Invariants
- **`INV-1`** — Every accepted event is delivered at least once.
MD
  run_selfchecks
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "ADVISORY: no '## Realization gaps' section"
  ! echo "$output" | grep -qE '^[[:space:]]*FAIL[[:space:]]'
}

@test "realization-register (INV-23): a present register with no OQ- misuse is clean" {
  cat > "$SET/README.md" <<'MD'
# Set

## Realization gaps

| Element | Intended                     | Where the implementation stands |
| ------- | ---------------------------- | ------------------------------- |
| `INV-1` | delivery is at-least-once    | delivery is best-effort         |
MD
  cat > "$SET/journeys.md" <<'MD'
# Open questions
- **`OQ-1` — Which durability guarantee delivery owes.** The intent is undecided, which is what
  makes this an open question rather than a register row.
MD
  run_selfchecks
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'register section present'
  echo "$output" | grep -q 'no gap recorded inside an OQ-'
}

@test "realization-register (INV-23): more than one register section is FAILed" {
  cat > "$SET/README.md" <<'MD'
# Set

## Realization gaps

Nothing to record.
MD
  cat > "$SET/invariants.md" <<'MD'
# Invariants

## Realization gaps

A second register: a set carries exactly one.
MD
  run_selfchecks
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'FAIL 2 register sections'
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

# --- Citable id families (bead pg2-rlu3m) --------------------------------------
# `IDRE` enumerates the typed-name families this script knows, and a family missing from
# it is a FALSE FAILURE rather than a blind spot: the definition line matches no ID, so
# its UUID carrier reads as an ORPHAN (a carrier with no ID on its line) and the UUID
# section FAILs on a conformant set. That is exactly why `USECASE` was added; `DEC-` and
# `IMPL-` — the decision-doc entry families every `docs/decisions/README.md` defines —
# reproduce it on any decisions area run through this script. This list MUST stay
# identical to the inter evaluator's `resolve-imports.sh` `IDRE`.

@test "id-family: a DEC- decision-entry carrier is a definition, not an orphan" {
  cat > "$SET/decisions.md" <<'MD'
# Decisions
### `DEC-SEAM-1` — the imports link points toward the more public side <!-- uuid: 33333333-3333-4333-8333-333333333333 -->
MD
  run_selfchecks
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "DEC-SEAM-1"
  # Trailing negation, per the suite's convention (SC2314 is an error for an earlier one).
  ! echo "$output" | grep -qi "orphan carrier"
}

@test "id-family: an IMPL- captured-entry carrier is a definition, not an orphan" {
  cat > "$SET/decisions.md" <<'MD'
# Decisions
### `IMPL-1` — governance authority, captured but not settled <!-- uuid: 44444444-4444-4444-8444-444444444444 -->
MD
  run_selfchecks
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "IMPL-1"
  # Trailing negation, per the suite's convention (SC2314 is an error for an earlier one).
  ! echo "$output" | grep -qi "orphan carrier"
}

@test "id-family: an UNRECOGNIZED family is still FAILed as an orphan carrier, naming the UUID" {
  # Widening IDRE MUST NOT become a catch-all. A typed id whose family no area defines
  # stays a loud finding here, so an unlearned family is reported rather than admitted.
  cat > "$SET/decisions.md" <<'MD'
# Decisions
### `DEC-SEAM-1` — a family that IS defined <!-- uuid: 33333333-3333-4333-8333-333333333333 -->
### `POLICY-3` — a typed id whose family no area defines <!-- uuid: 55555555-5555-4555-8555-555555555555 -->
MD
  run_selfchecks
  [ "$status" -eq 0 ]
  echo "$output" | grep -qi "orphan carrier"
  echo "$output" | grep -q "55555555-5555-4555-8555-555555555555"
  # The admitted family in the same set is NOT dragged into the finding.
  ! echo "$output" | grep -qi "orphan carrier.*33333333-3333-4333-8333-333333333333"
}

@test "self-checks fails on a MISSING dir AND on an empty set (no .md files)" {
  # #6: both runs are asserted — the missing-dir run (cd fails) and the
  # exists-but-empty run (no *.md) must each exit non-zero.
  run self-checks "$BATS_TEST_TMPDIR/empty"
  [ "$status" -ne 0 ]
  mkdir -p "$BATS_TEST_TMPDIR/empty"
  run self-checks "$BATS_TEST_TMPDIR/empty"
  [ "$status" -ne 0 ]
}

# --- Targeted hardening (bead pg2-vybrv) — each FAILs before its fix -----------

@test "inline-status (#7): contract prose 'interface to be implemented by an implementer' is NOT flagged" {
  # INV-8/INV-18 contract prose legitimately says an interface is "to be
  # implemented by an implementer". The old inline-status regex matched the bare
  # 'to be implemented' substring — a latent false positive. It must stay clean.
  cat > "$SET/invariants.md" <<'MD'
# Invariants
- **`INV-8`** — A cross-product interaction defines the interface to be implemented by an implementer, citing the owner's contract.
MD
  run_selfchecks
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -qi "to be implemented by an implementer"
}

@test "inline-status (#7): genuine status framing 'yet to be implemented' is still flagged" {
  cat > "$SET/invariants.md" <<'MD'
# Invariants
- **`INV-1`** — The core delivers each accepted event; this rule is yet to be implemented.
MD
  run_selfchecks
  [ "$status" -eq 0 ]
  echo "$output" | grep -qi "yet to be implemented"
}

@test "heuristic (#8): a glossary with no bold headwords emits a 'no headwords' NOTICE (not a vacuous pass)" {
  cat > "$SET/glossary.md" <<'MD'
# Glossary
Terms are described in prose here, without bold-bullet headwords.
- event: a typed message routed to a handler (plain bullet, no bold markup).
MD
  run_selfchecks
  [ "$status" -eq 0 ]
  echo "$output" | grep -qi "no bold headwords"
}

# --- Shipped corpus is genuinely exercised (#5) --------------------------------
# Drive the real self-checks.sh over the durable corpus/intra fixtures so the corpus
# cannot rot while the gate stays green. The two MECHANICAL categories
# (inline-status, floor-leakage) are asserted fail->flagged / pass->clean; every
# fixture (incl. the judgment-only categories) is asserted to run to completion.

# The fallback branch below is the SILENT-BREAK path: it is taken only when
# CORPUS_INTRA_DIR is unset, i.e. on a direct `bats tests/…` run, never under
# `nix flake check` (which always exports it). A stale path there therefore left
# the flake gate green while every corpus test `skip`ped — a gate reporting
# success having checked nothing. Two guards now make that impossible:
#   1. the path is asserted to EXIST rather than skipped over (see
#      `corpus (#5): the corpus directory RESOLVES` below), so a stale fallback
#      FAILS loudly instead of skipping; and
#   2. the path is built from ONE `readlink -f`-able string, kept beside the
#      skill it names, so a rename shows up here in a grep for the skill name.
corpus_intra_dir() {
  if [ -n "${CORPUS_INTRA_DIR:-}" ]; then
    printf '%s' "$CORPUS_INTRA_DIR"
  else
    printf '%s' "$BATS_TEST_DIRNAME/../claude-marketplace/behavior-docs-conformance/skills/behavior-docs-intra-conformance/corpus/intra"
  fi
}

@test "corpus (#5): the corpus directory RESOLVES (a stale path must FAIL, never skip)" {
  # This is the guard for the silent-break path: every other corpus test below
  # `skip`s when the directory is missing, which is correct for an intentionally
  # trimmed checkout but is exactly how a RENAME went unnoticed. Assert the
  # directory resolves, so a stale fallback path is a red test.
  C=$(corpus_intra_dir)
  [ -d "$C" ] || {
    echo "corpus directory does not resolve: $C"
    echo "if the intra skill or its corpus dir was renamed, update corpus_intra_dir() and CORPUS_INTRA_DIR in flake.nix"
    false
  }
  # And it must be the real thing, not an empty directory that happens to exist.
  [ -d "$C/inline-status/fail" ]
  [ -d "$C/floor-leakage/pass" ]
  [ -d "$C/realization-register/fail" ]
  [ -d "$C/relocation-decision-id/fail/behavior" ]
  [ -d "$C/relocation-tombstone/pass" ]
}

@test "corpus intra (#5): realization-register fail fixture is mechanically flagged; pass fixture is clean" {
  C=$(corpus_intra_dir); [ -d "$C/realization-register" ] || skip "corpus not found at $C"
  run self-checks "$C/realization-register/fail"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'FAIL realization gap recorded inside an open question'
  run self-checks "$C/realization-register/pass"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'register section present'
  ! echo "$output" | grep -q 'FAIL realization gap recorded inside an open question'
}

@test "corpus intra (#5): every fail/pass fixture is genuinely exercised (self-checks runs to completion)" {
  C=$(corpus_intra_dir); [ -d "$C" ] || skip "corpus not found at $C"
  local found=0
  for d in "$C"/*/fail "$C"/*/pass; do
    [ -d "$d" ] || continue
    # relocation-decision-id's fixtures nest a behavior/ + decisions/ pair
    # instead of carrying .md files at the top level (relocation-check.sh
    # needs a sibling ../decisions area, which fail and pass cannot share) —
    # not a self-checks target. Skip rather than false-fail on "no .md files".
    ls "$d"/*.md >/dev/null 2>&1 || continue
    found=$((found + 1))
    run self-checks "$d"
    [ "$status" -eq 0 ] || {
      echo "self-checks did not complete cleanly on $d (status=$status)"
      echo "$output"
      false
    }
    echo "$output" | grep -q "=== IDs present"
  done
  [ "$found" -gt 0 ]
}

@test "corpus intra (#5): inline-status fail fixture is mechanically flagged; pass fixture is clean" {
  C=$(corpus_intra_dir); [ -d "$C/inline-status" ] || skip "corpus not found at $C"
  run self-checks "$C/inline-status/fail"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qi "unmet by the current implementation"
  run self-checks "$C/inline-status/pass"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -qi "unmet by the current implementation"
}

@test "corpus intra (#5): floor-leakage fail fixture is mechanically flagged; pass reports none obvious" {
  C=$(corpus_intra_dir); [ -d "$C/floor-leakage" ] || skip "corpus not found at $C"
  run self-checks "$C/floor-leakage/fail"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "orchestrator.go:142"
  run self-checks "$C/floor-leakage/pass"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "none obvious"
}

# --- INV-22 traceability extractor (bead pg2-wr6lm.4) --------------------------
# trace-extract.sh is the defined-vs-referenced diff self-checks.sh never had: its
# no-orphans check flags a UUID CARRIER with no ID on its line, which is a
# different thing entirely. Every fixture below is generated in BATS_TEST_TMPDIR;
# no test reads or writes a real set.

trace_set() {
  T="$BATS_TEST_TMPDIR/tset"
  mkdir -p "$T"
  cat >"$T/invariants.md" <<'MD'
# Invariants
- **`INV-1`** — Every accepted event is delivered at least once.
- **`INV-2`** — A duplicate event within the window is absorbed.
MD
}

@test "traceability: a fully listed set is clean and exits 0" {
  trace_set
  cat >"$T/journeys.md" <<'MD'
# Journeys

## User stories

- **`STORY-1`** — as an operator, I want delivery. _(→ `USECASE-1`; `INV-1`.)_
- **`STORY-2`** — as an operator, I want dedup. _(→ `USECASE-1`; `INV-2`.)_

## Use cases

### `USECASE-1` — Submit an event

_Primary actor:_ the operator. _Level:_ **user-goal**.
_Requires:_ `INV-1`, `INV-2`.

Submit and observe.
MD
  run trace-extract "$T"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'adopted: 3 of 3'
  echo "$output" | grep -q 'INV-22: OK'
}

@test "traceability: an element carrying NO listing FAILs once the set has adopted listings" {
  trace_set
  cat >"$T/journeys.md" <<'MD'
# Journeys

- **`STORY-1`** — as an operator, I want delivery. _(→ `USECASE-1`; `INV-1`, `INV-2`.)_
- **`STORY-2`** — as an operator, I want dedup, and this story lists nothing.

### `USECASE-1` — Submit an event

_Requires:_ `INV-1`, `INV-2`.
MD
  run trace-extract "$T"
  [ "$status" -ne 0 ]
  echo "$output" | grep -q 'FAIL STORY-2 carries no listing'
}

@test "traceability: an invariant in NO listing is reported untraced (INV-22/INV-11)" {
  trace_set
  cat >"$T/journeys.md" <<'MD'
# Journeys

- **`STORY-1`** — as an operator, I want delivery. _(→ `USECASE-1`; `INV-1`.)_

### `USECASE-1` — Submit an event

_Requires:_ `INV-1`.
MD
  run trace-extract "$T"
  [ "$status" -ne 0 ]
  echo "$output" | grep -q 'FAIL untraced: INV-2'
}

@test "traceability: a listed name resolving to nothing FAILs in every mode (INV-22 verbatim)" {
  trace_set
  cat >"$T/journeys.md" <<'MD'
# Journeys

- **`STORY-1`** — as an operator, I want delivery. _(→ `USECASE-1`; `INV-1`, `INV-2`, `INV-99`.)_

### `USECASE-1` — Submit an event

_Requires:_ `INV-1`, `INV-2`.
MD
  run trace-extract "$T"
  [ "$status" -ne 0 ]
  echo "$output" | grep -q 'FAIL dangling in a listing: INV-99'
}

@test "traceability: a PROSE reference resolving to nothing FAILs unconditionally (operator ruling pg2-p7fnv Q1)" {
  # The listing-FAIL/prose-WARN severity split is OVERTURNED: a dangling name FAILs
  # regardless of whether it appears in a listing or in prose, with no --strict
  # gate on it either way.
  trace_set
  cat >"$T/journeys.md" <<'MD'
# Journeys

This line cites `INV-DISP-1`, which no definition here or declared import resolves.

- **`STORY-1`** — as an operator, I want delivery. _(→ `USECASE-1`; `INV-1`, `INV-2`.)_

### `USECASE-1` — Submit an event

_Requires:_ `INV-1`, `INV-2`.
MD
  run trace-extract "$T"
  [ "$status" -ne 0 ]
  echo "$output" | grep -q 'FAIL dangling in prose: INV-DISP-1'
  run trace-extract --strict "$T"
  [ "$status" -ne 0 ]
  echo "$output" | grep -q 'FAIL dangling in prose: INV-DISP-1'
}

@test "traceability: a style-example marked literal is NOT dangling (operator ruling pg2-p7fnv Q2)" {
  # A literal printed purely to ILLUSTRATE the naming convention -- not to cite an
  # element -- carries the "example only, not a citation" marker on the SAME LINE
  # as the id(s) it covers, and is excluded from reference resolution entirely: it
  # must not appear as a dangling finding, in either mode.
  trace_set
  cat >"$T/journeys.md" <<'MD'
# Journeys

Namespaced names look like (example only, not a citation: `INV-DISP-1`); this line illustrates the convention.

- **`STORY-1`** — as an operator, I want delivery. _(→ `USECASE-1`; `INV-1`, `INV-2`.)_

### `USECASE-1` — Submit an event

_Requires:_ `INV-1`, `INV-2`.
MD
  run trace-extract "$T"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -q 'INV-DISP-1'
  run trace-extract --strict "$T"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -q 'INV-DISP-1'
}

@test "traceability: a reference resolving through the imports table is NOT dangling" {
  trace_set
  cat >"$T/README.md" <<'MD'
# Set

## External references

| Name     | What it is        | Owner set-path               | Owner UUID                           |
| -------- | ----------------- | ---------------------------- | ------------------------------------ |
| `INV-90` | the owner's rule  | `other-repo · docs/behavior` | 90909090-9090-4909-8909-909090909090 |
MD
  cat >"$T/journeys.md" <<'MD'
# Journeys

This set defers identity to `INV-90`, which it declares.

- **`STORY-1`** — as an operator, I want delivery. _(→ `USECASE-1`; `INV-1`, `INV-2`.)_

### `USECASE-1` — Submit an event

_Requires:_ `INV-1`, `INV-2`.
MD
  run trace-extract --strict "$T"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -q 'INV-90'
}

@test "traceability: a set with NO listings reports NOT ADOPTED and does not fail by default" {
  # A set that has never been retrofitted is a scheduled work stream, not a
  # regression. It says so loudly, with the count, and --strict makes it fatal.
  trace_set
  cat >"$T/journeys.md" <<'MD'
# Journeys

- **`STORY-1`** — as an operator, I want delivery.
- **`STORY-2`** — as an operator, I want dedup.
MD
  run trace-extract "$T"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'NOT ADOPTED: 0 of 2'
  run trace-extract --strict "$T"
  [ "$status" -ne 0 ]
}

@test "traceability: a wrapped prose line opening with a code span is NOT read as a definition" {
  # This is the defect the mandatory bullet/heading marker fixes. Without it the
  # wrapped line below reads as a definition of INV-1, which resets the current
  # element and makes USECASE-1 look like it carries no listing at all.
  trace_set
  cat >"$T/journeys.md" <<'MD'
# Journeys

- **`STORY-1`** — as an operator, I want delivery. _(→ `USECASE-1`; `INV-1`, `INV-2`.)_

### `USECASE-1` — Submit an event

_Primary actor:_ the operator, whose obligations are set by
`INV-1` continuing this sentence across a line break.
_Requires:_ `INV-1`, `INV-2`.

Submit and observe.
MD
  run trace-extract "$T"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  USECASE-1 +lists: INV-1 INV-2'
}

@test "traceability: a family glob reference resolves against the family it names" {
  trace_set
  cat >"$T/families.md" <<'MD'
# Families
- **`INV-EVT-1`** — delivery is at-least-once.
- **`INV-EVT-2`** — listeners are idempotent.
MD
  cat >"$T/journeys.md" <<'MD'
# Journeys

Delivery semantics are all from `INV-EVT-*`.

- **`STORY-1`** — as an operator, I want delivery.
  _(→ `USECASE-1`; `INV-1`, `INV-2`, `INV-EVT-1`, `INV-EVT-2`.)_

### `USECASE-1` — Submit an event

_Requires:_ `INV-1`, `INV-2`, `INV-EVT-1`, `INV-EVT-2`.
MD
  run trace-extract --strict "$T"
  [ "$status" -eq 0 ]
}

# --- Citable id families in trace-extract (bead pg2-fbxdw) ---------------------
# `trace-extract.sh` carried the OLD eight-family list, so it could not see `DEC-`/`IMPL-`
# ids at all — it could not flag a dangling `DEC-` reference, which is the under-detection
# half of pg2-fbxdw. Widening it alone would have been worse than the blind spot: every
# CONFORMANT decision citation would read as dangling, because `GOAL-5` settles that "this
# product's own decision area is the sibling **input** of the two-input model, not an
# external set, so it needs no row". So the sibling `../decisions` area is now read as a
# third resolution source, and these tests pin BOTH directions — a defined entry resolves,
# an undefined one still dangles. A blanket family exemption would pass the first pair and
# fail the second.

@test "id-family (trace-extract): a DEC- reference resolving to the sibling decision area is NOT dangling" {
  trace_set
  mkdir -p "$T/../decisions"
  cat >"$T/../decisions/seams.md" <<'MD'
# Decisions

### `DEC-SEAM-1` — the imports link points toward the more public side
MD
  cat >"$T/journeys.md" <<'MD'
# Journeys

The seam direction is settled in `docs/decisions · DEC-SEAM-1`.

- **`STORY-1`** — as an operator, I want delivery. _(→ `USECASE-1`; `INV-1`, `INV-2`.)_

### `USECASE-1` — Submit an event

_Requires:_ `INV-1`, `INV-2`.
MD
  # --strict makes a prose-dangling reference FATAL, so exit 0 is the assertion that the
  # citation resolved rather than merely warned.
  run trace-extract --strict "$T"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -q 'dangling in prose: DEC-SEAM-1'
}

@test "id-family (trace-extract): an IMPL- reference resolving to the sibling decision area is NOT dangling" {
  trace_set
  mkdir -p "$T/../decisions"
  cat >"$T/../decisions/governance.md" <<'MD'
# Decisions

### `IMPL-1` — governance authority, captured but not settled
MD
  cat >"$T/journeys.md" <<'MD'
# Journeys

Authority is still open — see `docs/decisions · IMPL-1`.

- **`STORY-1`** — as an operator, I want delivery. _(→ `USECASE-1`; `INV-1`, `INV-2`.)_

### `USECASE-1` — Submit an event

_Requires:_ `INV-1`, `INV-2`.
MD
  run trace-extract --strict "$T"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -q 'dangling in prose: IMPL-1'
}

@test "id-family (trace-extract): a DEC- reference the decision area does NOT define still dangles" {
  # The detection the widening actually bought: before it this reference was invisible, so a
  # stale decision citation passed silently. Resolving against the decision area MUST NOT
  # become a blanket exemption for the family.
  trace_set
  mkdir -p "$T/../decisions"
  cat >"$T/../decisions/seams.md" <<'MD'
# Decisions

### `DEC-SEAM-1` — the entry that DOES exist
MD
  cat >"$T/journeys.md" <<'MD'
# Journeys

This cites `DEC-SEAM-9`, which no decision entry defines.

- **`STORY-1`** — as an operator, I want delivery. _(→ `USECASE-1`; `INV-1`, `INV-2`.)_

### `USECASE-1` — Submit an event

_Requires:_ `INV-1`, `INV-2`.
MD
  run trace-extract "$T"
  [ "$status" -ne 0 ]
  echo "$output" | grep -q 'FAIL dangling in prose: DEC-SEAM-9'
  # The entry that DOES exist is not dragged into the finding.
  ! echo "$output" | grep -q 'dangling in prose: DEC-SEAM-1'
}

@test "id-family (trace-extract): a set with NO sibling decision area is unaffected" {
  # The area is optional (a set may have none). Its absence MUST NOT change the verdict,
  # which is what keeps the resolution step from becoming a required-layout assumption.
  trace_set
  cat >"$T/journeys.md" <<'MD'
# Journeys

- **`STORY-1`** — as an operator, I want delivery. _(→ `USECASE-1`; `INV-1`, `INV-2`.)_

### `USECASE-1` — Submit an event

_Requires:_ `INV-1`, `INV-2`.
MD
  run trace-extract --strict "$T"
  [ "$status" -eq 0 ]
  [ ! -d "$T/../decisions" ]
}

@test "traceability: a usage error exits 2, distinct from a finding" {
  run trace-extract
  [ "$status" -eq 2 ]
  run trace-extract "$BATS_TEST_TMPDIR/nope"
  [ "$status" -eq 2 ]
}

@test "corpus intra: the traceability fail fixture FAILs and the pass fixture passes" {
  C=$(corpus_intra_dir)
  [ -d "$C/traceability" ] || skip "corpus not found at $C"
  run trace-extract "$C/traceability/fail"
  [ "$status" -ne 0 ]
  echo "$output" | grep -q 'FAIL STORY-2 carries no listing'
  echo "$output" | grep -q 'FAIL untraced: INV-2'
  echo "$output" | grep -q 'FAIL dangling in a listing: INV-99'
  run trace-extract "$C/traceability/pass"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'INV-22: OK'
}

# --- capture-prefix-snapshots.sh is wired to a check (bead pg2-wr6lm.4) --------
# The script existed and NO check ran it, so nothing would have noticed it
# breaking. It reads git history into a fresh directory, so it is exercised
# against a SYNTHETIC throwaway repo built here — never against a real clone.

@test "capture-prefix-snapshots: captures the PRE-FIX revision, not the current one" {
  repo="$BATS_TEST_TMPDIR/repo"
  mkdir -p "$repo/behavior-docs/docs/behavior" "$repo/packages/pr-pool/docs/behavior"
  _gfh_protect_fixture "capture-prefix-1"
  gfh_init_repo "$repo" "capture-prefix-1"
  cd "$repo"
  echo "PRE-FIX: unmet by the current implementation" >behavior-docs/docs/behavior/invariants.md
  echo "PRE-FIX pr-pool" >packages/pr-pool/docs/behavior/invariants.md
  git add -A
  git commit -qm prefix
  prefix_rev=$(git rev-parse HEAD)
  echo "POST-FIX: the rule as-if-true" >behavior-docs/docs/behavior/invariants.md
  git add -A
  git commit -qm postfix

  out="$BATS_TEST_TMPDIR/snap"
  run env AGENT_SUPPORT_REPO="$repo" AGENT_SUPPORT_REV="$prefix_rev" \
    capture-prefix-snapshots "$out"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'captured method-prefix'
  echo "$output" | grep -q 'captured pr-pool-prefix'
  # The captured content MUST be the pre-fix text — a capture that silently
  # returned HEAD would make every "real-world FAIL fixture" a PASS fixture.
  grep -q 'unmet by the current implementation' "$out/method-prefix/invariants.md"
  # `run !` rather than a bare `!`: a bare negation mid-test does not fail a Bats
  # test at all (SC2314), so the assertion would silently never assert.
  run ! grep -q 'as-if-true' "$out/method-prefix/invariants.md"
  grep -q 'PRE-FIX pr-pool' "$out/pr-pool-prefix/invariants.md"
}

@test "capture-prefix-snapshots: an unknown rev and a non-repo are reported, not silently empty" {
  repo="$BATS_TEST_TMPDIR/repo2"
  mkdir -p "$repo/behavior-docs/docs/behavior"
  _gfh_protect_fixture "capture-prefix-2"
  gfh_init_repo "$repo" "capture-prefix-2"
  cd "$repo"
  echo x >behavior-docs/docs/behavior/invariants.md
  git add -A
  git commit -qm one

  out="$BATS_TEST_TMPDIR/snap2"
  run env AGENT_SUPPORT_REPO="$repo" AGENT_SUPPORT_REV=deadbeefdeadbeef \
    capture-prefix-snapshots "$out"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'rev deadbeefdeadbeef not found'

  run env AGENT_SUPPORT_REPO="$BATS_TEST_TMPDIR/not-a-repo" capture-prefix-snapshots "$BATS_TEST_TMPDIR/snap3"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'is not a git repo'
}

# --- Findings must be CANONICAL where they are written -------------------------
# A finding that aggregates several locations used to serialize in whatever order
# the collation and the traversal produced, so the SAME finding read one way on a
# UTF-8 workstation (`invariants.md:75 README.md:61`) and another in the C-locale
# nix sandbox (`README.md:61 invariants.md:75`). A baseline comparison then flagged
# one identical finding as BOTH a new regression and a no-longer-occurring entry,
# which made the gate flaky — worse than the defects it was built to catch.
#
# The fixture below pins BOTH halves of the fix in one expected string:
#   * `README.md` before `invariants.md` — BYTE order (R < i), not the UTF-8
#     collation that ignores case and would put `invariants` first; and
#   * `:9` before `:10` — the line compared NUMERICALLY, not as text.

canon_set() {
  T="$BATS_TEST_TMPDIR/canon"
  mkdir -p "$T"
  # INV-99 is dangling, and is cited from two files at four lines. The APPEARANCE
  # order here (invariants.md:10 before :9, README.md last) is deliberately NOT
  # the canonical order, so a script that echoes traversal order fails.
  {
    echo "# Invariants"
    echo "- **\`INV-1\`** — a rule that resolves."
    echo "3"
    echo "4"
    echo "5"
    echo "6"
    echo "7"
    echo "8"
    echo "9"
    echo "line ten cites \`INV-99\`"
    echo "no, line nine did too: see above"
  } >"$T/invariants.md"
  # Rewrite line 9 to carry the citation (so :9 and :10 both cite it).
  awk 'NR==9 { print "line nine cites `INV-99`"; next } { print }' \
    "$T/invariants.md" >"$T/invariants.tmp"
  mv "$T/invariants.tmp" "$T/invariants.md"
  printf '# Readme\nfiller\nline three cites `INV-99`\n' >"$T/README.md"
}

@test "canonical findings: a multi-location finding sorts by path (BYTE order) then line NUMERICALLY" {
  canon_set
  run trace-extract "$T"
  # A prose-dangling reference is a hard FAIL now (operator ruling pg2-p7fnv Q1),
  # so this fixture's INV-99 citation makes the run exit non-zero; the finding
  # line itself is what this test actually pins.
  [ "$status" -ne 0 ]
  echo "$output" | grep -qF 'INV-99 (README.md:3 invariants.md:9 invariants.md:10)' || {
    echo "location list is not canonical; got:"
    echo "$output" | grep 'INV-99'
    false
  }
}

@test "canonical findings: trace-extract output is byte-identical under any ambient locale" {
  canon_set
  a="$BATS_TEST_TMPDIR/out.c"
  b="$BATS_TEST_TMPDIR/out.utf8"
  c="$BATS_TEST_TMPDIR/out.unset"
  # The fixture's dangling INV-99 now makes trace-extract exit non-zero (Q1), so
  # each call is allowed to fail here -- this test pins output BYTES across
  # locales, not the exit code (a status mismatch would itself show up as a diff).
  env LC_ALL=C trace-extract "$T" >"$a" 2>&1 || true
  env LC_ALL=en_US.UTF-8 LC_COLLATE=en_US.UTF-8 trace-extract "$T" >"$b" 2>&1 || true
  env -u LC_ALL -u LC_COLLATE LANG=en_US.UTF-8 trace-extract "$T" >"$c" 2>&1 || true
  diff "$a" "$b"
  diff "$a" "$c"
}

# --- Relocation check: the decidable half of USECASE-5 (bead pg2-9sslr) -------
# relocation-check.sh covers ONLY step 3 (a relocated entry carries a typed id
# + UUID at its new decision-area location) and step 5 (no tombstone note left
# in the behavior doc). Steps 1, 2 and step 4's judgment half are explicitly
# out of scope per the operator ruling recorded on pg2-9sslr — see the
# script's own header comment.

@test "relocation step 3: a decision entry with no UUID on its definition line is FAILed" {
  mkdir -p "$SET/../decisions"
  cat >"$SET/../decisions/wire.md" <<'MD'
# Wire

### `DEC-WIRE-1` — the default transport is a CLI invocation carrying JSON

**Decided.** ...
MD
  cat >"$SET/interfaces.md" <<'MD'
# Interfaces

Cited via `docs/decisions · DEC-WIRE-1`.
MD
  run relocation-check "$SET"
  [ "$status" -ne 0 ]
  echo "$output" | grep -q 'FAIL DEC-WIRE-1 (wire.md:3) carries no UUID'
}

@test "relocation step 3: a malformed UUID on the definition line is FAILed" {
  mkdir -p "$SET/../decisions"
  cat >"$SET/../decisions/wire.md" <<'MD'
# Wire

### `DEC-WIRE-1` — the default transport <!-- uuid: not-a-real-uuid -->
MD
  cat >"$SET/interfaces.md" <<'MD'
# Interfaces
MD
  run relocation-check "$SET"
  [ "$status" -ne 0 ]
  echo "$output" | grep -q 'FAIL DEC-WIRE-1 (wire.md:3) carries a malformed UUID: not-a-real-uuid'
}

@test "relocation step 3: two entries sharing one UUID are FAILed as a duplicate" {
  mkdir -p "$SET/../decisions"
  cat >"$SET/../decisions/wire.md" <<'MD'
# Wire

### `DEC-WIRE-1` — first entry <!-- uuid: 11111111-1111-4111-8111-111111111111 -->

### `DEC-WIRE-2` — second entry, wrongly reusing the same UUID <!-- uuid: 11111111-1111-4111-8111-111111111111 -->
MD
  cat >"$SET/interfaces.md" <<'MD'
# Interfaces
MD
  run relocation-check "$SET"
  [ "$status" -ne 0 ]
  echo "$output" | grep -q 'FAIL duplicate UUID within the decision area: 11111111-1111-4111-8111-111111111111'
}

@test "relocation step 3: a well-formed, unique decision entry is clean" {
  mkdir -p "$SET/../decisions"
  cat >"$SET/../decisions/wire.md" <<'MD'
# Wire

### `DEC-WIRE-1` — the default transport is a CLI invocation carrying JSON <!-- uuid: 6450eed7-228f-4a99-bfd9-6705a6c552ee -->

**Decided.** ...
MD
  cat >"$SET/interfaces.md" <<'MD'
# Interfaces

Cited via `docs/decisions · DEC-WIRE-1`.
MD
  run relocation-check "$SET"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'clean (1 entry: well-formed, unique UUID'
}

@test "relocation step 3: README.md's index BULLET (no heading, no UUID) is not mistaken for a definition" {
  # docs/decisions/README.md's own "Entries" section restates every id as a
  # plain bullet with no UUID — that is the INDEX, not the definition (which
  # lives in the topic file as a heading). A bullet-admitting DEFPAT would
  # misread the index as an un-UUID'd definition, false-FAILing every real
  # decisions/README.md in this repo.
  mkdir -p "$SET/../decisions"
  cat >"$SET/../decisions/wire.md" <<'MD'
# Wire

### `DEC-WIRE-1` — the default transport <!-- uuid: 6450eed7-228f-4a99-bfd9-6705a6c552ee -->
MD
  cat >"$SET/../decisions/README.md" <<'MD'
# Decision docs

## Entries

- **`DEC-WIRE-1`** — in [wire.md](wire.md) — the default transport is a CLI invocation.
MD
  cat >"$SET/interfaces.md" <<'MD'
# Interfaces
MD
  run relocation-check "$SET"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -qi FAIL
}

@test "relocation step 3: no sibling decision area is reported, not an error" {
  cat >"$SET/interfaces.md" <<'MD'
# Interfaces
MD
  run relocation-check "$SET"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q 'no sibling decision area'
}

@test "relocation step 5: a parenthetical 'moved to' tombstone note is FAILed" {
  cat >"$SET/interfaces.md" <<'MD'
# Interfaces

## `INTF-WIRE` — the wire boundary

_(moved to docs/decisions/wire.md)_
MD
  run relocation-check "$SET"
  [ "$status" -ne 0 ]
  echo "$output" | grep -q "FAIL tombstone note left in behavior doc: interfaces.md:5:_(moved to docs/decisions/wire.md)_"
}

@test "relocation step 5: an HTML-comment tombstone marker is FAILed" {
  cat >"$SET/interfaces.md" <<'MD'
# Interfaces

## `INTF-WIRE` — the wire boundary
<!-- relocated to DEC-WIRE-1 -->
MD
  run relocation-check "$SET"
  [ "$status" -ne 0 ]
  echo "$output" | grep -qi 'FAIL tombstone note left in behavior doc'
}

@test "relocation step 5: a restated behavior statement with a proper citation is clean" {
  cat >"$SET/interfaces.md" <<'MD'
# Interfaces

## `INTF-WIRE` — the wire boundary

The default transport is a CLI invocation carrying JSON
(`phillipgreenii-nix-agent-support · packages/pr-pool/docs/decisions · DEC-WIRE-1`).
MD
  run relocation-check "$SET"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "clean (no 'moved to"
}

@test "relocation step 5: USECASE-5's OWN illustrative quote 'no \"moved to ...\" note' is NOT flagged" {
  # The exact real false positive this corpus contains: a QUOTED example of the
  # forbidden note, describing the rule rather than violating it.
  cat >"$SET/journeys.md" <<'MD'
# Journeys

5. Delete it from the behavior doc, with no tombstone: no "moved to ..." note, no status header.
MD
  run relocation-check "$SET"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -qi FAIL
}

@test "relocation step 5: legitimate prose using 'relocated to' as ACTIVE behavior is NOT flagged" {
  # The other real false positive: pg-pr's invariants.md states, as a live
  # behavior rule about UUID provenance, that four invariants were "relocated
  # to" pg-pr from another set. Plain running prose, no note-shaped marker.
  cat >"$SET/invariants.md" <<'MD'
# Invariants

Four invariants below carry a UUID moved from the ZR deployment set rather than freshly minted,
because they are the same rule relocated to its correct owner.
MD
  run relocation-check "$SET"
  [ "$status" -eq 0 ]
  ! echo "$output" | grep -qi FAIL
}

@test "relocation: a usage error exits 2, distinct from a finding" {
  run relocation-check
  [ "$status" -eq 2 ]
  run relocation-check "$BATS_TEST_TMPDIR/nope-reloc"
  [ "$status" -eq 2 ]
}

@test "corpus intra: relocation-decision-id fail fixture FAILs and pass fixture passes" {
  C=$(corpus_intra_dir)
  [ -d "$C/relocation-decision-id" ] || skip "corpus not found at $C"
  run relocation-check "$C/relocation-decision-id/fail/behavior"
  [ "$status" -ne 0 ]
  echo "$output" | grep -q 'FAIL DEC-WIRE-1'
  run relocation-check "$C/relocation-decision-id/pass/behavior"
  [ "$status" -eq 0 ]
}

@test "corpus intra: relocation-tombstone fail fixture FAILs and pass fixture passes" {
  C=$(corpus_intra_dir)
  [ -d "$C/relocation-tombstone" ] || skip "corpus not found at $C"
  run relocation-check "$C/relocation-tombstone/fail"
  [ "$status" -ne 0 ]
  echo "$output" | grep -q 'FAIL tombstone note left in behavior doc'
  run relocation-check "$C/relocation-tombstone/pass"
  [ "$status" -eq 0 ]
}

@test "regression: a GIT_DIR/GIT_INDEX_FILE leaked into the parent shell before the capture-prefix-snapshots fixture is scrubbed, not honored" {
  # Simulates the pg2-67h4y hook-environment leak: GIT_DIR/GIT_INDEX_FILE
  # pointed at a bogus path BEFORE the fixture's own scrub runs. If
  # gfh_reset_env did not take effect, `git -C "$real_dir" init` (the exact
  # shape gfh_init_repo -- and, before this fix, the raw `git init .` this
  # file used -- issues) would operate against/create the bogus path instead
  # of the fixture's own repo.
  local bogus_parent bogus harness_path real_dir
  bogus_parent="$(mktemp -d)"
  bogus="$bogus_parent/leaked-gitdir"
  real_dir="$BATS_TEST_TMPDIR/regression-repo"
  mkdir -p "$real_dir"
  if [[ -n ${GFH_LIB:-} ]]; then
    harness_path="$GFH_LIB"
  else
    harness_path="$(cd "$BATS_TEST_DIRNAME/support" && pwd)/git-fixture-harness.bash"
  fi

  run env GIT_DIR="$bogus" GIT_INDEX_FILE="$bogus/index" HARNESS_PATH="$harness_path" REAL_DIR="$real_dir" bash -c '
    # Capture REAL_DIR into a plain (non-exported) local BEFORE gfh_reset_env
    # runs -- it scrubs every EXPORTED var not on its allowlist, and REAL_DIR
    # arrived via `env`, so it is exported and would otherwise be wiped too.
    real_dir_local="$REAL_DIR"
    source "$HARNESS_PATH"
    gfh_reset_env
    gfh_init_repo "$real_dir_local" "intra-regression"
    command git -C "$real_dir_local" rev-parse --git-dir
  '
  [ "$status" -eq 0 ]
  [[ "$output" != *"leaked-gitdir"* ]]

  # The bogus path must never have been created -- proves the scrub took
  # effect rather than the leaked vars silently being honored.
  [ ! -e "$bogus" ]

  rm -rf "$bogus_parent"
}
