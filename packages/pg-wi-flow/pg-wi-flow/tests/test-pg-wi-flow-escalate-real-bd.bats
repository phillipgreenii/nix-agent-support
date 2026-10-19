#!/usr/bin/env bats
# bats file_tags=type:integration
# Real-bd escalate -> resolve round trip (tc-ir4en). `escalate --question`
# used to create the question with `bd create --parent <item>` and then add
# `bd dep add <item> <question>`; real bd 1.3.1 refuses that edge (a parent
# cannot be blocked by its own descendant, D-9), and only mock-bd tests
# covered the path, so the break was invisible. These tests run the real
# pg-wi-flow CLI against a REAL throwaway embedded-Dolt `bd` database created
# by `bd init` inside the hermetic git-fixture-harness repo (same pattern as
# test-pg-wi-flow-homelab-data.bats) -- never the shared remote server, never
# a dolt sql-server. Skips when `bd` is not on PATH (the nix check sandbox).
bats_require_minimum_version 1.5.0

setup() {
  if ! command -v bd >/dev/null 2>&1; then
    skip "bd not on PATH; real-bd escalate tests need it"
  fi
  local gfh_lib="${GFH_LIB:-}"
  if [[ -z $gfh_lib ]]; then
    echo "GFH_LIB is not set: commit via the run-unit-tests hook, run under the workspace .envrc (direnv, or direnv exec <workspace-root> ...), or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # shellcheck disable=SC1090,SC1091 # harness path is runtime-provided
  source "$gfh_lib"
  gfh_save_env SCRIPTS_DIR SCRIPT_UNDER_TEST TEST_SUPPORT GFH_LIB
  gfh_setup "pg-wi-flow-escalate-real-bd"
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
  if ! bd init --non-interactive --prefix er -q >"$GFH_WORK/init.log" 2>&1; then
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

# is_ready ID -- true when ID is in the `bd ready` set.
is_ready() {
  bd ready --json --limit 0 |
    jq -e --arg id "$1" '(if type=="object" and has("data") then .data else . end) | map(.id) | index($id) != null' >/dev/null
}

# question_of PARENT -- the id of the (single) question blocking PARENT.
question_of() {
  bd dep list "$1" --direction=down --type blocks --json |
    jq -r '(if type=="object" and has("data") then .data else . end) | map(select((.labels // []) | index("question") != null)) | .[0].id // empty'
}

show_json() {
  bd show "$1" --json | bd_item
}

@test "escalate --question succeeds on real bd: parentless question, originating_item recorded, parent blocked by it" {
  local parent qid
  parent="$(make_item "item that needs an answer")"
  run --separate-stderr $SCRIPT escalate "$parent" --question "which host?" --trigger q:info
  [ "$status" -eq 0 ] || {
    echo "escalate failed: $stderr" >&2
    return 1
  }

  qid="$(question_of "$parent")"
  [ -n "$qid" ]
  # not a bd child of the item it blocks; provenance is metadata
  [ "$(show_json "$qid" | jq -r '.parent // "none"')" = none ]
  [ "$(show_json "$qid" | jq -r '.metadata.originating_item')" = "$parent" ]
  [ "$(show_json "$qid" | jq -r '.labels | sort | join(",")')" = "escalated,q:info,question" ]
  # the real blocks edge hides the parent and leaves the question workable
  run ! is_ready "$parent"
  is_ready "$qid"
}

@test "escalate on a parent that already has a child item still succeeds on real bd" {
  local parent child qid
  parent="$(make_item "container-ish parent")"
  child="$(make_item "its child" --parent "$parent")"
  run --separate-stderr $SCRIPT escalate "$parent" --question "which host?" --trigger q:info
  [ "$status" -eq 0 ] || {
    echo "escalate failed: $stderr" >&2
    return 1
  }
  qid="$(question_of "$parent")"
  [ -n "$qid" ]
  [ "$(show_json "$qid" | jq -r '.parent // "none"')" = none ]
  [ -n "$child" ]
}

@test "escalate -> resolve --answer round trip on real bd: question closes and the parent is ready again" {
  local parent qid
  parent="$(make_item "item that needs an answer")"
  run --separate-stderr $SCRIPT escalate "$parent" --question "which host?" --trigger q:info
  [ "$status" -eq 0 ]
  qid="$(question_of "$parent")"
  run ! is_ready "$parent"

  run --separate-stderr $SCRIPT resolve "$qid" --answer "use host-a"
  [ "$status" -eq 0 ] || {
    echo "resolve failed: $stderr" >&2
    return 1
  }
  [ "$(show_json "$qid" | jq -r '.status')" = closed ]
  is_ready "$parent"
}

@test "escalate -> resolve --abandon round trip on real bd: the last open blocker's abandonment closes the parent" {
  local parent qid
  parent="$(make_item "item that needs an answer")"
  run --separate-stderr $SCRIPT escalate "$parent" --question "which host?" --trigger q:stall
  [ "$status" -eq 0 ]
  qid="$(question_of "$parent")"

  run --separate-stderr $SCRIPT resolve "$qid" --abandon --reason-code wont-do
  [ "$status" -eq 0 ] || {
    echo "resolve failed: $stderr" >&2
    return 1
  }
  [ "$(show_json "$qid" | jq -r '.status')" = closed ]
  [ "$(show_json "$parent" | jq -r '.status')" = closed ]
}

@test "escalate reuses an open question with the same fingerprint on real bd: one question, both items blocked" {
  local a b qa qb
  a="$(make_item "first item")"
  b="$(make_item "second item")"
  run --separate-stderr $SCRIPT escalate "$a" --question "which host?" --trigger q:info
  [ "$status" -eq 0 ]
  run --separate-stderr $SCRIPT escalate "$b" --question "which host?" --trigger q:info
  [ "$status" -eq 0 ] || {
    echo "second escalate failed: $stderr" >&2
    return 1
  }
  qa="$(question_of "$a")"
  qb="$(question_of "$b")"
  [ -n "$qa" ]
  [ "$qa" = "$qb" ]
  # provenance stays with the item the question was first raised for
  [ "$(show_json "$qa" | jq -r '.metadata.originating_item')" = "$a" ]

  run --separate-stderr $SCRIPT context "$qa"
  [ "$status" -eq 0 ]
  [[ $output == *"WI_BLOCKED_PARENTS="*"$a"* ]]
  [[ $output == *"WI_BLOCKED_PARENTS="*"$b"* ]]
}

@test "escalate --concern destructive-actions then resolve --decision files the kind:docs follow-up on real bd" {
  local parent qid docs
  parent="$(make_item "parent needing a destructive action")"
  run --separate-stderr $SCRIPT escalate "$parent" --question "may we drop the scratch table?" --trigger q:info --concern destructive-actions
  [ "$status" -eq 0 ] || {
    echo "escalate failed: $stderr" >&2
    return 1
  }
  qid="$(question_of "$parent")"

  run --separate-stderr $SCRIPT resolve "$qid" --decision "yes, drop it" --rationale "scratch data"
  [ "$status" -eq 0 ] || {
    echo "resolve failed: $stderr" >&2
    return 1
  }
  [ "$(show_json "$qid" | jq -r '.status')" = closed ]
  docs="$(bd list --label kind:docs --json --limit 0 |
    jq -c --arg p "$parent" '(if type=="object" and has("data") then .data else . end) | map(select(.id | startswith($p + ".")))')"
  [ "$(jq 'length' <<<"$docs")" -eq 1 ]
}

# A multi-stage, non-primary workflow: the question must resolve the stage,
# workflow and WI_PARENT of the item it was escalated for through the
# originating_item link (the null workflow hides a broken link).
@test "escalate -> context on real bd: the question resolves its escalating item's stage, workflow and WI_PARENT; the item lists the question" {
  mkdir -p "$GFH_REPO/.claude/wi-flow"
  jq -n '{workflows: {
    alpha: {primary: true, stages: {groom: {order: 1, entry: true}, ship: {order: 2, closes: true}}},
    beta: {stages: {draft: {order: 1, entry: true}, build: {order: 2}, verify: {order: 3, closes: true}}}
  }}' >"$GFH_REPO/.claude/wi-flow/config.json"

  local parent qid
  parent="$(make_item "beta item at build" --labels stage:build --metadata '{"wi_workflow":"beta"}')"
  run --separate-stderr $SCRIPT escalate "$parent" --question "which host?" --trigger q:info
  [ "$status" -eq 0 ] || {
    echo "escalate failed: $stderr" >&2
    return 1
  }
  qid="$(question_of "$parent")"

  run --separate-stderr $SCRIPT context "$qid"
  [ "$status" -eq 0 ]
  [[ $output == *"WI_CLASS=question"* ]]
  [[ $output == *"WI_STAGE=build"* ]]
  [[ $output == *"WI_WORKFLOW=beta"* ]]
  [[ $output == *"WI_PARENT=$parent"* ]]

  run --separate-stderr $SCRIPT context "$parent"
  [ "$status" -eq 0 ]
  [[ $output == *"WI_QUESTIONS="*"$qid"* ]]
}
