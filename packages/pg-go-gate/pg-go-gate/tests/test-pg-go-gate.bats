#!/usr/bin/env bats
# bats file_tags=type:unit

# These tests drive the REAL pg-go-gate and its real slot-lock mechanism against
# real processes in a throwaway lock root (V-3). Only the `go` binary is a stub
# on PATH (the gate keys on the command name `go`); the locking is never mocked.

bats_require_minimum_version 1.5.0

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  export HOME="$TEST_DIR/home"
  mkdir -p "$HOME" "$TEST_DIR/bin"
  export PG_GO_GATE_LOCK_ROOT="$TEST_DIR/slots"

  # Stub go: records start, reports its niceness, then holds until the release
  # file named by $HOLD_FILE exists (no hold when unset).
  cat >"$TEST_DIR/bin/go" <<'GO'
#!/usr/bin/env bash
echo "started $*" >>"$TEST_DIR/events"
echo "nice=$(ps -o ni= -p $$ | tr -d ' ')" >>"$TEST_DIR/events"
if [[ -n ${HOLD_FILE:-} ]]; then
  for _ in $(seq 1 300); do [[ -e $HOLD_FILE ]] && break; sleep 0.1; done
fi
exit "${GO_EXIT:-0}"
GO
  chmod +x "$TEST_DIR/bin/go"
  export PATH="$TEST_DIR/bin:$PATH"

  cat >"$TEST_DIR/pg-go-gate" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "${SCRIPTS_DIR}/pg-go-gate.sh"
WRAPPER
  chmod +x "$TEST_DIR/pg-go-gate"
}

teardown() {
  touch "$TEST_DIR/release"
  pkill -f "$TEST_DIR/pg-go-gate" 2>/dev/null || true
  rm -rf "$TEST_DIR"
}

wait_for_count() { # $1 = pattern, $2 = count, bounded 10s
  local tries=100
  while [[ $(grep -c "$1" "$TEST_DIR/events" 2>/dev/null || true) -lt $2 && $tries -gt 0 ]]; do
    sleep 0.1
    tries=$((tries - 1))
  done
  [[ $(grep -c "$1" "$TEST_DIR/events" 2>/dev/null || true) -ge $2 ]]
}

require_ps() {
  [[ -n "$(ps -p $$ -o lstart= 2>/dev/null)" ]] || skip "ps cannot inspect processes in this sandbox"
}

start_holder() { # $1 = tag; holds a slot until $TEST_DIR/release exists
  HOLD_FILE="$TEST_DIR/release" "$TEST_DIR/pg-go-gate" go test "$1" &
}

@test "4th concurrent go test waits while 3 slots are held, then runs when one frees" {
  require_ps
  start_holder h1
  start_holder h2
  start_holder h3
  wait_for_count "started test h" 3
  "$TEST_DIR/pg-go-gate" --wait 30 go test fourth &
  local p4=$!
  sleep 1
  run grep -c "started test fourth" "$TEST_DIR/events"
  [ "$output" = "0" ]
  kill -0 "$p4"
  touch "$TEST_DIR/release"
  wait_for_count "started test fourth" 1
  wait "$p4"
}

@test "a stale slot (dead owner pid and start time) is reclaimed" {
  require_ps
  mkdir -p "$PG_GO_GATE_LOCK_ROOT"
  for k in 1 2 3; do
    ln -s "999999:Mon Jan 1 00:00:00 2001" "$PG_GO_GATE_LOCK_ROOT/slot.$k"
  done
  run --separate-stderr "$TEST_DIR/pg-go-gate" --wait 0 go test stale
  [ "$status" -eq 0 ]
  [[ $stderr != *"without one"* ]]
  grep -q "started test stale" "$TEST_DIR/events"
  # slot.1 was reclaimed, used and released on exit
  [ ! -L "$PG_GO_GATE_LOCK_ROOT/slot.1" ]
}

@test "a live pid recycled with a different start time counts as stale" {
  require_ps
  mkdir -p "$PG_GO_GATE_LOCK_ROOT"
  for k in 1 2 3; do
    ln -s "$$:Mon Jan 1 00:00:00 2001" "$PG_GO_GATE_LOCK_ROOT/slot.$k"
  done
  run --separate-stderr "$TEST_DIR/pg-go-gate" --wait 0 go build stale
  [ "$status" -eq 0 ]
  [[ $stderr != *"without one"* ]]
}

@test "the wait fails open after the bound, with a warning and the command's exit status" {
  require_ps
  start_holder h1
  start_holder h2
  start_holder h3
  wait_for_count "started test h" 3
  GO_EXIT=7 run --separate-stderr "$TEST_DIR/pg-go-gate" --wait 1 go test open
  [ "$status" -eq 7 ]
  [[ $stderr == *"running without one"* ]]
  grep -q "started test open" "$TEST_DIR/events"
}

@test "a gated command runs at nice 10 and the gate never invokes taskpolicy" {
  require_ps
  run "$TEST_DIR/pg-go-gate" go test niced
  [ "$status" -eq 0 ]
  grep -q "^nice=10$" "$TEST_DIR/events"
  if grep -v "^ *#" "$SCRIPTS_DIR/pg-go-gate.sh" | grep -q taskpolicy; then false; fi
}

@test "an already-niced caller is not pushed above nice 10" {
  require_ps
  # A caller already at or above 10 keeps exactly its own level (no extra nice).
  local expected
  expected="$(nice -n 15 sh -c 'ps -o ni= -p $$' | tr -d ' ')"
  run nice -n 15 "$TEST_DIR/pg-go-gate" go test niced
  [ "$status" -eq 0 ]
  [ "$expected" -ge 10 ]
  grep -q "^nice=$expected$" "$TEST_DIR/events"
}

@test "go subcommands other than test/build/install and other commands are not gated" {
  mkdir -p "$PG_GO_GATE_LOCK_ROOT"
  for k in 1 2 3; do
    ln -s "$$:$(ps -o lstart= -p $$ | tr -s ' ')" "$PG_GO_GATE_LOCK_ROOT/slot.$k"
  done
  run --separate-stderr "$TEST_DIR/pg-go-gate" --wait 0 go vet ./...
  [ "$status" -eq 0 ]
  [[ $stderr != *"without one"* ]]
  run "$TEST_DIR/pg-go-gate" echo hello
  [ "$output" = "hello" ]
}

@test "rejects nice above 10 and missing command" {
  run "$TEST_DIR/pg-go-gate" --nice 19 go test x
  [ "$status" -eq 1 ]
  run "$TEST_DIR/pg-go-gate"
  [ "$status" -eq 1 ]
}
