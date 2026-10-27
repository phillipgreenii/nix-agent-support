#!/usr/bin/env bats
# bats file_tags=type:unit
# Regression guard for the script that is the body of the nix check
# `pg-decider-lane-e2e` and of the by-hand run (docket pg2-6mo45.3, packet P4c-1;
# bead pg2-by94zu removed the commit/pre-land hook and its path regex, because the
# suite is a multi-process end-to-end test and so MUST NOT gate a commit or a land):
# what the script demands of the run.
#
# Deterministic logic (global rule V-3). The script is run for real against PATH
# shims for `go`, `bd`, `git` and `sqlite3`: they stand in for the tools, and the
# thing under test is which tool absence is fatal, what `go test` is asked to run,
# and which run outputs the gate refuses (a missing scenario PASS line, a SKIP line,
# too few scenarios).

bats_require_minimum_version 1.5.0

SCRIPT="$BATS_TEST_DIRNAME/../scripts/run-pg-decider-lane-e2e.sh"
SCENARIOS="$BATS_TEST_DIRNAME/../packages/pg-decider/internal/prchange/testdata/lane-e2e-scenarios.txt"

setup() {
  SHIM="$BATS_TEST_TMPDIR/shim"
  MODULE="$BATS_TEST_TMPDIR/pkgs/pg-decider"
  mkdir -p "$SHIM" "$MODULE/internal/prchange/testdata"
  # The script's own plumbing is real; only the four tools under test are shims.
  for t in mktemp mkdir rm grep cat sed nice dirname; do
    ln -s "$(command -v "$t")" "$SHIM/$t"
  done
  cp "$SCENARIOS" "$MODULE/internal/prchange/testdata/lane-e2e-scenarios.txt"
}

shim() { # shim <tool> [body]: an executable PATH shim
  printf '#!/bin/sh\n%s\n' "${2:-exit 0}" >"$SHIM/$1"
  chmod +x "$SHIM/$1"
}

shim_tools() { # every tool present; go prints a passing run of every named scenario
  shim bd
  shim git
  shim sqlite3
  shim go 'if [ "$1" = build ]; then : > "$3"; exit 0; fi
printf "%s\n" "$*" >>"'"$BATS_TEST_TMPDIR"'/go-args"
printf "%s\n" "$(pwd -P)" >"'"$BATS_TEST_TMPDIR"'/go-pwd"
printf "%s\n" "$PG_PR_CHANGE_REQUIRE_BINARIES" >"'"$BATS_TEST_TMPDIR"'/go-require"
cat "'"$BATS_TEST_TMPDIR"'/go-output"'
}

passing_output() { # passing_output [skip-name] : the verbose output of a green run
  {
    echo "=== RUN   TestLaneE2E"
    while IFS= read -r n; do
      case "$n" in '' | '#'*) continue ;; esac
      [ "$n" = "${1:-}" ] && continue
      echo "    --- PASS: TestLaneE2E/$n (1.00s)"
    done <"$SCENARIOS"
    echo "--- PASS: TestLaneE2E (9.00s)"
    echo "PASS"
  } >"$BATS_TEST_TMPDIR/go-output"
}

run_gate() { run --separate-stderr env PATH="$SHIM" "$BASH" "$SCRIPT" --prebuilt --module-dir "$MODULE"; }

@test "the scenario list is not empty and the script's minimum is the list's size" {
  n="$(grep -vcE '^(#|$)' "$SCENARIOS")"
  [ "$n" -gt 0 ]
  min="$(sed -n 's/^MIN_SCENARIOS=\([0-9][0-9]*\)$/\1/p' "$SCRIPT")"
  [ "$min" -gt 0 ]
  [ "$min" -eq "$n" ]
}

@test "each missing tool fails loudly with exit 2 and names the tool" {
  for missing in go bd git; do
    rm -f "$SHIM/go" "$SHIM/bd" "$SHIM/git" "$SHIM/sqlite3"
    shim_tools
    rm -f "$SHIM/$missing"
    run_gate
    [ "$status" -eq 2 ]
    [[ $stderr == *"$missing not found on PATH"* ]]
    done
}

@test "a missing sqlite3 fails loudly unless the environment names one" {
  shim_tools
  rm -f "$SHIM/sqlite3"
  passing_output
  run_gate
  [ "$status" -eq 2 ]
  [[ $stderr == *"sqlite3 not found on PATH"* ]]
  run --separate-stderr env PATH="$SHIM" PG_PR_CHANGE_SQLITE3_BIN=/nonexistent/sqlite3 "$BASH" "$SCRIPT" --prebuilt --module-dir "$MODULE"
  [ "$status" -eq 0 ]
}

@test "with every tool it runs only TestLaneE2E in the module, with a missing binary made fatal" {
  shim_tools
  passing_output
  run_gate
  [ "$status" -eq 0 ]
  args="$(cat "$BATS_TEST_TMPDIR/go-args")"
  [[ $args == *"test "* ]]
  [[ $args == *'-run ^TestLaneE2E$'* ]]
  [[ $args == *"./internal/prchange/"* ]]
  [ "$(cat "$BATS_TEST_TMPDIR/go-pwd")" = "$(cd "$MODULE" && pwd -P)" ]
  [ "$(cat "$BATS_TEST_TMPDIR/go-require")" = 1 ]
  [[ $output == *"24 scenarios, each with a PASS line, none skipped"* ]]
}

@test "without --prebuilt it builds the five binaries before the run" {
  shim_tools
  passing_output
  # a go that logs every build
  shim go 'if [ "$1" = build ]; then printf "%s %s\n" "$(pwd -P | sed "s|.*/||")" "$*" >>"'"$BATS_TEST_TMPDIR"'/builds"; : > "$3"; exit 0; fi
cat "'"$BATS_TEST_TMPDIR"'/go-output"'
  mkdir -p "$BATS_TEST_TMPDIR/pkgs/pg-desk" "$BATS_TEST_TMPDIR/pkgs/pg-connector"
  run --separate-stderr env PATH="$SHIM" "$BASH" "$SCRIPT" --module-dir "$MODULE"
  [ "$status" -eq 0 ]
  [ "$(grep -c . "$BATS_TEST_TMPDIR/builds")" -eq 5 ]
  grep -q '^pg-desk build .* ./cmd/pg-desk$' "$BATS_TEST_TMPDIR/builds"
  grep -q '^pg-decider build .* ./cmd/pg-pr-change$' "$BATS_TEST_TMPDIR/builds"
  grep -q '^pg-connector build .* ./cmd/pg-connector-issue-beads$' "$BATS_TEST_TMPDIR/builds"
}

@test "a scenario without a PASS line fails the gate and is named" {
  shim_tools
  name="$(grep -vE '^(#|$)' "$SCENARIOS" | sed -n 3p)"
  passing_output "$name"
  run_gate
  [ "$status" -eq 1 ]
  [[ $stderr == *"scenario $name has no PASS line"* ]]
}

@test "a SKIP line fails the gate" {
  shim_tools
  passing_output
  echo "    --- SKIP: TestLaneE2E/some-scenario (0.00s)" >>"$BATS_TEST_TMPDIR/go-output"
  run_gate
  [ "$status" -eq 1 ]
  [[ $stderr == *"was SKIPPED"* ]]
}

@test "a run that never reports the test's own PASS line fails the gate" {
  shim_tools
  passing_output
  grep -v '^--- PASS: TestLaneE2E ' "$BATS_TEST_TMPDIR/go-output" >"$BATS_TEST_TMPDIR/go-output.2"
  mv "$BATS_TEST_TMPDIR/go-output.2" "$BATS_TEST_TMPDIR/go-output"
  run_gate
  [ "$status" -eq 1 ]
  [[ $stderr == *"did not report PASS"* ]]
}

@test "a list with fewer scenarios than the minimum fails the gate, and an empty list never passes" {
  shim_tools
  grep -vE '^(#|$)' "$SCENARIOS" | head -2 >"$MODULE/internal/prchange/testdata/lane-e2e-scenarios.txt"
  {
    echo "--- PASS: TestLaneE2E (1.00s)"
    while IFS= read -r n; do echo "    --- PASS: TestLaneE2E/$n (1.00s)"; done <"$MODULE/internal/prchange/testdata/lane-e2e-scenarios.txt"
  } >"$BATS_TEST_TMPDIR/go-output"
  run_gate
  [ "$status" -eq 1 ]
  [[ $stderr == *"only 2 scenarios"* ]]
  : >"$MODULE/internal/prchange/testdata/lane-e2e-scenarios.txt"
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

@test "an unknown argument is a usage error" {
  run --separate-stderr bash "$SCRIPT" --nope
  [ "$status" -eq 2 ]
}

# Operator standard V-4 (bead pg2-by94zu): a multi-process end-to-end suite MUST NOT
# gate a commit or a land, but MUST keep its own check so it can still fail (ADR 0024
# of phillipg-nix-repo-base). Reads the flake source, so it skips off the repo.
@test "the suite is a nix check running the real test, and is not a pre-commit hook" {
  flake="$BATS_TEST_DIRNAME/../flake.nix"
  [ -f "$flake" ] || skip "flake.nix is not next to the tests"
  # no hook of that name, no hook wrapper, no path regex
  run grep -En '^ *name = "pg-decider-lane-e2e"|pg-decider-lane-e2e-hook|pg-decider-lane-e2e-files' "$flake"
  [ "$status" -eq 1 ]
  [ ! -e "$BATS_TEST_DIRNAME/../scripts/pg-decider-lane-e2e-files.regex" ]
  # the check exists and runs the script that runs only TestLaneE2E
  grep -Eq '^ *pg-decider-lane-e2e =$' "$flake"
  grep -q 'run-pg-decider-lane-e2e.sh} --prebuilt' "$flake"
  grep -q "go test .*-run '\^TestLaneE2E\\$'" "$SCRIPT"
}
