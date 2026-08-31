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
  export BG_DIR="$TEST_DIR/bg"
}

teardown() {
  rm -rf "$TEST_DIR"
}

resolve_lib() {
  if [[ -d ${LIB_PATH} ]]; then
    echo "${LIB_PATH}/bg-tools-lib.bash"
  else
    echo "${LIB_PATH%%:*}"
  fi
}

# Replicates the builder's composition: library sourced ahead of the .sh.
# $1 = script name (bgrun | bgcheck); creates $TEST_DIR/run_<name>.
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

# Bounded wait for a file to appear (a launched job's exit record).
wait_for_file() {
  local file="$1" tries="${2:-100}"
  while [[ ! -f $file && $tries -gt 0 ]]; do
    sleep 0.05
    tries=$((tries - 1))
  done
  [[ -f $file ]]
}
