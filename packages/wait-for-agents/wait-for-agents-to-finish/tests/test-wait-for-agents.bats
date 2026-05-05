#!/usr/bin/env bats
# Tests for wait-for-agents-to-finish thin wrapper around claude-agents-tui

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi

  WFA_SCRIPT="$SCRIPTS_DIR/wait-for-agents-to-finish.sh"

  # Create temporary test directory
  TEST_DIR="$(mktemp -d)"

  # Create stub bin directory and prepend to PATH so stubs are found
  STUB_DIR="$TEST_DIR/bin"
  mkdir -p "$STUB_DIR"
  export PATH="$STUB_DIR:$PATH"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# Invoke the script under test via bash directly (no installed binary needed)
run_wait_for_agents() {
  run bash "$WFA_SCRIPT" "$@"
}

# Helper: Create a stub claude-agents-tui that records its arguments and exits
# with the given exit code.
create_stub_tui() {
  local exit_code="${1:-0}"
  local args_log="$TEST_DIR/tui-args"

  cat >"$STUB_DIR/claude-agents-tui" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$@" > "$args_log"
exit $exit_code
EOF
  chmod +x "$STUB_DIR/claude-agents-tui"
}

@test "exits 0 when claude-agents-tui exits 0" {
  create_stub_tui 0
  run_wait_for_agents --maximum-wait 5
  [ "$status" -eq 0 ]
}

@test "exits 1 when claude-agents-tui exits 1" {
  create_stub_tui 1
  run_wait_for_agents --maximum-wait 5
  [ "$status" -eq 1 ]
}

@test "rejects unknown options with exit code 2" {
  run_wait_for_agents --bogus
  [ "$status" -eq 2 ]
  [[ "$output" =~ "Unknown option" ]]
}

@test "--help shows usage and exits 0" {
  run_wait_for_agents --help
  [ "$status" -eq 0 ]
  [[ "$output" =~ "Usage: wait-for-agents-to-finish" ]]
}

@test "-h shows usage and exits 0" {
  run_wait_for_agents -h
  [ "$status" -eq 0 ]
  [[ "$output" =~ "Usage: wait-for-agents-to-finish" ]]
}

@test "--version delegates to claude-agents-tui --version" {
  cat >"$STUB_DIR/claude-agents-tui" <<'EOF'
#!/usr/bin/env bash
if [[ "$1" == "--version" ]]; then
  echo "claude-agents-tui 1.2.3"
  exit 0
fi
exit 99
EOF
  chmod +x "$STUB_DIR/claude-agents-tui"

  run_wait_for_agents --version
  [ "$status" -eq 0 ]
  [[ "$output" =~ "claude-agents-tui 1.2.3" ]]
}

@test "forwards --wait-until-idle by default" {
  create_stub_tui 0
  run_wait_for_agents
  [ "$status" -eq 0 ]
  grep -q -- "--wait-until-idle" "$TEST_DIR/tui-args"
}

@test "forwards --maximum-wait to claude-agents-tui" {
  create_stub_tui 0
  run_wait_for_agents --maximum-wait 42
  [ "$status" -eq 0 ]
  grep -q -- "--maximum-wait" "$TEST_DIR/tui-args"
  grep -q -- "^42$" "$TEST_DIR/tui-args"
}

@test "forwards --time-between-checks to claude-agents-tui" {
  create_stub_tui 0
  run_wait_for_agents --time-between-checks 7
  [ "$status" -eq 0 ]
  grep -q -- "--time-between-checks" "$TEST_DIR/tui-args"
  grep -q -- "^7$" "$TEST_DIR/tui-args"
}

@test "forwards --consecutive-idle-checks to claude-agents-tui" {
  create_stub_tui 0
  run_wait_for_agents --consecutive-idle-checks 4
  [ "$status" -eq 0 ]
  grep -q -- "--consecutive-idle-checks" "$TEST_DIR/tui-args"
  grep -q -- "^4$" "$TEST_DIR/tui-args"
}

@test "forwards --caffeinate to claude-agents-tui" {
  create_stub_tui 0
  run_wait_for_agents --caffeinate
  [ "$status" -eq 0 ]
  grep -q -- "--caffeinate" "$TEST_DIR/tui-args"
}

@test "--maximum-wait requires a value" {
  run_wait_for_agents --maximum-wait
  [ "$status" -eq 2 ]
  [[ "$output" =~ "requires a value" ]]
}

@test "--time-between-checks requires a value" {
  run_wait_for_agents --time-between-checks
  [ "$status" -eq 2 ]
  [[ "$output" =~ "requires a value" ]]
}

@test "--consecutive-idle-checks requires a value" {
  run_wait_for_agents --consecutive-idle-checks
  [ "$status" -eq 2 ]
  [[ "$output" =~ "requires a value" ]]
}

@test "combines multiple flags and forwards them all" {
  create_stub_tui 0
  run_wait_for_agents --maximum-wait 60 --time-between-checks 5 --consecutive-idle-checks 3 --caffeinate
  [ "$status" -eq 0 ]
  grep -q -- "--wait-until-idle" "$TEST_DIR/tui-args"
  grep -q -- "--maximum-wait" "$TEST_DIR/tui-args"
  grep -q -- "--time-between-checks" "$TEST_DIR/tui-args"
  grep -q -- "--consecutive-idle-checks" "$TEST_DIR/tui-args"
  grep -q -- "--caffeinate" "$TEST_DIR/tui-args"
}
