#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Script-level tests that need no real bd: argument validation (nothing may
# reach bd), and the whole create + read-back flow against a bd SHIM that
# answers `create` with an id and `show --json` with canned output in either
# envelope shape.

bats_require_minimum_version 1.5.0

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  # The shim lives OUTSIDE any cwd the script is run from.
  SHIM_DIR="$(mktemp -d)"
  export BD_CALL_LOG="$SHIM_DIR/calls.log"
  : >"$BD_CALL_LOG"
  cat >"$SHIM_DIR/bd" <<'SHIM'
#!/usr/bin/env bash
# Records argv; `create` echoes a fixed id (or fails per BD_SHIM_CREATE_FAIL);
# `show` prints $BD_SHIM_SHOW_JSON.
printf '%s\n' "${*//$'\n'/\\n}" >>"$BD_CALL_LOG"
for a in "$@"; do
  case "$a" in
  create)
    if [ -n "${BD_SHIM_CREATE_FAIL:-}" ]; then
      echo "$BD_SHIM_CREATE_FAIL" >&2
      exit 1
    fi
    echo "shim-1"
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
  chmod +x "$SHIM_DIR/bd"
  PATH="$SHIM_DIR:$PATH"
  export PATH

  cat >"$TEST_DIR/run-handoff-create" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "${SCRIPTS_DIR}/handoff-create.bash"
source "${SCRIPTS_DIR}/handoff-create.sh"
WRAPPER
  chmod +x "$TEST_DIR/run-handoff-create"
  HC="$TEST_DIR/run-handoff-create"
  printf 'Carry-over: pg2-aaa11.\n' >"$TEST_DIR/body.md"
  : >"$TEST_DIR/empty.md"
}

teardown() {
  rm -rf "$TEST_DIR" "$SHIM_DIR"
}

shim_issue() { # type labels-json [first-line]
  jq -cn --arg t "$1" --argjson l "$2" --arg fl "${3:-Handoff from session s1}" \
    '{id:"shim-1", issue_type:$t, title:"Handoff: subj", priority:0, description:($fl + "\n\nCarry-over: pg2-aaa11."), metadata:{handed_off_from_session:"s1"}, labels:$l}'
}

assert_no_bd_call() { [ ! -s "$BD_CALL_LOG" ]; }

@test "no mode flag: usage error 2 and bd is never called" {
  run --separate-stderr "$HC" --session-id s1 --title subj --body-file "$TEST_DIR/body.md"
  [ "$status" -eq 2 ]
  [[ $stderr == *"one of --attended or --unattended is required"* ]]
  assert_no_bd_call
}

@test "both mode flags: usage error 2" {
  run --separate-stderr "$HC" --attended --unattended --session-id s1 --title subj --body-file "$TEST_DIR/body.md"
  [ "$status" -eq 2 ]
  [[ $stderr == *"mutually exclusive"* ]]
  assert_no_bd_call
}

@test "each missing required value is a usage error 2 and bd is never called" {
  run --separate-stderr "$HC" --attended --title subj --body-file "$TEST_DIR/body.md"
  [ "$status" -eq 2 ]
  [[ $stderr == *"--session-id is required"* ]]
  run --separate-stderr "$HC" --attended --session-id s1 --body-file "$TEST_DIR/body.md"
  [ "$status" -eq 2 ]
  [[ $stderr == *"--title is required"* ]]
  run --separate-stderr "$HC" --attended --session-id s1 --title subj
  [ "$status" -eq 2 ]
  [[ $stderr == *"--body-file is required"* ]]
  assert_no_bd_call
}

@test "invalid inputs are usage errors: body file, session id, label, bd-dir, unknown flag" {
  run --separate-stderr "$HC" --attended --session-id s1 --title subj --body-file "$TEST_DIR/nope.md"
  [ "$status" -eq 2 ]
  [[ $stderr == *"not a readable file"* ]]
  run --separate-stderr "$HC" --attended --session-id s1 --title subj --body-file "$TEST_DIR/empty.md"
  [ "$status" -eq 2 ]
  [[ $stderr == *"is empty"* ]]
  run --separate-stderr "$HC" --attended --session-id 'bad id' --title subj --body-file "$TEST_DIR/body.md"
  [ "$status" -eq 2 ]
  [[ $stderr == *"--session-id must match"* ]]
  run --separate-stderr "$HC" --attended --session-id s1 --title subj --body-file "$TEST_DIR/body.md" --label 'a,b'
  [ "$status" -eq 2 ]
  [[ $stderr == *"invalid --label"* ]]
  run --separate-stderr "$HC" --attended --session-id s1 --title subj --body-file "$TEST_DIR/body.md" --bd-dir "$TEST_DIR/missing"
  [ "$status" -eq 2 ]
  [[ $stderr == *"--bd-dir is not a directory"* ]]
  run --separate-stderr "$HC" --attended --session-id s1 --title subj --body-file "$TEST_DIR/body.md" --bogus
  [ "$status" -eq 2 ]
  [[ $stderr == *"unknown argument"* ]]
  run --separate-stderr "$HC" --attended --session-id
  [ "$status" -eq 2 ]
  [[ $stderr == *"--session-id requires a value"* ]]
  assert_no_bd_call
}

@test "--label human with --unattended is refused before bd is called" {
  run --separate-stderr "$HC" --unattended --session-id s1 --title subj --body-file "$TEST_DIR/body.md" --label human
  [ "$status" -eq 2 ]
  [[ $stderr == *"MUST NOT carry human"* ]]
  assert_no_bd_call
}

@test "--help prints usage and exits 0 without calling bd" {
  run "$HC" --help
  [ "$status" -eq 0 ]
  [[ $output == *"--attended | --unattended"* ]]
  assert_no_bd_call
}

@test "attended flow on the envelope shape: create passes -l human, id on stdout" {
  export BD_SHIM_SHOW_JSON
  BD_SHIM_SHOW_JSON="$(jq -cn --argjson i "$(shim_issue handoff '["human"]')" '{data:[$i], schema_version:1}')"
  run --separate-stderr "$HC" --attended --session-id s1 --title subj --body-file "$TEST_DIR/body.md" --bd-dir "$TEST_DIR"
  [ "$status" -eq 0 ]
  [ "$output" = "shim-1" ]
  create_line="$(grep -E '(^| )create( |$)' "$BD_CALL_LOG")"
  [[ $create_line == "-C $TEST_DIR create -t handoff -p 0 --title Handoff: subj "* ]]
  [[ $create_line == *"-l human"* ]]
  [[ $create_line == *"--actor s1"* ]]
  [[ $create_line == *'--metadata {"handed_off_from_session":"s1"}'* ]]
  grep -qE '^-C .* show shim-1 --json$' "$BD_CALL_LOG"
}

@test "unattended flow on the bare-array shape: no human label on the create" {
  export BD_SHIM_SHOW_JSON
  BD_SHIM_SHOW_JSON="$(jq -cn --argjson i "$(shim_issue handoff '[]')" '[$i]')"
  run --separate-stderr "$HC" --unattended --session-id s1 --title subj --body-file "$TEST_DIR/body.md"
  [ "$status" -eq 0 ]
  [ "$output" = "shim-1" ]
  create_line="$(grep -E '(^| )create( |$)' "$BD_CALL_LOG")"
  [[ $create_line != *"-l human"* ]]
  [[ $create_line != *" -l "* ]]
}

@test "read-back disagreement: exit 4, the bead id is named, stdout stays empty" {
  export BD_SHIM_SHOW_JSON
  BD_SHIM_SHOW_JSON="$(jq -cn --argjson i "$(shim_issue handoff '[]')" '[$i]')"
  # attended asked for human, the shim's bead has none
  run --separate-stderr "$HC" --attended --session-id s1 --title subj --body-file "$TEST_DIR/body.md"
  [ "$status" -eq 4 ]
  [ -z "$output" ]
  [[ $stderr == *"MISMATCH"* ]]
  [[ $stderr == *"shim-1"* ]]
}

@test "unregistered type: one retry as task, both creates identical but for the type" {
  # first create fails as an unregistered type, the retry (a second run of the
  # shim) must succeed: switch the failure off after the first call.
  cat >"$SHIM_DIR/bd" <<'SHIM'
#!/usr/bin/env bash
printf '%s\n' "${*//$'\n'/\\n}" >>"$BD_CALL_LOG"
for a in "$@"; do
  case "$a" in
  create)
    if [ "$(grep -cE '(^| )create( |$)' "$BD_CALL_LOG")" -eq 1 ]; then
      echo "Error: validation failed for issue : invalid issue type: handoff" >&2
      exit 1
    fi
    echo "shim-1"; exit 0 ;;
  show) printf '%s\n' "$BD_SHIM_SHOW_JSON"; exit 0 ;;
  esac
done
SHIM
  export BD_SHIM_SHOW_JSON
  BD_SHIM_SHOW_JSON="$(jq -cn --argjson i "$(shim_issue task '[]')" '[$i]')"
  run --separate-stderr "$HC" --unattended --session-id s1 --title subj --body-file "$TEST_DIR/body.md"
  [ "$status" -eq 0 ]
  mapfile -t creates < <(grep -E '(^| )create( |$)' "$BD_CALL_LOG")
  [ "${#creates[@]}" -eq 2 ]
  [ "${creates[0]/-t handoff/-t X}" = "${creates[1]/-t task/-t X}" ]
}

@test "unregistered type then the retry also fails: exit 3, no third attempt" {
  export BD_SHIM_CREATE_FAIL="Error: validation failed for issue : invalid issue type: handoff"
  run --separate-stderr "$HC" --unattended --session-id s1 --title subj --body-file "$TEST_DIR/body.md"
  [ "$status" -eq 3 ]
  [ "$(grep -cE '(^| )create( |$)' "$BD_CALL_LOG")" -eq 2 ]
}

@test "any other create failure: exit 3, exactly one attempt, message surfaced" {
  export BD_SHIM_CREATE_FAIL="Error: database not found"
  run --separate-stderr "$HC" --attended --session-id s1 --title subj --body-file "$TEST_DIR/body.md"
  [ "$status" -eq 3 ]
  [[ $stderr == *"database not found"* ]]
  [ "$(grep -cE '(^| )create( |$)' "$BD_CALL_LOG")" -eq 1 ]
}
