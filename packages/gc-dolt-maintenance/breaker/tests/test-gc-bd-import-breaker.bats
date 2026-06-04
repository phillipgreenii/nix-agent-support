# shellcheck shell=bash

setup() {
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  export HOME="$TEST_DIR"
  for v in $(env | grep -oE '^(OTEL|GC|BEADS|DOLT)[A-Z_]*' || true); do unset "$v"; done
  CITY="$TEST_DIR/city"
  export CITY
  mkdir -p "$CITY/.beads/dolt/hq"   # pretend dolt exists
  printf 'x\n' >"$CITY/.beads/issues.jsonl"                # non-empty -> must be backed up
  BREAKER="${SCRIPT_PATH:-${SCRIPTS_DIR:-$BATS_TEST_DIRNAME/..}/gc-bd-import-breaker.sh}"
  export BREAKER
}

teardown() {
  /usr/bin/chflags nouchg "$CITY/.beads/issues.jsonl" 2>/dev/null || true
  rm -rf "$TEST_DIR"
}

@test "apply makes issues.jsonl 0-byte + uchg and backs up" {
  run bash "$BREAKER" apply --city "$CITY"
  [ "$status" -eq 0 ]
  [ "$(/usr/bin/stat -f '%z' "$CITY/.beads/issues.jsonl")" = "0" ]
  /usr/bin/stat -f '%Sf' "$CITY/.beads/issues.jsonl" | grep -q uchg
  ls "$CITY/.beads/"issues.jsonl.breaker-backup-* >/dev/null
}

@test "apply refuses when no .beads/dolt" {
  rm -rf "$CITY/.beads/dolt"
  run bash "$BREAKER" apply --city "$CITY"
  [ "$status" -ne 0 ]
}

@test "status reports APPLIED after apply" {
  bash "$BREAKER" apply --city "$CITY"
  run bash "$BREAKER" --status --city "$CITY"
  [[ "$output" == *APPLIED* ]]
}

@test "revert clears uchg" {
  bash "$BREAKER" apply --city "$CITY"
  run bash "$BREAKER" --revert --city "$CITY"
  [ "$status" -eq 0 ]
  /usr/bin/stat -f '%Sf' "$CITY/.beads/issues.jsonl" | grep -qv uchg
}
