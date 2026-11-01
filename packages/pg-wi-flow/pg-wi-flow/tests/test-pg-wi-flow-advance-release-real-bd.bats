#!/usr/bin/env bats
# bats file_tags=type:integration
# Real-bd advance releases the caller's claim (tc-n3tzb). `advance` used to
# swap the stage label and leave the previous stage worker's claim on the item,
# so it stayed in_progress under a subagent identity at a stage nobody was
# working and `next` could not hand it to the next stage's worker until the
# /drain stale-reservation sweep. These tests run the real pg-wi-flow CLI
# against a REAL throwaway embedded-Dolt `bd` database created by `bd init`
# inside the hermetic git-fixture-harness repo (same pattern as
# test-pg-wi-flow-escalate-real-bd.bats) -- never the shared remote server,
# never a dolt sql-server. Skips when `bd` is not on PATH (the nix check
# sandbox).
bats_require_minimum_version 1.5.0

setup() {
  if ! command -v bd >/dev/null 2>&1; then
    skip "bd not on PATH; real-bd advance-release tests need it"
  fi
  local gfh_lib="${GFH_LIB:-}"
  if [[ -z $gfh_lib ]]; then
    echo "GFH_LIB is not set: commit via the run-unit-tests hook, run under the workspace .envrc (direnv, or direnv exec <workspace-root> ...), or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # shellcheck disable=SC1090,SC1091 # harness path is runtime-provided
  source "$gfh_lib"
  gfh_save_env SCRIPTS_DIR SCRIPT_UNDER_TEST TEST_SUPPORT GFH_LIB
  gfh_setup "pg-wi-flow-advance-release-real-bd"
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
  if ! bd init --non-interactive --prefix ad -q >"$GFH_WORK/init.log" 2>&1; then
    cat "$GFH_WORK/init.log" >&2
    skip "bd init could not create a throwaway embedded database"
  fi
  [[ "$(jq -r '.dolt_mode // empty' .beads/metadata.json)" == embedded ]] ||
    skip "throwaway bd database is not embedded; refusing to run"

  export PG_WI_FLOW_IDENT="aaaaaaaa-main-worker"
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

make_item() {
  bd create "$1" -t task --json | bd_item | jq -r '.id'
}

show_json() {
  bd show "$1" --json | bd_item
}

# two_stage_config -- a repo overlay with a groom -> implement workflow, so
# `advance --to implement` is a legal move and `next --stage implement` a query.
two_stage_config() {
  mkdir -p "$GFH_REPO/.claude/wi-flow"
  printf '%s\n' '{"workflows":{"flow":{"primary":true,"stages":{"groom":{"order":1,"entry":true},"implement":{"order":2,"closes":true}}}}}' \
    >"$GFH_REPO/.claude/wi-flow/config.json"
}

# claim_as_worker ID -- the dispatcher reserves ID, the worker (same session)
# transfers the claim to itself.
claim_as_worker() {
  PG_WI_FLOW_IDENT="aaaaaaaa-main-dispatcher" run --separate-stderr $SCRIPT next --id "$1"
  [ "$status" -eq 0 ]
  PG_WI_FLOW_IDENT="aaaaaaaa-bbbb-worker" run --separate-stderr $SCRIPT claim "$1"
  [ "$status" -eq 0 ] || {
    echo "claim failed: $stderr" >&2
    return 1
  }
  [ "$(show_json "$1" | jq -r '.status')" = in_progress ]
}

@test "advance releases the caller's claim: unclaimed, open and visible to next for the new stage on real bd" {
  local id
  two_stage_config
  id="$(make_item "item the groom worker advances")"
  claim_as_worker "$id"

  PG_WI_FLOW_IDENT="aaaaaaaa-bbbb-worker" run --separate-stderr $SCRIPT advance "$id" --to implement
  [ "$status" -eq 0 ] || {
    echo "advance failed: $stderr" >&2
    return 1
  }
  [ "$(show_json "$id" | jq -r '.assignee // ""')" = "" ]
  [ "$(show_json "$id" | jq -r '.status')" = open ]
  [ "$(show_json "$id" | jq -r '.labels | index("stage:implement") != null')" = true ]

  PG_WI_FLOW_IDENT="aaaaaaaa-main-dispatcher" run --separate-stderr $SCRIPT next --stage implement
  [ "$status" -eq 0 ] || {
    echo "next failed: $stderr" >&2
    return 1
  }
  [[ $output == *"$id"* ]]
}

@test "advance does not touch a claim held by a DIFFERENT session on real bd" {
  local id
  two_stage_config
  id="$(make_item "foreign claim")"
  PG_WI_FLOW_IDENT="cccccccc-main-dispatcher" run --separate-stderr $SCRIPT next --id "$id"
  [ "$status" -eq 0 ]

  PG_WI_FLOW_IDENT="aaaaaaaa-bbbb-worker" run --separate-stderr $SCRIPT advance "$id" --to implement
  [ "$status" -eq 0 ]
  [ "$(show_json "$id" | jq -r '.assignee // ""')" != "" ]
  [ "$(show_json "$id" | jq -r '.status')" = in_progress ]
}
