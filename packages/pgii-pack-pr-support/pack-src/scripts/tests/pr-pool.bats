#!/usr/bin/env bats
# Unit tests for ../pr-pool.sh. External CLIs (bd, pg-pr, tmux, claude,
# uuidgen) are stubbed via PATH; stubs log argv to $CALLS_LOG.

setup() {
  TEST_DIR="$(mktemp -d)"; export TEST_DIR
  export REPO_ROOT="$TEST_DIR/monorepo"
  export CALLS_LOG="$TEST_DIR/calls.log"
  export PR_POOL_LOG_DIR="$TEST_DIR/log"
  mkdir -p "$REPO_ROOT/.beads" "$TEST_DIR/bin" "$PR_POOL_LOG_DIR"
  printf 'issue_prefix: zr\n' > "$REPO_ROOT/.beads/config.yaml"
  : > "$CALLS_LOG"
  make_stub() {  # name + body
    cat > "$TEST_DIR/bin/$1" <<EOF
#!/usr/bin/env bash
echo "$1 \$*" >> "$CALLS_LOG"
$2
EOF
    chmod +x "$TEST_DIR/bin/$1"
  }
  export -f make_stub
  PATH="$TEST_DIR/bin:$PATH"; export PATH
}

teardown() { rm -rf "$TEST_DIR"; }

# Source pr-pool.sh with its final `main "$@"` line removed so functions are
# callable in isolation.
load_script() {
  local tmp="$TEST_DIR/pr-pool-sourceable.sh"
  sed '$d' "$BATS_TEST_DIRNAME/../pr-pool.sh" > "$tmp"
  # shellcheck disable=SC1090
  source "$tmp"
}

@test "precheck: passes with zr .beads and reachable bd" {
  make_stub bd 'exit 0'
  load_script
  run precheck
  [ "$status" -eq 0 ]
}

@test "precheck: fails when .beads prefix is not zr" {
  printf 'issue_prefix: gc\n' > "$REPO_ROOT/.beads/config.yaml"
  make_stub bd 'exit 0'
  load_script
  run precheck
  [ "$status" -ne 0 ]
}

@test "precheck: fails when bd/dolt is unreachable" {
  make_stub bd 'exit 1'
  load_script
  run precheck
  [ "$status" -ne 0 ]
}

@test "dispatch: starts a detached tmux session running claude with the right flags" {
  make_stub tmux 'exit 0'
  make_stub uuidgen 'echo 11111111-2222-3333-4444-555555555555'
  load_script
  run dispatch zr-mine
  [ "$status" -eq 0 ]
  # session name echoed for the caller
  [[ "$output" == *"pf-zr-mine"* ]]
  grep -q -- "-L pgpool new-session -d -s pf-zr-mine" "$CALLS_LOG"
  grep -q -- "-u" "$CALLS_LOG"
  grep -q -- "claude --dangerously-skip-permissions --effort max --session-id 11111111-2222-3333-4444-555555555555" "$CALLS_LOG"
  grep -q -- "BEADS_ACTOR=pgii-pool__process-feedback" "$CALLS_LOG"
}

@test "discover_cycles: returns only cycles whose parent PR author is me" {
  export SELF_LOGIN="phillipgziprecruiter"
  # bd list -> two process-feedback cycles (mine zr-mine, team zr-team) + noise.
  # bd show zr-mine -> parent zr-prm ; bd show zr-team -> parent zr-prt.
  # bd show zr-prm -> author me ; bd show zr-prt -> author someone-else.
  make_stub bd '
case "$1 $2" in
  "list --type=task")
    echo "[{\"id\":\"zr-mine\",\"title\":\"process-feedback: o/r#1\",\"status\":\"open\",\"issue_type\":\"task\"},{\"id\":\"zr-team\",\"title\":\"process-feedback: o/r#2\",\"status\":\"open\",\"issue_type\":\"task\"},{\"id\":\"zr-x\",\"title\":\"other task\",\"status\":\"open\",\"issue_type\":\"task\"}]" ;;
  "show zr-mine") echo "{\"id\":\"zr-mine\",\"parent\":\"zr-prm\"}" ;;
  "show zr-team") echo "{\"id\":\"zr-team\",\"parent\":\"zr-prt\"}" ;;
  "show zr-prm")  echo "{\"id\":\"zr-prm\",\"metadata\":{\"author\":\"phillipgziprecruiter\",\"pr_number\":1}}" ;;
  "show zr-prt")  echo "{\"id\":\"zr-prt\",\"metadata\":{\"author\":\"someone-else\",\"pr_number\":2}}" ;;
esac'
  load_script
  run discover_cycles
  [ "$status" -eq 0 ]
  [[ "$output" == *"zr-mine"* ]]
  [[ "$output" != *"zr-team"* ]]
  [[ "$output" != *"zr-x"* ]]
}
