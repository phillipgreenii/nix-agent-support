#!/usr/bin/env bats
# bats file_tags=type:unit
# Script-level tests for pg-wi-flow's entry point (tc-9ddu3.1.1): --help,
# subcommand dispatch/unknown-command handling, and the --actor global
# option's Claude-Code gating [design: ## Architecture, "Command pattern";
# ## State model -> "Identity"]. Runs the raw .sh as a subprocess, mirroring
# pg-wi-flow-identity's test-pg-wi-flow-identity.bats.
bats_require_minimum_version 1.5.0

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  SCRIPT="${SCRIPT_UNDER_TEST:-bash $SCRIPTS_DIR/pg-wi-flow.sh}"
  unset PG_WI_FLOW_IDENT

  MOCK_BIN="$(mktemp -d)"
  MOCK_BD_LOG="$(mktemp)"
  export MOCK_BD_LOG
  cat >"$MOCK_BIN/bd" <<'MOCKBD'
#!/usr/bin/env bash
echo "$*" >>"$MOCK_BD_LOG"
case "$1" in
ready) printf '[]\n' ;;
show)
  printf '{"data":[{"id":"%s","title":"an item","labels":[],"metadata":{}}]}\n' "$2"
  ;;
list) printf '{"data":[]}\n' ;;
children) printf '{"data":[]}\n' ;;
dep) printf '{"data":[]}\n' ;;
*)
  echo "mock bd: unhandled subcommand: $1" >&2
  exit 1
  ;;
esac
MOCKBD
  chmod +x "$MOCK_BIN/bd"
  export PATH="$MOCK_BIN:$PATH"

  TEST_DIR="$(mktemp -d)"
  HOME="$TEST_DIR/home"
  XDG_CONFIG_HOME="$TEST_DIR/xdg"
  mkdir -p "$HOME" "$XDG_CONFIG_HOME"
  export HOME XDG_CONFIG_HOME
  PGWF_TEST_CWD="$TEST_DIR/work"
  mkdir -p "$PGWF_TEST_CWD"
  cd "$PGWF_TEST_CWD" || exit 1
}

teardown() {
  rm -rf "$MOCK_BIN" "$MOCK_BD_LOG" "$TEST_DIR"
}

run_pg_wi_flow() {
  # shellcheck disable=SC2086 # SCRIPT may be a two-word "bash <path>" form
  run $SCRIPT "$@"
}

@test "--help shows usage and exits 0" {
  run_pg_wi_flow --help
  [ "$status" -eq 0 ]
  [[ "$output" =~ "Usage: pg-wi-flow" ]]
}

@test "missing command errors non-zero" {
  run_pg_wi_flow
  [ "$status" -ne 0 ]
}

@test "unknown command errors non-zero" {
  run_pg_wi_flow bogus-command
  [ "$status" -ne 0 ]
}

@test "unknown global option errors non-zero" {
  run_pg_wi_flow --bogus-flag query
  [ "$status" -ne 0 ]
}

@test "query dispatches and prints the default filter set" {
  run_pg_wi_flow query
  [ "$status" -eq 0 ]
  [[ "$output" == *"--exclude-label"* ]]
  [[ "$output" == *"human"* ]]
}

@test "--actor is accepted outside Claude Code (PG_WI_FLOW_IDENT unset)" {
  run_pg_wi_flow --actor outside-actor query
  [ "$status" -eq 0 ]
}

@test "--actor is refused inside Claude Code (PG_WI_FLOW_IDENT set)" {
  export PG_WI_FLOW_IDENT="sess1234-agent-1-worker"
  run_pg_wi_flow --actor should-be-refused query
  [ "$status" -ne 0 ]
  [[ "$output" == *"not accepted inside Claude Code"* ]]
}

# --- tc-9ddu3.1.2: context/explain/history/duplicates/docs dispatch -----

@test "context dispatches to the render engine" {
  run_pg_wi_flow context tc-1
  [ "$status" -eq 0 ]
  [[ "$output" == *"WI_ID=tc-1"* ]]
}

@test "explain dispatches and reports a holder" {
  run_pg_wi_flow explain tc-1
  [ "$status" -eq 0 ]
  [[ "$output" == *"WI_HOLDER=unclaimed"* ]]
}

@test "history dispatches and prints an empty trail under the null workflow" {
  run_pg_wi_flow history tc-1
  [ "$status" -eq 0 ]
  [ "$output" = "[]" ]
}

@test "duplicates dispatches and prints WI_DUPLICATES/WI_RELATED" {
  run_pg_wi_flow duplicates tc-1
  [ "$status" -eq 0 ]
  [[ "$output" == *"WI_DUPLICATES="* ]]
  [[ "$output" == *"WI_RELATED="* ]]
}

@test "docs dispatches and prints WI_APPLICABLE_DOCS" {
  run_pg_wi_flow docs tc-1
  [ "$status" -eq 0 ]
  [[ "$output" == *"WI_APPLICABLE_DOCS="* ]]
}
