#!/usr/bin/env bats
# bats file_tags=type:integration
# Real-bd regression guards for the pg-wi-flow dry-run defects observed while
# running Phase 2 packet P11 (tc-9ddu3.2.11): the kind:<k> label vanishing,
# the worker's claim surviving an escalation (dispatcher release refused), and
# the missing top-level-session / PG_WI_FLOW_IDENT precondition. These run the
# real pg-wi-flow CLI against a REAL throwaway embedded-Dolt `bd` database
# created by `bd init` inside the hermetic git-fixture-harness repo (same
# pattern as test-pg-wi-flow-escalate-real-bd.bats) -- never the shared remote
# server, never a dolt sql-server. Skips when `bd` is not on PATH.
bats_require_minimum_version 1.5.0

setup() {
  if ! command -v bd >/dev/null 2>&1; then
    skip "bd not on PATH; real-bd defect tests need it"
  fi
  local gfh_lib="${GFH_LIB:-}"
  if [[ -z $gfh_lib ]]; then
    echo "GFH_LIB is not set: commit via the run-unit-tests hook, run under the workspace .envrc (direnv, or direnv exec <workspace-root> ...), or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # shellcheck disable=SC1090,SC1091 # harness path is runtime-provided
  source "$gfh_lib"
  gfh_save_env SCRIPTS_DIR SCRIPT_UNDER_TEST TEST_SUPPORT GFH_LIB
  gfh_setup "pg-wi-flow-defects-real-bd"
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
  if ! bd init --non-interactive --prefix df -q >"$GFH_WORK/init.log" 2>&1; then
    cat "$GFH_WORK/init.log" >&2
    skip "bd init could not create a throwaway embedded database"
  fi
  [[ "$(jq -r '.dolt_mode // empty' .beads/metadata.json)" == embedded ]] ||
    skip "throwaway bd database is not embedded; refusing to run"

  unset PG_WI_FLOW_IDENT
}

teardown() {
  if declare -F gfh_teardown >/dev/null; then
    gfh_teardown
  fi
  return 0
}

bd_item() {
  jq -c '(if type=="object" and has("data") then .data else . end) | if type=="array" then .[0] else . end'
}

make_item() {
  local title="$1"
  shift
  bd create "$title" -t task "$@" --json | bd_item | jq -r '.id'
}

show_json() {
  bd show "$1" --json | bd_item
}

# --- D-A: annotate --kind must never leave the item without its kind label ---

@test "annotate --kind with the kind the item already carries keeps kind:<k> on real bd" {
  local id
  id="$(make_item "groomed item" --labels kind:feature)"
  PG_WI_FLOW_IDENT="aaaaaaaa-main-worker" run --separate-stderr $SCRIPT annotate "$id" --kind feature
  [ "$status" -eq 0 ] || {
    echo "annotate failed: $stderr" >&2
    return 1
  }
  [ "$(show_json "$id" | jq -r '[.labels[] | select(startswith("kind:"))] | join(",")')" = "kind:feature" ]
}

@test "annotate --kind with a different kind replaces the old kind label on real bd" {
  local id
  id="$(make_item "groomed item" --labels kind:feature)"
  PG_WI_FLOW_IDENT="aaaaaaaa-main-worker" run --separate-stderr $SCRIPT annotate "$id" --kind bug
  [ "$status" -eq 0 ] || {
    echo "annotate failed: $stderr" >&2
    return 1
  }
  [ "$(show_json "$id" | jq -r '[.labels[] | select(startswith("kind:"))] | join(",")')" = "kind:bug" ]
}

@test "annotate --component with the component the item already carries keeps component:<c> on real bd" {
  local id
  id="$(make_item "groomed item" --labels component:cli)"
  PG_WI_FLOW_IDENT="aaaaaaaa-main-worker" run --separate-stderr $SCRIPT annotate "$id" --component cli
  [ "$status" -eq 0 ] || {
    echo "annotate failed: $stderr" >&2
    return 1
  }
  [ "$(show_json "$id" | jq -r '[.labels[] | select(startswith("component:"))] | join(",")')" = "component:cli" ]
}

# --- D-B: the dispatcher can release what the worker left claimed ------------

@test "dispatcher release succeeds on an item the worker (same session) claimed and escalated on real bd" {
  local id
  id="$(make_item "item the worker escalates")"
  # dispatcher reserves, worker transfers, worker escalates (still holding the claim)
  PG_WI_FLOW_IDENT="aaaaaaaa-main-dispatcher" run --separate-stderr $SCRIPT next --id "$id"
  [ "$status" -eq 0 ] || {
    echo "next failed: $stderr" >&2
    return 1
  }
  PG_WI_FLOW_IDENT="aaaaaaaa-bbbb-worker" run --separate-stderr $SCRIPT claim "$id"
  [ "$status" -eq 0 ] || {
    echo "claim failed: $stderr" >&2
    return 1
  }
  PG_WI_FLOW_IDENT="aaaaaaaa-bbbb-worker" run --separate-stderr $SCRIPT escalate "$id" --question "which host?" --trigger q:stall
  [ "$status" -eq 0 ]

  PG_WI_FLOW_IDENT="aaaaaaaa-main-dispatcher" run --separate-stderr $SCRIPT release "$id"
  [ "$status" -eq 0 ] || {
    echo "release failed: $stderr" >&2
    return 1
  }
  [ "$(show_json "$id" | jq -r '.assignee // ""')" = "" ]
  [ "$(show_json "$id" | jq -r '.status')" = open ]
}

@test "escalate with --question leaves no claim on the escalated item on real bd" {
  local id
  id="$(make_item "item the worker escalates")"
  PG_WI_FLOW_IDENT="aaaaaaaa-main-dispatcher" run --separate-stderr $SCRIPT next --id "$id"
  [ "$status" -eq 0 ]
  PG_WI_FLOW_IDENT="aaaaaaaa-bbbb-worker" run --separate-stderr $SCRIPT claim "$id"
  [ "$status" -eq 0 ]
  PG_WI_FLOW_IDENT="aaaaaaaa-bbbb-worker" run --separate-stderr $SCRIPT escalate "$id" --question "which host?" --trigger q:stall
  [ "$status" -eq 0 ]
  [ "$(show_json "$id" | jq -r '.assignee // ""')" = "" ]
  [ "$(show_json "$id" | jq -r '.status')" = open ]
}

@test "release does not touch an item claimed by a DIFFERENT session on real bd" {
  local id
  id="$(make_item "foreign claim")"
  PG_WI_FLOW_IDENT="cccccccc-main-dispatcher" run --separate-stderr $SCRIPT next --id "$id"
  [ "$status" -eq 0 ]
  PG_WI_FLOW_IDENT="aaaaaaaa-main-dispatcher" run --separate-stderr $SCRIPT release "$id"
  [ "$status" -ne 0 ]
  [[ "$(show_json "$id" | jq -r '.assignee // ""')" == cccccccc-* ]]
}

# --- D-C: fail fast with an actionable message when the identity is missing ---

@test "next without PG_WI_FLOW_IDENT fails fast and names the top-level-session requirement" {
  make_item "ready item" >/dev/null
  run --separate-stderr $SCRIPT next
  [ "$status" -ne 0 ]
  [[ $stderr == *PG_WI_FLOW_IDENT* ]]
  [[ $stderr == *"top-level"* ]]
}

@test "next without PG_WI_FLOW_IDENT fails fast even when the queue is empty" {
  run --separate-stderr $SCRIPT next
  [ "$status" -ne 0 ]
  [[ $stderr == *"top-level"* ]]
}

@test "claim without PG_WI_FLOW_IDENT fails with the top-level-session hint" {
  local id
  id="$(make_item "ready item")"
  run --separate-stderr $SCRIPT claim "$id"
  [ "$status" -ne 0 ]
  [[ $stderr == *"top-level"* ]]
}

@test "an explicit --actor still lets next run without PG_WI_FLOW_IDENT" {
  run --separate-stderr $SCRIPT --actor someone next
  [ "$status" -eq 0 ] || {
    echo "next failed: $stderr" >&2
    return 1
  }
}
