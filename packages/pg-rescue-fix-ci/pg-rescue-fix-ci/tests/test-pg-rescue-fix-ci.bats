#!/usr/bin/env bats
# bats file_tags=type:integration
#
# pg-rescue-fix-ci (docket pg2-q6a96, design D6 and D9) against the REAL
# pg-rescue binary with fixture handlers in a test config and the REAL bd (an
# embedded throwaway database, see test-support/bd_fixture.bash: no dolt server,
# never the shared tracker). Two stubs only: a pg-connector test double that
# records its argv (the one external tool the shim calls with fixed argv), and a
# pg-rescue that exits 9, 75 or 70, used only to prove the exit mapping and the
# failure counter.
#
# failing_builds and ci_rerun_of use the REAL encoding: a comma-separated
# string of <run id>:<attempt> entries, never a JSON array.

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
  # The double and the handlers read these from their environment.
  export CONNECTOR_LOG="$WORK/connector.log"
  export CONNECTOR_SEEN="$WORK/connector.seen"
  export CONNECTOR_FAIL="$WORK/connector.fail"
  export CONNECTOR_HANG="$WORK/connector.hang"
  export CONNECTOR_PID="$WORK/connector.pid"
  export BD_FAIL_RECORD="$WORK/bd.fail-record"
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

  write_bd_shim
  write_connector_double
  write_handlers
  write_chains
  PATH="$DOUBLE_DIR:$PATH"
  export PATH
}

teardown() {
  rm -rf "$BD_SHIM_DIR" "$WORK"
}

# ---------------------------------------------------------------- fixtures

# The bd call-log shim of the fixture, plus one switch: while $BD_FAIL_RECORD
# exists, a bd call that writes ci_rerun_of fails (proves a failed record makes
# no rerun call). Every other call is logged and passed to the real bd.
write_bd_shim() {
  local real_bd
  real_bd="$(PATH="${PATH#"$BD_SHIM_DIR:"}" command -v bd)"
  cat >"$BD_SHIM_DIR/bd" <<SHIM
#!/usr/bin/env bash
printf '%s\n' "\${*//\$'\n'/\\\\n}" >>"$BD_CALL_LOG"
if [ -e "\$BD_FAIL_RECORD" ] && [[ "\$*" == *ci_rerun_of=* ]]; then
  echo "bd shim: failing the ci_rerun_of write on request" >&2
  exit 1
fi
exec "$real_bd" "\$@"
SHIM
  chmod +x "$BD_SHIM_DIR/bd"
}

# The pg-connector double: appends its argv to a file the test reads, records
# the ci_rerun_of the item carried AT CALL TIME (read through bd show), exits 0,
# non-zero while $CONNECTOR_FAIL exists, or hangs (recording its pid) while
# $CONNECTOR_HANG exists.
write_connector_double() {
  cat >"$DOUBLE_DIR/pg-connector" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$CONNECTOR_LOG"
bd show "$PG_ROUTER_ITEM_ID" --json |
  jq -r '(if type=="object" and has("data") then .data else . end) | (if type=="array" then .[0] else . end) | .metadata.ci_rerun_of // "ABSENT"' \
    >>"$CONNECTOR_SEEN"
if [ -e "$CONNECTOR_HANG" ]; then
  echo $$ >"$CONNECTOR_PID"
  exec sleep 120
fi
if [ -e "$CONNECTOR_FAIL" ]; then
  echo "pg-connector double: failing on request" >&2
  exit 1
fi
exit 0
EOF
  chmod +x "$DOUBLE_DIR/pg-connector"
  : >"$CONNECTOR_LOG"
  : >"$CONNECTOR_SEEN"
}

# The fixture give-up handler (pg-rescue design 3.2: exit 3 is deferred). It
# adds the marker and logs the item id it was handed; handlers log to files,
# never to stdout.
write_handlers() {
  cat >"$HANDLERS/give-up.sh" <<EOF
#!/usr/bin/env bash
set -eu
touch "$HLOG/give-up.ran"
printf '%s\n' "\$PG_ROUTER_ITEM_ID" >>"$HLOG/give-up.item"
printf '%s\n' "\$BEADS_DIR" >"$HLOG/give-up.beads-dir"
printf '%s\n' "\$BEADS_ACTOR" >"$HLOG/give-up.actor"
pwd -P >"$HLOG/give-up.cwd"
bd update "\$PG_ROUTER_ITEM_ID" --add-label script-gave-up >&2
exit 3
EOF
  chmod +x "$HANDLERS/give-up.sh"
}

# write_chains: production's fix-ci and give-up chains are the give-up handler
# alone.
write_chains() {
  cat >"$PG_RESCUE_CONFIG" <<EOF
[handler.give-up]
command = ["$HANDLERS/give-up.sh"]
timeout = "1m"

[chain.fix-ci]
handlers = ["give-up"]

[chain.give-up]
handlers = ["give-up"]
EOF
}

# new_item [METADATA_JSON]: a fix-ci item in the throwaway database, print id.
new_item() {
  local meta='{"failing_builds":"101:1"}'
  [[ $# -eq 0 ]] || meta=$1
  bd -C "$TDIR" create --title "fix-ci: acme/widget#7" --label worker-ready \
    --metadata "$meta" --silent 2>/dev/null
}

# next_state: a fresh state directory for the NEXT run_shim (set in RUN_STATE).
next_state() {
  RUN_STATE="$WORK/runs/$((RUN_N + 1))"
  mkdir -p "$RUN_STATE"
}

# run_shim [EXTRA ARGS...]: the shim with the full valid command line, each run
# in its own state directory (SHIM_ITEM overrides the item id; RUN_STATE, set
# by next_state, overrides the directory).
run_shim() {
  RUN_N=$((RUN_N + 1))
  LAST_STATE="${RUN_STATE:-$WORK/runs/$RUN_N}"
  RUN_STATE=""
  run --separate-stderr "${SHIM[@]}" \
    --item-id "${SHIM_ITEM:-$ITEM}" --repo acme/widget --pr-number 7 \
    --tracker-dir "$TDIR" --state-dir "$LAST_STATE" "$@"
}

item_field() { bd_fixture_show_json "$ITEM" | jq -r "$1"; }
item_meta() { bd_fixture_show_json "$ITEM" | jq -r ".metadata.$1"; }
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

# claim_files STATE: the number of <run-id>.claim files in a state directory.
claim_files() {
  local -a files=("$1"/*.claim)
  [ -e "${files[0]}" ] || {
    echo 0
    return 0
  }
  echo "${#files[@]}"
}

# A pg-rescue stub first on PATH, exiting with the given codes in turn (the last
# repeats): the tests that prove the exit mapping and the failure counter
# (every other test runs the real binary).
stub_pg_rescue() { # EXIT_CODE...
  STUB_DIR="$WORK/stub-bin"
  STUB_LOG="$WORK/stub-pg-rescue.args"
  STUB_N="$WORK/stub-pg-rescue.n"
  mkdir -p "$STUB_DIR"
  : >"$STUB_N"
  local codes="$*"
  cat >"$STUB_DIR/pg-rescue" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$STUB_LOG"
echo x >>"$STUB_N"
n=\$(grep -c . "$STUB_N")
codes=($codes)
i=\$((n - 1))
[ "\$i" -lt "\${#codes[@]}" ] || i=\$((\${#codes[@]} - 1))
exit "\${codes[\$i]}"
EOF
  chmod +x "$STUB_DIR/pg-rescue"
  PATH="$STUB_DIR:$PATH"
  export PATH
}

# start_hung_shim: launch the shim in the background (its own job-control
# process group, so INT is not ignored) against an item whose rerun call hangs;
# wait until the double is running. Sets SHIM_PID.
start_hung_shim() {
  touch "$CONNECTOR_HANG"
  HUNG_STATE="$WORK/runs/hung"
  mkdir -p "$HUNG_STATE"
  set -m
  "${SHIM[@]}" --item-id "$ITEM" --repo acme/widget --pr-number 7 \
    --tracker-dir "$TDIR" --state-dir "$HUNG_STATE" >"$WORK/hung.out" 2>&1 &
  SHIM_PID=$!
  local tries=200
  while [ "$tries" -gt 0 ]; do
    [ -s "$CONNECTOR_PID" ] && return 0
    sleep 0.1
    tries=$((tries - 1))
  done
  echo "the connector double never started" >&2
  return 1
}

# signal_hung_shim SIG: signal the shim, then end the hung double so the shim's
# deferred trap runs; sets HUNG_STATUS to the shim's exit status.
signal_hung_shim() {
  kill "-$1" "$SHIM_PID"
  kill "$(cat "$CONNECTOR_PID")"
  HUNG_STATUS=0
  wait "$SHIM_PID" || HUNG_STATUS=$?
  set +m
}

# --------------------------------------------------------------------- tests

@test "isolation guard: the fixture database is the temp embedded one" {
  bd_fixture_assert_isolated
  [ "$PWD" = "$TDIR" ]
}

@test "sequence on one item: record, then one rerun, then nothing while CI is quiet, then give-up on a new failing build" {
  ITEM=$(new_item)

  # 1. First run: ci_rerun_of already equals failing_builds when the double is
  # called (record BEFORE the rerun), exactly one ci rerun-failed for the PR,
  # exit 0, the item open and unclaimed, no marker, no give-up, no counted
  # failure.
  run_shim
  [ "$status" -eq 0 ]
  [ "$(cat "$CONNECTOR_LOG")" = "ci rerun-failed acme/widget#7" ]
  [ "$(cat "$CONNECTOR_SEEN")" = "101:1" ]
  [ "$(item_meta ci_rerun_of)" = "101:1" ]
  [ "$(item_meta failing_builds)" = "101:1" ]
  [ "$(item_meta script_failures)" = "null" ]
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

  # 3. A new failing build appended: the chain runs give-up, so the item is
  # open, unclaimed and marked, with no further rerun, and the shim exits 0.
  bd -C "$TDIR" update "$ITEM" --set-metadata failing_builds=101:1,101:2 >/dev/null
  run_shim
  [ "$status" -eq 0 ]
  [ "$(connector_calls)" = "1" ]
  [ -e "$HLOG/give-up.ran" ]
  assert_released
  assert_marked
  [ "$(last_outcome)" = "gave-up" ]
  [ "$(item_meta script_failures)" = "null" ]
  # The handler received the item id (PG_ROUTER_ITEM_ID), the tracker and the
  # run's actor, and ran in the state directory (-C).
  [ "$(cat "$HLOG/give-up.item")" = "$ITEM" ]
  [ "$(cat "$HLOG/give-up.beads-dir")" = "$TDIR/.beads" ]
  [[ "$(cat "$HLOG/give-up.actor")" == pg-rescue/* ]]
  [ "$(cat "$HLOG/give-up.cwd")" = "$LAST_STATE" ]
}

@test "a recorded set compares as a SET: a subset or a reordering is nothing new, a build outside it gives up" {
  ITEM=$(new_item '{"failing_builds":"101:1,101:2"}')
  run_shim
  [ "$status" -eq 0 ]
  [ "$(item_meta ci_rerun_of)" = "101:1,101:2" ]
  [ "$(connector_calls)" = "1" ]

  # A subset: nothing new.
  bd -C "$TDIR" update "$ITEM" --set-metadata failing_builds=101:1 >/dev/null
  run_shim
  [ "$status" -eq 0 ]
  assert_unmarked
  [ ! -e "$HLOG/give-up.ran" ]

  # The same elements in another order, with blanks around them: nothing new.
  bd -C "$TDIR" update "$ITEM" --set-metadata "failing_builds=101:2, 101:1" >/dev/null
  run_shim
  [ "$status" -eq 0 ]
  assert_unmarked
  [ ! -e "$HLOG/give-up.ran" ]

  # An empty failing_builds: nothing new.
  bd -C "$TDIR" update "$ITEM" --set-metadata failing_builds=- >/dev/null
  run_shim
  [ "$status" -eq 0 ]
  assert_unmarked
  [ "$(connector_calls)" = "1" ]
  assert_released

  # A build not in the record (the lists are not the same length either way):
  # give-up, no further rerun.
  bd -C "$TDIR" update "$ITEM" --set-metadata failing_builds=101:1,101:3 >/dev/null
  run_shim
  [ "$status" -eq 0 ]
  [ "$(connector_calls)" = "1" ]
  [ -e "$HLOG/give-up.ran" ]
  assert_marked
  assert_released
  [ "$(last_outcome)" = "gave-up" ]
}

@test "a fresh item (a new head commit) gets its own rerun" {
  ITEM=$(new_item)
  run_shim
  [ "$status" -eq 0 ]
  first=$ITEM
  ITEM=$(new_item '{"failing_builds":"202:1"}')
  run_shim
  [ "$status" -eq 0 ]
  [ "$(connector_calls)" = "2" ]
  [ "$(item_meta ci_rerun_of)" = "202:1" ]
  assert_released
  assert_unmarked
  [ "$(bd_fixture_show_json "$first" | jq -r .metadata.ci_rerun_of)" = "101:1" ]
}

@test "a failed ci rerun-failed call leaves the record in place and the chain's give-up hands the item to the worker" {
  ITEM=$(new_item)
  touch "$CONNECTOR_FAIL"
  run_shim
  [ "$status" -eq 0 ]
  [ "$(connector_calls)" = "1" ]
  # The record was made before the call and stays: the rerun is spent.
  [ "$(item_meta ci_rerun_of)" = "101:1" ]
  [ -e "$HLOG/give-up.ran" ]
  assert_released
  assert_marked
  [ "$(last_outcome)" = "gave-up" ]
  # A later run sees the record and issues no second rerun.
  rm "$CONNECTOR_FAIL"
  run_shim
  [ "$status" -eq 0 ]
  [ "$(connector_calls)" = "1" ]
}

@test "a failed ci_rerun_of record makes NO rerun call and the chain's give-up hands the item to the worker" {
  ITEM=$(new_item)
  touch "$BD_FAIL_RECORD"
  run_shim
  [ "$status" -eq 0 ]
  [ "$(connector_calls)" = "0" ]
  [ "$(item_meta ci_rerun_of)" = "null" ]
  [ -e "$HLOG/give-up.ran" ]
  assert_released
  assert_marked
  [ "$(last_outcome)" = "gave-up" ]
}

@test "the first run on an item with no failing_builds reads it as the empty set: one rerun, an empty record" {
  ITEM=$(new_item '{"note":"no builds yet"}')
  run_shim
  [ "$status" -eq 0 ]
  [ "$(connector_calls)" = "1" ]
  bd_fixture_show_json "$ITEM" | jq -e '.metadata | has("ci_rerun_of")' >/dev/null
  # bd stores either the empty string or the single character - (an empty set).
  [[ "$(item_meta ci_rerun_of)" == "" || "$(item_meta ci_rerun_of)" == "-" ]]
  assert_released
  assert_unmarked
  # The record stands: the next run issues no second rerun.
  run_shim
  [ "$status" -eq 0 ]
  [ "$(connector_calls)" = "1" ]
  assert_unmarked
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

@test "a passed-through pg-rescue exit 9 is remapped to 1 and pg-rescue exit 75 to 0, the claim released each time" {
  ITEM=$(new_item)
  stub_pg_rescue 9 75
  run_shim
  [ "$status" -eq 1 ]
  assert_released
  assert_unmarked
  [ "$(connector_calls)" = "0" ]
  [ "$(last_outcome)" = "failed" ]
  # The second call of the stub exits 75: the chain gave up, the shim exits 0
  # and adds no marker itself.
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

@test "a closed item at claim time is outcome gone: exit 0, nothing changed, nothing requested" {
  ITEM=$(new_item)
  bd -C "$TDIR" close "$ITEM" --reason "closed by a peer" >/dev/null 2>&1
  [ "$(item_field '.status')" = "closed" ]
  before=$(bd_fixture_show_json "$ITEM")
  run_shim
  [ "$status" -eq 0 ]
  [ "$(bd_fixture_show_json "$ITEM")" = "$before" ]
  [ "$(connector_calls)" = "0" ]
  [ ! -e "$HLOG/give-up.ran" ]
  [ "$(last_outcome)" = "gone" ]
  [ "$(claim_files "$LAST_STATE")" = "0" ]
}

@test "a claim on a missing item is a failure: exit 1, nothing requested" {
  ITEM=$(new_item)
  SHIM_ITEM=tst-nosuchitem run_shim
  [ "$status" -eq 1 ]
  [ "$(connector_calls)" = "0" ]
  [ "$(last_outcome)" = "failed" ]
}

@test "a TERM during the step leaves no claim: the item is released and the claim record removed" {
  ITEM=$(new_item)
  start_hung_shim
  # While the step runs the claim is held and recorded.
  [ "$(item_field '.assignee // ""')" != "" ]
  [ "$(claim_files "$HUNG_STATE")" = "1" ]
  signal_hung_shim TERM
  [ "$HUNG_STATUS" -eq 143 ]
  assert_released
  [ "$(claim_files "$HUNG_STATE")" = "0" ]
  [ "$(awk '{print $2}' "$HUNG_STATE"/*.outcome)" = "failed" ]
}

@test "an INT during the step leaves no claim: the item is released and the claim record removed" {
  # A shell started with SIGINT ignored (a detached or backgrounded test run)
  # can never trap it, so no script it starts can; probe that exact case.
  set -m
  bash -c 'trap "exit 7" INT; kill -INT $$; sleep 1' &
  probe_status=0
  wait $! || probe_status=$?
  set +m
  [ "$probe_status" -eq 7 ] || skip "SIGINT is ignored at entry in this environment, so it cannot be trapped"
  ITEM=$(new_item)
  start_hung_shim
  [ "$(claim_files "$HUNG_STATE")" = "1" ]
  signal_hung_shim INT
  [ "$HUNG_STATUS" -eq 130 ]
  assert_released
  [ "$(claim_files "$HUNG_STATE")" = "0" ]
}

@test "the claim is recorded as the item id and the actor, one per line, and removed on release" {
  ITEM=$(new_item)
  start_hung_shim
  [ "$(sed -n 1p "$HUNG_STATE"/*.claim)" = "$ITEM" ]
  [[ "$(sed -n 2p "$HUNG_STATE"/*.claim)" == pg-rescue/* ]]
  [ "$(item_field '.assignee')" = "$(sed -n 2p "$HUNG_STATE"/*.claim)" ]
  signal_hung_shim TERM
  [ "$(claim_files "$HUNG_STATE")" = "0" ]
}

@test "a stranded <run-id>.claim older than SWEEP_AGE is released by the next run and its file deleted" {
  STRANDED=$(new_item)
  bd -C "$TDIR" update "$STRANDED" --assignee pg-rescue/old-run --status in_progress --actor pg-rescue/old-run >/dev/null 2>&1
  [ "$(bd_fixture_show_json "$STRANDED" | jq -r .assignee)" = "pg-rescue/old-run" ]
  next_state
  printf '%s\n%s\n' "$STRANDED" pg-rescue/old-run >"$RUN_STATE/old-run.claim"
  ITEM=$(new_item)
  SWEEP_AGE=0 run_shim
  [ "$status" -eq 0 ]
  [ ! -e "$LAST_STATE/old-run.claim" ]
  [ "$(bd_fixture_show_json "$STRANDED" | jq -r '.status')" = "open" ]
  [ -z "$(bd_fixture_show_json "$STRANDED" | jq -r '.assignee // ""')" ]
  assert_released
  [ "$(last_outcome)" = "ok" ]
  [[ "$stderr" == *"released the stranded claim"* ]]
}

@test "a claim record younger than SWEEP_AGE is left alone" {
  STRANDED=$(new_item)
  bd -C "$TDIR" update "$STRANDED" --assignee pg-rescue/old-run --status in_progress --actor pg-rescue/old-run >/dev/null 2>&1
  next_state
  printf '%s\n%s\n' "$STRANDED" pg-rescue/old-run >"$RUN_STATE/old-run.claim"
  ITEM=$(new_item)
  run_shim
  [ "$status" -eq 0 ]
  [ -e "$LAST_STATE/old-run.claim" ]
  [ "$(bd_fixture_show_json "$STRANDED" | jq -r .assignee)" = "pg-rescue/old-run" ]
}

@test "a stale claim record whose item names another assignee deletes the file and touches nothing else" {
  STRANDED=$(new_item)
  bd -C "$TDIR" update "$STRANDED" --assignee someone-else --status in_progress --actor someone-else >/dev/null 2>&1
  next_state
  printf '%s\n%s\n' "$STRANDED" pg-rescue/old-run >"$RUN_STATE/old-run.claim"
  ITEM=$(new_item)
  SWEEP_AGE=0 run_shim
  [ "$status" -eq 0 ]
  [ ! -e "$LAST_STATE/old-run.claim" ]
  [ "$(bd_fixture_show_json "$STRANDED" | jq -r .assignee)" = "someone-else" ]
  [ "$(bd_fixture_show_json "$STRANDED" | jq -r .status)" = "in_progress" ]
}

@test "an item at SCRIPT_MAX_FAILURES runs no step and gets the marker through the give-up chain" {
  ITEM=$(new_item '{"failing_builds":"101:1","script_failures":3}')
  run_shim
  [ "$status" -eq 0 ]
  [ "$(connector_calls)" = "0" ]
  [ "$(item_meta ci_rerun_of)" = "null" ]
  [ -e "$HLOG/give-up.ran" ]
  [ "$(cat "$HLOG/give-up.item")" = "$ITEM" ]
  assert_marked
  assert_released
  [ "$(last_outcome)" = "gave-up" ]
  # Capped, not failed: the counter is not advanced.
  [ "$(item_meta script_failures)" = "3" ]
}

@test "SCRIPT_MAX_FAILURES is overridden by the environment; below the cap the step runs" {
  ITEM=$(new_item '{"failing_builds":"101:1","script_failures":"2"}')
  run_shim
  [ "$status" -eq 0 ]
  [ "$(connector_calls)" = "1" ]
  assert_unmarked
  ITEM=$(new_item '{"failing_builds":"101:1","script_failures":"2"}')
  SCRIPT_MAX_FAILURES=2 run_shim
  [ "$status" -eq 0 ]
  [ "$(connector_calls)" = "1" ]
  assert_marked
}

@test "an unparsable script_failures counts as 0" {
  ITEM=$(new_item '{"failing_builds":"101:1","script_failures":"many"}')
  run_shim
  [ "$status" -eq 0 ]
  [ "$(connector_calls)" = "1" ]
  assert_unmarked
}

@test "a failed run adds one to script_failures in the releasing call, and the cap is then reached" {
  ITEM=$(new_item)
  stub_pg_rescue 70
  : >"$BD_CALL_LOG"
  run_shim
  [ "$status" -eq 70 ]
  [ "$(item_meta script_failures)" = "1" ]
  assert_released
  # One call releases the claim and carries the counter.
  [ "$(grep -c -- "^update $ITEM --status open --assignee  --actor pg-rescue/[^ ]* --set-metadata script_failures=1\$" "$BD_CALL_LOG")" = "1" ]
  run_shim
  [ "$(item_meta script_failures)" = "2" ]
  run_shim
  [ "$(item_meta script_failures)" = "3" ]
  # Now at the cap: the real pg-rescue (no stub) runs the give-up chain, no step.
  PATH="${PATH#"$STUB_DIR:"}"
  export PATH
  run_shim
  [ "$status" -eq 0 ]
  assert_marked
  [ "$(connector_calls)" = "0" ]
  [ "$(last_outcome)" = "gave-up" ]
  [ "$(item_meta script_failures)" = "3" ]
}

@test "the step's hand-over to the worker (exit 1, chain gave up) is not a counted failure" {
  ITEM=$(new_item '{"failing_builds":"101:1,101:2","ci_rerun_of":"101:1"}')
  run_shim
  [ "$status" -eq 0 ]
  assert_marked
  [ "$(last_outcome)" = "gave-up" ]
  [ "$(item_meta script_failures)" = "null" ]
}

@test "the shim's stderr is kept in <run-id>.log" {
  ITEM=$(new_item)
  stub_pg_rescue 70
  run_shim
  [ "$status" -eq 70 ]
  [[ "$stderr" == *"pg-rescue exited 70"* ]]
  [ "$(printf '%s\n' "$LAST_STATE"/*.log | wc -l)" -eq 1 ]
  grep -q "pg-rescue exited 70" "$LAST_STATE"/*.log
}

@test "PG_ROUTER_ITEM_ID reaches the handler and PG_RESCUE_ITEM_ID appears nowhere in the shim" {
  ITEM=$(new_item '{"failing_builds":"101:1,101:2","ci_rerun_of":"101:1"}')
  run_shim
  [ "$status" -eq 0 ]
  [ "$(cat "$HLOG/give-up.item")" = "$ITEM" ]
  ! grep -q PG_RESCUE_ITEM_ID "${SHIM[0]}"
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
