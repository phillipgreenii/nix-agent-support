#!/usr/bin/env bats
# bats file_tags=type:integration
# Real-bd pointer close (tc-lx4nn). The pointer checklist says a Resume:
# bullet without a disposition blocks the close, but only `close --trace`
# enforced it: a plain `close` of a kind:pointer item with bullets succeeded.
# These tests run the real pg-wi-flow CLI against a REAL throwaway
# embedded-Dolt `bd` database created by `bd init` inside the hermetic
# git-fixture-harness repo (same pattern as test-pg-wi-flow-escalate-real-bd.bats)
# -- never the shared remote server, never a dolt sql-server. Skips when `bd`
# is not on PATH (the nix check sandbox).
bats_require_minimum_version 1.5.0

setup() {
  if ! command -v bd >/dev/null 2>&1; then
    skip "bd not on PATH; real-bd pointer close tests need it"
  fi
  local gfh_lib="${GFH_LIB:-}"
  if [[ -z $gfh_lib ]]; then
    echo "GFH_LIB is not set: commit via the run-unit-tests hook, run under the workspace .envrc (direnv, or direnv exec <workspace-root> ...), or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # shellcheck disable=SC1090,SC1091 # harness path is runtime-provided
  source "$gfh_lib"
  gfh_save_env SCRIPTS_DIR SCRIPT_UNDER_TEST TEST_SUPPORT GFH_LIB
  gfh_setup "pg-wi-flow-pointer-close-real-bd"
  gfh_restore_env

  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  SCRIPT="${SCRIPT_UNDER_TEST:-bash $SCRIPTS_DIR/pg-wi-flow.sh}"

  export BEADS_DOLT_AUTO_START=0 BD_NON_INTERACTIVE=1
  XDG_CONFIG_HOME="$GFH_WORK/xdg"
  mkdir -p "$XDG_CONFIG_HOME"
  export XDG_CONFIG_HOME
  cd "$GFH_REPO" || exit 1
  if ! bd init --non-interactive --prefix pc -q >"$GFH_WORK/init.log" 2>&1; then
    cat "$GFH_WORK/init.log" >&2
    skip "bd init could not create a throwaway embedded database"
  fi
  [[ "$(jq -r '.dolt_mode // empty' .beads/metadata.json)" == embedded ]] ||
    skip "throwaway bd database is not embedded; refusing to run"

  export PG_WI_FLOW_IDENT="aaaaaaaa-main-main"
}

teardown() {
  if declare -F gfh_teardown >/dev/null; then
    gfh_teardown
  fi
  return 0
}

# Unwrap bd's {"data": ...} envelope; the first element when it is an array.
bd_item() {
  jq -c '(if type=="object" and has("data") then .data else . end) | if type=="array" then .[0] else . end'
}

# make_item TITLE [bd create args...] -- prints the new id
make_item() {
  local title="$1"
  shift
  bd create "$title" -t task "$@" --json | bd_item | jq -r '.id'
}

item_status() {
  bd show "$1" --json | bd_item | jq -r '.status'
}

@test "plain close of a kind:pointer item with bullets is refused on real bd and leaves it open" {
  local id
  id="$(make_item "resume bead" --labels kind:pointer --description $'Resume:\n- first thing\n- second thing')"
  run --separate-stderr $SCRIPT close "$id" --reason "handled"
  [ "$status" -ne 0 ]
  [[ "$stderr" == *"--trace"* ]]
  [[ "$stderr" == *"first thing"* ]]
  [[ "$stderr" == *"second thing"* ]]
  [ "$(item_status "$id")" != closed ]
}

@test "close --trace with every bullet dispositioned closes a kind:pointer item on real bd" {
  local id
  id="$(make_item "resume bead" --labels kind:pointer --description $'Resume:\n- first thing\n- second thing')"
  run --separate-stderr $SCRIPT close "$id" --reason "handled" --trace "first thing=not needed" --trace "second thing=also not needed"
  [ "$status" -eq 0 ] || {
    echo "close failed: $stderr" >&2
    return 1
  }
  [ "$(item_status "$id")" = closed ]
}

@test "plain close of a kind:pointer item without bullets still closes on real bd" {
  local id
  id="$(make_item "resume bead" --labels kind:pointer --description "Resume: nothing further to follow up.")"
  run --separate-stderr $SCRIPT close "$id" --reason "handled"
  [ "$status" -eq 0 ] || {
    echo "close failed: $stderr" >&2
    return 1
  }
  [ "$(item_status "$id")" = closed ]
}

@test "plain close of a non-pointer item with bullets still closes on real bd" {
  local id
  id="$(make_item "ordinary bug" --labels kind:bug --description $'Repro:\n- step one\n- step two')"
  run --separate-stderr $SCRIPT close "$id" --reason "fixed"
  [ "$status" -eq 0 ] || {
    echo "close failed: $stderr" >&2
    return 1
  }
  [ "$(item_status "$id")" = closed ]
}
