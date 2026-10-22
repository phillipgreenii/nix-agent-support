#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Script-level tests that need no real bd: argument validation (nothing may reach
# bd or pg-connector), the exact shape of the ONE update call in each mode, and
# the read-back verdict against a bd SHIM that answers `show --json` with canned
# output in either envelope shape. The real-bd behaviour is
# test-hand-to-human.bats.

bats_require_minimum_version 1.5.0

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  # The shims live OUTSIDE any cwd the script is run from.
  SHIM_DIR="$(mktemp -d)"
  export BD_CALL_LOG="$SHIM_DIR/bd-calls.log"
  export PGC_CALL_LOG="$SHIM_DIR/pgc-calls.log"
  : >"$BD_CALL_LOG"
  : >"$PGC_CALL_LOG"
  cat >"$SHIM_DIR/bd" <<'SHIM'
#!/usr/bin/env bash
# Records argv one call per line; `update` fails per BD_SHIM_UPDATE_FAIL; `show`
# prints $BD_SHIM_SHOW_JSON.
printf '%s\n' "${*//$'\n'/\\n}" >>"$BD_CALL_LOG"
for a in "$@"; do
  case "$a" in
  update)
    if [ -n "${BD_SHIM_UPDATE_FAIL:-}" ]; then
      echo "$BD_SHIM_UPDATE_FAIL" >&2
      exit 1
    fi
    exit 0
    ;;
  show)
    printf '%s\n' "$BD_SHIM_SHOW_JSON"
    exit 0
    ;;
  esac
done
exit 0
SHIM
  cat >"$SHIM_DIR/pg-connector" <<'SHIM'
#!/usr/bin/env bash
printf '%s\n' "${*//$'\n'/\\n}" >>"$PGC_CALL_LOG"
if [ -n "${PGC_SHIM_FAIL:-}" ]; then
  echo "$PGC_SHIM_FAIL" >&2
  exit 1
fi
exit 0
SHIM
  chmod +x "$SHIM_DIR/bd" "$SHIM_DIR/pg-connector"
  PATH="$SHIM_DIR:$PATH"
  export PATH

  cat >"$TEST_DIR/run-hand-to-human" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "${SCRIPTS_DIR}/hand-to-human.bash"
source "${SCRIPTS_DIR}/hand-to-human.sh"
WRAPPER
  chmod +x "$TEST_DIR/run-hand-to-human"
  H="$TEST_DIR/run-hand-to-human"
}

teardown() {
  rm -rf "$TEST_DIR" "$SHIM_DIR"
}

show_json() { # labels-json
  jq -cn --argjson l "$1" '{id:"shim-1", labels:$l}'
}

assert_no_bd_call() { [ ! -s "$BD_CALL_LOG" ]; }
assert_no_pgc_call() { [ ! -s "$PGC_CALL_LOG" ]; }

@test "no bead id: usage error 2 and nothing is called" {
  run --separate-stderr "$H"
  [ "$status" -eq 2 ]
  [[ $stderr == *"a bead id is required"* ]]
  assert_no_bd_call
  assert_no_pgc_call
}

@test "invalid inputs are usage errors and nothing is called" {
  run --separate-stderr "$H" shim-1 other-2
  [ "$status" -eq 2 ]
  [[ $stderr == *"exactly one bead id"* ]]
  run --separate-stderr "$H" 'bad id'
  [ "$status" -eq 2 ]
  [[ $stderr == *"invalid bead id"* ]]
  run --separate-stderr "$H" shim-1 --bogus
  [ "$status" -eq 2 ]
  [[ $stderr == *"unknown argument"* ]]
  run --separate-stderr "$H" shim-1 --actor
  [ "$status" -eq 2 ]
  [[ $stderr == *"--actor requires a value"* ]]
  run --separate-stderr "$H" shim-1 --bd-dir "$TEST_DIR/missing"
  [ "$status" -eq 2 ]
  [[ $stderr == *"--bd-dir is not a directory"* ]]
  run --separate-stderr "$H" shim-1 --also-remove 'a,b'
  [ "$status" -eq 2 ]
  [[ $stderr == *"invalid --also-remove"* ]]
  run --separate-stderr "$H" shim-1 --backend b1 --bd-dir "$TEST_DIR"
  [ "$status" -eq 2 ]
  [[ $stderr == *"mutually exclusive"* ]]
  run --separate-stderr "$H" shim-1 --backend b1 --actor a1
  [ "$status" -eq 2 ]
  [[ $stderr == *"--actor is not supported with --backend"* ]]
  assert_no_bd_call
  assert_no_pgc_call
}

@test "--also-remove human or attention is refused (the call adds them)" {
  run --separate-stderr "$H" shim-1 --also-remove human
  [ "$status" -eq 2 ]
  [[ $stderr == *"refused"* ]]
  run --separate-stderr "$H" shim-1 --also-remove attention
  [ "$status" -eq 2 ]
  assert_no_bd_call
}

@test "--help prints usage and exits 0 without calling anything" {
  run "$H" --help
  [ "$status" -eq 0 ]
  [[ $output == *"human + attention"* ]]
  assert_no_bd_call
  assert_no_pgc_call
}

@test "bd mode: ONE update call, adds before the removal, id after --; then one show" {
  export BD_SHIM_SHOW_JSON
  BD_SHIM_SHOW_JSON="$(show_json '["human","attention"]')"
  run --separate-stderr "$H" shim-1 --actor sess-1
  [ "$status" -eq 0 ]
  [ "$output" = "shim-1" ]
  [ "$(grep -c ' update \|^update ' "$BD_CALL_LOG")" -eq 1 ]
  [ "$(grep -c 'update' "$BD_CALL_LOG")" -eq 1 ]
  [ "$(grep -c . "$BD_CALL_LOG")" -eq 2 ]
  grep -qx 'update --add-label human --add-label attention --remove-label escalated --actor sess-1 -- shim-1' "$BD_CALL_LOG"
  [[ $stderr == *"ok: label human present"* ]]
  [[ $stderr == *"ok: label escalated absent"* ]]
}

@test "bd mode with --also-remove and --bd-dir: still one update, extras after escalated" {
  export BD_SHIM_SHOW_JSON
  BD_SHIM_SHOW_JSON="$(jq -cn --argjson i "$(show_json '["human","attention"]')" '{data:[$i], schema_version:1}')"
  run --separate-stderr "$H" shim-1 --also-remove needs-split-review --also-remove needs-split-review --bd-dir "$TEST_DIR"
  [ "$status" -eq 0 ]
  [ "$(grep -c 'update' "$BD_CALL_LOG")" -eq 1 ]
  grep -qx -- "-C $TEST_DIR update --add-label human --add-label attention --remove-label escalated --remove-label needs-split-review -- shim-1" "$BD_CALL_LOG"
}

@test "read-back mismatch (a label is missing or escalated remains): exit 4 naming the id" {
  export BD_SHIM_SHOW_JSON
  BD_SHIM_SHOW_JSON="$(show_json '["human","escalated"]')"
  run --separate-stderr "$H" shim-1
  [ "$status" -eq 4 ]
  [[ $stderr == *"MISMATCH: label attention missing"* ]]
  [[ $stderr == *"MISMATCH: label escalated still present"* ]]
  [[ $stderr == *"shim-1 WAS updated"* ]]
}

@test "bd update failure: exit 3, no read-back, not retried" {
  export BD_SHIM_UPDATE_FAIL="no issue found matching"
  run --separate-stderr "$H" shim-1
  [ "$status" -eq 3 ]
  [[ $stderr == *"nothing was written"* ]]
  [[ $stderr == *"no issue found matching"* ]]
  [ "$(grep -c . "$BD_CALL_LOG")" -eq 1 ]
}

@test "backend mode: ONE pg-connector issue update, no bd call" {
  run --separate-stderr "$H" shim-1 --backend pg-connector-issue-beads-pg2
  [ "$status" -eq 0 ]
  [ "$output" = "shim-1" ]
  [ "$(grep -c . "$PGC_CALL_LOG")" -eq 1 ]
  grep -qx 'issue update shim-1 --backend pg-connector-issue-beads-pg2 --add-label human --add-label attention --remove-label escalated' "$PGC_CALL_LOG"
  assert_no_bd_call
}

@test "backend mode with --also-remove appends the extra removal to the same call" {
  run --separate-stderr "$H" shim-1 --backend b1 --also-remove needs-split-review
  [ "$status" -eq 0 ]
  [ "$(grep -c . "$PGC_CALL_LOG")" -eq 1 ]
  grep -qx 'issue update shim-1 --backend b1 --add-label human --add-label attention --remove-label escalated --remove-label needs-split-review' "$PGC_CALL_LOG"
}

@test "backend mode failure: exit 3, not retried" {
  export PGC_SHIM_FAIL="backend unavailable"
  run --separate-stderr "$H" shim-1 --backend b1
  [ "$status" -eq 3 ]
  [[ $stderr == *"backend unavailable"* ]]
  [ "$(grep -c . "$PGC_CALL_LOG")" -eq 1 ]
}
