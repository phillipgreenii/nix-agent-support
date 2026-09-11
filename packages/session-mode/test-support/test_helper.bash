# shellcheck shell=bash

setup() {
  # SCRIPTS_DIR: injected by nix check, or computed relative to test file
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi

  # LIB_PATH: injected by nix check (composed library file), or the source dir
  if [[ -z ${LIB_PATH:-} ]]; then
    LIB_PATH="$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../lib" && pwd)"
  fi

  # Standard test isolation
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  export REAL_HOME="$HOME"
  export HOME="$TEST_DIR/home"
  mkdir -p "$HOME"

  # Every test gets its own state dir; nothing touches the real default.
  export SESSION_MODE_STATE_DIR="$TEST_DIR/session-mode"
  export CLAUDE_SESSION_ID="test-sess-1"
}

teardown() {
  rm -rf "$TEST_DIR"
}

resolve_lib() {
  if [[ -d ${LIB_PATH} ]]; then
    echo "${LIB_PATH}/session-mode-lib.bash"
  else
    echo "${LIB_PATH%%:*}"
  fi
}

# Replicates the builder's composition: library sourced ahead of the .sh.
# $1 = script name (session-mode); creates $TEST_DIR/run_<name>.
create_cmd_wrapper() {
  local name="$1"
  cat >"$TEST_DIR/run_${name}" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "$(resolve_lib)"
source "${SCRIPTS_DIR}/${name}.sh"
WRAPPER
  chmod +x "$TEST_DIR/run_${name}"
}
