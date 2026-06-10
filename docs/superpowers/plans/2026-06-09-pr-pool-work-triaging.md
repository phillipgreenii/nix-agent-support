# pr-pool work triaging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Generalize `pr-pool.sh` from a single hardcoded feedback-cycle dispatcher into a mechanical two-role triager (`feedback-processor` + a new code-mutating `worker`) driven by `bd ready`, and add the worker's SKILL contract.

**Architecture:** A per-role config table (bash-3.2-safe `case` resolvers) maps a role → session name / `BEADS_ACTOR` / SKILL path / nudge / conversation-name / concurrency cap. Discovery switches to `bd ready` (per role; native `--label` for the worker) and emits `role<TAB>bead-id`. The shipped session-lifecycle functions are generalized to take a role/session, `wait_done` becomes role-aware (feedback completes on cycle-close + unclaims on failure; worker completes on a `needs-push` label + stamps `worker-stuck` on failure, never unclaiming), and `drain_once` drains each role up to its own cap then tears down all role sessions. `pr-pool` stays git-free; the worker agent does all git work, guided by a new SKILL.

**Tech Stack:** Bash (`/usr/bin/env bash`, must stay 3.2-compatible), `bats` (run via `nix shell nixpkgs#bats`), `jq`, `bd`/`pg-pr` CLIs, `tmux`, `git`. Spec: `docs/superpowers/specs/2026-06-09-pr-pool-work-triaging-design.md`.

---

## File Structure

- **Modify:** `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh` — the orchestrator. Gains the role config table, role-tagged `bd ready` discovery, per-role lifecycle, role-aware completion, and per-role drain. Single file (unchanged location/responsibility).
- **Modify:** `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats` — the PATH-stub unit suite. Existing tests migrate from `bd list` → `bd ready` and from the `ROLE_NAME` globals to role-parameterized calls; new tests cover the worker route.
- **Create:** `packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-work-bead/SKILL.md` — the worker agent's contract (worktree → implement → commit, no push → record → swap `worker-ready`→`needs-push`).

### Conventions (apply throughout)

- Follow the repo's `bash-scripting` skill conventions for any script/bats edits.
- Fast test loop (one file): `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
- Full gate before declaring done: `nix flake check` (specifically the `test-pgii-pack-pr-support-bats` check) and `prek run --all-files` (or `pre-commit run --all-files`).
- Commit only the files named in each task — the working tree has unrelated staged `.gc/**` WIP; **path-scope every `git commit`** to the files listed (`git commit -m "…" -- <paths>`). treefmt may reformat a committed markdown/script and abort the first attempt; re-`git add` the same path and re-commit.
- The branch is `phillipg.pr-pool-orchestrator`.

---

## Task 1: Per-role config table + worker nudge (additive)

Introduces the role resolvers and the worker-facing text helpers without changing any existing control flow. The only behavior-affecting change is renaming `nudge_text` → `nudge_text_feedback` and updating its one caller.

**Files:**

- Modify: `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh` (top-of-script vars; rename `nudge_text`; add resolvers + `worker_label` + `nudge_text_worker`; update `send_nudge`'s internal call)
- Test: `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`

- [x] **Step 1: Write the failing tests** (append to `pr-pool.bats`; also rename the existing `nudge_text` test)

Rename the existing test `@test "nudge_text: instructs dedup …"` to call `nudge_text_feedback` (function name only — keep its body/asserts):

```bash
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
```

Append these new tests:

```bash
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
  export PR_POOL_WORKER_SKILL_MD="/abs/worker.md"
  load_script
  run nudge_text_worker zr-w1
  [ "$status" -eq 0 ]
  [[ "$output" == *"/abs/worker.md"* ]]
  [[ "$output" == *"zr-w1"* ]]
  [[ "$output" == *"worktree"* ]]
  [[ "$output" == *"do NOT push"* ]]
  [[ "$output" == *"needs-push"* ]]
  [[ "$output" == *"worker-ready"* ]]
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
```

- [x] **Step 2: Run the tests to verify they fail**

Run: `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
Expected: the four tests above FAIL (`role_session`/`role_actor`/`role_skill`/`role_max`/`nudge_text_worker`/`worker_label`/`nudge_text_feedback` not defined).

- [x] **Step 3: Add the new top-of-script variables**

In `pr-pool.sh`, in the variable block (after the existing `ACTOR=…` / before `QUOTA_PAUSED=…`), add:

```bash
WORKER_SKILL_MD="${PR_POOL_WORKER_SKILL_MD:-}"                          # worker SKILL.md (analogue of SKILL_MD)
FEEDBACK_SESSION="${PR_POOL_FEEDBACK_SESSION:-$ROLE_NAME}"              # tmux session for the feedback-processor role
WORKER_SESSION="${PR_POOL_WORKER_SESSION:-WORKER}"                      # tmux session for the worker role
FEEDBACK_ACTOR="${PR_POOL_FEEDBACK_ACTOR:-$ACTOR}"                      # BEADS_ACTOR for feedback-processor
WORKER_ACTOR="${PR_POOL_WORKER_ACTOR:-pgii-pool__worker}"              # BEADS_ACTOR for worker
MAX_FEEDBACK="${PR_POOL_MAX_FEEDBACK:-1}"                               # per-role concurrency cap (feedback)
MAX_WORKER="${PR_POOL_MAX_WORKER:-1}"                                   # per-role concurrency cap (worker)
WORKTREE_DIR="${PR_POOL_WORKTREE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/pr-pool/worktrees}"  # passed to the worker in its nudge
ROLES="feedback-processor worker"                                      # role list for drain + teardown
```

(`ROLE_NAME`, `ACTOR`, `SKILL_MD`, `MAX` remain for now; later tasks remove `MAX`'s use and keep `ROLE_NAME`/`ACTOR` only as the feedback defaults referenced above.)

- [x] **Step 4: Add the role resolvers** (place just above `nudge_text`)

```bash
# --- per-role config table (bash-3.2-safe case resolvers) ----------------
# The "*" default branch resolves to the feedback-processor role so callers
# that omit the role keep step-1 behavior.
role_session()    { case "$1" in worker) printf '%s' "$WORKER_SESSION";;   *) printf '%s' "$FEEDBACK_SESSION";; esac; }
role_actor()      { case "$1" in worker) printf '%s' "$WORKER_ACTOR";;     *) printf '%s' "$FEEDBACK_ACTOR";; esac; }
role_skill()      { case "$1" in worker) printf '%s' "$WORKER_SKILL_MD";;  *) printf '%s' "$SKILL_MD";; esac; }
role_max()        { case "$1" in worker) printf '%s' "$MAX_WORKER";;       *) printf '%s' "$MAX_FEEDBACK";; esac; }
role_nudge()      { case "$1" in worker) nudge_text_worker "$2";;          *) nudge_text_feedback "$2";; esac; }
role_convo_name() { case "$1" in worker) worker_label "$2";;               *) cycle_label "$2";; esac; }
```

- [x] **Step 5: Rename `nudge_text` → `nudge_text_feedback` and add `nudge_text_worker` + `worker_label`**

Rename the existing `nudge_text` function to `nudge_text_feedback` (keep its body verbatim). Immediately after it, add:

```bash
# nudge_text_worker builds the worker's instruction line. The worker does all
# git work itself (pr-pool stays git-free): resolve PR+branch bead-first, work in
# an isolated worktree, commit but never push, record then swap labels, never
# close. WORKTREE_DIR is expanded so the agent gets a concrete path.
nudge_text_worker() {
  local id="$1"
  printf '%s' "Read $WORKER_SKILL_MD and implement work bead $id. Claim it (bd update $id --claim). Resolve its PR + head branch bead-first from the parent merge-request bead's metadata (repo, pr_number, branch — no gh needed) and assert metadata.author is me; if you cannot resolve the PR or it is not mine, abort WITHOUT editing anything and leave it for worker-stuck. Create or reuse an isolated git worktree for that branch under $WORKTREE_DIR, implement the change the bead describes, and commit it (do NOT push, do NOT force). Then record the worktree path + commit SHA on the bead with bd comment, and ONLY AFTER that swap labels atomically: bd update $id --add-label needs-push --remove-label worker-ready. Leave the bead claimed/in_progress; do NOT close it."
}

# worker_label builds the claude conversation name for a work bead:
# "worker <id> PR #<n>", falling back to just the id.
worker_label() {
  local id="$1" pid pr
  pid="$(bd_obj "$id" | jq -r '.parent // empty')"
  pr="$(bd_obj "$pid" | jq -r '.metadata.pr_number // empty')"
  if [ -n "$pr" ]; then
    printf 'worker %s PR #%s' "$id" "$pr"
  else
    printf 'worker %s' "$id"
  fi
}
```

- [x] **Step 6: Update `send_nudge`'s internal call** (keep its signature for now)

In `send_nudge`, change the line that builds the nudge from `nudge_text "$cid"` to `nudge_text_feedback "$cid"`.

- [x] **Step 7: Run the suite to verify green**

Run: `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
Expected: ALL tests PASS (new role/worker tests + the renamed `nudge_text_feedback` test + every existing test).

- [x] **Step 8: Commit**

```bash
git add packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
git commit -m "feat(pr-pool): per-role config resolvers + worker nudge/label" -- packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
```

---

## Task 2: Generalize the session lifecycle per role

Make `ensure_session`/`claude_rename`/`clear_context`/`teardown_session`/`send_nudge`/`work_one` role- or session-parameterized. Feedback behavior is unchanged; this just threads the role/session through. `drain_once` still uses `discover_cycles` + `MAX` (rewritten in Task 4).

**Files:**

- Modify: `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh`
- Test: `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`

- [x] **Step 1: Update the affected existing tests to the new signatures** (they will fail against the old code)

Replace these existing tests' call lines (bodies otherwise unchanged):

```bash
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
```

Add a new test for the worker session creation:

```bash
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
```

- [x] **Step 2: Run the suite to confirm the updated/new tests fail**

Run: `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
Expected: the five rewritten tests + the new `ensure_session worker` test FAIL (old signatures use the `ROLE_NAME` global / `ensure_session` ignores its arg).

- [x] **Step 3: Generalize `ensure_session` to take a role**

Replace `ensure_session` with:

```bash
# ensure_session creates the role's named claude session if absent (pinning
# BEADS_DIR/WORKSPACE_ROOT and the role's BEADS_ACTOR), else reuses it. The role
# defaults to feedback-processor. Waits for the prompt before returning.
ensure_session() {
  local role="${1:-feedback-processor}" sess actor
  sess="$(role_session "$role")"
  actor="$(role_actor "$role")"
  if ! tmux -L "$SOCKET" has-session -t "$sess" 2>/dev/null; then
    tmux -u -L "$SOCKET" new-session -d -s "$sess" -c "$REPO_ROOT" \
      -e "BEADS_ACTOR=$actor" \
      -e "BEADS_DIR=$REPO_ROOT/.beads" \
      -e "WORKSPACE_ROOT=$REPO_ROOT" \
      claude --dangerously-skip-permissions --effort max --session-id "$(uuidgen)" \
      >/dev/null || {
      log "ERROR: tmux new-session failed for role '$role' (session '$sess')"
      return 1
    }
  fi
  wait_ready "$sess"
}
```

- [x] **Step 4: Generalize `claude_rename`, `clear_context`, `teardown_session` to take the session**

```bash
# claude_rename names the current claude conversation in the given session.
claude_rename() { submit_line "$1" "/rename \"$2\""; }
```

```bash
# clear_context resets claude's context in the given session, then waits for the
# prompt so the session is ready for the next item.
clear_context() {
  submit_line "$1" "/clear" || return 1
  wait_ready "$1"
}
```

```bash
# teardown_session gracefully exits claude in the given session, then kills it.
# kill-session is the guaranteed teardown. No-op if the session is absent.
teardown_session() {
  local sess="$1"
  tmux -L "$SOCKET" has-session -t "$sess" 2>/dev/null || return 0
  submit_line "$sess" "$EXIT_CMD" || true
  tmux -L "$SOCKET" kill-session -t "$sess" >/dev/null 2>&1 || true
}
```

- [x] **Step 5: Generalize `send_nudge` to pick the nudge by role**

Replace `send_nudge` with:

```bash
# send_nudge sends the role-appropriate instruction line into the session.
send_nudge() {
  local role="$1" sess="$2" id="$3"
  [ -z "$(role_skill "$role")" ] && {
    log "ERROR: SKILL.md path unset for role '$role'"
    return 1
  }
  submit_line "$sess" "$(role_nudge "$role" "$id")"
}
```

- [x] **Step 6: Update `work_one` to thread the role + session** (keep `wait_done`'s current 2-arg call — changed in Task 3)

Replace `work_one` with:

```bash
# work_one drives one bead to completion in its role's (reused) session: ensure
# the session, name the conversation, nudge, wait for the role's completion
# signal, then /clear for the next item. clear_context always runs so the session
# is left ready/reusable.
work_one() {
  local role="$1" id="$2" sess rc
  sess="$(role_session "$role")"
  ensure_session "$role" || return 1
  claude_rename "$sess" "$(role_convo_name "$role" "$id")"
  if ! send_nudge "$role" "$sess" "$id"; then
    unclaim "$id"
    clear_context "$sess"
    return 1
  fi
  wait_done "$id" "$sess"
  rc=$?
  clear_context "$sess"
  return "$rc"
}
```

- [x] **Step 7: Update `drain_once`'s call sites to the new signatures** (still `discover_cycles` + `MAX`; rewritten in Task 4)

In `drain_once`, change `work_one "$cid"` to `work_one feedback-processor "$cid"`, and change the trailing `teardown_session` call to `teardown_session "$(role_session feedback-processor)"`.

- [x] **Step 8: Run the suite to verify green**

Run: `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
Expected: ALL tests PASS (rewritten lifecycle tests, the new worker-session test, and the still-`bd list` drain tests).

- [x] **Step 9: Commit**

```bash
git add packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
git commit -m "refactor(pr-pool): parameterize session lifecycle by role" -- packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
```

---

## Task 3: Role-aware completion (`wait_done`)

Add the worker's completion signal (`needs-push` label) and failure handling (`worker-stuck` label, never unclaim), keeping feedback behavior identical.

**Files:**

- Modify: `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh`
- Test: `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`

- [x] **Step 1: Update the existing `wait_done` tests to the role-first signature, and add worker tests**

Rewrite the three existing `wait_done` tests' call lines to `wait_done feedback-processor <id> <sess>`:

```bash
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
```

Append the worker completion/failure tests:

```bash
@test "wait_done worker: succeeds when the bead gains the needs-push label" {
  export PR_POOL_MAX_WAIT=2 PR_POOL_POLL_INTERVAL=1
  make_stub bd 'echo "{\"id\":\"zr-w1\",\"status\":\"in_progress\",\"labels\":[\"needs-push\"]}"'
  make_stub tmux 'echo "pane alive"'
  load_script
  run wait_done worker zr-w1 WORKER
  [ "$status" -eq 0 ]
  ! grep -q -- "update zr-w1 --status=open" "$CALLS_LOG"   # never unclaim a worker
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
```

- [x] **Step 2: Run the suite to confirm the new/updated tests fail**

Run: `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
Expected: the rewritten `wait_done` tests (arity) and the two worker tests FAIL.

- [x] **Step 3: Add label helpers** (place near `unclaim`)

```bash
# bead_labels prints one label per line for a bead (handles labels==null).
bead_labels() { bd_obj "$1" | jq -r '(.labels // []) | .[]' 2>/dev/null; }

# bead_has_label returns 0 if the bead carries the exact label.
bead_has_label() { bead_labels "$1" | grep -qxF "$2"; }

# mark_stuck flags a worker bead the orchestrator could not see to completion so
# it surfaces in `bd list --label worker-stuck`. Best-effort.
mark_stuck() { bd update "$1" --add-label worker-stuck >/dev/null 2>&1 || true; }
```

- [x] **Step 4: Make `wait_done` role-aware**

Replace `wait_done` (and its helpers) with:

```bash
# done_signal returns 0 when the role's completion signal is present:
#   feedback-processor -> the cycle bead is closed
#   worker             -> the bead carries the needs-push label
done_signal() {
  case "$1" in
  worker) bead_has_label "$2" needs-push ;;
  *) [ "$(cycle_status "$2")" = "closed" ] ;;
  esac
}

# wait_done_fail performs the role-specific failure action:
#   feedback-processor -> unclaim (so the open pool resurfaces the cycle)
#   worker             -> stamp worker-stuck, NEVER unclaim (a dead worker may
#                         hold a half-built worktree; blind retry is unsafe)
wait_done_fail() {
  case "$1" in
  worker) log "wait_done: worker $2 $3; flagging worker-stuck"; mark_stuck "$2" ;;
  *) log "wait_done: $2 $3; unclaiming"; unclaim "$2" ;;
  esac
}

# wait_done polls until the role's completion signal fires (success) or MAX_WAIT
# elapses / the pane dies (failure). On failure it runs the role's fail action;
# it NEVER auto-closes. It re-checks the signal after a pane death so a bead
# completed in the same instant the pane exited is not treated as a failure.
wait_done() {
  local role="$1" id="$2" sess="$3" deadline
  deadline=$(($(date +%s) + MAX_WAIT))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    done_signal "$role" "$id" && return 0
    if ! pane_alive "$sess"; then
      done_signal "$role" "$id" && return 0
      wait_done_fail "$role" "$id" "exited before completing"
      return 1
    fi
    sleep "$POLL_INTERVAL"
  done
  done_signal "$role" "$id" && return 0
  wait_done_fail "$role" "$id" "not complete within ${MAX_WAIT}s"
  return 1
}
```

- [x] **Step 5: Update `work_one` to pass the role to `wait_done`**

In `work_one`, change `wait_done "$id" "$sess"` to `wait_done "$role" "$id" "$sess"`.

- [x] **Step 6: Run the suite to verify green**

Run: `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
Expected: ALL tests PASS (feedback `wait_done` behavior preserved; worker success/stuck covered).

- [x] **Step 7: Commit**

```bash
git add packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
git commit -m "feat(pr-pool): role-aware wait_done (worker needs-push / worker-stuck)" -- packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
```

---

## Task 4: `bd ready` discovery + per-role drain + teardown-all

Switch discovery to `bd ready` (per role; native `--label` for the worker), emit `role<TAB>id`, and rewrite `drain_once` to drain each role up to its own cap then tear down every role's session.

**Files:**

- Modify: `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh`
- Test: `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`

- [x] **Step 1: Rewrite the discovery + drain tests for `bd ready` + role tags**

Replace the existing `discover_cycles` test with discovery tests against `bd ready`:

```bash
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
  grep -qP "^feedback-processor\tzr-mine$" <<< "$output" || [[ "$output" == *"feedback-processor	zr-mine"* ]]
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
```

Replace the `drain_once: works one cycle …` test (now `bd ready`, plus assert the worker query is issued):

```bash
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
```

Replace the `drain_once: stops after MAX cycles per pass` test with a per-role cap test:

```bash
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
```

Append a no-orphan teardown test (a stray session for a role not dispatched this pass is still reaped):

```bash
@test "drain_once: tears down every role session, incl. one not dispatched this pass" {
  export SELF_LOGIN="me" PR_POOL_SKILL_MD="/abs/SKILL.md" PR_POOL_WORKER_SKILL_MD="/abs/W.md"
  export PR_POOL_SEND_SETTLE=0
  # No ready work at all this pass, but a leftover WORKER session exists.
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
```

Also update the `main: optional sentinel pauses…` test's `bd` stub to answer `ready` instead of `list --type=task` (gated still short-circuits, but keep the stub honest):

```bash
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
```

- [x] **Step 2: Run the suite to confirm the discovery/drain tests fail**

Run: `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
Expected: discovery + drain tests FAIL (`discover` not defined; `drain_once` still on `discover_cycles`/`MAX`).

- [x] **Step 3: Replace `discover_cycles` with `discover_feedback` + `discover_worker` + `discover`**

Remove `discover_cycles`. Add:

```bash
# discover_feedback prints "feedback-processor<TAB><cycle-id>" for each open
# process-feedback cycle (from bd ready) whose parent merge-request is mine.
discover_feedback() {
  local self
  self="$(resolve_self)"
  [ -z "$self" ] && {
    log "ERROR: could not resolve self_login from pg-pr config"
    return 1
  }
  bd ready --json --limit 0 2>/dev/null |
    jq -r 'if type=="array" then . else [] end
             | map(select(.issue_type=="task" and (.title | startswith("process-feedback:"))))
             | .[].id' |
    while read -r cid; do
      [ -z "$cid" ] && continue
      local pid author
      pid="$(bd_obj "$cid" | jq -r '.parent // empty')"
      [ -z "$pid" ] && continue
      author="$(bd_obj "$pid" | jq -r '.metadata.author // ""')"
      [ "$author" = "$self" ] && printf 'feedback-processor\t%s\n' "$cid" || true
    done
}

# discover_worker prints "worker<TAB><bead-id>" for each worker-ready bead, using
# bd ready's native label filter (the .labels field is null when unset, so a jq
# label check is avoided here).
discover_worker() {
  bd ready --label worker-ready --json --limit 0 2>/dev/null |
    jq -r 'if type=="array" then . else [] end | .[].id' |
    while read -r id; do
      [ -n "$id" ] && printf 'worker\t%s\n' "$id"
    done
}

# discover prints role<TAB>bead-id lines for every dispatchable ready bead.
discover() {
  discover_feedback
  discover_worker
}
```

- [x] **Step 4: Rewrite `drain_once` for per-role caps + teardown-all**

Add a `teardown_all` helper (place near `teardown_session`):

```bash
# teardown_all tears down every known role's session (not only ones created this
# pass), reaping strays from crashed/earlier runs or roles no longer dispatched.
teardown_all() {
  local role
  for role in $ROLES; do
    teardown_session "$(role_session "$role")"
  done
}
```

Replace `drain_once` with:

```bash
# drain_once works each role's discovered beads up to that role's cap (so neither
# role can starve the other), then tears down all role sessions. Returns 0
# whether gated (paused) or after attempting the capped work.
drain_once() {
  gated && return 0
  local all role total=0
  all="$(discover)"
  for role in $ROLES; do
    local cap worked=0 r id
    cap="$(role_max "$role")"
    while IFS="$(printf '\t')" read -r r id; do
      [ "$r" = "$role" ] || continue
      [ -z "$id" ] && continue
      if [ "$worked" -ge "$cap" ]; then
        log "pr-pool: reached cap=$cap for role '$role' this pass; stopping that role"
        break
      fi
      log "pr-pool: working $role $id"
      work_one "$role" "$id" || log "pr-pool: $role $id did not complete (flagged)"
      worked=$((worked + 1))
      total=$((total + 1))
    done <<EOF
$all
EOF
  done
  teardown_all
  log "pr-pool: drain pass complete ($total item(s) attempted)"
  return 0
}
```

- [x] **Step 5: Run the suite to verify green**

Run: `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
Expected: ALL tests PASS.

- [x] **Step 6: Full flake check (the bats flake check + formatting)**

Run: `nix flake check 2>&1 | tail -20`
Expected: the `test-pgii-pack-pr-support-bats` check passes; no failures.

- [x] **Step 7: Commit**

```bash
git add packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
git commit -m "feat(pr-pool): bd ready discovery + per-role drain/teardown (triager)" -- packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
```

---

## Task 5: Worker SKILL contract

Create the worker agent's SKILL, mirroring the structure of the existing `pg-pr-process-feedback/SKILL.md`. There is no bats coverage for SKILL prose (the `nudge_text_worker` test in Task 1 covers the orchestrator side); this task is the contract the dispatched worker reads.

**Files:**

- Create: `packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-work-bead/SKILL.md`

- [x] **Step 1: Write the worker SKILL**

Create `packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-work-bead/SKILL.md` with:

````markdown
---
name: pg-pr-work-bead
description: Implement a single worker-ready PR work bead in an isolated git worktree — claim, resolve the PR/branch bead-first, implement, commit (do NOT push), record the worktree path + SHA on the bead, and swap worker-ready→needs-push for human review. Use when dispatched to work a bead labeled worker-ready.
---

# pg-pr work bead

You are the **worker**. You take ONE `worker-ready` work bead, make the code
change it describes in an isolated git worktree, and **commit but do not push** —
a human reviews and pushes. You do not triage feedback and you do not close the
bead.

## Roles (do only your part)

- **pg-pr (producer):** creates PR / cycle / feedback beads. Not you.
- **feedback-processor (someone else):** turns feedback into work beads. Not you.
- **You — the worker:** implement one `worker-ready` work bead, commit (no push),
  and hand it back for review.

## Inputs

- A work-bead id (`task`/`bug`), a child of a PR (merge-request) bead, labeled
  `worker-ready`.
- The PR bead's `metadata` carries everything you need to find the branch:
  `repo`, `pr_number`, `branch`, `base`, `author`, `state` (verified present on
  merge-request beads). **Resolve bead-first; do not call `gh` unless the bead
  lacks the field.**

## Workflow

1. **Claim** the work bead:

   ```bash
   bd update <id> --claim
   ```

2. **Resolve the PR + head branch, bead-first.** Walk to the parent PR bead and
   read its metadata:

   ```bash
   bd show <id> --json | jq -r '.parent'                 # -> PR bead id
   bd show <PR-id> --json | jq -r '.metadata | {repo, pr_number, branch, base, author, state}'
   ```

3. **Abort safely if you cannot proceed** — do this BEFORE editing anything,
   because you have already claimed the bead:
   - no parent PR bead, missing `branch`, or PR `state` is not `open`; **or**
   - `metadata.author` is **not** you (`pg-pr config show --json | jq -r '.self_login'`).

   On abort: make no code changes, add a one-line `bd comment <id>` explaining
   why, and stop. (The orchestrator will mark the bead `worker-stuck`; the human
   inspects `bd list --label worker-stuck`.) The triager trusted the
   `worker-ready` label — this author check is your own safety net so a mislabel
   never commits to someone else's branch.

4. **Create or reuse an isolated git worktree** for the head branch. `git
worktree add` refuses a branch already checked out elsewhere, so reuse an
   existing worktree rather than re-adding:

   ```bash
   WT="${PR_POOL_WORKTREE_DIR:-$HOME/.local/state/pr-pool/worktrees}/<repo-slug>-pr<pr_number>"
   git -C "$REPO_ROOT" fetch origin "<branch>"
   if [ -d "$WT" ]; then
     git -C "$WT" checkout "<branch>" && git -C "$WT" pull --ff-only
   else
     git -C "$REPO_ROOT" worktree add "$WT" "<branch>"
   fi
   cd "$WT"
   ```

5. **Implement** the change the bead describes, scoped to only the files it
   implies. Run a cheap local check/build if one is feasible and fast.

6. **Commit — never push, never force.** Conventional message referencing the
   bead and PR:

   ```bash
   git add -A
   git commit -m "<type>(<scope>): <change> (bead <id>, PR #<pr_number>)"
   ```

7. **Record, then signal (order matters).** First record where the work is, then
   swap the labels. The orchestrator watches for `needs-push` and resets the
   session the instant it appears, so the comment MUST land first:

   ```bash
   SHA="$(git -C "$WT" rev-parse HEAD)"
   bd comment <id> "Committed $SHA on branch <branch> in worktree $WT (unpushed). Ready for review + push."
   bd update <id> --add-label needs-push --remove-label worker-ready
   ```

   Leave the bead **claimed / in_progress** — do **not** close it.

## Boundaries

- One bead per run. Touch only files the bead implies.
- **Never `git push`, never `--force`.** A human reviews and pushes.
- Do not comment on the PR (GitHub) — you write to the **bead**, not the PR.
- Do not close the bead; `needs-push` is the human's review queue.
````

- [x] **Step 2: Verify formatting + structure**

Run: `prek run --all-files 2>&1 | tail -20` (or `pre-commit run --all-files`)
Expected: PASS (treefmt may reformat the new SKILL.md — re-stage if so).

- [x] **Step 3: Commit**

```bash
git add packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-work-bead/SKILL.md
git commit -m "feat(pg-pr-plugin): pg-pr-work-bead worker SKILL contract" -- packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-work-bead/SKILL.md
```

---

## Final verification (after all tasks)

- [ ] **Full gate:** `nix flake check` passes (incl. `test-pgii-pack-pr-support-bats`); `prek run --all-files` passes.
- [ ] **Live smoke (manual — CONFIRM the target bead with the user first; it mutates real beads and creates a real worktree).** From the monorepo root: hand-label one of _your own_ work beads `worker-ready` (`bd update <id> --add-label worker-ready`), then run `pr-pool.sh` with `PR_POOL_SKILL_MD` + `PR_POOL_WORKER_SKILL_MD` pointing at the two SKILLs and default caps. Verify: a `WORKER` tmux session spawns on `-L pgpool`; the agent resolves the branch from the MR bead (no `gh`); a worktree + commit appear and the **remote branch head is unchanged** (`git ls-remote`); the bead gains `needs-push` + a path/SHA comment and stays `in_progress`; the session is torn down. Then force a failure (label a bead whose PR is closed) and confirm it lands in `bd list --label worker-stuck`.

---

## Self-Review

**1. Spec coverage** (spec `2026-06-09-pr-pool-work-triaging-design.md`):

- Two-role mechanical triager → Task 4 (`discover`/`drain_once`), Task 1 (resolvers). ✓
- `bd ready` replaces `bd list --status=open`; native `--label worker-ready` → Task 4. ✓
- Per-role config table (`role_session/actor/skill/nudge/convo_name/max`) → Task 1. ✓
- Generalized lifecycle per role → Task 2. ✓
- Role-aware completion: feedback close + unclaim; worker `needs-push` + `worker-stuck`, no unclaim → Task 3. ✓
- Per-role caps + teardown-all → Task 4. ✓
- Worker SKILL: bead-first branch, ownership assert, worktree reuse, commit-no-push, record-then-swap, no-close → Task 5. ✓
- Feedback-processor SKILL unchanged → not touched (correct). ✓
- Testing (per-role caps, native `--label`, worker success/stuck, no-orphan, regression) → Tasks 1–4 tests + Final verification live smoke. ✓

**2. Placeholder scan:** No `TBD`/`TODO`/"add error handling"/"similar to Task N"; every code step shows complete function bodies and complete test bodies. ✓

**3. Type/signature consistency across tasks:**

- `ensure_session <role>` (Task 2) ↔ called `ensure_session "$role"` in `work_one` (Task 2). ✓
- `claude_rename <sess> <name>` / `clear_context <sess>` / `teardown_session <sess>` (Task 2) ↔ `work_one` + `teardown_all` (Tasks 2/4) call with a session string. ✓
- `send_nudge <role> <sess> <id>` (Task 2) ↔ `work_one` call (Task 2). ✓
- `wait_done <role> <id> <sess>` (Task 3) ↔ `work_one` call updated in Task 3 Step 5. ✓
- `role_nudge`/`role_convo_name` (Task 1) depend on `nudge_text_feedback`/`nudge_text_worker`/`cycle_label`/`worker_label` — all defined in Task 1 (`cycle_label` pre-exists). ✓
- `discover` emits `role<TAB>id` (Task 4) ↔ `drain_once` reads `IFS=tab` (Task 4). ✓
- `bead_has_label`/`mark_stuck` (Task 3) used only within `wait_done` helpers (Task 3). ✓
- `MAX` global: used by `wait_done`'s `MAX_WAIT` (separate var, unaffected); the drain `MAX` cap is replaced by `role_max` in Task 4 — no remaining reference to the old `MAX` in drain after Task 4. (If `MAX` is otherwise unreferenced after Task 4, leaving the unused var is harmless; optionally remove it.)
