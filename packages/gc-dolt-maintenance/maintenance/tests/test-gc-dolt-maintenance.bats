# shellcheck shell=bash

setup() {
  command -v dolt >/dev/null || skip "dolt not on PATH"
  # In the nix build sandbox, starting dolt sql-server as a background process
  # causes the bats process to hang waiting for the server after tests complete
  # (the dolt server stays alive after kill; process-group cleanup blocks the
  # nix sandbox builder). Skip the integration test in that context.
  [[ -n "${NIX_BUILD_TOP:-}" ]] && skip "nix sandbox: dolt sql-server integration skipped (process-group hang)"
  TEST_DIR="$(mktemp -d)"; export HOME="$TEST_DIR"
  for v in $(env | grep -oE '^(OTEL|GC|BEADS|DOLT)[A-Z_]*' || true); do unset "$v"; done
  export GC_OTEL_DISABLE=1
  # dolt requires HOME/.dolt/config_global.json for user identity
  mkdir -p "$HOME/.dolt"
  printf '{"user.email":"test@test.invalid","user.name":"test"}\n' >"$HOME/.dolt/config_global.json"
  # provide lib functions to the script subprocess; LIB_PATH set by nix check
  # shellcheck source=/dev/null
  if [[ -n "${LIB_PATH:-}" ]]; then
    # nix sandbox: source each colon-separated composed library path
    IFS=: read -ra _libs <<< "$LIB_PATH"
    for _lib in "${_libs[@]}"; do source "$_lib"; done
  else
    source "$BATS_TEST_DIRNAME/../../decision/gc-dolt-maintenance-lib.bash"
  fi
  # shellcheck disable=SC2329  # defined for export -f to subprocess; not called directly
  otlp_gauge() { :; }
  # shellcheck disable=SC2329  # defined for export -f to subprocess; not called directly
  otlp_log() { :; }
  export -f should_flatten otlp_gauge otlp_log
  CITY="$TEST_DIR/city"; DB="$CITY/.beads/dolt/hq"; mkdir -p "$DB"
  ( cd "$DB" && dolt init >/dev/null 2>&1 \
      && dolt sql -q "CREATE TABLE issues (id varchar(64) primary key); INSERT INTO issues VALUES ('sentinel-test-db')" >/dev/null 2>&1 \
      && dolt sql -q "CALL DOLT_COMMIT('-A','-m','seed')" >/dev/null 2>&1 )
  PORT=$(( (RANDOM % 20000) + 30000 ))     # ephemeral; NEVER 24158/24159
  ( cd "$DB" && dolt sql-server --host 127.0.0.1 --port "$PORT" >/dev/null 2>&1 & echo $! >"$TEST_DIR/dolt.pid" )
  # poll for readiness (up to ~10s)
  for _ in $(seq 1 20); do ( cd "$DB" && dolt sql -q "SELECT 1" >/dev/null 2>&1 ) && break; sleep 0.5; done
  # assert it's the TEST db (sentinel), never a real one
  run bash -c "cd '$DB' && dolt sql -r csv -q \"SELECT id FROM issues\" | tail -1"
  [[ "$output" == "sentinel-test-db" ]] || { echo "WRONG DB: $output"; return 1; }
  # mock gc/bd/curl so nothing real is touched
  mkdir -p "$TEST_DIR/bin"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$TEST_DIR/bin/gc"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$TEST_DIR/bin/bd"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$TEST_DIR/bin/curl"
  chmod +x "$TEST_DIR/bin/"*; export PATH="$TEST_DIR/bin:$PATH"
  SCRIPT="${SCRIPT_PATH:-${SCRIPTS_DIR:-$BATS_TEST_DIRNAME/..}/gc-dolt-maintenance.sh}"
}

teardown() {     # ALWAYS runs, even on failure
  [[ -f "$TEST_DIR/dolt.pid" ]] && kill "$(cat "$TEST_DIR/dolt.pid")" 2>/dev/null || true
  pkill -f "dolt sql-server --host 127.0.0.1 --port ${PORT:-0}" 2>/dev/null || true
  rm -rf "$TEST_DIR"
}

@test "cheap tier runs; no flatten when below threshold" {
  run bash "$SCRIPT" --city "$CITY"
  [ "$status" -eq 0 ]
  [[ "$output" == *"DOLT_STATS_PURGE ok"* ]]
  [[ "$output" == *"flatten decision=no:"* ]]
}
