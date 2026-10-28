#!/usr/bin/env bats
# bats file_tags=type:unit
# Regression guard for scripts/check-section-sign-citations.sh, the body of the
# `section-sign-citations` hook (bead pg2-2yayms), which replaced the two Go tests and
# the nix check that each banned the section sign over part of the ccpool surface and
# pg-connector.
#
# Deterministic logic against the REAL script and a REAL git, in a hermetic fixture from
# the shared git fixture harness (global rule V-3). The fixture mirrors the guarded
# layout with just enough files to satisfy the script's liveness canaries, so the
# script's own built-in scope table is what is under test (no override hook exists).
# This file never contains the character it forbids: it is built from its UTF-8 bytes.

# GFH_LIB is the one required file (tc-4ehow): no filesystem search.
if [[ -z ${GFH_LIB:-} ]]; then
  echo "GFH_LIB is not set: commit via the run-unit-tests hook, run under the workspace .envrc (direnv, or direnv exec <workspace-root> ...), or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
  exit 1
fi
GFH_HARNESS_FILE="$GFH_LIB"
# shellcheck disable=SC1090
source "$GFH_HARNESS_FILE"

SCRIPT="$BATS_TEST_DIRNAME/../scripts/check-section-sign-citations.sh"
SUITE="section-sign-citations"

setup() {
  gfh_setup "$SUITE"
  SIGN="$(printf '\302\247')"
  ROOT="$GFH_REPO"

  # The five guarded surfaces, each with its required default.nix, plus filler to
  # reach the per-surface minimum file counts the canaries demand.
  local dir i
  for dir in home/programs/ccpool darwin/modules/ccpool nixos/modules/ccpool packages/ccpool packages/pg-connector; do
    mkdir -p "$ROOT/$dir"
    printf '{ }\n' >"$ROOT/$dir/default.nix"
  done
  mkdir -p "$ROOT/packages/ccpool/cmd" "$ROOT/packages/pg-connector/cmd" "$ROOT/packages/ccpool/testdata"
  for i in $(seq 1 25); do
    printf 'package x\n' >"$ROOT/packages/ccpool/cmd/f$i.go"
  done
  for i in $(seq 1 110); do
    printf 'package x\n' >"$ROOT/packages/pg-connector/cmd/f$i.go"
  done
  printf 'fixture\n' >"$ROOT/packages/ccpool/testdata/case.txt"

  # Out of scope on purpose: legitimate uses elsewhere in the repo.
  mkdir -p "$ROOT/docs/adr" "$ROOT/packages/other"
  printf 'See ADR 0038 %s7.\n' "$SIGN" >"$ROOT/docs/adr/0001-x.md"
  printf '// cites %s8.1\npackage other\n' "$SIGN" >"$ROOT/packages/other/o.go"

  git -C "$ROOT" add -A
  git -C "$ROOT" commit -q -m "fixture"
}

teardown() {
  gfh_teardown
}

# plant <relpath> -- writes a line carrying the sign on line 2 of <relpath> and tracks it.
plant() {
  mkdir -p "$ROOT/$(dirname "$1")"
  printf 'first line\nspec %s8.1 cited here\n' "$SIGN" >"$ROOT/$1"
  git -C "$ROOT" add -A
}

@test "clean fixture passes and reports what it scanned" {
  run bash "$SCRIPT" --root "$ROOT"
  [ "$status" -eq 0 ]
  [[ $output == *"OK:"*"5 surface(s)"* ]]
}

@test "a planted sign in a .go file in packages/pg-connector is flagged with file:line" {
  plant packages/pg-connector/cmd/planted.go
  run bash "$SCRIPT" --root "$ROOT"
  [ "$status" -eq 1 ]
  [[ $output == *"packages/pg-connector/cmd/planted.go:2: forbidden section sign"* ]]
}

@test "a planted sign in a .nix file on the ccpool surface is flagged" {
  plant home/programs/ccpool/extra.nix
  run bash "$SCRIPT" --root "$ROOT"
  [ "$status" -eq 1 ]
  [[ $output == *"home/programs/ccpool/extra.nix:2: forbidden section sign"* ]]
}

@test "a planted sign in a .md file in packages/ccpool is flagged" {
  plant packages/ccpool/README.md
  run bash "$SCRIPT" --root "$ROOT"
  [ "$status" -eq 1 ]
  [[ $output == *"packages/ccpool/README.md:2: forbidden section sign"* ]]
}

@test "a planted sign in a testdata file is flagged" {
  plant packages/ccpool/testdata/golden.json
  run bash "$SCRIPT" --root "$ROOT"
  [ "$status" -eq 1 ]
  [[ $output == *"packages/ccpool/testdata/golden.json:2: forbidden section sign"* ]]
}

@test "a planted sign in a _test.go file is flagged (tests are in scope)" {
  plant packages/ccpool/cmd/x_test.go
  run bash "$SCRIPT" --root "$ROOT"
  [ "$status" -eq 1 ]
  [[ $output == *"packages/ccpool/cmd/x_test.go:2:"* ]]
}

@test "every planted file is listed, not just the first" {
  plant packages/ccpool/a.go
  plant nixos/modules/ccpool/b.nix
  run bash "$SCRIPT" --root "$ROOT"
  [ "$status" -eq 1 ]
  [[ $output == *"packages/ccpool/a.go:2:"* ]]
  [[ $output == *"nixos/modules/ccpool/b.nix:2:"* ]]
}

@test "the same character OUTSIDE the scope is not flagged" {
  # docs/adr/0001-x.md and packages/other/o.go carry it from setup().
  plant docs/superpowers/plans/old.md
  plant packages/ccpool-sibling/x.go
  run bash "$SCRIPT" --root "$ROOT"
  [ "$status" -eq 0 ]
}

@test ".db, .out and .test files in scope are skipped" {
  plant packages/ccpool/state.db
  plant packages/pg-connector/cover.out
  plant packages/pg-connector/cmd/pkg.test
  run bash "$SCRIPT" --root "$ROOT"
  [ "$status" -eq 0 ]
}

@test "an untracked planted file is not scanned (hooks only see git-known files)" {
  printf 'x %s1\n' "$SIGN" >"$ROOT/packages/ccpool/untracked.go"
  run bash "$SCRIPT" --root "$ROOT"
  [ "$status" -eq 0 ]
}

@test "changed files outside the scope cost no scan, even when the scope is dirty" {
  plant packages/ccpool/a.go
  run bash "$SCRIPT" --root "$ROOT" README.md docs/adr/0001-x.md packages/other/o.go
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "a changed file inside the scope triggers the scan" {
  plant packages/ccpool/a.go
  run bash "$SCRIPT" --root "$ROOT" packages/ccpool/a.go
  [ "$status" -eq 1 ]
  [[ $output == *"packages/ccpool/a.go:2:"* ]]
}

@test "canary: an empty scope fails loudly instead of passing" {
  git -C "$ROOT" rm -rq packages/ccpool packages/pg-connector home/programs/ccpool darwin/modules/ccpool nixos/modules/ccpool
  run bash "$SCRIPT" --root "$ROOT"
  [ "$status" -eq 1 ]
  [[ $output == *"canary: packages/ccpool: never scanned packages/ccpool/default.nix"* ]]
  [[ $output == *"canary: packages/pg-connector: scanned only 0 file(s)"* ]]
}

@test "canary: the ccpool surface losing its .nix file fails" {
  git -C "$ROOT" rm -q home/programs/ccpool/default.nix
  run bash "$SCRIPT" --root "$ROOT"
  [ "$status" -eq 1 ]
  [[ $output == *"canary: home/programs/ccpool: never scanned home/programs/ccpool/default.nix"* ]]
}

@test "canary: a surface scanned below its minimum file count fails" {
  git -C "$ROOT" rm -q packages/pg-connector/cmd/f1*.go
  run bash "$SCRIPT" --root "$ROOT"
  [ "$status" -eq 1 ]
  [[ $output == *"canary: packages/pg-connector: scanned only"* ]]
}

@test "usage: an unknown option exits 2" {
  run bash "$SCRIPT" --nope
  [ "$status" -eq 2 ]
}
