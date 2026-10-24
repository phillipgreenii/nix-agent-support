#!/usr/bin/env bats
# bats file_tags=type:integration
# Real-bd regression guard for the premise round trip (tc-7fn8b).
# `annotate --premise` wrote bd metadata key wi_premise while `context`
# (lib/context.bash pgwf_context_premise / pgwf_context_premise_stale) read
# .metadata.premise, so an annotated premise never surfaced as WI_PREMISE and
# WI_PREMISE_STALE could never become true. The writer and reader were each
# tested alone against hand-written metadata, which hid the mismatch. These
# tests run the real pg-wi-flow CLI against a REAL throwaway embedded-Dolt `bd`
# database created by `bd init` inside the hermetic git-fixture-harness repo
# (same pattern as test-pg-wi-flow-escalate-real-bd.bats) -- never the shared
# remote server, never a dolt sql-server. Skips when `bd` is not on PATH (the
# nix check sandbox).
bats_require_minimum_version 1.5.0

setup() {
  if ! command -v bd >/dev/null 2>&1; then
    skip "bd not on PATH; real-bd premise tests need it"
  fi
  local gfh_lib="${GFH_LIB:-}"
  if [[ -z $gfh_lib ]]; then
    echo "GFH_LIB is not set: commit via the run-unit-tests hook, run under the workspace .envrc (direnv, or direnv exec <workspace-root> ...), or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # shellcheck disable=SC1090,SC1091 # harness path is runtime-provided
  source "$gfh_lib"
  gfh_save_env SCRIPTS_DIR SCRIPT_UNDER_TEST TEST_SUPPORT GFH_LIB
  gfh_setup "pg-wi-flow-premise-real-bd"
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
  if ! bd init --non-interactive --prefix pr -q >"$GFH_WORK/init.log" 2>&1; then
    cat "$GFH_WORK/init.log" >&2
    skip "bd init could not create a throwaway embedded database"
  fi
  [[ "$(jq -r '.dolt_mode // empty' .beads/metadata.json)" == embedded ]] ||
    skip "throwaway bd database is not embedded; refusing to run"

  mkdir -p "$GFH_REPO/.claude/wi-flow"
  jq -n '{workflows: {homelab: {primary: true, stages: {
      work: {order: 1, entry: true, closes: true, concerns: []}}}},
    labels: {kind_prefix: "kind:"}}' >"$GFH_REPO/.claude/wi-flow/config.json"
  export PG_WI_FLOW_IDENT="aaaaaaaa-main-main"
}

teardown() {
  if declare -F gfh_teardown >/dev/null; then
    gfh_teardown
  fi
  return 0
}

# make_item TITLE [bd create args...] -- prints the new id
make_item() {
  local title="$1"
  shift
  bd create "$title" -t task "$@" --json |
    jq -r '(if type=="object" and has("data") then .data else . end) | if type=="array" then .[0] else . end | .id'
}

# ctx_line ID KEY -- the value of `context ID`'s KEY= line.
ctx_line() {
  $SCRIPT context "$1" | grep "^$2=" | cut -d= -f2-
}

@test "annotate --premise then context shows WI_PREMISE on real bd" {
  local id
  id="$(make_item "depends on grafana-old")"
  run --separate-stderr $SCRIPT annotate "$id" --premise "grafana-old"
  [ "$status" -eq 0 ] || {
    echo "annotate failed: $stderr" >&2
    return 1
  }
  [ "$(ctx_line "$id" WI_PREMISE)" = "grafana-old" ]
  [ "$(ctx_line "$id" WI_PREMISE_STALE)" = false ]
}

@test "annotate --premise then context flags WI_PREMISE_STALE once a closed kind:remove item names the target on real bd" {
  local id rm
  id="$(make_item "depends on grafana-old")"
  run --separate-stderr $SCRIPT annotate "$id" --premise "grafana-old"
  [ "$status" -eq 0 ] || {
    echo "annotate failed: $stderr" >&2
    return 1
  }
  rm="$(make_item "remove grafana-old dashboard" --labels kind:remove)"
  bd close "$rm" --reason "removed" >/dev/null
  [ "$(ctx_line "$id" WI_PREMISE)" = "grafana-old" ]
  [ "$(ctx_line "$id" WI_PREMISE_STALE)" = true ]
}

@test "an unrelated closed kind:remove item does not flag a real-bd premise stale" {
  local id rm
  id="$(make_item "depends on grafana-old")"
  run --separate-stderr $SCRIPT annotate "$id" --premise "grafana-old"
  [ "$status" -eq 0 ]
  rm="$(make_item "remove prometheus-legacy" --labels kind:remove)"
  bd close "$rm" --reason "removed" >/dev/null
  [ "$(ctx_line "$id" WI_PREMISE_STALE)" = false ]
}
