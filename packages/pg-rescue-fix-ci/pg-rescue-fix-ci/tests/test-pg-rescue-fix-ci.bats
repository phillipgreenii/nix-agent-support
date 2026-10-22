#!/usr/bin/env bats
# bats file_tags=type:integration
#
# pg-rescue-fix-ci (docket pg2-q6a96, design D6 and D9) against the REAL
# pg-rescue binary with fixture handlers in a test config and the REAL bd (an
# embedded throwaway database, see test-support/bd_fixture.bash: no dolt server,
# never the shared tracker). Two stubs only: a pg-connector test double that
# records its argv (the one external tool the shim calls with fixed argv), and a
# pg-rescue that exits 9, 75 or 70, used only to prove the exit mapping.

bats_require_minimum_version 1.5.0

if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi
if [[ -z ${TEST_SUPPORT:-} ]]; then
  TEST_SUPPORT="$(cd "${SCRIPTS_DIR}/../test-support" && pwd)"
fi
load "$TEST_SUPPORT/bd_fixture"

setup_file() {
  command -v bd >/dev/null 2>&1 || skip "bd not on PATH"
  bd_fixture_setup_file
}

teardown_file() {
  bd_fixture_teardown_file
}

setup() {
  bd_fixture_setup
  bd_fixture_assert_isolated
  bd_call_log_install

  WORK="$(mktemp -d)"
  WORK="$(cd "$WORK" && pwd -P)"
  HANDLERS="$WORK/handlers"
  HLOG="$WORK/handler-log"
  DOUBLE_DIR="$WORK/double-bin"
  CONNECTOR_LOG="$WORK/connector.log"
  CONNECTOR_FAIL="$WORK/connector.fail"
  mkdir -p "$HANDLERS" "$HLOG" "$DOUBLE_DIR"
  export XDG_STATE_HOME="$WORK/state"
  STATE_DIR="$XDG_STATE_HOME/pg-rescue-fix-ci"
  RUN_N=0
  export PG_RESCUE_CONFIG="$WORK/pg-rescue.toml"

  # The shim re-executes itself as the step through its own resolved path, so
  # the raw source needs a shebang and strict mode; the assembled script has
  # both already.
  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    SHIM=("$SCRIPT_UNDER_TEST")
  else
    {
      printf '#!/usr/bin/env bash\nset -euo pipefail\n'
      cat "$SCRIPTS_DIR/pg-rescue-fix-ci.sh"
    } >"$WORK/pg-rescue-fix-ci"
    chmod +x "$WORK/pg-rescue-fix-ci"
    SHIM=("$WORK/pg-rescue-fix-ci")
  fi

  write_connector_double
  write_handlers
  write_chain
  PATH="$DOUBLE_DIR:$PATH"
  export PATH
}

teardown() {
  rm -rf "$BD_SHIM_DIR" "$WORK"
}

# ---------------------------------------------------------------- fixtures

# The pg-connector double: appends its argv to a file the test reads, exits 0,
# or non-zero while $CONNECTOR_FAIL exists.
write_connector_double() {
  cat >"$DOUBLE_DIR/pg-connector" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$CONNECTOR_LOG"
if [ -e "$CONNECTOR_FAIL" ]; then
  echo "pg-connector double: failing on request" >&2
  exit 1
fi
exit 0
EOF
  chmod +x "$DOUBLE_DIR/pg-connector"
  : >"$CONNECTOR_LOG"
}

# The fixture give-up handler (pg-rescue design 3.2: exit 3 is deferred). It
# adds the marker and logs the item id it was handed; handlers log to files,
# never to stdout.
write_handlers() {
  cat >"$HANDLERS/give-up.sh" <<EOF
#!/usr/bin/env bash
set -eu
touch "$HLOG/give-up.ran"
printf '%s\n' "\$PG_RESCUE_ITEM_ID" >>"$HLOG/give-up.item"
printf '%s\n' "\$BEADS_DIR" >"$HLOG/give-up.beads-dir"
printf '%s\n' "\$BEADS_ACTOR" >"$HLOG/give-up.actor"
pwd -P >"$HLOG/give-up.cwd"
bd update "\$PG_RESCUE_ITEM_ID" --add-label script-gave-up >&2
exit 3
EOF
  chmod +x "$HANDLERS/give-up.sh"
}

# write_chain: production's fix-ci chain is the give-up handler alone.
write_chain() {
  cat >"$PG_RESCUE_CONFIG" <<EOF
[handler.give-up]
command = ["$HANDLERS/give-up.sh"]
timeout = "1m"

[chain.fix-ci]
handlers = ["give-up"]
EOF
}

# new_item [METADATA_JSON]: a fix-ci item in the throwaway database, print id.
new_item() {
  local meta='{"failing_builds":["101:1"]}'
  [[ $# -eq 0 ]] || meta=$1
  bd -C "$TDIR" create --title "fix-ci: acme/widget#7" --label worker-ready \
    --metadata "$meta" --silent 2>/dev/null
}

# run_shim [EXTRA ARGS...]: the shim with the full valid command line, each run
# in its own state directory (SHIM_ITEM overrides the item id).
run_shim() {
  RUN_N=$((RUN_N + 1))
  LAST_STATE="$WORK/runs/$RUN_N"
  run --separate-stderr "${SHIM[@]}" \
    --item-id "${SHIM_ITEM:-$ITEM}" --repo acme/widget --pr-number 7 \
    --tracker-dir "$TDIR" --state-dir "$LAST_STATE" "$@"
}

item_field() { bd_fixture_show_json "$ITEM" | jq -r "$1"; }
item_meta() { bd_fixture_show_json "$ITEM" | jq -c ".metadata.$1"; }
connector_calls() { grep -c . "$CONNECTOR_LOG" || true; }

# assert_released: open, unassigned (the item is claimable again).
assert_released() {
  [ "$(item_field '.status')" = "open" ]
  [ -z "$(item_field '.assignee // ""')" ]
}

assert_marked() {
  bd_fixture_show_json "$ITEM" | jq -e '.labels | index("script-gave-up")' >/dev/null
}

assert_unmarked() {
  bd_fixture_show_json "$ITEM" | jq -e '(.labels // []) | index("script-gave-up") | not' >/dev/null
}

# last_outcome: the outcome word of the most recent run_shim.
last_outcome() {
  local -a files=("$LAST_STATE"/*.outcome)
  [ "${#files[@]}" -eq 1 ] && [ -f "${files[0]}" ]
  awk '{print $2}' "${files[0]}"
}

# A pg-rescue stub first on PATH, exiting with the given code: the one test per
# exit code that proves the exit mapping (every other test runs the real binary).
stub_pg_rescue() { # EXIT_CODE
  STUB_DIR="$WORK/stub-bin"
  STUB_LOG="$WORK/stub-pg-rescue.args"
  mkdir -p "$STUB_DIR"
  cat >"$STUB_DIR/pg-rescue" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >"$STUB_LOG"
exit $1
EOF
  chmod +x "$STUB_DIR/pg-rescue"
  PATH="$STUB_DIR:$PATH"
  export PATH
}

# --------------------------------------------------------------------- tests

@test "isolation guard: the fixture database is the temp embedded one" {
  bd_fixture_assert_isolated
  [ "$PWD" = "$TDIR" ]
}

@test "sequence on one item: one rerun, then nothing while CI is quiet, then give-up on a new failing build" {
  ITEM=$(new_item)

  # 1. First run: exactly one ci rerun-failed for the PR, ci_rerun_of recorded
  # unchanged, exit 0, the item open and unclaimed, no marker, no give-up.
  run_shim
  [ "$status" -eq 0 ]
  [ "$(cat "$CONNECTOR_LOG")" = "ci rerun-failed acme/widget#7" ]
  [ "$(item_meta ci_rerun_of | jq -r .)" = '["101:1"]' ]
  [ "$(item_meta failing_builds)" = '["101:1"]' ]
  assert_released
  assert_unmarked
  [ ! -e "$HLOG/give-up.ran" ]
  [ "$(last_outcome)" = "ok" ]
  # The step also left a comment on the item.
  [ "$(bd -C "$TDIR" comments "$ITEM" --json | jq 'length')" -ge 1 ]

  # 2. Second run, failing_builds unchanged: no new rerun, exit 0, no marker.
  run_shim
  [ "$status" -eq 0 ]
  [ "$(connector_calls)" = "1" ]
  assert_released
  assert_unmarked
  [ ! -e "$HLOG/give-up.ran" ]
  [ "$(last_outcome)" = "ok" ]

  # 3. A new failing build id appended: the chain runs give-up, so the item is
  # open, unclaimed and marked, with no further rerun, and the shim exits 0.
  bd -C "$TDIR" update "$ITEM" --set-metadata 'failing_builds=["101:1","101:2"]' >/dev/null
  run_shim
  [ "$status" -eq 0 ]
  [ "$(connector_calls)" = "1" ]
  [ -e "$HLOG/give-up.ran" ]
  assert_released
  assert_marked
  [ "$(last_outcome)" = "gave-up" ]
  # The handler received the item id, the tracker and the run's actor, and ran
  # in the state directory (-C).
  [ "$(cat "$HLOG/give-up.item")" = "$ITEM" ]
  [ "$(cat "$HLOG/give-up.beads-dir")" = "$TDIR/.beads" ]
  [[ "$(cat "$HLOG/give-up.actor")" == pg-rescue/* ]]
  [ "$(cat "$HLOG/give-up.cwd")" = "$LAST_STATE" ]
}

@test "a fresh item (a new head commit) gets its own rerun" {
  ITEM=$(new_item)
  run_shim
  [ "$status" -eq 0 ]
  first=$ITEM
  ITEM=$(new_item '{"failing_builds":["202:1"]}')
  run_shim
  [ "$status" -eq 0 ]
  [ "$(connector_calls)" = "2" ]
  [ "$(item_meta ci_rerun_of | jq -r .)" = '["202:1"]' ]
  assert_released
  assert_unmarked
  [ "$(bd_fixture_show_json "$first" | jq -c .metadata.ci_rerun_of | jq -r .)" = '["101:1"]' ]
}

@test "a failed ci rerun-failed call records nothing and the chain's give-up hands the item to the worker" {
  ITEM=$(new_item)
  touch "$CONNECTOR_FAIL"
  run_shim
  [ "$status" -eq 0 ]
  [ "$(connector_calls)" = "1" ]
  [ "$(item_meta ci_rerun_of)" = "null" ]
  [ -e "$HLOG/give-up.ran" ]
  assert_released
  assert_marked
  [ "$(last_outcome)" = "gave-up" ]
}

@test "the first run on an item with no failing_builds reads it as an empty list: one rerun, [] recorded" {
  ITEM=$(new_item '{"note":"no builds yet"}')
  run_shim
  [ "$status" -eq 0 ]
  [ "$(connector_calls)" = "1" ]
  [ "$(item_meta ci_rerun_of | jq -r .)" = "[]" ]
  assert_released
  assert_unmarked
}

@test "a later run whose failing builds are a subset, an empty list or absent does nothing (compared as sets)" {
  # Reordered: same set.
  ITEM=$(new_item '{"failing_builds":["b:1","a:1"],"ci_rerun_of":["a:1","b:1"]}')
  run_shim
  [ "$status" -eq 0 ]
  assert_unmarked
  # A strict subset.
  ITEM=$(new_item '{"failing_builds":["a:1"],"ci_rerun_of":["a:1","b:1"]}')
  run_shim
  [ "$status" -eq 0 ]
  assert_unmarked
  # An empty list.
  ITEM=$(new_item '{"failing_builds":[],"ci_rerun_of":["a:1"]}')
  run_shim
  [ "$status" -eq 0 ]
  assert_unmarked
  # An absent list, and an empty (but present) ci_rerun_of still counts as recorded.
  ITEM=$(new_item '{"ci_rerun_of":[]}')
  run_shim
  [ "$status" -eq 0 ]
  assert_unmarked
  [ "$(connector_calls)" = "0" ]
  [ ! -e "$HLOG/give-up.ran" ]
  assert_released
}

@test "an element of failing_builds missing from ci_rerun_of gives up, even when the lists are the same length (JSON values compared, not text)" {
  ITEM=$(new_item '{"failing_builds":[{"run":1,"attempt":2}],"ci_rerun_of":[{"run":1,"attempt":1}]}')
  run_shim
  [ "$status" -eq 0 ]
  [ "$(connector_calls)" = "0" ]
  assert_marked
  assert_released
}

@test "--ci-backend is passed to ci rerun-failed as --backend" {
  ITEM=$(new_item)
  run_shim --ci-backend zr-ci
  [ "$status" -eq 0 ]
  [ "$(cat "$CONNECTOR_LOG")" = "ci rerun-failed acme/widget#7 --backend zr-ci" ]
}

@test "the shim claims with bd update --claim as the run actor, releases in one call, and never closes the item" {
  ITEM=$(new_item)
  : >"$BD_CALL_LOG"
  run_shim
  calls=$(cat "$BD_CALL_LOG")
  [ "$status" -eq 0 ]
  [ "$(grep -c -- "^update $ITEM --claim --actor pg-rescue/" <<<"$calls")" = "1" ]
  [ "$(grep -c -- "^update $ITEM --status open --assignee  --actor pg-rescue/" <<<"$calls")" = "1" ]
  [ -z "$(grep -- "^close " <<<"$calls" || true)" ]
  [ "$(item_field '.status')" = "open" ]
}

@test "the outcome record is <run-id> <outcome> <item id>, next to pg-rescue's result file, in the default state directory" {
  ITEM=$(new_item)
  run --separate-stderr "${SHIM[@]}" --item-id "$ITEM" --repo acme/widget --pr-number 7 --tracker-dir "$TDIR"
  [ "$status" -eq 0 ]
  [[ "$(cat "$STATE_DIR"/*.outcome)" =~ ^[0-9]{8}T[0-9]{6}Z-[0-9]+\ ok\ $ITEM$ ]]
  [ -s "$(printf '%s\n' "$STATE_DIR"/*.result.json | head -1)" ]
}

@test "a passed-through pg-rescue exit 9 is remapped to 1; the claim is released and nothing is requested" {
  ITEM=$(new_item)
  stub_pg_rescue 9
  run_shim
  [ "$status" -eq 1 ]
  assert_released
  assert_unmarked
  [ "$(connector_calls)" = "0" ]
  [ "$(last_outcome)" = "failed" ]
}

@test "pg-rescue exit 75 maps to 0; the claim is released, no marker is added by the shim" {
  ITEM=$(new_item)
  stub_pg_rescue 75
  run_shim
  [ "$status" -eq 0 ]
  assert_released
  assert_unmarked
  [ "$(last_outcome)" = "gave-up" ]
}

@test "pg-rescue exit 70 (a wrapper error) is unchanged: the shim exits 70 and releases the claim" {
  ITEM=$(new_item)
  stub_pg_rescue 70
  run_shim
  [ "$status" -eq 70 ]
  assert_released
  [ "$(last_outcome)" = "failed" ]
}

@test "the pg-rescue invocation carries --chain fix-ci, -C <state-dir>, the result file and this tool's absolute path as the step" {
  ITEM=$(new_item)
  stub_pg_rescue 75
  run_shim
  [ "$status" -eq 0 ]
  args=$(cat "$STUB_LOG")
  [[ $args == "--chain fix-ci -C $LAST_STATE "* ]]
  [[ $args == *"--result-file $LAST_STATE/"*".result.json -- /"*" step --item-id $ITEM --repo acme/widget --pr-number 7 --tracker-dir $TDIR --state-dir $LAST_STATE" ]]
}

@test "a config with no fix-ci chain (pg-rescue exit 70) is a failure: the claim is released and nothing is marked" {
  ITEM=$(new_item)
  cat >"$PG_RESCUE_CONFIG" <<EOF
[handler.give-up]
command = ["$HANDLERS/give-up.sh"]

[chain.other]
handlers = ["give-up"]
EOF
  run_shim
  [ "$status" -ne 0 ]
  assert_released
  assert_unmarked
  [ "$(last_outcome)" = "failed" ]
}

@test "a claim lost to a peer exits 0 and leaves the item untouched" {
  ITEM=$(new_item)
  bd -C "$TDIR" update "$ITEM" --assignee other-actor --status in_progress --actor other-actor >/dev/null 2>&1
  before=$(bd_fixture_show_json "$ITEM")
  [ "$(jq -r .assignee <<<"$before")" = "other-actor" ]
  run_shim
  [ "$status" -eq 0 ]
  [ "$(bd_fixture_show_json "$ITEM")" = "$before" ]
  [ "$(connector_calls)" = "0" ]
  [ ! -e "$HLOG/give-up.ran" ]
  [ "$(last_outcome)" = "peer-held" ]
}

@test "a claim on a missing item is a failure: exit 1, nothing requested" {
  ITEM=$(new_item)
  SHIM_ITEM=tst-nosuchitem run_shim
  [ "$status" -eq 1 ]
  [ "$(connector_calls)" = "0" ]
  [ "$(last_outcome)" = "failed" ]
}

@test "usage errors exit 2 before bd is touched" {
  ITEM=$(new_item)
  : >"$BD_CALL_LOG"
  run --separate-stderr "${SHIM[@]}" --bogus
  [ "$status" -eq 2 ]
  run --separate-stderr "${SHIM[@]}" --item-id "$ITEM"
  [ "$status" -eq 2 ]
  # A non-numeric PR, a tracker without .beads, a relative state dir, a flag
  # lacking its value and an empty backend.
  run --separate-stderr "${SHIM[@]}" --item-id "$ITEM" --repo acme/widget --pr-number x --tracker-dir "$TDIR"
  [ "$status" -eq 2 ]
  run --separate-stderr "${SHIM[@]}" --item-id "$ITEM" --repo acme/widget --pr-number 7 --tracker-dir "$WORK"
  [ "$status" -eq 2 ]
  run --separate-stderr "${SHIM[@]}" --item-id "$ITEM" --repo acme/widget --pr-number 7 --tracker-dir "$TDIR" --state-dir relative/dir
  [ "$status" -eq 2 ]
  run --separate-stderr "${SHIM[@]}" --item-id "$ITEM" --repo acme/widget --pr-number 7 --tracker-dir "$TDIR" --ci-backend ""
  [ "$status" -eq 2 ]
  run --separate-stderr "${SHIM[@]}" --item-id "$ITEM" --repo acme/widget --pr-number 7 --tracker-dir
  [ "$status" -eq 2 ]
  [ ! -s "$BD_CALL_LOG" ]
  [ "$(item_field '.status')" = "open" ]
  [ -z "$(item_field '.assignee // ""')" ]
}

@test "--help prints usage on stdout and exits 0" {
  run "${SHIM[@]}" --help
  [ "$status" -eq 0 ]
  [[ $output == *"Usage: pg-rescue-fix-ci"* ]]
}
