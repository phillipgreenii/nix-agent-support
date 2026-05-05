# shellcheck shell=bash

setup() {
  # SCRIPTS_DIR: injected by nix check, or computed relative to test file
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi

  # LIB_PATH: injected by nix check (composed library file or dir)
  if [[ -z ${LIB_PATH:-} ]]; then
    LIB_PATH="$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../lib" && pwd)"
  fi

  # Standard test isolation
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  export REAL_HOME="$HOME"
  export HOME="$TEST_DIR/home"
  mkdir -p "$HOME"
}

teardown() {
  rm -rf "$TEST_DIR"
}
