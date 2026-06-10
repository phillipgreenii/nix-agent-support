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


@test "discover: feedback route tags my process-feedback cycles (via bd ready)" {
  export SELF_LOGIN="phillipgziprecruiter"
  make_stub bd '
case "$1" in
  ready)
    case "$*" in
      *"--label worker-ready"*) echo "[]" ;;
      *) echo "[{\"id\":\"zr-mine\",\"title\":\"process-feedback: o/r#1\",\"status\":\"open\",\"issue_type\":\"task\"},{\"id\":\"zr-team\",\"title\":\"process-feedback: o/r#2\",\"status\":\"open\",\"issue_type\":\"task\"},{\"id\":\"zr-x\",\"title\":\"other task\",\"status\":\"open\",\"issue_type\":\"task\"}]" ;;
    esac ;;
  show)
    case "$2" in
      zr-mine) echo "{\"id\":\"zr-mine\",\"parent\":\"zr-prm\"}" ;;
      zr-team) echo "{\"id\":\"zr-team\",\"parent\":\"zr-prt\"}" ;;
      zr-prm)  echo "{\"id\":\"zr-prm\",\"metadata\":{\"author\":\"phillipgziprecruiter\",\"pr_number\":1}}" ;;
      zr-prt)  echo "{\"id\":\"zr-prt\",\"metadata\":{\"author\":\"someone-else\",\"pr_number\":2}}" ;;
      *) echo "{}" ;;
    esac ;;
esac'
  load_script
  run discover
  [ "$status" -eq 0 ]
  [[ "$output" == *"feedback-processor	zr-mine"* ]]
  [[ "$output" != *"zr-team"* ]]
  [[ "$output" != *"zr-x"* ]]
}

@test "discover: worker route tags worker-ready beads (native --label)" {
  export SELF_LOGIN="me"
  make_stub bd '
case "$1" in
  ready)
    case "$*" in
      *"--label worker-ready"*) echo "[{\"id\":\"zr-w1\",\"title\":\"Fix X\",\"status\":\"open\",\"issue_type\":\"bug\"}]" ;;
      *) echo "[]" ;;
    esac ;;
  *) echo "{}" ;;
esac'
  load_script
  run discover
  [ "$status" -eq 0 ]
  [[ "$output" == *"worker	zr-w1"* ]]
  grep -q -- "ready --label worker-ready" "$CALLS_LOG"
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
  run send_nudge feedback-processor pf-zr-mine zr-mine
  [ "$status" -eq 0 ]
  grep -q -- "send-keys -t pf-zr-mine" "$CALLS_LOG"
  grep -q -- "/abs/SKILL.md" "$CALLS_LOG"
  grep -q -- "zr-mine" "$CALLS_LOG"
  grep -qE "send-keys -t pf-zr-mine Enter$" "$CALLS_LOG"
}

@test "wait_done: returns 0 when the cycle closes" {
  export PR_POOL_MAX_WAIT=2 PR_POOL_POLL_INTERVAL=1
  make_stub bd 'echo "{\"id\":\"zr-mine\",\"status\":\"closed\"}"'
  make_stub tmux 'echo "pane alive"'
  load_script
  run wait_done feedback-processor zr-mine pf-zr-mine
  [ "$status" -eq 0 ]
}

@test "wait_done: on timeout, unclaims the cycle and returns nonzero" {
  export PR_POOL_MAX_WAIT=1 PR_POOL_POLL_INTERVAL=1
  make_stub bd 'echo "{\"id\":\"zr-mine\",\"status\":\"in_progress\"}"'
  make_stub tmux 'echo "pane alive"'
  load_script
  run wait_done feedback-processor zr-mine pf-zr-mine
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
  run wait_done feedback-processor zr-mine pf-zr-mine
  [ "$status" -eq 0 ]
  ! grep -q -- "update zr-mine --status=open" "$CALLS_LOG"
}

@test "wait_done worker: succeeds when the bead gains the needs-push label" {
  export PR_POOL_MAX_WAIT=2 PR_POOL_POLL_INTERVAL=1
  make_stub bd 'echo "{\"id\":\"zr-w1\",\"status\":\"in_progress\",\"labels\":[\"needs-push\"]}"'
  make_stub tmux 'echo "pane alive"'
  load_script
  run wait_done worker zr-w1 WORKER
  [ "$status" -eq 0 ]
  ! grep -q -- "update zr-w1 --status=open" "$CALLS_LOG"
}

@test "wait_done worker: on timeout, stamps worker-stuck and does NOT unclaim" {
  export PR_POOL_MAX_WAIT=1 PR_POOL_POLL_INTERVAL=1
  make_stub bd 'echo "{\"id\":\"zr-w1\",\"status\":\"in_progress\",\"labels\":[\"worker-ready\"]}"'
  make_stub tmux 'echo "pane alive"'
  load_script
  run wait_done worker zr-w1 WORKER
  [ "$status" -ne 0 ]
  grep -q -- "update zr-w1 --add-label worker-stuck" "$CALLS_LOG"
  ! grep -q -- "update zr-w1 --status=open" "$CALLS_LOG"
}

@test "main: optional sentinel pauses before any work" {
  export PR_POOL_QUOTA_PAUSED="$TEST_DIR/PAUSED"; : > "$PR_POOL_QUOTA_PAUSED"
  export SELF_LOGIN="me"
  make_stub bd '
case "$1" in
  ready) echo "[{\"id\":\"zr-c\",\"title\":\"process-feedback: o/r#1\",\"status\":\"open\",\"issue_type\":\"task\"}]" ;;
  show)  case "$2" in zr-c) echo "{\"id\":\"zr-c\",\"parent\":\"zr-p\"}" ;; zr-p) echo "{\"id\":\"zr-p\",\"metadata\":{\"author\":\"me\"}}" ;; *) echo "{}" ;; esac ;;
  *) exit 0 ;;
esac'
  make_stub tmux 'exit 0'
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

@test "drain_once: works one feedback cycle in the role session, then tears down" {
  export SELF_LOGIN="me" PR_POOL_SKILL_MD="/abs/SKILL.md"
  export PR_POOL_MAX_WAIT=2 PR_POOL_POLL_INTERVAL=1 PR_POOL_SEND_SETTLE=0
  make_stub uuidgen 'echo uuid'
  make_stub tmux '
case "$*" in
  *new-session*)  : > "$TEST_DIR/sess" ;;
  *has-session*)  [ -f "$TEST_DIR/sess" ] && exit 0 || exit 1 ;;
  *kill-session*) rm -f "$TEST_DIR/sess" ;;
  *capture-pane*) echo "❯ " ;;
esac'
  make_stub bd '
case "$1" in
  ready)
    case "$*" in
      *"--label worker-ready"*) echo "[]" ;;
      *) echo "[{\"id\":\"zr-c\",\"title\":\"process-feedback: o/r#1\",\"status\":\"open\",\"issue_type\":\"task\"}]" ;;
    esac ;;
  show)
    case "$2" in
      zr-c) echo "{\"id\":\"zr-c\",\"parent\":\"zr-p\",\"status\":\"closed\"}" ;;
      zr-p) echo "{\"id\":\"zr-p\",\"metadata\":{\"author\":\"me\",\"pr_number\":7}}" ;;
      *) echo "{}" ;;
    esac ;;
  *) echo "{}" ;;
esac'
  load_script
  run drain_once
  [ "$status" -eq 0 ]
  grep -q -- "new-session -d -s PR FEEDBACK PROCESSOR" "$CALLS_LOG"
  grep -q -- "send-keys -t PR FEEDBACK PROCESSOR /rename" "$CALLS_LOG"
  grep -q -- "/abs/SKILL.md" "$CALLS_LOG"
  grep -q -- "kill-session -t PR FEEDBACK PROCESSOR" "$CALLS_LOG"
}

@test "nudge_text_feedback: instructs dedup vs the PR's open work beads + work beads as PR children" {
  export PR_POOL_SKILL_MD="/abs/SKILL.md"
  load_script
  run nudge_text_feedback zr-c
  [ "$status" -eq 0 ]
  [[ "$output" == *"/abs/SKILL.md"* ]]
  [[ "$output" == *"zr-c"* ]]
  [[ "$output" == *"open work bead"* ]]
  [[ "$output" == *"child of the PR bead"* ]]
  [[ "$output" != *"exit"* ]]
}

@test "submit_line: types the text then submits with a SEPARATE Enter" {
  export PR_POOL_SEND_SETTLE=0
  make_stub tmux 'exit 0'
  load_script
  run submit_line "SESS" "hello world"
  [ "$status" -eq 0 ]
  grep -q -- "send-keys -t SESS hello world" "$CALLS_LOG"
  grep -qE "send-keys -t SESS Enter$" "$CALLS_LOG"
}

@test "drain_once: per-role cap stops the feedback role after MAX_FEEDBACK" {
  export SELF_LOGIN="me" PR_POOL_SKILL_MD="/abs/SKILL.md"
  export PR_POOL_MAX_FEEDBACK=1 PR_POOL_MAX_WORKER=0
  export PR_POOL_MAX_WAIT=2 PR_POOL_POLL_INTERVAL=1 PR_POOL_SEND_SETTLE=0
  make_stub uuidgen 'echo uuid'
  make_stub tmux '
case "$*" in
  *new-session*)  : > "$TEST_DIR/sess" ;;
  *has-session*)  [ -f "$TEST_DIR/sess" ] && exit 0 || exit 1 ;;
  *kill-session*) rm -f "$TEST_DIR/sess" ;;
  *capture-pane*) echo "❯ " ;;
esac'
  make_stub bd '
case "$1" in
  ready)
    case "$*" in
      *"--label worker-ready"*) echo "[]" ;;
      *) echo "[{\"id\":\"zr-a\",\"title\":\"process-feedback: o/r#1\",\"status\":\"open\",\"issue_type\":\"task\"},{\"id\":\"zr-b\",\"title\":\"process-feedback: o/r#2\",\"status\":\"open\",\"issue_type\":\"task\"}]" ;;
    esac ;;
  show)
    case "$2" in
      zr-a)  echo "{\"id\":\"zr-a\",\"parent\":\"zr-pa\",\"status\":\"closed\"}" ;;
      zr-b)  echo "{\"id\":\"zr-b\",\"parent\":\"zr-pb\",\"status\":\"closed\"}" ;;
      zr-pa) echo "{\"id\":\"zr-pa\",\"metadata\":{\"author\":\"me\",\"pr_number\":1}}" ;;
      zr-pb) echo "{\"id\":\"zr-pb\",\"metadata\":{\"author\":\"me\",\"pr_number\":2}}" ;;
      *) echo "{}" ;;
    esac ;;
  *) echo "{}" ;;
esac'
  load_script
  run drain_once
  [ "$status" -eq 0 ]
  [ "$(grep -c -- '/abs/SKILL.md' "$CALLS_LOG")" -eq 1 ]
}

@test "ensure_session: creates the role session when absent, with pinned env" {
  make_stub uuidgen 'echo uuid'
  make_stub tmux '
case "$*" in
  *new-session*)  : > "$TEST_DIR/sess" ;;
  *has-session*)  [ -f "$TEST_DIR/sess" ] && exit 0 || exit 1 ;;
  *kill-session*) rm -f "$TEST_DIR/sess" ;;
  *capture-pane*) echo "❯ " ;;
esac'
  load_script
  run ensure_session
  [ "$status" -eq 0 ]
  grep -q -- "new-session -d -s PR FEEDBACK PROCESSOR" "$CALLS_LOG"
  grep -q -- "BEADS_DIR=$REPO_ROOT/.beads" "$CALLS_LOG"
  grep -q -- "WORKSPACE_ROOT=$REPO_ROOT" "$CALLS_LOG"
  grep -q -- "BEADS_ACTOR=pgii-pool__process-feedback" "$CALLS_LOG"
}

@test "ensure_session: reuses an existing role session (no second new-session)" {
  make_stub uuidgen 'echo uuid'
  : > "$TEST_DIR/sess"   # pretend the session already exists
  make_stub tmux '
case "$*" in
  *new-session*)  : > "$TEST_DIR/sess" ;;
  *has-session*)  [ -f "$TEST_DIR/sess" ] && exit 0 || exit 1 ;;
  *kill-session*) rm -f "$TEST_DIR/sess" ;;
  *capture-pane*) echo "❯ " ;;
esac'
  load_script
  run ensure_session
  [ "$status" -eq 0 ]
  ! grep -q -- "new-session" "$CALLS_LOG"
}

@test "cycle_label: includes the cycle id and the parent PR number" {
  make_stub bd '
case "$1 $2" in
  "show zr-c") echo "{\"id\":\"zr-c\",\"parent\":\"zr-p\"}" ;;
  "show zr-p") echo "{\"id\":\"zr-p\",\"metadata\":{\"pr_number\":7}}" ;;
  *) echo "{}" ;;
esac'
  load_script
  run cycle_label zr-c
  [ "$status" -eq 0 ]
  [[ "$output" == *"zr-c"* ]]
  [[ "$output" == *"PR #7"* ]]
}

@test "cycle_label: falls back to the cycle id when no PR number" {
  make_stub bd 'echo "{}"'
  load_script
  run cycle_label zr-c
  [ "$status" -eq 0 ]
  [[ "$output" == *"zr-c"* ]]
}

@test "claude_rename: submits a /rename with the given name" {
  export PR_POOL_SEND_SETTLE=0
  make_stub tmux 'exit 0'
  load_script
  run claude_rename "PR FEEDBACK PROCESSOR" "process-feedback zr-c PR #7"
  [ "$status" -eq 0 ]
  grep -q -- "send-keys -t PR FEEDBACK PROCESSOR /rename" "$CALLS_LOG"
  grep -q -- "process-feedback zr-c PR #7" "$CALLS_LOG"
}

@test "clear_context: submits /clear and waits for the prompt" {
  export PR_POOL_SEND_SETTLE=0 PR_POOL_READY_TIMEOUT=2
  make_stub tmux 'case "$*" in *capture-pane*) echo "❯ " ;; esac'
  load_script
  run clear_context "PR FEEDBACK PROCESSOR"
  [ "$status" -eq 0 ]
  grep -q -- "send-keys -t PR FEEDBACK PROCESSOR /clear" "$CALLS_LOG"
  grep -q -- "capture-pane" "$CALLS_LOG"
}

@test "teardown_session: sends exit then kills the role session" {
  export PR_POOL_SEND_SETTLE=0
  : > "$TEST_DIR/sess"
  make_stub tmux '
case "$*" in
  *new-session*)  : > "$TEST_DIR/sess" ;;
  *has-session*)  [ -f "$TEST_DIR/sess" ] && exit 0 || exit 1 ;;
  *kill-session*) rm -f "$TEST_DIR/sess" ;;
  *capture-pane*) echo "❯ " ;;
esac'
  load_script
  run teardown_session "PR FEEDBACK PROCESSOR"
  [ "$status" -eq 0 ]
  grep -q -- "send-keys -t PR FEEDBACK PROCESSOR /exit" "$CALLS_LOG"
  grep -q -- "kill-session -t PR FEEDBACK PROCESSOR" "$CALLS_LOG"
}

@test "teardown_session: no-op when no role session exists" {
  make_stub tmux 'case "$*" in *has-session*) exit 1 ;; esac'
  load_script
  run teardown_session "PR FEEDBACK PROCESSOR"
  [ "$status" -eq 0 ]
  ! grep -q -- "kill-session" "$CALLS_LOG"
}

@test "ensure_session worker: creates the WORKER session with the worker actor" {
  make_stub uuidgen 'echo uuid'
  make_stub tmux '
case "$*" in
  *new-session*)  : > "$TEST_DIR/wsess" ;;
  *has-session*)  [ -f "$TEST_DIR/wsess" ] && exit 0 || exit 1 ;;
  *capture-pane*) echo "❯ " ;;
esac'
  load_script
  run ensure_session worker
  [ "$status" -eq 0 ]
  grep -q -- "new-session -d -s WORKER" "$CALLS_LOG"
  grep -q -- "BEADS_ACTOR=pgii-pool__worker" "$CALLS_LOG"
}

@test "role resolvers: worker vs feedback-processor (and default) map correctly" {
  export PR_POOL_SKILL_MD="/abs/feedback.md" PR_POOL_WORKER_SKILL_MD="/abs/worker.md"
  load_script
  [ "$(role_session worker)" = "WORKER" ]
  [ "$(role_session feedback-processor)" = "PR FEEDBACK PROCESSOR" ]
  [ "$(role_session)" = "PR FEEDBACK PROCESSOR" ]          # default branch
  [ "$(role_actor worker)" = "pgii-pool__worker" ]
  [ "$(role_actor feedback-processor)" = "pgii-pool__process-feedback" ]
  [ "$(role_skill worker)" = "/abs/worker.md" ]
  [ "$(role_skill feedback-processor)" = "/abs/feedback.md" ]
  [ "$(role_max worker)" = "1" ]
  [ "$(role_max feedback-processor)" = "1" ]
}

@test "nudge_text_worker: worktree, commit-no-push, record-then-swap, abort-to-stuck" {
  export PR_POOL_WORKER_SKILL_MD="/abs/worker.md" PR_POOL_WORKTREE_DIR="/tmp/test-worktrees"
  load_script
  run nudge_text_worker zr-w1
  [ "$status" -eq 0 ]
  [[ "$output" == *"/abs/worker.md"* ]]
  [[ "$output" == *"zr-w1"* ]]
  [[ "$output" == *"worktree"* ]]
  [[ "$output" == *"do NOT push"* ]]
  [[ "$output" == *"needs-push"* ]]
  [[ "$output" == *"worker-ready"* ]]
  [[ "$output" == *"worker-stuck"* ]]
  [[ "$output" == *"/tmp/test-worktrees"* ]]
}

@test "worker_label: includes the work-bead id and the parent PR number" {
  make_stub bd '
case "$1 $2" in
  "show zr-w1") echo "{\"id\":\"zr-w1\",\"parent\":\"zr-p\"}" ;;
  "show zr-p")  echo "{\"id\":\"zr-p\",\"metadata\":{\"pr_number\":42}}" ;;
  *) echo "{}" ;;
esac'
  load_script
  run worker_label zr-w1
  [ "$status" -eq 0 ]
  [[ "$output" == *"zr-w1"* ]]
  [[ "$output" == *"PR #42"* ]]
}

@test "worker_label: falls back to the work-bead id when no PR number" {
  make_stub bd 'echo "{}"'
  load_script
  run worker_label zr-w1
  [ "$status" -eq 0 ]
  [[ "$output" == *"zr-w1"* ]]
  [[ "$output" != *"PR #"* ]]
}

@test "role_nudge / role_convo_name: dispatch to the right per-role helper" {
  export PR_POOL_SKILL_MD="/abs/feedback.md" PR_POOL_WORKER_SKILL_MD="/abs/worker.md"
  make_stub bd '
case "$1 $2" in
  "show zr-w1") echo "{\"id\":\"zr-w1\",\"parent\":\"zr-p\"}" ;;
  "show zr-c")  echo "{\"id\":\"zr-c\",\"parent\":\"zr-pc\"}" ;;
  *) echo "{}" ;;
esac'
  load_script
  [ "$(role_nudge worker zr-w1)" = "$(nudge_text_worker zr-w1)" ]
  [ "$(role_nudge feedback-processor zr-c)" = "$(nudge_text_feedback zr-c)" ]
  [ "$(role_convo_name worker zr-w1)" = "$(worker_label zr-w1)" ]
  [ "$(role_convo_name feedback-processor zr-c)" = "$(cycle_label zr-c)" ]
}

@test "wait_done worker: pane dies just as needs-push lands -> success, no worker-stuck" {
  export PR_POOL_MAX_WAIT=5 PR_POOL_POLL_INTERVAL=1
  # First poll: only worker-ready (not done). Pane then reads dead; the re-check
  # sees needs-push and must succeed WITHOUT stamping worker-stuck.
  make_stub bd '
n="$(cat "$TEST_DIR/bd_n" 2>/dev/null || echo 0)"
echo $((n + 1)) > "$TEST_DIR/bd_n"
case "$1" in
  show)
    if [ "$n" -ge 1 ]; then echo "{\"id\":\"zr-w1\",\"status\":\"in_progress\",\"labels\":[\"needs-push\"]}";
    else echo "{\"id\":\"zr-w1\",\"status\":\"in_progress\",\"labels\":[\"worker-ready\"]}"; fi ;;
  *) echo "{}" ;;
esac'
  make_stub tmux 'exit 1'
  load_script
  run wait_done worker zr-w1 WORKER
  [ "$status" -eq 0 ]
  ! grep -q -- "add-label worker-stuck" "$CALLS_LOG"
}

@test "work_one: send_nudge failure unclaims, still clears, and returns nonzero" {
  unset PR_POOL_SKILL_MD                      # role_skill feedback-processor -> "" -> send_nudge fails
  export PR_POOL_SEND_SETTLE=0 PR_POOL_READY_TIMEOUT=2
  make_stub uuidgen 'echo uuid'
  make_stub bd 'echo "{}"'                    # cycle_label parent-walk + unclaim
  make_stub tmux '
case "$*" in
  *new-session*)  : > "$TEST_DIR/sess" ;;
  *has-session*)  [ -f "$TEST_DIR/sess" ] && exit 0 || exit 1 ;;
  *kill-session*) rm -f "$TEST_DIR/sess" ;;
  *capture-pane*) echo "❯ " ;;
esac'
  load_script
  run work_one feedback-processor zr-c
  [ "$status" -ne 0 ]
  grep -q -- "update zr-c --status=open --assignee=" "$CALLS_LOG"      # unclaim ran
  grep -q -- "send-keys -t PR FEEDBACK PROCESSOR /clear" "$CALLS_LOG"  # clear_context ran
}

@test "drain_once: tears down every role session, incl. one not dispatched this pass" {
  export SELF_LOGIN="me" PR_POOL_SKILL_MD="/abs/SKILL.md" PR_POOL_WORKER_SKILL_MD="/abs/W.md"
  export PR_POOL_SEND_SETTLE=0
  : > "$TEST_DIR/WORKER.sess"
  make_stub tmux '
case "$*" in
  *"has-session -t WORKER"*)               [ -f "$TEST_DIR/WORKER.sess" ] && exit 0 || exit 1 ;;
  *"kill-session -t WORKER"*)              rm -f "$TEST_DIR/WORKER.sess" ;;
  *"has-session -t PR FEEDBACK PROCESSOR"*) exit 1 ;;
  *capture-pane*) echo "❯ " ;;
esac'
  make_stub bd 'case "$1" in ready) echo "[]" ;; *) echo "{}" ;; esac'
  load_script
  run drain_once
  [ "$status" -eq 0 ]
  grep -q -- "kill-session -t WORKER" "$CALLS_LOG"
}

@test "drain_once: worker cap=0 skips a worker-ready bead even when one exists" {
  export SELF_LOGIN="me" PR_POOL_SKILL_MD="/abs/SKILL.md" PR_POOL_WORKER_SKILL_MD="/abs/W.md"
  export PR_POOL_MAX_WORKER=0 PR_POOL_SEND_SETTLE=0
  make_stub uuidgen 'echo uuid'
  make_stub tmux '
case "$*" in
  *has-session*) exit 1 ;;
  *capture-pane*) echo "❯ " ;;
esac'
  make_stub bd '
case "$1" in
  ready)
    case "$*" in
      *"--label worker-ready"*) echo "[{\"id\":\"zr-w1\",\"title\":\"Fix X\",\"status\":\"open\",\"issue_type\":\"bug\"}]" ;;
      *) echo "[]" ;;
    esac ;;
  *) echo "{}" ;;
esac'
  load_script
  run drain_once
  [ "$status" -eq 0 ]
  ! grep -q -- "/abs/W.md" "$CALLS_LOG"                 # worker nudge never sent (cap=0)
  ! grep -q -- "new-session -d -s WORKER" "$CALLS_LOG"  # worker session never created
}
