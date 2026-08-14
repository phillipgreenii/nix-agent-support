#!/usr/bin/env bats
# Unit tests for wait-for-agents-to-finish's argument assembly.
#
# Every case here STUBS `pa-monitor` on PATH, so none of them can see the
# wrapper handing pa-monitor an argument pa-monitor does not define -- which is
# exactly how the removed `--wait-until-idle` flag survived here (bead
# pg2-3fv9l). The real-binary argument contract is covered by
# test-wait-for-agents-real-pa-monitor.bats; keep both.

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi

  WFA_SCRIPT="$SCRIPTS_DIR/wait-for-agents-to-finish.sh"

  # Absolute bash, resolved before any test narrows PATH to the stub dir.
  BASH_BIN="$(command -v bash)"

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

# Helper: Create a stub pa-monitor that records its arguments and exits
# with the given exit code.
create_stub_tui() {
  local exit_code="${1:-0}"
  local args_log="$TEST_DIR/tui-args"

  cat >"$STUB_DIR/pa-monitor" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$@" > "$args_log"
exit $exit_code
EOF
  chmod +x "$STUB_DIR/pa-monitor"
}

# Assert pa-monitor was NOT handed the given argument (matched as a whole
# recorded line). Written as a `[[ ... != ... ]]` test rather than `! grep`
# because in bats a `!`-prefixed command cannot fail the test (shellcheck
# SC2314), so the negation must live inside the test expression.
refute_forwarded() {
  local args
  args=$'\n'"$(cat "$TEST_DIR/tui-args")"$'\n'
  [[ $args != *$'\n'"$1"$'\n'* ]]
}

@test "exits 0 when pa-monitor exits 0" {
  create_stub_tui 0
  run_wait_for_agents --maximum-wait 5
  [ "$status" -eq 0 ]
}

@test "exits 1 when pa-monitor exits 1" {
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

@test "--version delegates to pa-monitor --version" {
  cat >"$STUB_DIR/pa-monitor" <<'EOF'
#!/usr/bin/env bash
if [[ "$1" == "--version" ]]; then
  echo "pa-monitor 1.2.3"
  exit 0
fi
exit 99
EOF
  chmod +x "$STUB_DIR/pa-monitor"

  run_wait_for_agents --version
  [ "$status" -eq 0 ]
  [[ "$output" == *"pa-monitor 1.2.3"* ]]
}

@test "invokes the wait-until-agents-finished subcommand by default" {
  create_stub_tui 0
  run_wait_for_agents
  [ "$status" -eq 0 ]
  grep -q -- "^wait-until-agents-finished$" "$TEST_DIR/tui-args"
  # Position is load-bearing: pa-monitor's pickSubcommand routes a LEADING flag
  # to `tui`, so the subcommand token MUST be argument 1.
  [ "$(head -1 "$TEST_DIR/tui-args")" = "wait-until-agents-finished" ]
}

@test "never passes the removed --wait-until-idle flag" {
  create_stub_tui 0
  run_wait_for_agents --maximum-wait 5 --consecutive-idle-checks 2
  [ "$status" -eq 0 ]
  refute_forwarded "--wait-until-idle"
  [ "$(head -1 "$TEST_DIR/tui-args")" = "wait-until-agents-finished" ]
}

@test "forwards --maximum-wait to pa-monitor" {
  create_stub_tui 0
  run_wait_for_agents --maximum-wait 42
  [ "$status" -eq 0 ]
  grep -q -- "--maximum-wait" "$TEST_DIR/tui-args"
  grep -q -- "^42$" "$TEST_DIR/tui-args"
}

@test "accepts --time-between-checks but does NOT forward it" {
  create_stub_tui 0
  run_wait_for_agents --time-between-checks 7
  [ "$status" -eq 0 ]
  # pa-monitor defines no poll-interval option on wait-until-agents-finished
  # (the cadence comes from the daemon's push stream), so forwarding it would
  # abort the wait on an undefined flag.
  refute_forwarded "--time-between-checks"
  refute_forwarded "7"
  [[ "$output" =~ "--time-between-checks is ignored" ]]
}

@test "forwards --consecutive-idle-checks to pa-monitor" {
  create_stub_tui 0
  run_wait_for_agents --consecutive-idle-checks 4
  [ "$status" -eq 0 ]
  grep -q -- "--consecutive-idle-checks" "$TEST_DIR/tui-args"
  grep -q -- "^4$" "$TEST_DIR/tui-args"
}

@test "does NOT forward --caffeinate to pa-monitor" {
  create_stub_tui 0
  run_wait_for_agents --caffeinate
  [ "$status" -eq 0 ]
  # Handled locally via the system `caffeinate`; pa-monitor's own caffeinate is
  # a daemon-owned mode with its own subcommand, not a wait option.
  refute_forwarded "--caffeinate"
}

@test "--caffeinate keeps the Mac awake via the system caffeinate" {
  create_stub_tui 0
  cat >"$STUB_DIR/caffeinate" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$@" > "$TEST_DIR/caffeinate-args"
EOF
  chmod +x "$STUB_DIR/caffeinate"

  run_wait_for_agents --caffeinate
  [ "$status" -eq 0 ]
  # `-w <pid>`: caffeinate exits when the wrapper's process does.
  grep -q -- "^-w$" "$TEST_DIR/caffeinate-args"
}

@test "--caffeinate is a no-op when caffeinate is unavailable" {
  create_stub_tui 0
  # No caffeinate stub, and PATH narrowed so the host's real /usr/bin/caffeinate
  # cannot be found either: the lookup must fail softly rather than abort the
  # wait. bash's own dir stays on PATH -- the stub's `env bash` shebang needs it.
  PATH="$STUB_DIR:$(dirname "$BASH_BIN")" run "$BASH_BIN" "$WFA_SCRIPT" --caffeinate
  [ "$status" -eq 0 ]
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

@test "combines multiple flags and forwards only the ones pa-monitor defines" {
  create_stub_tui 0
  run_wait_for_agents --maximum-wait 60 --time-between-checks 5 --consecutive-idle-checks 3 --caffeinate
  [ "$status" -eq 0 ]
  [ "$(head -1 "$TEST_DIR/tui-args")" = "wait-until-agents-finished" ]
  grep -q -- "--maximum-wait" "$TEST_DIR/tui-args"
  grep -q -- "--consecutive-idle-checks" "$TEST_DIR/tui-args"
  # Neither of these exists on `pa-monitor wait-until-agents-finished`.
  refute_forwarded "--time-between-checks"
  refute_forwarded "--caffeinate"
}
