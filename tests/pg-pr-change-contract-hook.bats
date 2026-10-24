#!/usr/bin/env bats
# bats file_tags=type:unit
# Regression guard for the `pg-pr-change-contract-test` hook (docket pg2-6mo45.3,
# packet P3b1b): the path regex it fires on and the script that is its body.
#
# Deterministic logic (global rule V-3). The regex is read from the same file the
# flake reads, and fed one real path of every input of every binary the real-tracker
# tests build or run, so a rename that leaves an input outside the regex fails here
# instead of silently dropping the gate. The script is run for real against PATH
# shims for `go` and `bd`: they stand in for the tools, and the thing under test is
# which tool absence is fatal and what `go test` is asked to run.

bats_require_minimum_version 1.5.0

SCRIPT="$BATS_TEST_DIRNAME/../scripts/run-pg-pr-change-contract.sh"
REGEX_FILE="$BATS_TEST_DIRNAME/../scripts/pg-pr-change-contract-files.regex"

setup() {
  SHIM="$BATS_TEST_TMPDIR/shim"
  ROOT="$BATS_TEST_TMPDIR/root"
  mkdir -p "$SHIM" "$ROOT/packages/pg-decider"
  # `nice` is a coreutils program the script puts in front of go; a real one is
  # fine, but the shim dir must come first so the fake go is found.
  NICE="$(command -v nice)"
  ln -s "$NICE" "$SHIM/nice"
}

shim() { # shim <tool> [body]: an executable PATH shim
  printf '#!/bin/sh\n%s\n' "${2:-exit 0}" >"$SHIM/$1"
  chmod +x "$SHIM/$1"
}

@test "the path regex matches one path of every input of every binary involved" {
  regex="$(cat "$REGEX_FILE")"
  [ -n "$regex" ]
  paths=(
    # pg-pr-change itself: its main, its logic, its test rig and the config, view
    # and work-item packages it links
    packages/pg-decider/cmd/pg-pr-change/main.go
    packages/pg-decider/internal/prchange/run.go
    packages/pg-decider/internal/prchange/realbd_contract_test.go
    # pg-pr-backfill, the backfill driver whose `enumerate` the same contract tests
    # run against a real bd (docket pg2-6mo45.3, packet P4b): its main, its listing,
    # its run loop and its rate-limit sources
    packages/pg-decider/cmd/pg-pr-backfill/main.go
    packages/pg-decider/internal/backfill/set.go
    packages/pg-decider/internal/backfill/run.go
    packages/pg-decider/internal/backfill/rate.go
    packages/pg-decider/internal/parity/fakeconnector.go
    packages/pg-decider/internal/config/config.go
    packages/pg-decider/internal/view/view.go
    packages/pg-decider/internal/workitem/index.go
    packages/pg-decider/go.mod
    packages/pg-decider/go.sum
    # pg-desk (refresh, mark-inactive, the store)
    packages/pg-desk/cmd/pg-desk/lane.go
    packages/pg-desk/internal/changes/refreshclass.go
    packages/pg-desk/internal/store/unhydrated.go
    packages/pg-desk/go.mod
    packages/pg-desk/go.sum
    # pg-connector (the umbrella, the beads backend and the shared wire packages)
    packages/pg-connector/cmd/pg-connector/search.go
    packages/pg-connector/cmd/pg-connector-issue-beads/internal/search.go
    packages/pg-connector/pkg/schema/search.go
    packages/pg-connector/go.mod
    packages/pg-connector/go.sum
    # the hook's own body
    scripts/run-pg-pr-change-contract.sh
  )
  for p in "${paths[@]}"; do
    if ! printf '%s\n' "$p" | grep -Eq -- "$regex"; then
      echo "the hook regex does not match $p" >&2
      return 1
    fi
  done
}

@test "the path regex leaves unrelated paths alone" {
  regex="$(cat "$REGEX_FILE")"
  for p in \
    packages/pg-router/cmd/pg-router/main.go \
    packages/pg-decider/internal/rules/anchor.go \
    packages/pg-connector/cmd/pg-connector-github/main.go \
    docs/behavior/pg-decider/pr-change.md \
    scripts/run-tmp-sweep-bats.sh; do
    if printf '%s\n' "$p" | grep -Eq -- "$regex"; then
      echo "the hook regex matches the unrelated path $p" >&2
      return 1
    fi
  done
}

@test "every file the regex names as an input exists in the repo" {
  # A regex whose literal directories no longer exist would match nothing and
  # never fire. Each directory prefix must be a real directory.
  cd "$BATS_TEST_DIRNAME/.."
  for d in packages/pg-decider/cmd/pg-pr-change packages/pg-decider/cmd/pg-pr-backfill \
    packages/pg-decider/internal/prchange packages/pg-decider/internal/backfill \
    packages/pg-decider/internal/parity packages/pg-decider/internal/config \
    packages/pg-decider/internal/view packages/pg-decider/internal/workitem \
    packages/pg-desk/cmd/pg-desk packages/pg-desk/internal \
    packages/pg-connector/cmd/pg-connector packages/pg-connector/cmd/pg-connector-issue-beads \
    packages/pg-connector/pkg; do
    [ -d "$d" ] || {
      echo "missing directory $d" >&2
      return 1
    }
  done
  [ -f scripts/run-pg-pr-change-contract.sh ]
}

@test "a missing bd fails loudly with exit 2 and names the tool" {
  shim go
  run --separate-stderr env PATH="$SHIM" "$BASH" "$SCRIPT" --root "$ROOT"
  [ "$status" -eq 2 ]
  [[ $stderr == *"bd not found on PATH"* ]]
  [[ $stderr == *"Do NOT bypass with --no-verify"* ]]
}

@test "a missing go fails loudly with exit 2 and names the tool" {
  shim bd
  run --separate-stderr env PATH="$SHIM" "$BASH" "$SCRIPT" --root "$ROOT"
  [ "$status" -eq 2 ]
  [[ $stderr == *"go not found on PATH"* ]]
}

@test "with both tools it runs the contract-tagged real-bd tests and no others" {
  shim bd
  shim go 'printf "%s\n" "$*" >"'"$BATS_TEST_TMPDIR"'/go-args"; printf "%s\n" "$(pwd -P)" >"'"$BATS_TEST_TMPDIR"'/go-pwd"'
  run --separate-stderr env PATH="$SHIM" "$BASH" "$SCRIPT" --root "$ROOT"
  [ "$status" -eq 0 ]
  args="$(cat "$BATS_TEST_TMPDIR/go-args")"
  [[ $args == *"-tags contract"* ]]
  [[ $args == *"-run ^TestPRChangeRealBD_"* ]]
  [[ $args == *"./internal/prchange/"* ]]
  [ "$(cat "$BATS_TEST_TMPDIR/go-pwd")" = "$(cd "$ROOT/packages/pg-decider" && pwd -P)" ]
}

@test "the backfill driver's real-bd enumerate test is among the tests the hook runs" {
  # The hook runs -run ^TestPRChangeRealBD_; the driver's test must carry the prefix,
  # and must sit in the contract-tagged file the hook's package builds.
  grep -q '^func TestPRChangeRealBD_BackfillEnumerate' "$BATS_TEST_DIRNAME/../packages/pg-decider/internal/prchange/realbd_contract_test.go"
  head -1 "$BATS_TEST_DIRNAME/../packages/pg-decider/internal/prchange/realbd_contract_test.go" | grep -q '^//go:build contract$'
}

@test "a path of the driver matches and a path of its docs does not" {
  regex="$(cat "$REGEX_FILE")"
  printf '%s\n' packages/pg-decider/internal/backfill/summary.go | grep -Eq -- "$regex"
  ! printf '%s\n' docs/behavior/pg-decider/backfill.md | grep -Eq -- "$regex"
}

@test "a failing test run fails the gate with its own exit code" {
  shim bd
  shim go 'exit 7'
  run --separate-stderr env PATH="$SHIM" "$BASH" "$SCRIPT" --root "$ROOT"
  [ "$status" -eq 7 ]
}

@test "an unknown argument is a usage error" {
  run --separate-stderr bash "$SCRIPT" --nope
  [ "$status" -eq 2 ]
}
