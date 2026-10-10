#!/usr/bin/env bats
# bats file_tags=type:integration
# Integration tests for pg-wi-flow's claim path against a REAL `bd` (tc-qepyv).
# Every other pg-wi-flow bats file mocks `bd`, which is how the worker-claim
# failure after a dispatcher reservation shipped: real bd refuses a plain
# `--assignee` reassignment of another actor's in_progress claim. These tests
# run the raw .sh against a throwaway embedded-Dolt database created by
# `bd init` inside the hermetic git-fixture-harness repo -- never the shared
# remote server, never a dolt sql-server. They skip when `bd` is not on PATH
# (the nix check sandbox).
bats_require_minimum_version 1.5.0

setup() {
  if ! command -v bd >/dev/null 2>&1; then
    skip "bd not on PATH; real-bd integration tests need it"
  fi
  # SCRIPTS_DIR / SCRIPT_UNDER_TEST / TEST_SUPPORT may be exported by the nix
  # check; gfh_setup scrubs every exported var off its allowlist, so carry
  # them across it. GFH_LIB is the one required file (tc-4ehow); nix package
  # checks inject TEST_SUPPORT (the directory holding it) instead.
  local gfh_lib="${GFH_LIB:-${TEST_SUPPORT:+$TEST_SUPPORT/git-fixture-harness.bash}}"
  if [[ -z $gfh_lib ]]; then
    echo "GFH_LIB is not set: commit via the run-unit-tests hook, run under the workspace .envrc (direnv, or direnv exec <workspace-root> ...), or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # shellcheck disable=SC1090,SC1091 # harness path is runtime-provided
  source "$gfh_lib"
  gfh_save_env SCRIPTS_DIR SCRIPT_UNDER_TEST TEST_SUPPORT GFH_LIB
  gfh_setup "pg-wi-flow-integration"
  gfh_restore_env

  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  SCRIPT="${SCRIPT_UNDER_TEST:-bash $SCRIPTS_DIR/pg-wi-flow.sh}"

  # Throwaway embedded-Dolt database inside the hermetic fixture repo. The
  # harness rebuilt the environment from an allowlist, so no ambient
  # BEADS_* / shared-server configuration survives into these tests.
  export BEADS_DOLT_AUTO_START=0 BD_NON_INTERACTIVE=1
  XDG_CONFIG_HOME="$GFH_WORK/xdg"
  mkdir -p "$XDG_CONFIG_HOME"
  export XDG_CONFIG_HOME
  cd "$GFH_REPO" || exit 1
  if ! bd init --non-interactive --prefix it -q >"$GFH_WORK/init.log" 2>&1; then
    cat "$GFH_WORK/init.log" >&2
    skip "bd init could not create a throwaway embedded database"
  fi
  # Guard: the throwaway database MUST be embedded, never a server.
  [[ "$(jq -r '.dolt_mode // empty' .beads/metadata.json)" == embedded ]] ||
    skip "throwaway bd database is not embedded; refusing to run"

  DISPATCHER_IDENT="aaaaaaaa-main-main"
  FOREIGN_IDENT="bbbbbbbb-main-main"
  ITEM="$(bd create "integration item" -t task --json | jq -r '(if type=="object" and has("data") then .data else . end) | if type=="array" then .[0] else . end | .id')"
}

teardown() {
  if declare -F gfh_teardown >/dev/null; then
    gfh_teardown
  fi
  return 0
}

# Envelope-agnostic: bd returns {"data": ...} only when BD_JSON_ENVELOPE is
# set, otherwise a bare array/object (the hermetic env here has it unset).
assignee_of() {
  bd show "$1" --json | jq -r '(if type=="object" and has("data") then .data else . end) | if type=="array" then .[0] else . end | .assignee // ""'
}

status_of() {
  bd show "$1" --json | jq -r '(if type=="object" and has("data") then .data else . end) | if type=="array" then .[0] else . end | .status'
}

@test "next --id reserves the item under <ident>-<stage>" {
  PG_WI_FLOW_IDENT="$DISPATCHER_IDENT" run $SCRIPT next --id "$ITEM"
  [ "$status" -eq 0 ]
  [[ ${lines[0]} == "$ITEM work "* ]]
  [ "$(status_of "$ITEM")" = in_progress ]
  [ "$(assignee_of "$ITEM")" = "$DISPATCHER_IDENT-work" ]
}

@test "claim by the same session after next transfers the reservation (tc-92yvn / tc-qho87)" {
  PG_WI_FLOW_IDENT="$DISPATCHER_IDENT" run $SCRIPT next --id "$ITEM"
  [ "$status" -eq 0 ]
  local before
  before="$(assignee_of "$ITEM")"

  # The worker runs as a different actor within the same session: the
  # session8 prefix matches, the identity tail differs.
  PG_WI_FLOW_IDENT="aaaaaaaa-worker-main" run $SCRIPT claim "$ITEM"
  [ "$status" -eq 0 ]
  [[ ${lines[0]} == "$ITEM work "* ]]
  [ "$(status_of "$ITEM")" = in_progress ]
  [ "$(assignee_of "$ITEM")" = "aaaaaaaa-worker-main-work" ]
  [ "$(assignee_of "$ITEM")" != "$before" ]
}

@test "claim with the identical actor is idempotent" {
  PG_WI_FLOW_IDENT="$DISPATCHER_IDENT" run $SCRIPT next --id "$ITEM"
  [ "$status" -eq 0 ]
  PG_WI_FLOW_IDENT="$DISPATCHER_IDENT" run $SCRIPT claim "$ITEM"
  [ "$status" -eq 0 ]
  [ "$(assignee_of "$ITEM")" = "$DISPATCHER_IDENT-work" ]
}

@test "claim from a different session fails and leaves the reservation intact" {
  PG_WI_FLOW_IDENT="$DISPATCHER_IDENT" run $SCRIPT next --id "$ITEM"
  [ "$status" -eq 0 ]

  PG_WI_FLOW_IDENT="$FOREIGN_IDENT" run --separate-stderr $SCRIPT claim "$ITEM"
  [ "$status" -ne 0 ]
  [[ $stderr == *"failed to claim $ITEM"* ]]
  [ "$(assignee_of "$ITEM")" = "$DISPATCHER_IDENT-work" ]
}

@test "claim with an explicit --actor and no PG_WI_FLOW_IDENT fails against a held item and changes nothing" {
  PG_WI_FLOW_IDENT="$DISPATCHER_IDENT" run $SCRIPT next --id "$ITEM"
  [ "$status" -eq 0 ]
  run env -u PG_WI_FLOW_IDENT $SCRIPT --actor "someone-else-work" claim "$ITEM"
  [ "$status" -ne 0 ]
  [ "$(assignee_of "$ITEM")" = "$DISPATCHER_IDENT-work" ]
}

@test "next --id on an item another session holds reports none and changes nothing" {
  PG_WI_FLOW_IDENT="$FOREIGN_IDENT" run $SCRIPT next --id "$ITEM"
  [ "$status" -eq 0 ]
  PG_WI_FLOW_IDENT="$DISPATCHER_IDENT" run $SCRIPT next --id "$ITEM"
  [ "$status" -eq 0 ]
  [ "$output" = none ]
  [ "$(assignee_of "$ITEM")" = "$FOREIGN_IDENT-work" ]
}

@test "release clears status and assignee in one step" {
  PG_WI_FLOW_IDENT="$DISPATCHER_IDENT" run $SCRIPT next --id "$ITEM"
  [ "$status" -eq 0 ]
  PG_WI_FLOW_IDENT="$DISPATCHER_IDENT" run $SCRIPT release "$ITEM"
  [ "$status" -eq 0 ]
  [ "$(status_of "$ITEM")" = open ]
  [ -z "$(assignee_of "$ITEM")" ]
}

@test "a released item can be reserved again by a different session" {
  PG_WI_FLOW_IDENT="$DISPATCHER_IDENT" run $SCRIPT next --id "$ITEM"
  [ "$status" -eq 0 ]
  PG_WI_FLOW_IDENT="$DISPATCHER_IDENT" run $SCRIPT release "$ITEM"
  [ "$status" -eq 0 ]
  PG_WI_FLOW_IDENT="$FOREIGN_IDENT" run $SCRIPT next --id "$ITEM"
  [ "$status" -eq 0 ]
  [[ ${lines[0]} == "$ITEM work "* ]]
  [ "$(assignee_of "$ITEM")" = "$FOREIGN_IDENT-work" ]
}

@test "list --stale --reserved-hours does not report a fresh reservation" {
  PG_WI_FLOW_IDENT="$DISPATCHER_IDENT" run $SCRIPT next --id "$ITEM"
  [ "$status" -eq 0 ]
  run $SCRIPT list --stale --reserved-hours 2
  [ "$status" -eq 0 ]
  [ "$(jq 'length' <<<"$output")" -eq 0 ]
}

@test "full round trip: next, claim, release, next again" {
  PG_WI_FLOW_IDENT="$DISPATCHER_IDENT" run $SCRIPT next --id "$ITEM"
  [ "$status" -eq 0 ]
  PG_WI_FLOW_IDENT="aaaaaaaa-worker-main" run $SCRIPT claim "$ITEM"
  [ "$status" -eq 0 ]
  PG_WI_FLOW_IDENT="aaaaaaaa-worker-main" run $SCRIPT release "$ITEM"
  [ "$status" -eq 0 ]
  [ "$(status_of "$ITEM")" = open ]
  PG_WI_FLOW_IDENT="$DISPATCHER_IDENT" run $SCRIPT next --id "$ITEM"
  [ "$status" -eq 0 ]
  [ "$(assignee_of "$ITEM")" = "$DISPATCHER_IDENT-work" ]
}

# Literal scenario of the post-deploy verify bead tc-n84l6 (tc-1lmua), kept as
# a test so that verification is not a one-off manual probe (tc-fyfbl):
#   PG_WI_FLOW_IDENT=<8hex>-aaa-dispatcher pg-wi-flow next --id X, then
#   PG_WI_FLOW_IDENT=<same8hex>-bbb-worker pg-wi-flow claim X exits 0 and the
#   assignee becomes the worker actor; a different-session prefix still fails
#   with the assignee unchanged.
@test "tc-n84l6 scenario: <8hex>-aaa-dispatcher next, <same8hex>-bbb-worker claim transfers; other prefix refused" {
  PG_WI_FLOW_IDENT="ab12cd34-aaa-dispatcher" run $SCRIPT next --id "$ITEM"
  [ "$status" -eq 0 ]
  [ "$(assignee_of "$ITEM")" = "ab12cd34-aaa-dispatcher-work" ]

  PG_WI_FLOW_IDENT="ffff0000-bbb-worker" run $SCRIPT claim "$ITEM"
  [ "$status" -ne 0 ]
  [ "$(assignee_of "$ITEM")" = "ab12cd34-aaa-dispatcher-work" ]

  PG_WI_FLOW_IDENT="ab12cd34-bbb-worker" run $SCRIPT claim "$ITEM"
  [ "$status" -eq 0 ]
  [ "$(assignee_of "$ITEM")" = "ab12cd34-bbb-worker-work" ]
}

# --- next --attended / --questions against real bd (tc-9ddu3.1.21) ---

make_item() {
  # make_item TITLE LABEL... -- prints the new id
  local title="$1" label args=()
  shift
  for label in "$@"; do args+=(--labels "$label"); done
  bd create "$title" -t task "${args[@]}" --json | jq -r '(if type=="object" and has("data") then .data else . end) | if type=="array" then .[0] else . end | .id'
}

@test "next --attended reserves a human-labeled question item; plain next never does" {
  local q
  q="$(make_item "a question" question human)"
  # plain next: the only candidate besides $ITEM is excluded; it reserves $ITEM
  PG_WI_FLOW_IDENT="$DISPATCHER_IDENT" run $SCRIPT next
  [ "$status" -eq 0 ]
  [[ ${lines[0]} == "$ITEM "* ]]
  [ "$(status_of "$q")" = open ]

  PG_WI_FLOW_IDENT="$DISPATCHER_IDENT" run $SCRIPT next --attended
  [ "$status" -eq 0 ]
  [[ ${lines[0]} == "$q "* ]]
  [ "$(status_of "$q")" = in_progress ]
}

@test "next --questions never reserves stage work" {
  # $ITEM is plain stage work and there is no attention item at all
  PG_WI_FLOW_IDENT="$DISPATCHER_IDENT" run $SCRIPT next --questions
  [ "$status" -eq 0 ]
  [ "$output" = none ]
  [ "$(status_of "$ITEM")" = open ]
  [ -z "$(assignee_of "$ITEM")" ]
}

@test "list --attended surfaces legacy human items (no question label) and question items, not stage work" {
  local legacy q
  legacy="$(make_item "legacy human" human)"
  q="$(make_item "a question" question human)"
  run $SCRIPT list --attended
  [ "$status" -eq 0 ]
  ids="$(jq -r '(if type=="object" and has("data") then .data else . end) | map(.id) | sort | join(",")' <<<"$output")"
  expected="$(printf '%s\n%s\n' "$legacy" "$q" | sort | paste -sd, -)"
  [ "$ids" = "$expected" ]
  [[ $ids != *"$ITEM"* ]]
}

@test "next --attended can reserve a legacy human item" {
  local legacy
  legacy="$(make_item "legacy human" human)"
  PG_WI_FLOW_IDENT="$DISPATCHER_IDENT" run $SCRIPT next --attended
  [ "$status" -eq 0 ]
  [[ ${lines[0]} == "$legacy "* ]]
}
