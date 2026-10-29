#!/usr/bin/env bats
# bats file_tags=type:unit
# Regression guard for the script that runs the PR lane's end-to-end suite
# (docket pg2-6mo45.3, packet P4c-1): the body of the nix check
# `pg-decider-lane-fake` (--suite fake: TestLaneFake over the fake bd) and of the
# by-hand real-bd soak (--suite soak, the default: TestLaneE2E behind the laneE2E
# build tag). Bead pg2-by94zu removed the soak's commit/pre-land hook, and bead
# pg2-s4xeju took it out of the flake checks (operator ruling 2026-10-28: a test
# must not run for 1.5 hours), leaving the fast run as the automatic one.
#
# Deterministic logic (global rule V-3). The script is run for real against PATH
# shims for `go`, `bd`, `git` and `sqlite3`: they stand in for the tools, and the
# thing under test is which tool absence is fatal, what `go test` is asked to run,
# and which run outputs the gate refuses (a missing scenario PASS line, a SKIP line,
# too few scenarios).

bats_require_minimum_version 1.5.0

SCRIPT="$BATS_TEST_DIRNAME/../scripts/run-pg-decider-lane-e2e.sh"
TESTDATA="$BATS_TEST_DIRNAME/../packages/pg-decider/internal/prchange/testdata"
SOAK_LIST="$TESTDATA/lane-e2e-scenarios.txt"
FAKE_LIST="$TESTDATA/lane-fake-scenarios.txt"

setup() {
  SHIM="$BATS_TEST_TMPDIR/shim"
  MODULE="$BATS_TEST_TMPDIR/pkgs/pg-decider"
  mkdir -p "$SHIM" "$MODULE/internal/prchange/testdata"
  # The script's own plumbing is real; only the four tools under test are shims.
  for t in mktemp mkdir rm grep cat sed nice dirname; do
    ln -s "$(command -v "$t")" "$SHIM/$t"
  done
  cp "$SOAK_LIST" "$FAKE_LIST" "$MODULE/internal/prchange/testdata/"
}

shim() { # shim <tool> [body]: an executable PATH shim
  printf '#!/bin/sh\n%s\n' "${2:-exit 0}" >"$SHIM/$1"
  chmod +x "$SHIM/$1"
}

shim_tools() { # every tool present; go prints the prepared run output
  shim bd
  shim git
  shim sqlite3
  shim go 'if [ "$1" = build ]; then : > "$3"; exit 0; fi
printf "%s\n" "$*" >>"'"$BATS_TEST_TMPDIR"'/go-args"
printf "%s\n" "$(pwd -P)" >"'"$BATS_TEST_TMPDIR"'/go-pwd"
printf "%s\n" "$PG_PR_CHANGE_REQUIRE_BINARIES" >"'"$BATS_TEST_TMPDIR"'/go-require"
cat "'"$BATS_TEST_TMPDIR"'/go-output"'
}

passing_output() { # passing_output <test> <list> [skip-name] : the verbose output of a green run
  {
    echo "=== RUN   $1"
    while IFS= read -r n; do
      case "$n" in '' | '#'*) continue ;; esac
      [ "$n" = "${3:-}" ] && continue
      echo "    --- PASS: $1/$n (1.00s)"
    done <"$2"
    echo "--- PASS: $1 (9.00s)"
    echo "PASS"
  } >"$BATS_TEST_TMPDIR/go-output"
}

soak_passing() { passing_output TestLaneE2E "$SOAK_LIST" "${1:-}"; }
fake_passing() { passing_output TestLaneFake "$FAKE_LIST" "${1:-}"; }

run_gate() { run --separate-stderr env PATH="$SHIM" "$BASH" "$SCRIPT" --prebuilt --module-dir "$MODULE" "$@"; }

@test "each scenario list is not empty and the script's minimum is the list's size" {
  for pair in "FAKE:$FAKE_LIST" "SOAK:$SOAK_LIST"; do
    n="$(grep -vcE '^(#|$)' "${pair#*:}")"
    [ "$n" -gt 0 ]
    min="$(sed -n "s/^MIN_SCENARIOS_${pair%%:*}=\([0-9][0-9]*\)$/\1/p" "$SCRIPT")"
    [ "$min" -gt 0 ]
    [ "$min" -eq "$n" ]
  done
}

@test "the fast list is the soak list without the scenarios that need the real bd" {
  only_soak="$(grep -vE '^(#|$)' "$SOAK_LIST" | grep -vxF -f <(grep -vE '^(#|$)' "$FAKE_LIST"))"
  [ "$only_soak" = "the-bd-list-query-of-a-prs-review-items-returns-every-state" ]
  # nothing in the fast list that the soak does not run
  run grep -vxF -f <(grep -vE '^(#|$)' "$SOAK_LIST") <(grep -vE '^(#|$)' "$FAKE_LIST")
  [ "$status" -eq 1 ]
}

@test "each missing tool of the soak fails loudly with exit 2 and names the tool" {
  for missing in go bd git; do
    rm -f "$SHIM/go" "$SHIM/bd" "$SHIM/git" "$SHIM/sqlite3"
    shim_tools
    rm -f "$SHIM/$missing"
    run_gate
    [ "$status" -eq 2 ]
    [[ $stderr == *"$missing not found on PATH"* ]]
  done
}

@test "the fast suite needs go but neither bd nor git" {
  shim_tools
  rm -f "$SHIM/bd" "$SHIM/git"
  fake_passing
  run_gate --suite fake
  [ "$status" -eq 0 ]
  rm -f "$SHIM/go"
  run_gate --suite fake
  [ "$status" -eq 2 ]
  [[ $stderr == *"go not found on PATH"* ]]
}

@test "a missing sqlite3 fails loudly unless the environment names one" {
  shim_tools
  rm -f "$SHIM/sqlite3"
  soak_passing
  run_gate
  [ "$status" -eq 2 ]
  [[ $stderr == *"sqlite3 not found on PATH"* ]]
  run --separate-stderr env PATH="$SHIM" PG_PR_CHANGE_SQLITE3_BIN=/nonexistent/sqlite3 "$BASH" "$SCRIPT" --prebuilt --module-dir "$MODULE"
  [ "$status" -eq 0 ]
}

@test "the soak runs only TestLaneE2E with the laneE2E tag in the module, with a missing binary made fatal" {
  shim_tools
  soak_passing
  run_gate
  [ "$status" -eq 0 ]
  args="$(cat "$BATS_TEST_TMPDIR/go-args")"
  [[ $args == *"test "* ]]
  [[ $args == *"-tags laneE2E"* ]]
  [[ $args == *'-run ^TestLaneE2E$'* ]]
  [[ $args == *"./internal/prchange/"* ]]
  [ "$(cat "$BATS_TEST_TMPDIR/go-pwd")" = "$(cd "$MODULE" && pwd -P)" ]
  [ "$(cat "$BATS_TEST_TMPDIR/go-require")" = 1 ]
  [[ $output == *"soak suite: 25 scenarios, each with a PASS line, none skipped"* ]]
}

@test "the fast suite runs only TestLaneFake, untagged, with a missing binary made fatal" {
  shim_tools
  fake_passing
  run_gate --suite fake
  [ "$status" -eq 0 ]
  args="$(cat "$BATS_TEST_TMPDIR/go-args")"
  [[ $args == *'-run ^TestLaneFake$'* ]]
  [[ $args == *"./internal/prchange/"* ]]
  [[ $args != *"-tags"* ]]
  [ "$(cat "$BATS_TEST_TMPDIR/go-require")" = 1 ]
  [[ $output == *"fake suite: 24 scenarios, each with a PASS line, none skipped"* ]]
}

@test "without --prebuilt it builds the six binaries before the run" {
  shim_tools
  soak_passing
  # a go that logs every build
  shim go 'if [ "$1" = build ]; then printf "%s %s\n" "$(pwd -P | sed "s|.*/||")" "$*" >>"'"$BATS_TEST_TMPDIR"'/builds"; : > "$3"; exit 0; fi
cat "'"$BATS_TEST_TMPDIR"'/go-output"'
  mkdir -p "$BATS_TEST_TMPDIR/pkgs/pg-desk" "$BATS_TEST_TMPDIR/pkgs/pg-connector"
  run --separate-stderr env PATH="$SHIM" "$BASH" "$SCRIPT" --module-dir "$MODULE"
  [ "$status" -eq 0 ]
  [ "$(grep -c . "$BATS_TEST_TMPDIR/builds")" -eq 6 ]
  grep -q '^pg-desk build .* ./cmd/pg-desk$' "$BATS_TEST_TMPDIR/builds"
  grep -q '^pg-decider build .* ./cmd/pg-pr-change$' "$BATS_TEST_TMPDIR/builds"
  grep -q '^pg-decider build .* ./cmd/pg-pr-backfill$' "$BATS_TEST_TMPDIR/builds"
  grep -q '^pg-connector build .* ./cmd/pg-connector-issue-beads$' "$BATS_TEST_TMPDIR/builds"
}

@test "a scenario without a PASS line fails the gate and is named, in either suite" {
  shim_tools
  name="$(grep -vE '^(#|$)' "$SOAK_LIST" | sed -n 3p)"
  soak_passing "$name"
  run_gate
  [ "$status" -eq 1 ]
  [[ $stderr == *"scenario $name has no PASS line"* ]]
  fake_passing "$name"
  run_gate --suite fake
  [ "$status" -eq 1 ]
  [[ $stderr == *"scenario $name has no PASS line"* ]]
}

@test "a soak scenario's PASS line does not stand for the fast suite's" {
  shim_tools
  soak_passing
  run_gate --suite fake
  [ "$status" -eq 1 ]
  [[ $stderr == *"TestLaneFake did not report PASS"* ]]
}

@test "a SKIP line fails the gate" {
  shim_tools
  soak_passing
  echo "    --- SKIP: TestLaneE2E/some-scenario (0.00s)" >>"$BATS_TEST_TMPDIR/go-output"
  run_gate
  [ "$status" -eq 1 ]
  [[ $stderr == *"was SKIPPED"* ]]
}

@test "a run that never reports the test's own PASS line fails the gate" {
  shim_tools
  soak_passing
  grep -v '^--- PASS: TestLaneE2E ' "$BATS_TEST_TMPDIR/go-output" >"$BATS_TEST_TMPDIR/go-output.2"
  mv "$BATS_TEST_TMPDIR/go-output.2" "$BATS_TEST_TMPDIR/go-output"
  run_gate
  [ "$status" -eq 1 ]
  [[ $stderr == *"did not report PASS"* ]]
}

@test "a list with fewer scenarios than the minimum fails the gate, and an empty list never passes" {
  shim_tools
  list="$MODULE/internal/prchange/testdata/lane-e2e-scenarios.txt"
  grep -vE '^(#|$)' "$SOAK_LIST" | head -2 >"$list"
  {
    echo "--- PASS: TestLaneE2E (1.00s)"
    while IFS= read -r n; do echo "    --- PASS: TestLaneE2E/$n (1.00s)"; done <"$list"
  } >"$BATS_TEST_TMPDIR/go-output"
  run_gate
  [ "$status" -eq 1 ]
  [[ $stderr == *"only 2 scenarios"* ]]
  : >"$list"
  run_gate
  [ "$status" -eq 1 ]
  [[ $stderr == *"only 0 scenarios"* ]]
}

@test "a failing test run fails the gate with its own exit code" {
  shim_tools
  shim go 'exit 7'
  run_gate
  [ "$status" -eq 7 ]
}

@test "an unknown argument or suite is a usage error" {
  run --separate-stderr bash "$SCRIPT" --nope
  [ "$status" -eq 2 ]
  run --separate-stderr bash "$SCRIPT" --suite bogus
  [ "$status" -eq 2 ]
  [[ $stderr == *"--suite must be fake or soak"* ]]
}

# Operator standard V-4 (bead pg2-by94zu) and the operator ruling of 2026-10-28
# (bead pg2-s4xeju): the real-bd soak MUST NOT gate a commit or a land and is not
# a flake check; the fast run is the check that keeps the suite able to fail (ADR
# 0024 of phillipg-nix-repo-base). Reads the flake and the test sources, so it
# skips off the repo.
@test "the fast run is the nix check, the soak is tagged and only an opt-in package" {
  flake="$BATS_TEST_DIRNAME/../flake.nix"
  prchange="$BATS_TEST_DIRNAME/../packages/pg-decider/internal/prchange"
  [ -f "$flake" ] || skip "flake.nix is not next to the tests"
  # no hook of either name, no hook wrapper, no path regex
  run grep -En '^ *name = "pg-decider-lane-(e2e|fake)"|pg-decider-lane-e2e-hook|pg-decider-lane-e2e-files' "$flake"
  [ "$status" -eq 1 ]
  [ ! -e "$BATS_TEST_DIRNAME/../scripts/pg-decider-lane-e2e-files.regex" ]
  # the soak is no longer a check; it is the opt-in package that runs the script
  run grep -Eq '^ *pg-decider-lane-e2e =$' "$flake"
  [ "$status" -eq 1 ]
  grep -Eq '^ *pg-decider-lane-e2e-soak = pkgs.writeShellApplication \{$' "$flake"
  # the fast check runs the script's fast suite over the prebuilt binaries
  grep -Eq '^ *pg-decider-lane-fake =$' "$flake"
  grep -q 'run-pg-decider-lane-e2e.sh} --suite fake --prebuilt' "$flake"
  # the soak's driver carries the tag; the fast run and the shared scenarios do not
  [ "$(head -1 "$prchange/lane_e2e_test.go")" = "//go:build laneE2E" ]
  for f in lane_fake_test.go lane_world_test.go lane_scenarios_test.go lane_review_test.go lane_race_test.go lane_rollback_test.go; do
    run grep -q '^//go:build' "$prchange/$f"
    [ "$status" -eq 1 ]
  done
}
