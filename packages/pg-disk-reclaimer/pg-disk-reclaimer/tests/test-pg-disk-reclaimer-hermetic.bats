#!/usr/bin/env bats
# Guard for the test isolation itself (bead pg2-kb5dby; helper: tests/hermetic.bash).
#
# The tool reads its registry from ${XDG_CONFIG_HOME:-$HOME/.config} and runs the
# commands that registry names, so a test that reaches the CLI with an ambient
# environment can run the operator's real registry (a reviewer's hand run did, on
# 2026-10-28). Every suite isolates its environment and asserts it did; these tests
# prove the helper does that, using a CANARY registry (in a temp dir, so nothing
# real is ever at risk) whose commands touch marker files:
#   - the control run shows the canary DOES fire when the CLI reads it, so a silent
#     canary below means something;
#   - the isolated runs show it never fires once the environment is isolated, with
#     a hostile ambient environment pointing straight at it.
# This suite deliberately does NOT isolate in setup(): it builds its own hostile
# environment per test, then isolates.
bats_require_minimum_version 1.5.0
load 'hermetic'

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  SCRIPT="$SCRIPTS_DIR/pg-disk-reclaimer.sh"

  CANARY_ROOT="$(mktemp -d)"
  ISO_ROOT="$(mktemp -d)"
  mkdir -p "$CANARY_ROOT/xdg/pg-disk-reclaimer" "$CANARY_ROOT/area"
  jq -n --arg c "$CANARY_ROOT" --arg area "$CANARY_ROOT/area" '[{
    id: "canary",
    description: "touches a marker when any of its commands runs",
    path: $area,
    displayCommand: "touch \($c)/display-ran; echo canary",
    sizeCommand: "touch \($c)/size-ran; echo 1",
    variants: [{
      aggressiveness: 1,
      variantDescription: "canary",
      dryRunCommand: "touch \($c)/dry-ran; echo dry-canary",
      removeCommand: "touch \($c)/remove-ran; echo remove-canary"
    }]
  }]' >"$CANARY_ROOT/xdg/pg-disk-reclaimer/registry.json"
}

teardown() {
  rm -rf "$CANARY_ROOT" "$ISO_ROOT"
}

# A hostile ambient environment: everything the tool or its helpers read from the
# environment points OUTSIDE the isolation root, and the registry at the canary.
hostile_env() {
  export HOME="$CANARY_ROOT/hostile-home"
  export XDG_CONFIG_HOME="$CANARY_ROOT/xdg"
  export XDG_STATE_HOME="$CANARY_ROOT/hostile-state"
  export XDG_CACHE_HOME="$CANARY_ROOT/hostile-cache"
  export XDG_DATA_HOME="$CANARY_ROOT/hostile-data"
  export TMPDIR="$CANARY_ROOT/hostile-tmp"
  export PGDR_SIZE_TIMEOUT_SECONDS=1
  export PGDR_DISPLAY_TIMEOUT_SECONDS=1
  export PGDR_LONG_LINE_CHARS=1
}

canary_markers() {
  local f
  for f in display-ran size-ran dry-ran remove-ran; do
    [[ -e $CANARY_ROOT/$f ]] && printf '%s ' "$f"
  done
  return 0
}

@test "control: with an ambient XDG_CONFIG_HOME and no isolation the real CLI runs the canary registry's commands" {
  run env XDG_CONFIG_HOME="$CANARY_ROOT/xdg" bash "$SCRIPT" reclaim --aggressiveness 1 --apply </dev/null
  [ "$status" -eq 0 ]
  [[ "$output" == *"remove-canary"* ]]
  [ -e "$CANARY_ROOT/remove-ran" ]
}

@test "isolation: a hostile ambient environment is moved under the root and XDG_CONFIG_HOME is unset" {
  hostile_env
  pgdr_test_isolate "$ISO_ROOT"
  pgdr_test_assert_hermetic "$ISO_ROOT"
  [[ $HOME == "$ISO_ROOT"/* ]]
  [[ $TMPDIR == "$ISO_ROOT"/* ]]
  [[ $XDG_STATE_HOME == "$ISO_ROOT"/* ]]
  [[ $XDG_CACHE_HOME == "$ISO_ROOT"/* ]]
  [[ $XDG_DATA_HOME == "$ISO_ROOT"/* ]]
  [ -z "${XDG_CONFIG_HOME+set}" ]
}

@test "isolation: exported PGDR_ tunables are dropped, the harness switches and the sweep library path are kept" {
  hostile_env
  export PGDR_REQUIRE_PTY=1 PGDR_REQUIRE_SWEEP_TOOLS=1 PGDR_TMP_SWEEP_LIB=/somewhere/lib.bash
  pgdr_test_isolate "$ISO_ROOT"
  [ -z "${PGDR_SIZE_TIMEOUT_SECONDS+set}" ]
  [ -z "${PGDR_DISPLAY_TIMEOUT_SECONDS+set}" ]
  [ -z "${PGDR_LONG_LINE_CHARS+set}" ]
  [ "$PGDR_REQUIRE_PTY" = 1 ]
  [ "$PGDR_REQUIRE_SWEEP_TOOLS" = 1 ]
  [ "$PGDR_TMP_SWEEP_LIB" = /somewhere/lib.bash ]
  pgdr_test_assert_hermetic "$ISO_ROOT"
}

@test "assertion: it fails, naming the variable, for each way the environment can leak" {
  pgdr_test_isolate "$ISO_ROOT"

  HOME="$CANARY_ROOT/elsewhere" run pgdr_test_assert_hermetic "$ISO_ROOT"
  [ "$status" -ne 0 ]
  [[ $output == *"HOME"* ]]

  TMPDIR="$CANARY_ROOT/elsewhere" run pgdr_test_assert_hermetic "$ISO_ROOT"
  [ "$status" -ne 0 ]
  [[ $output == *"TMPDIR"* ]]

  XDG_CONFIG_HOME="$CANARY_ROOT/xdg" run pgdr_test_assert_hermetic "$ISO_ROOT"
  [ "$status" -ne 0 ]
  [[ $output == *"registry would be read from"* ]]

  PGDR_SIZE_TIMEOUT_SECONDS=1 run pgdr_test_assert_hermetic "$ISO_ROOT"
  [ "$status" -ne 0 ]
  [[ $output == *"PGDR_SIZE_TIMEOUT_SECONDS"* ]]
}

@test "assertion: a hostile environment that was never isolated fails it" {
  hostile_env
  run pgdr_test_assert_hermetic "$ISO_ROOT"
  [ "$status" -ne 0 ]
}

@test "after isolation the real CLI never reaches an ambient registry: list, validate, dry run and --apply leave the canary untouched" {
  hostile_env
  pgdr_test_isolate "$ISO_ROOT"
  pgdr_test_assert_hermetic "$ISO_ROOT"

  run bash "$SCRIPT" list --aggressiveness 5
  run bash "$SCRIPT" validate
  run bash "$SCRIPT" reclaim --aggressiveness 5 </dev/null
  run bash "$SCRIPT" reclaim --aggressiveness 5 --apply </dev/null
  [ -z "$(canary_markers)" ]
}
