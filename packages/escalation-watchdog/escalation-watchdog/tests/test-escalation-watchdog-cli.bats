#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Argument validation of escalation-watchdog: every rejection is exit 2 and happens
# before bd is touched (no bd is needed here).

bats_require_minimum_version 1.5.0

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  TEST_DIR="$(mktemp -d)"
  SHIM_DIR="$(mktemp -d)"
  export BD_CALL_LOG="$SHIM_DIR/bd-calls.log"
  : >"$BD_CALL_LOG"
  # Tools that record every call; validation failures must never reach them.
  for t in bd hand-to-human; do
    cat >"$SHIM_DIR/$t" <<'SHIM'
#!/usr/bin/env bash
printf '%s %s\n' "$(basename "$0")" "$*" >>"$BD_CALL_LOG"
exit 0
SHIM
    chmod +x "$SHIM_DIR/$t"
  done
  PATH="$SHIM_DIR:$PATH"
  cat >"$TEST_DIR/run-escalation-watchdog" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "${SCRIPTS_DIR}/escalation-watchdog.bash"
source "${SCRIPTS_DIR}/escalation-watchdog.sh"
WRAPPER
  chmod +x "$TEST_DIR/run-escalation-watchdog"
  W="$TEST_DIR/run-escalation-watchdog"
}

teardown() {
  rm -rf "$TEST_DIR" "$SHIM_DIR"
}

assert_nothing_called() { [ ! -s "$BD_CALL_LOG" ]; }

@test "--help documents the options, the exit codes and the clock override" {
  run "$W" --help
  [ "$status" -eq 0 ]
  [[ $output == *"--bd-dir"* ]]
  [[ $output == *"--stale-after"* ]]
  [[ $output == *"--max-per-tick"* ]]
  [[ $output == *"--quiet-for"* ]]
  [[ $output == *"--dry-run"* ]]
  [[ $output == *"ESCALATION_WATCHDOG_NOW"* ]]
}

@test "strict durations: anything but <n>h|<n>m|<n>s is exit 2, nothing called" {
  for bad in 12 1h30m 1.5h 12H 5d -5m +5m ' 5m' '5m ' h 0x10m 5ms; do
    run --separate-stderr "$W" --bd-dir "$TEST_DIR" --stale-after "$bad"
    [ "$status" -eq 2 ]
    [[ $stderr == *"invalid --stale-after"* ]]
    run --separate-stderr "$W" --bd-dir "$TEST_DIR" --stale-after 12h --quiet-for "$bad"
    [ "$status" -eq 2 ]
    [[ $stderr == *"invalid --quiet-for"* ]]
  done
  assert_nothing_called
}

@test "valid durations are accepted" {
  for ok in 12h 90m 30s 0s 007m; do
    ESCALATION_WATCHDOG_NOW=1800000000 run --separate-stderr "$W" --bd-dir "$TEST_DIR" --stale-after "$ok" --dry-run
    [ "$status" -eq 0 ]
  done
}

@test "required options, unknown arguments, bad values: exit 2, nothing called" {
  run --separate-stderr "$W"
  [ "$status" -eq 2 ]
  [[ $stderr == *"--bd-dir is required"* ]]
  run --separate-stderr "$W" --bd-dir "$TEST_DIR"
  [ "$status" -eq 2 ]
  [[ $stderr == *"--stale-after is required"* ]]
  run --separate-stderr "$W" --bd-dir "$TEST_DIR/nope" --stale-after 1h
  [ "$status" -eq 2 ]
  [[ $stderr == *"not a directory"* ]]
  run --separate-stderr "$W" --bd-dir "$TEST_DIR" --stale-after 1h --bogus
  [ "$status" -eq 2 ]
  [[ $stderr == *"unknown argument"* ]]
  run --separate-stderr "$W" --bd-dir "$TEST_DIR" --stale-after
  [ "$status" -eq 2 ]
  for bad in 0 -1 1.5 abc 1000000; do
    run --separate-stderr "$W" --bd-dir "$TEST_DIR" --stale-after 1h --max-per-tick "$bad"
    [ "$status" -eq 2 ]
    [[ $stderr == *"invalid --max-per-tick"* ]]
  done
  ESCALATION_WATCHDOG_NOW=yesterday run --separate-stderr "$W" --bd-dir "$TEST_DIR" --stale-after 1h
  [ "$status" -eq 2 ]
  [[ $stderr == *"unusable ESCALATION_WATCHDOG_NOW"* ]]
  assert_nothing_called
}
