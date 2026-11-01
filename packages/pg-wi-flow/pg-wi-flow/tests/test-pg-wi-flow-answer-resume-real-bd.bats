#!/usr/bin/env bats
# bats file_tags=type:integration
# Real-bd answered-block resume (tc-5as4k). A q:intent block recorded in the
# last allowed review round used to leave the parent at round >= iteration_bound
# with WI_MUST_ESCALATE=true forever, so after `resolve --answer` the worker
# re-escalated the identical question and the answer never reached the parent.
# These tests run the real pg-wi-flow CLI against a REAL throwaway embedded-Dolt
# `bd` database created by `bd init` inside the hermetic git-fixture-harness
# repo (same pattern as test-pg-wi-flow-escalate-real-bd.bats) -- never the
# shared remote server, never a dolt sql-server. Skips when `bd` is not on PATH
# (the nix check sandbox).
bats_require_minimum_version 1.5.0

setup() {
  if ! command -v bd >/dev/null 2>&1; then
    skip "bd not on PATH; real-bd answer-resume tests need it"
  fi
  local gfh_lib="${GFH_LIB:-}"
  if [[ -z $gfh_lib ]]; then
    echo "GFH_LIB is not set: commit via the run-unit-tests hook, run under the workspace .envrc (direnv, or direnv exec <workspace-root> ...), or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # shellcheck disable=SC1090,SC1091 # harness path is runtime-provided
  source "$gfh_lib"
  gfh_save_env SCRIPTS_DIR SCRIPT_UNDER_TEST TEST_SUPPORT GFH_LIB
  gfh_setup "pg-wi-flow-answer-resume-real-bd"
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
  if ! bd init --non-interactive --prefix ar -q >"$GFH_WORK/init.log" 2>&1; then
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

# record ID CONCERN VERDICT -- run record-verdict through the real CLI.
record() {
  local id="$1" concern="$2" verdict="$3"
  jq -nc --arg c "$concern" --arg v "$verdict" '{concern: $c, verdict: $v}' >"$GFH_WORK/verdict.json"
  run --separate-stderr $SCRIPT record-verdict "$id" --concern "$concern" --json "$GFH_WORK/verdict.json"
  [ "$status" -eq 0 ] || {
    echo "record-verdict $concern failed: $stderr" >&2
    return 1
  }
}

# block_at_final_round ID -- round 0 gaps, round 1 intent blocked: the counter
# reaches iteration_bound (2) and `round` prints WI_MUST_ESCALATE=true.
block_at_final_round() {
  local id="$1"
  record "$id" intent gaps
  run --separate-stderr $SCRIPT round "$id"
  [ "$status" -eq 0 ]
  record "$id" intent blocked
  run --separate-stderr $SCRIPT round "$id"
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = blocked ]
  [ "${lines[1]}" = WI_MUST_ESCALATE=true ]
}

question_of() {
  bd dep list "$1" --direction=down --type blocks --json |
    jq -r '(if type=="object" and has("data") then .data else . end) | map(select((.labels // []) | index("question") != null)) | .[0].id // empty'
}

@test "an answered q:intent block at the final round clears WI_MUST_ESCALATE so the parent can run another round" {
  local id qid
  id="$(make_item "item whose intent is ambiguous")"
  block_at_final_round "$id"
  run --separate-stderr $SCRIPT escalate "$id" --question "which release is newest?" --trigger q:intent --concern intent
  [ "$status" -eq 0 ]
  qid="$(question_of "$id")"
  [ -n "$qid" ]
  run --separate-stderr $SCRIPT context "$id"
  [[ $output == *"WI_MUST_ESCALATE=true"* ]]

  run --separate-stderr $SCRIPT resolve "$qid" --answer "A: the newest stable release"
  [ "$status" -eq 0 ] || {
    echo "resolve failed: $stderr" >&2
    return 1
  }

  PG_WI_FLOW_IDENT="aaaaaaaa-main-dispatcher" run --separate-stderr $SCRIPT next --id "$id"
  [ "$status" -eq 0 ] || {
    echo "next failed: $stderr" >&2
    return 1
  }
  run --separate-stderr $SCRIPT context "$id"
  [ "$status" -eq 0 ]
  [[ $output == *"WI_MUST_ESCALATE=false"* ]]
  [[ $output == *"WI_ROUND=2"* ]]
}

@test "the answer text is visible in the parent's render" {
  local id qid
  id="$(make_item "item whose intent is ambiguous")"
  block_at_final_round "$id"
  run --separate-stderr $SCRIPT escalate "$id" --question "which release is newest?" --trigger q:intent --concern intent
  [ "$status" -eq 0 ]
  qid="$(question_of "$id")"
  run --separate-stderr $SCRIPT resolve "$qid" --answer "A: the newest stable release"
  [ "$status" -eq 0 ]

  run --separate-stderr $SCRIPT context --render "$id"
  [ "$status" -eq 0 ]
  [[ $output == *"answers:"*"A: the newest stable release"* ]]
  [[ "$(show_json "$id" | jq -r '.notes // ""')" == *"A: the newest stable release"* ]]
}

@test "after the extra round the grace is spent: a still-failing round must escalate again" {
  local id qid
  id="$(make_item "item whose intent is ambiguous")"
  block_at_final_round "$id"
  run --separate-stderr $SCRIPT escalate "$id" --question "which release is newest?" --trigger q:intent --concern intent
  qid="$(question_of "$id")"
  run --separate-stderr $SCRIPT resolve "$qid" --answer "A: the newest stable release"
  [ "$status" -eq 0 ]

  record "$id" intent gaps
  run --separate-stderr $SCRIPT round "$id"
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = gaps ]
  [ "${lines[1]}" = WI_MUST_ESCALATE=true ]
  run --separate-stderr $SCRIPT context "$id"
  [[ $output == *"WI_MUST_ESCALATE=true"* ]]
}
