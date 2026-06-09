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
  grep -q -- "BEADS_DIR=$REPO_ROOT/.beads" "$CALLS_LOG"
  grep -q -- "WORKSPACE_ROOT=$REPO_ROOT" "$CALLS_LOG"
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

@test "wait_ready: returns 0 once the ready prompt appears" {
  export PR_POOL_READY_TIMEOUT=2
  make_stub tmux 'echo "welcome to claude"; echo "❯ "'
  load_script
  run wait_ready pf-zr-mine
  [ "$status" -eq 0 ]
  grep -q "tmux" "$CALLS_LOG"
}

@test "wait_ready: times out (nonzero) when the prompt never appears" {
  export PR_POOL_READY_TIMEOUT=1
  make_stub tmux 'echo "still booting"'
  load_script
  run wait_ready pf-zr-mine
  [ "$status" -ne 0 ]
}

@test "send_nudge: types the nudge then submits with a separate Enter" {
  export PR_POOL_SKILL_MD="/abs/SKILL.md"
  export PR_POOL_SEND_SETTLE=0
  make_stub tmux 'exit 0'
  load_script
  run send_nudge pf-zr-mine zr-mine
  [ "$status" -eq 0 ]
  grep -q -- "send-keys -t pf-zr-mine" "$CALLS_LOG"
  grep -q -- "/abs/SKILL.md" "$CALLS_LOG"
  grep -q -- "zr-mine" "$CALLS_LOG"
  # Enter must be a SEPARATE send-keys (not bundled with the text), else claude
  # ingests the burst as a paste and never submits.
  grep -qE "send-keys -t pf-zr-mine Enter$" "$CALLS_LOG"
}

@test "wait_done: returns 0 when the cycle closes" {
  export PR_POOL_MAX_WAIT=2 PR_POOL_POLL_INTERVAL=1
  make_stub bd 'echo "{\"id\":\"zr-mine\",\"status\":\"closed\"}"'
  make_stub tmux 'echo "pane alive"'
  load_script
  run wait_done zr-mine pf-zr-mine
  [ "$status" -eq 0 ]
}

@test "wait_done: on timeout, unclaims the cycle and returns nonzero" {
  export PR_POOL_MAX_WAIT=1 PR_POOL_POLL_INTERVAL=1
  make_stub bd 'echo "{\"id\":\"zr-mine\",\"status\":\"in_progress\"}"'
  make_stub tmux 'echo "pane alive"'
  load_script
  run wait_done zr-mine pf-zr-mine
  [ "$status" -ne 0 ]
  grep -q -- "update zr-mine --status=open --assignee=" "$CALLS_LOG"
}

@test "wait_done: pane dies just as the cycle closes -> success, no unclaim" {
  export PR_POOL_MAX_WAIT=5 PR_POOL_POLL_INTERVAL=1
  # bd show: in_progress on the first poll, closed thereafter — simulates the
  # worker closing the cycle and exiting between the status poll and the pane
  # check. A stranded close must NOT be unclaimed.
  make_stub bd '
n="$(cat "$TEST_DIR/bd_n" 2>/dev/null || echo 0)"
echo $((n + 1)) > "$TEST_DIR/bd_n"
case "$1" in
  show)
    if [ "$n" -ge 1 ]; then echo "{\"id\":\"zr-mine\",\"status\":\"closed\"}";
    else echo "{\"id\":\"zr-mine\",\"status\":\"in_progress\"}"; fi ;;
  *) echo "{}" ;;
esac'
  make_stub tmux 'exit 1'
  load_script
  run wait_done zr-mine pf-zr-mine
  [ "$status" -eq 0 ]
  ! grep -q -- "update zr-mine --status=open" "$CALLS_LOG"
}

@test "main: optional sentinel pauses before any work" {
  export PR_POOL_QUOTA_PAUSED="$TEST_DIR/PAUSED"; : > "$PR_POOL_QUOTA_PAUSED"
  export SELF_LOGIN="me"
  # A cycle that WOULD be discovered + dispatched if gated() didn't pause first.
  make_stub bd '
case "$1 $2" in
  "list --type=task") echo "[{\"id\":\"zr-c\",\"title\":\"process-feedback: o/r#1\",\"status\":\"open\",\"issue_type\":\"task\"}]" ;;
  "show zr-c")  echo "{\"id\":\"zr-c\",\"parent\":\"zr-p\"}" ;;
  "show zr-p")  echo "{\"id\":\"zr-p\",\"metadata\":{\"author\":\"me\"}}" ;;
  *) exit 0 ;;
esac'
  make_stub tmux 'exit 0'   # guard: must never be called while paused
  load_script
  run main
  [ "$status" -eq 0 ]
  ! grep -q "new-session" "$CALLS_LOG"
}

@test "wait_ready: matches claude's real prompt (glyph + non-breaking space)" {
  export PR_POOL_READY_TIMEOUT=2
  # Real claude renders ❯ followed by U+00A0 (non-breaking space, bytes c2 a0),
  # NOT an ASCII space — surfaced by the live smoke test. wait_ready must match.
  make_stub tmux 'printf "\xe2\x9d\xaf\xc2\xa0\n"'
  load_script
  run wait_ready pf-x
  [ "$status" -eq 0 ]
}

@test "drain_once: dispatches, nudges and waits for one discovered cycle" {
  export SELF_LOGIN="me" PR_POOL_SKILL_MD="/abs/SKILL.md"
  export PR_POOL_MAX_WAIT=2 PR_POOL_POLL_INTERVAL=1 PR_POOL_SEND_SETTLE=0
  make_stub tmux 'case "$*" in *capture-pane*) echo "❯ " ;; esac'
  make_stub uuidgen 'echo uuid'
  make_stub bd '
case "$1 $2" in
  "list --type=task") echo "[{\"id\":\"zr-c\",\"title\":\"process-feedback: o/r#1\",\"status\":\"open\",\"issue_type\":\"task\"}]" ;;
  "show zr-c")  echo "{\"id\":\"zr-c\",\"parent\":\"zr-p\",\"status\":\"closed\"}" ;;
  "show zr-p")  echo "{\"id\":\"zr-p\",\"metadata\":{\"author\":\"me\"}}" ;;
  *) echo "{\"id\":\"zr-c\",\"status\":\"closed\"}" ;;
esac'
  load_script
  run drain_once
  [ "$status" -eq 0 ]
  grep -q -- "new-session -d -s pf-zr-c" "$CALLS_LOG"
  grep -q -- "send-keys -t pf-zr-c" "$CALLS_LOG"
}

@test "nudge_text: instructs dedup vs the PR's open work beads + work beads as PR children" {
  export PR_POOL_SKILL_MD="/abs/SKILL.md"
  load_script
  run nudge_text zr-c
  [ "$status" -eq 0 ]
  [[ "$output" == *"/abs/SKILL.md"* ]]
  [[ "$output" == *"zr-c"* ]]
  [[ "$output" == *"open work bead"* ]]
  [[ "$output" == *"child of the PR bead"* ]]
  [[ "$output" != *"exit"* ]]
}

@test "drain_once: stops after MAX cycles per pass" {
  export SELF_LOGIN="me" PR_POOL_SKILL_MD="/abs/SKILL.md"
  export PR_POOL_MAX=1 PR_POOL_MAX_WAIT=2 PR_POOL_POLL_INTERVAL=1 PR_POOL_SEND_SETTLE=0
  # TWO discoverable cycles (both mine, both close immediately); MAX=1 must work only one.
  make_stub tmux 'case "$*" in *capture-pane*) echo "❯ " ;; esac'
  make_stub uuidgen 'echo uuid'
  make_stub bd '
case "$1 $2" in
  "list --type=task") echo "[{\"id\":\"zr-a\",\"title\":\"process-feedback: o/r#1\",\"status\":\"open\",\"issue_type\":\"task\"},{\"id\":\"zr-b\",\"title\":\"process-feedback: o/r#2\",\"status\":\"open\",\"issue_type\":\"task\"}]" ;;
  "show zr-a")  echo "{\"id\":\"zr-a\",\"parent\":\"zr-pa\",\"status\":\"closed\"}" ;;
  "show zr-b")  echo "{\"id\":\"zr-b\",\"parent\":\"zr-pb\",\"status\":\"closed\"}" ;;
  "show zr-pa") echo "{\"id\":\"zr-pa\",\"metadata\":{\"author\":\"me\"}}" ;;
  "show zr-pb") echo "{\"id\":\"zr-pb\",\"metadata\":{\"author\":\"me\"}}" ;;
  *) echo "{}" ;;
esac'
  load_script
  run drain_once
  [ "$status" -eq 0 ]
  # MAX=1 -> exactly ONE cycle dispatched, even though TWO are discoverable.
  [ "$(grep -c -- 'new-session -d -s pf-' "$CALLS_LOG")" -eq 1 ]
}
