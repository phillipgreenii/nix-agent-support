#!/usr/bin/env bats
# bats file_tags=type:integration
# Real-bd record-verdict -> history/round round trip for hyphenated concern
# names (tc-2t7dc). record-verdict used to key the verdict
# wi_verdict_r<round>_<concern> verbatim, and real bd rejects metadata keys
# outside ^[a-zA-Z_][a-zA-Z0-9_./]*$, so secondary-effects and
# destructive-actions could never be recorded; the mock-bd suite accepts any
# key and never saw it. These tests run the real pg-wi-flow CLI against a REAL
# throwaway embedded-Dolt `bd` database created by `bd init` inside the
# hermetic git-fixture-harness repo (same pattern as
# test-pg-wi-flow-escalate-real-bd.bats) -- never the shared remote server,
# never a dolt sql-server. Skips when `bd` is not on PATH (the nix check
# sandbox).
bats_require_minimum_version 1.5.0

setup() {
  if ! command -v bd >/dev/null 2>&1; then
    skip "bd not on PATH; real-bd verdict tests need it"
  fi
  local gfh_lib="${GFH_LIB:-}"
  if [[ -z $gfh_lib ]]; then
    echo "GFH_LIB is not set: commit via the run-unit-tests hook, run under the workspace .envrc (direnv, or direnv exec <workspace-root> ...), or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # shellcheck disable=SC1090,SC1091 # harness path is runtime-provided
  source "$gfh_lib"
  gfh_save_env SCRIPTS_DIR SCRIPT_UNDER_TEST TEST_SUPPORT GFH_LIB
  gfh_setup "pg-wi-flow-verdict-real-bd"
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
  if ! bd init --non-interactive --prefix vr -q >"$GFH_WORK/init.log" 2>&1; then
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

make_item() {
  bd create "$1" -t task --json | bd_item | jq -r '.id'
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

@test "record-verdict stores secondary-effects and destructive-actions verdicts on real bd and history reads them back" {
  local id
  id="$(make_item "item under review")"
  record "$id" intent ready
  record "$id" conformance ready
  record "$id" secondary-effects gaps
  record "$id" destructive-actions ready

  run --separate-stderr $SCRIPT history "$id"
  [ "$status" -eq 0 ]
  [ "$(jq -r 'map(.concern) | sort | join(",")' <<<"$output")" = "conformance,destructive-actions,intent,secondary-effects" ]
  [ "$(jq -r '.[] | select(.concern == "secondary-effects") | .verdict' <<<"$output")" = gaps ]
  [ "$(jq -r '.[] | select(.concern == "destructive-actions") | .verdict' <<<"$output")" = ready ]
  [ "$(jq -r 'map(.round) | unique | join(",")' <<<"$output")" = 0 ]
}

@test "round sees every hyphenated-concern verdict on real bd and merges them" {
  local id
  id="$(make_item "item under review")"
  record "$id" intent ready
  record "$id" conformance ready
  record "$id" secondary-effects gaps
  record "$id" destructive-actions ready

  # gaps from the hyphenated concern must win: proves round read the record
  run --separate-stderr $SCRIPT round "$id"
  [ "$status" -eq 0 ] || {
    echo "round failed: $stderr" >&2
    return 1
  }
  [ "${lines[0]}" = gaps ]
}

@test "re-recording a hyphenated concern in the same round replaces its verdict on real bd" {
  local id
  id="$(make_item "item under review")"
  record "$id" secondary-effects gaps
  record "$id" secondary-effects ready

  run --separate-stderr $SCRIPT history "$id"
  [ "$status" -eq 0 ]
  [ "$(jq 'length' <<<"$output")" -eq 1 ]
  [ "$(jq -r '.[0].verdict' <<<"$output")" = ready ]
}
