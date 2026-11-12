#!/usr/bin/env bats
# bats file_tags=type:unit
# Guard for scripts/check-no-contract-tag-in-hooks.sh: no hook entry in flake.nix
# (or any script a hook execs) may run `go test -tags contract`. The contract
# real-bd suites live in checks.<system>.*-bd-contract-tests (HK-2, nix-repo-base
# flake-modules/pre-commit.nix). Deterministic text scan, no external tool.

GUARD="$BATS_TEST_DIRNAME/../scripts/check-no-contract-tag-in-hooks.sh"
ROOT="$BATS_TEST_DIRNAME/.."

@test "hook definitions in flake.nix and scripts/*.sh carry no -tags contract" {
  run "$GUARD" "$ROOT/flake.nix" ""/scripts/*.sh
  [ "$status" -eq 0 ]
}

@test "the three real-bd suites are checks, one named *-bd-contract-tests each" {
  for n in pg-pr-change pg-router-ccpool-handler work-report; do
    grep -q "^              $n-bd-contract-tests =" "$ROOT/flake.nix"
  done
}

@test "negative control: a hook entry running go test -tags contract is flagged" {
  f="$BATS_TEST_TMPDIR/hook.nix"
  printf '%s\n' 'exec nice -n 10 go test -p 4 -count=1 -tags contract -timeout 10m ./x/' >"$f"
  run "$GUARD" "$f"
  [ "$status" -eq 1 ]
  [[ "$output" == *"hook.nix:1:"* ]]
}

@test "negative control: -tags=contract and tag lists are flagged too" {
  f="$BATS_TEST_TMPDIR/hook.sh"
  printf '%s\n' 'go test -tags=contract ./x/' 'go test -tags "integration,contract" ./y/' >"$f"
  run "$GUARD" "$f"
  [ "$status" -eq 1 ]
  [[ "$output" == *"hook.sh:1:"* ]]
  [[ "$output" == *"hook.sh:2:"* ]]
}

@test "comments and other tags are not flagged" {
  f="$BATS_TEST_TMPDIR/ok.nix"
  printf '%s\n' '# go test -tags contract is documented here' 'go test -tags integration ./x/' 'go test -tags contractual ./x/' >"$f"
  run "$GUARD" "$f"
  [ "$status" -eq 0 ]
}

@test "no arguments is a usage error" {
  run "$GUARD"
  [ "$status" -eq 2 ]
}
