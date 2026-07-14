#!/usr/bin/env bats

setup() {
  # SCRIPTS_DIR: injected by nix check (raw src dir), or computed relative to
  # this test file for a local `bats tests/` run. MUST honor an already-set
  # env var — the nix check harness copies tests/* flat into a bare $TMPDIR,
  # so recomputing unconditionally from BATS_TEST_FILENAME would resolve to
  # the wrong directory there.
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  BIN="${SCRIPTS_DIR}/integrate-branch-support.sh"
  TEST_DIR="$(mktemp -d)"
  cd "$TEST_DIR" || return 1
  git init -q --initial-branch=main
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m init
}

teardown() {
  rm -rf "$TEST_DIR"
}

@test "prints valid JSON with a strategy field" {
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e 'has("strategy")'
}
