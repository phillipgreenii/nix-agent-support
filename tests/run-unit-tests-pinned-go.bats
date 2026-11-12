#!/usr/bin/env bats
# bats file_tags=type:unit
# The `run-unit-tests` hook wrapper must run the suite under the flake's pinned
# go and export GOTOOLCHAIN=local, even when an older go is earlier on the
# caller's PATH. pg-test-runner itself stays ambient (a fake here).
# Env (set by the flake check run-unit-tests-pinned-go): WRAPPER, PINNED_GO_BIN.

setup() {
  [[ -n "${WRAPPER-}" && -n "${PINNED_GO_BIN-}" ]] ||
    skip "WRAPPER/PINNED_GO_BIN come from the flake check run-unit-tests-pinned-go"
  work="$BATS_TEST_TMPDIR"
  mkdir -p "$work/fakebin" "$work/repo"
  git -C "$work/repo" init -q -b main
  cat >"$work/fakebin/go" <<'FAKE'
#!/bin/sh
echo "fake old go"
FAKE
  cat >"$work/fakebin/pg-test-runner" <<'FAKE'
#!/bin/sh
echo "GO_PATH=$(command -v go)"
echo "GOTOOLCHAIN=${GOTOOLCHAIN-<unset>}"
FAKE
  chmod +x "$work/fakebin/go" "$work/fakebin/pg-test-runner"
}

run_wrapper() {
  cd "$work/repo" || return 1
  run env -u NIX_BUILD_TOP -u IN_NIX_BUILD -u GOTOOLCHAIN \
    PATH="$work/fakebin:$PATH" "$WRAPPER" some/file
}

@test "pinned go shadows an older go earlier on PATH" {
  run_wrapper
  [ "$status" -eq 0 ]
  [[ "$output" == *"GO_PATH=$PINNED_GO_BIN/go"* ]]
  [[ "$output" != *"$work/fakebin/go"* ]]
}

@test "GOTOOLCHAIN=local is exported to the runner" {
  run_wrapper
  [ "$status" -eq 0 ]
  [[ "$output" == *"GOTOOLCHAIN=local"* ]]
}
