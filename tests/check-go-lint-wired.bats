#!/usr/bin/env bats
# bats file_tags=type:unit
# Regression guard for scripts/check-go-lint-wired.sh (part of the go-deps-wired
# check): a Go module with no `<module>-golangci` check must fail unless named in
# the exemption file. Deterministic logic: the REAL script runs against a
# throwaway packages/ tree.

SCRIPT="$BATS_TEST_DIRNAME/../scripts/check-go-lint-wired.sh"

setup() {
  ROOT="$BATS_TEST_TMPDIR/root"
  LINTED="$BATS_TEST_TMPDIR/linted.txt"
  EXEMPT="$BATS_TEST_TMPDIR/exempt.txt"
  mkdir -p "$ROOT/packages/alpha" "$ROOT/packages/beta" "$ROOT/packages/notgo"
  : >"$ROOT/packages/alpha/go.mod"
  : >"$ROOT/packages/beta/go.mod"
  printf 'alpha-golangci\nbeta-golangci\n' >"$LINTED"
  printf '# nothing exempt\n' >"$EXEMPT"
}

run_check() { run bash "$SCRIPT" "$ROOT" "$LINTED" "$EXEMPT"; }

@test "every module linted passes (a dir without go.mod is ignored)" {
  run_check
  [ "$status" -eq 0 ]
}

@test "a new Go module with no lint check fails and is named" {
  mkdir "$ROOT/packages/gamma"
  : >"$ROOT/packages/gamma/go.mod"
  run_check
  [ "$status" -eq 1 ]
  [[ "$output" == *"gamma: packages/gamma/go.mod has no gamma-golangci flake check"* ]]
}

@test "naming the new module in the exemption file makes it pass" {
  mkdir "$ROOT/packages/gamma"
  : >"$ROOT/packages/gamma/go.mod"
  printf 'gamma # reason\n' >"$EXEMPT"
  run_check
  [ "$status" -eq 0 ]
}

@test "an exemption for a module that is now linted fails" {
  printf 'alpha\n' >"$EXEMPT"
  run_check
  [ "$status" -eq 1 ]
  [[ "$output" == *"alpha: has a alpha-golangci check but is still listed as exempt"* ]]
}

@test "an exemption for a module that does not exist fails" {
  printf 'ghost\n' >"$EXEMPT"
  run_check
  [ "$status" -eq 1 ]
  [[ "$output" == *"ghost: exempt but packages/ghost/go.mod does not exist"* ]]
}
