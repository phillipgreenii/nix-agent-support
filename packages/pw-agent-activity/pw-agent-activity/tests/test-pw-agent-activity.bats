#!/usr/bin/env bats
# bats file_tags=type:unit
# Unit tests for pw-agent-activity's passthrough contract.
#
# Every case here STUBS `agent-activity-api` on PATH, so none of them can see
# the real tool rejecting an argument this wrapper handed it. The real-binary
# contract lives in test-pw-agent-activity-real-agent-activity-api.bats; keep
# both.

# The last case uses `run -127`; flags on `run` need bats >= 1.5.0 and bats warns
# (BW02) unless the requirement is declared.
bats_require_minimum_version 1.5.0

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi

  PW_SCRIPT="$SCRIPTS_DIR/pw-agent-activity.sh"

  TEST_DIR="$(mktemp -d)"

  STUB_DIR="$TEST_DIR/bin"
  mkdir -p "$STUB_DIR"
  export PATH="$STUB_DIR:$PATH"

  ARGS_LOG="$TEST_DIR/api-args"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# Invoke the script under test via bash directly (no installed binary needed).
run_pw_agent_activity() {
  run bash "$PW_SCRIPT" "$@"
}

# Stub agent-activity-api: record every argument, one per line, then exit with
# the given code.
create_stub_api() {
  local exit_code="${1:-0}"

  cat >"$STUB_DIR/agent-activity-api" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$@" > "$ARGS_LOG"
exit $exit_code
EOF
  chmod +x "$STUB_DIR/agent-activity-api"
}

# Assert the recorded argument list is exactly the expected sequence.
assert_forwarded_exactly() {
  local expected
  expected="$(printf '%s\n' "$@")"
  [ "$(cat "$ARGS_LOG")" = "$expected" ]
}

@test "delegates to the wait subcommand with no arguments of its own" {
  create_stub_api 0
  run_pw_agent_activity
  [ "$status" -eq 0 ]
  assert_forwarded_exactly wait
}

@test "subcommand token is first so agent-activity-api dispatches on it" {
  create_stub_api 0
  run_pw_agent_activity --maximum-wait 42
  [ "$status" -eq 0 ]
  [ "$(head -1 "$ARGS_LOG")" = "wait" ]
}

@test "forwards every option verbatim and in order" {
  create_stub_api 0
  run_pw_agent_activity --maximum-wait 42 --time-between-checks 7 --caffeinate
  [ "$status" -eq 0 ]
  assert_forwarded_exactly wait --maximum-wait 42 --time-between-checks 7 --caffeinate
}

@test "forwards an unrecognised option instead of rejecting it locally" {
  # The passthrough contract: agent-activity-api owns the option surface, so an
  # option this wrapper has never heard of MUST reach it (and be judged there),
  # not be rejected here.
  create_stub_api 0
  run_pw_agent_activity --some-future-option value
  [ "$status" -eq 0 ]
  assert_forwarded_exactly wait --some-future-option value
}

@test "forwards arguments containing spaces as single words" {
  create_stub_api 0
  run_pw_agent_activity --maximum-wait "1 2"
  [ "$status" -eq 0 ]
  assert_forwarded_exactly wait --maximum-wait "1 2"
}

@test "propagates exit code 0" {
  create_stub_api 0
  run_pw_agent_activity
  [ "$status" -eq 0 ]
}

@test "propagates the timeout exit code 1" {
  create_stub_api 1
  run_pw_agent_activity
  [ "$status" -eq 1 ]
}

@test "propagates the error exit code 2" {
  create_stub_api 2
  run_pw_agent_activity
  [ "$status" -eq 2 ]
}

@test "--help shows usage and exits 0" {
  create_stub_api 0
  run_pw_agent_activity --help
  [ "$status" -eq 0 ]
  [[ "$output" =~ "Usage: pw-agent-activity" ]]
}

@test "-h shows usage and exits 0" {
  create_stub_api 0
  run_pw_agent_activity -h
  [ "$status" -eq 0 ]
  [[ "$output" =~ "Usage: pw-agent-activity" ]]
}

@test "--help never reaches agent-activity-api" {
  create_stub_api 0
  run_pw_agent_activity --help
  [ "$status" -eq 0 ]
  # No args log means the stub was never invoked.
  [ ! -e "$ARGS_LOG" ]
}

@test "--help is honoured after other options too" {
  create_stub_api 0
  run_pw_agent_activity --maximum-wait 42 --help
  [ "$status" -eq 0 ]
  [[ "$output" =~ "Usage: pw-agent-activity" ]]
  [ ! -e "$ARGS_LOG" ]
}

@test "help text names the delegate so callers can find the real option list" {
  create_stub_api 0
  run_pw_agent_activity --help
  [ "$status" -eq 0 ]
  [[ "$output" =~ "agent-activity-api" ]]
}

@test "fails when agent-activity-api cannot be found" {
  # The delegate is a hard requirement, so its absence must be a loud 127 rather
  # than a silent success. PATH is narrowed to a dir holding ONLY a bash symlink:
  # `dirname "$(command -v bash)"` would NOT do -- on this workspace bash and
  # agent-activity-api ship from the same profile bin dir, so the host's real
  # binary would satisfy the lookup and the assertion would pass vacuously (or
  # worse, start a real two-hour wait).
  local bash_bin minimal
  bash_bin="$(command -v bash)"
  minimal="$TEST_DIR/minimal-path"
  mkdir -p "$minimal"
  ln -s "$bash_bin" "$minimal/bash"

  # `run -127`: 127 is the expected outcome here, and bats warns (BW01) about an
  # unannounced 127 from a plain `run`.
  PATH="$minimal" run -127 "$bash_bin" "$PW_SCRIPT"
  [ "$status" -eq 127 ]
}
