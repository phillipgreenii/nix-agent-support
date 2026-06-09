# pr-pool session lifecycle + work-bead dedup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Also use superpowers:bash-scripting (workspace bash conventions) and superpowers:test-driven-development.

**Goal:** Give the dispatched claude session a managed lifecycle within one on-demand `pr-pool.sh` run (role-named tmux session, per-item `/rename`, `/clear` between items, `exit`+`kill-session` teardown — no more orphaned panes), and make the feedback-processor contract de-duplicate work beads by considering the PR's existing open work beads.

**Architecture:** Two independent parts. **Part A** edits the feedback processor's _contract_ (the plugin `SKILL.md` + the orchestrator's `nudge_text`) so work beads become children of the PR bead and the processor links/updates an existing open work bead instead of creating a duplicate. **Part B** refactors `pr-pool.sh`: a shared `submit_line` helper, `ensure_session` (create-if-absent role session) / `claude_rename` / `clear_context` / `teardown`, replacing the per-cycle `dispatch`/`session_name`. **Part C** is a live smoke test. Stays on-demand, N=1; pool/idle-timeout/daemon/triaging deferred.

**Tech Stack:** bash, bats (PATH-stubbed CLIs), tmux (`-L pgpool`), `bd`/`pg-pr`/`jq`, Claude Code slash commands (`/rename`, `/clear`).

**Spec:** `docs/superpowers/specs/2026-06-09-pr-pool-session-lifecycle-and-dedup-design.md`.

Local test command throughout (bats is not on PATH):

```
nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
```

**Commit hygiene (all tasks):** stage only the files named in each task. Do **not** stage anything under `home/programs/pgii-packs/`. Pre-commit hooks (treefmt/shfmt) may reformat staged files; if a commit aborts with "files were modified by this hook", re-stage the same files and re-run the commit. Commit in the foreground.

---

## File Structure

- **Modify** `packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-process-feedback/SKILL.md` — refresh the feedback-processor contract (Part A).
- **Modify** `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh` — `nudge_text` (Part A) + session-lifecycle functions (Part B).
- **Modify** `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats` — new/updated unit tests.

No new files; the flake check `test-pgii-pack-pr-support-bats` already runs the suite.

---

## Task A1: Refresh the feedback-processor SKILL.md

The current `SKILL.md` is stale (says feedback "lands in Phase 3"; tells the agent to _implement the change_, which the nudge overrides) and has no notion of de-duplicating work. This is a documentation/contract change — there is no unit test; verification is the self-review checklist below plus the live smoke in Task C1.

**Files:**

- Modify: `packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-process-feedback/SKILL.md`

- [x] **Step 1: Replace the file contents**

Overwrite `SKILL.md` with exactly:

````markdown
---
name: pg-pr-process-feedback
description: Process the lifecycle of a processing-cycle bead — claim, review its feedback, create or update work beads (children of the PR bead), close the feedback and the cycle. Use when the user asks to "process feedback", "work the PR feedback queue", or you spot an open processing-cycle bead.
---

# pg-pr process feedback

Lifecycle handler for processing-cycle / feedback / work beads on a merge-request.

## Roles (do only your part)

- **pg-pr (producer):** creates/closes the **PR bead**; creates **cycle** and **feedback** beads. Not you.
- **You — the feedback processor:** review and close **cycle + feedback** beads, and create **work beads** from the feedback. You do **not** implement fixes.
- **Worker agent (someone else):** performs the work described in the work beads. Not you.

## Bead shapes

- **PR bead** — the merge-request. Parent of cycle beads and work beads.
- **processing-cycle** — `process-feedback: …`; child of the PR bead. Tracks the feedback accumulated since the last review. Children are feedback beads.
- **feedback** — one upstream review comment or CI failure; child of a cycle. Carries `repo`, `pr`, `thread_id`, `author`, `author_role`, `path`, `line`.
- **work bead** — a proposed change (`task`/`bug`) you create in response to feedback. A **child of the PR bead**, `discovered-from` the feedback that motivated it.

There can legitimately be more than one open cycle for a PR (pg-pr starts a new cycle when the existing one is already in-progress). That is fine — you work one cycle at a time and de-duplicate at the work-bead level (below).

## Workflow

1. Claim the processing-cycle bead:

   ```bash
   bd update <cycle-id> --claim
   ```

2. Read its feedback children, and resolve the **PR bead** (the cycle's parent):

   ```bash
   bd children <cycle-id>
   bd show <cycle-id> --json | jq -r '.parent'   # -> the PR bead id
   ```

3. List the PR's **existing open work beads** — the ones you must avoid duplicating:

   ```bash
   bd children <PR-bead-id> --status=open        # filter to task/bug (work beads)
   ```

4. For each feedback bead:
   1. Read upstream context (`pg-pr pr show`, `pg-pr pr files`, etc.) and decide the work it implies (or that it is non-actionable).
   2. **De-duplicate:** if that work matches an existing open work bead, **link/update** it — add this feedback as another `discovered-from` and refine the description if warranted — instead of creating a duplicate. Multiple comments, or a later cycle's feedback, commonly map to the same work.
   3. Otherwise create a **new work bead** (`task`/`bug`) as a **child of the PR bead**, `discovered-from` this feedback, describing the needed change.
   4. Do **not** implement the change and do **not** work the new bead — that is the worker agent's job.
   5. Close the feedback bead:
      ```bash
      bd close <feedback-id> --reason="<short verb-phrase>"
      ```

5. Close the processing-cycle bead with a one-line summary.

## Boundaries

- The processing-cycle bead is the unit of work. Never close it before every feedback child is closed.
- You create/link work beads only; you never apply fixes.
- Author precedence on responses: `self > team_member > org_member > bot`.
- Don't strip the 🤖 marker — `pg-pr comment` adds it automatically.
````

- [x] **Step 2: Self-review the new content against the contract**

Confirm, by re-reading the file: (a) no "Phase 3" / "implement the change" language remains; (b) work beads are described as **children of the PR bead**, `discovered-from` feedback; (c) the de-dup step (consider the PR's existing open work beads, link/update instead of duplicate) is present; (d) the "never close the cycle before all feedback children are closed" boundary remains. Fix inline if any are missing.

- [ ] **Step 3: Commit**

```bash
git add packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-process-feedback/SKILL.md
git commit -m "docs(pg-pr): refresh process-feedback skill (work beads = PR children; dedup open work)"
```

> ✅ **Task A1 complete** — commit `1d4935a`. Spec+quality review clean (contract-compliant, internally consistent, stale Phase-3/implement language removed).

---

## Task A2: Align `nudge_text` to the refreshed contract

The nudge no longer needs to override "implement the change" (the skill is now correct). It must name the cycle, point at the skill, and state the dedup expectation + guardrails. It must **not** tell the worker to `exit` (the orchestrator now owns session teardown).

**Files:**

- Modify: `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh` (the `nudge_text` function)
- Test: `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`

- [x] **Step 1: Write the failing test**

Append to `pr-pool.bats`:

```bash
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
```

- [x] **Step 2: Run it to verify it fails**

Run: `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats -f "nudge_text"`
Expected: FAIL — the current nudge lacks "open work bead"/"child of the PR bead" and still contains "exit".

- [x] **Step 3: Replace `nudge_text`**

In `pr-pool.sh`, replace the whole `nudge_text` function (its comment currently begins `# nudge_text builds the instruction sent to the worker.`) with:

```bash
# nudge_text builds the instruction sent to the feedback processor. Points at the
# refreshed SKILL.md; the processor creates/links work beads (children of the PR
# bead) and de-duplicates against the PR's existing open work beads. It does NOT
# implement fixes, does NOT work the new beads, and does NOT exit — the
# orchestrator owns session teardown.
nudge_text() {
  local cid="$1"
  printf '%s' "Read $SKILL_MD and process process-feedback cycle $cid: claim it, read its feedback children (bd children $cid), resolve the parent PR bead and review the PR's existing open work beads (bd children <PR> --status=open). For each feedback, create a work bead (task/bug) as a child of the PR bead, discovered-from the feedback — but if that work matches an existing open work bead, link/update it instead of creating a duplicate. Do NOT apply fixes and do NOT work the new work beads. Close each feedback bead, then close the cycle with a one-line summary."
}
```

- [x] **Step 4: Run it to verify it passes**

Run: `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats -f "nudge_text"` → PASS.
Then run the full suite to confirm no regressions.

- [x] **Step 5: Commit**

```bash
git add packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh \
        packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
git commit -m "feat(pr-pool): nudge_text de-dups work beads as PR children, no worker exit"
```

---

## Task B1: Add `ROLE_NAME` + `submit_line`; route `send_nudge` through it

Generalize the "type a line into claude, settle, then a SEPARATE Enter" pattern (the shipped paste-mode fix) into one helper used by `/rename`, the nudge, `/clear`, and exit.

**Files:**

- Modify: `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh`
- Test: `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`

- [x] **Step 1: Write the failing test**

Append to `pr-pool.bats`:

```bash
@test "submit_line: types the text then submits with a SEPARATE Enter" {
  export PR_POOL_SEND_SETTLE=0
  make_stub tmux 'exit 0'
  load_script
  run submit_line "SESS" "hello world"
  [ "$status" -eq 0 ]
  grep -q -- "send-keys -t SESS hello world" "$CALLS_LOG"
  grep -qE "send-keys -t SESS Enter$" "$CALLS_LOG"
}
```

- [x] **Step 2: Run it to verify it fails**

Run: `... -f "submit_line"`
Expected: FAIL — `submit_line: command not found`.

- [x] **Step 3: Implement**

In `pr-pool.sh`, add a config var immediately after the `SEND_SETTLE=…` config line (in the env-var block near the top):

```bash
ROLE_NAME="${PR_POOL_ROLE_NAME:-PR FEEDBACK PROCESSOR}" # tmux session name = the role; monitoring keys on this
```

Add `submit_line` immediately before `nudge_text` (so it's defined before its first use):

```bash
# submit_line types a line into the claude pane, settles, then sends a SEPARATE
# Enter. Bundling text+Enter makes claude treat the burst as a paste and the
# trailing Enter becomes a newline instead of submitting (confirmed live). Used
# for /rename, the nudge, /clear, and exit.
submit_line() {
  local sess="$1" text="$2"
  tmux -L "$SOCKET" send-keys -t "$sess" "$text" || return 1
  sleep "$SEND_SETTLE"
  tmux -L "$SOCKET" send-keys -t "$sess" Enter
}
```

Replace the whole `send_nudge` function to route through `submit_line`:

```bash
send_nudge() {
  local sess="$1" cid="$2"
  [ -z "$SKILL_MD" ] && {
    log "ERROR: PR_POOL_SKILL_MD unset (path to pg-pr-process-feedback SKILL.md)"
    return 1
  }
  submit_line "$sess" "$(nudge_text "$cid")"
}
```

- [x] **Step 4: Run it to verify it passes**

Run: `... -f "submit_line"` → PASS. Then the full suite (the existing `send_nudge` test still passes — it asserts the text + a separate `Enter`, which `submit_line` produces).

- [x] **Step 5: Commit**

```bash
git add packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh \
        packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
git commit -m "refactor(pr-pool): add ROLE_NAME + submit_line; route send_nudge through it"
```

---

## Task B2: `ensure_session` — create-if-absent role session (alongside `dispatch`)

Add the new function without removing `dispatch` yet (so the suite stays green). It creates the role-named session if absent, then waits for the prompt; on reuse it just waits for the prompt.

**Files:**

- Modify: `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh`
- Test: `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`

- [x] **Step 1: Write the failing tests**

Append to `pr-pool.bats`. The tmux stub is **stateful** (a file marks whether the session exists) so `has-session`/`new-session`/`kill-session` behave realistically:

```bash
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
```

- [x] **Step 2: Run them to verify they fail**

Run: `... -f "ensure_session"`
Expected: FAIL — `ensure_session: command not found`.

- [x] **Step 3: Implement**

In `pr-pool.sh`, add `ensure_session` immediately after the `dispatch` function. It targets `$ROLE_NAME` and waits for readiness on both create and reuse (so a stale leftover session is still confirmed ready before use):

```bash
# ensure_session creates the role-named claude session if it does not exist
# (pinning BEADS_DIR/WORKSPACE_ROOT/BEADS_ACTOR per session), else reuses it.
# Idempotent across work items in a run. Waits for the prompt before returning.
ensure_session() {
  if ! tmux -L "$SOCKET" has-session -t "$ROLE_NAME" 2>/dev/null; then
    tmux -u -L "$SOCKET" new-session -d -s "$ROLE_NAME" -c "$REPO_ROOT" \
      -e "BEADS_ACTOR=$ACTOR" \
      -e "BEADS_DIR=$REPO_ROOT/.beads" \
      -e "WORKSPACE_ROOT=$REPO_ROOT" \
      claude --dangerously-skip-permissions --effort max --session-id "$(uuidgen)" \
      >/dev/null || {
      log "ERROR: tmux new-session failed for role '$ROLE_NAME'"
      return 1
    }
  fi
  wait_ready "$ROLE_NAME"
}
```

- [x] **Step 4: Run them to verify they pass**

Run: `... -f "ensure_session"` → PASS (both). Then the full suite.

- [x] **Step 5: Commit**

```bash
git add packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh \
        packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
git commit -m "feat(pr-pool): ensure_session creates/reuses the role-named claude session"
```

---

## Task B3: `claude_rename` + `cycle_label`

Name the claude conversation per work item for findability. The tmux session stays role-named.

**Files:**

- Modify: `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh`
- Test: `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`

- [x] **Step 1: Write the failing tests**

Append to `pr-pool.bats`:

```bash
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
  run claude_rename "process-feedback zr-c PR #7"
  [ "$status" -eq 0 ]
  grep -q -- "send-keys -t PR FEEDBACK PROCESSOR /rename" "$CALLS_LOG"
  grep -q -- "process-feedback zr-c PR #7" "$CALLS_LOG"
}
```

- [x] **Step 2: Run them to verify they fail**

Run: `... -f "cycle_label|claude_rename"`
Expected: FAIL — functions not defined.

- [x] **Step 3: Implement**

In `pr-pool.sh`, add after `ensure_session`:

```bash
# cycle_label builds a human-friendly claude conversation name for a cycle:
# "process-feedback <cid> PR #<n>", falling back to just the cid if the parent
# PR number can't be resolved.
cycle_label() {
  local cid="$1" pid pr
  pid="$(bd_obj "$cid" | jq -r '.parent // empty')"
  pr="$(bd_obj "$pid" | jq -r '.metadata.pr_number // empty')"
  if [ -n "$pr" ]; then
    printf 'process-feedback %s PR #%s' "$cid" "$pr"
  else
    printf 'process-feedback %s' "$cid"
  fi
}

# claude_rename names the current claude conversation (findability + monitoring).
claude_rename() { submit_line "$ROLE_NAME" "/rename \"$1\""; }
```

- [x] **Step 4: Run them to verify they pass**

Run: `... -f "cycle_label|claude_rename"` → PASS. Then the full suite.

- [x] **Step 5: Commit**

```bash
git add packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh \
        packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
git commit -m "feat(pr-pool): claude_rename + cycle_label (per-item conversation name)"
```

---

## Task B4: `clear_context`

Reset claude's context for the next work item and wait for the prompt to return.

**Files:**

- Modify: `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh`
- Test: `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`

- [x] **Step 1: Write the failing test**

Append to `pr-pool.bats`:

```bash
@test "clear_context: submits /clear and waits for the prompt" {
  export PR_POOL_SEND_SETTLE=0 PR_POOL_READY_TIMEOUT=2
  make_stub tmux 'case "$*" in *capture-pane*) echo "❯ " ;; esac'
  load_script
  run clear_context
  [ "$status" -eq 0 ]
  grep -q -- "send-keys -t PR FEEDBACK PROCESSOR /clear" "$CALLS_LOG"
  grep -q -- "capture-pane" "$CALLS_LOG"
}
```

- [x] **Step 2: Run it to verify it fails**

Run: `... -f "clear_context"`
Expected: FAIL — `clear_context: command not found`.

- [x] **Step 3: Implement**

In `pr-pool.sh`, add after `claude_rename`:

```bash
# clear_context resets claude's context for the next work item, then waits for
# the prompt to return so the session is ready to be reused.
clear_context() {
  submit_line "$ROLE_NAME" "/clear" || return 1
  wait_ready "$ROLE_NAME"
}
```

- [x] **Step 4: Run it to verify it passes**

Run: `... -f "clear_context"` → PASS. Then the full suite.

- [x] **Step 5: Commit**

```bash
git add packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh \
        packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
git commit -m "feat(pr-pool): clear_context resets claude between work items"
```

---

## Task B5: `teardown`

End-of-run cleanup: graceful exit, then guaranteed `kill-session`. No-op when no role session exists.

**Files:**

- Modify: `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh`
- Test: `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`

- [x] **Step 1: Write the failing tests**

Append to `pr-pool.bats`:

```bash
@test "teardown: sends exit then kills the role session" {
  export PR_POOL_SEND_SETTLE=0
  : > "$TEST_DIR/sess"   # session exists
  make_stub tmux '
case "$*" in
  *new-session*)  : > "$TEST_DIR/sess" ;;
  *has-session*)  [ -f "$TEST_DIR/sess" ] && exit 0 || exit 1 ;;
  *kill-session*) rm -f "$TEST_DIR/sess" ;;
  *capture-pane*) echo "❯ " ;;
esac'
  load_script
  run teardown
  [ "$status" -eq 0 ]
  grep -q -- "send-keys -t PR FEEDBACK PROCESSOR /exit" "$CALLS_LOG"
  grep -q -- "kill-session -t PR FEEDBACK PROCESSOR" "$CALLS_LOG"
}

@test "teardown: no-op when no role session exists" {
  make_stub tmux '
case "$*" in
  *has-session*) exit 1 ;;
esac'
  load_script
  run teardown
  [ "$status" -eq 0 ]
  ! grep -q -- "kill-session" "$CALLS_LOG"
}
```

- [x] **Step 2: Run them to verify they fail**

Run: `... -f "teardown"`
Expected: FAIL — `teardown: command not found`.

- [x] **Step 3: Implement**

In `pr-pool.sh`, add a config var after the `ROLE_NAME` line:

```bash
EXIT_CMD="${PR_POOL_EXIT_CMD:-/exit}" # graceful claude exit; kill-session is the guaranteed fallback
```

Add `teardown` after `clear_context`:

```bash
# teardown gracefully exits claude, then closes the session. kill-session is the
# guaranteed teardown even if the graceful exit doesn't land. No-op if absent.
teardown() {
  tmux -L "$SOCKET" has-session -t "$ROLE_NAME" 2>/dev/null || return 0
  submit_line "$ROLE_NAME" "$EXIT_CMD" || true
  tmux -L "$SOCKET" kill-session -t "$ROLE_NAME" >/dev/null 2>&1 || true
}
```

- [x] **Step 4: Run them to verify they pass**

Run: `... -f "teardown"` → PASS (both). Then the full suite.

- [x] **Step 5: Commit**

```bash
git add packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh \
        packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
git commit -m "feat(pr-pool): teardown sends exit + guaranteed kill-session"
```

---

## Task B6: Rewire `work_one` + `drain_once`; remove `dispatch`/`session_name`

Switch the orchestration onto the new lifecycle and delete the obsolete per-cycle functions and their test.

**Files:**

- Modify: `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh` (`work_one`, `drain_once`; remove `session_name` + `dispatch`)
- Test: `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats` (delete the `dispatch` test; update the two `drain_once` tests)

- [x] **Step 1: Update the tests first (they should fail against current code)**

In `pr-pool.bats`:

(a) **Delete** the entire `@test "dispatch: starts a detached tmux session running claude with the right flags"` block — `dispatch` is being removed.

(b) **Replace** the `@test "drain_once: dispatches, nudges and waits for one discovered cycle"` block with:

```bash
@test "drain_once: works one cycle in the role session, then tears down" {
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
case "$1 $2" in
  "list --type=task") echo "[{\"id\":\"zr-c\",\"title\":\"process-feedback: o/r#1\",\"status\":\"open\",\"issue_type\":\"task\"}]" ;;
  "show zr-c")  echo "{\"id\":\"zr-c\",\"parent\":\"zr-p\",\"status\":\"closed\"}" ;;
  "show zr-p")  echo "{\"id\":\"zr-p\",\"metadata\":{\"author\":\"me\",\"pr_number\":7}}" ;;
  *) echo "{}" ;;
esac'
  load_script
  run drain_once
  [ "$status" -eq 0 ]
  grep -q -- "new-session -d -s PR FEEDBACK PROCESSOR" "$CALLS_LOG"
  grep -q -- "send-keys -t PR FEEDBACK PROCESSOR /rename" "$CALLS_LOG"
  grep -q -- "send-keys -t PR FEEDBACK PROCESSOR" "$CALLS_LOG"   # the nudge
  grep -q -- "/abs/SKILL.md" "$CALLS_LOG"
  grep -q -- "kill-session -t PR FEEDBACK PROCESSOR" "$CALLS_LOG"
}
```

(c) **Replace** the `@test "drain_once: stops after MAX cycles per pass"` block with (reuse means only ONE `new-session`, so cap is asserted by counting nudges, not sessions):

```bash
@test "drain_once: stops after MAX cycles per pass" {
  export SELF_LOGIN="me" PR_POOL_SKILL_MD="/abs/SKILL.md"
  export PR_POOL_MAX=1 PR_POOL_MAX_WAIT=2 PR_POOL_POLL_INTERVAL=1 PR_POOL_SEND_SETTLE=0
  make_stub uuidgen 'echo uuid'
  make_stub tmux '
case "$*" in
  *new-session*)  : > "$TEST_DIR/sess" ;;
  *has-session*)  [ -f "$TEST_DIR/sess" ] && exit 0 || exit 1 ;;
  *kill-session*) rm -f "$TEST_DIR/sess" ;;
  *capture-pane*) echo "❯ " ;;
esac'
  make_stub bd '
case "$1 $2" in
  "list --type=task") echo "[{\"id\":\"zr-a\",\"title\":\"process-feedback: o/r#1\",\"status\":\"open\",\"issue_type\":\"task\"},{\"id\":\"zr-b\",\"title\":\"process-feedback: o/r#2\",\"status\":\"open\",\"issue_type\":\"task\"}]" ;;
  "show zr-a")  echo "{\"id\":\"zr-a\",\"parent\":\"zr-pa\",\"status\":\"closed\"}" ;;
  "show zr-b")  echo "{\"id\":\"zr-b\",\"parent\":\"zr-pb\",\"status\":\"closed\"}" ;;
  "show zr-pa") echo "{\"id\":\"zr-pa\",\"metadata\":{\"author\":\"me\",\"pr_number\":1}}" ;;
  "show zr-pb") echo "{\"id\":\"zr-pb\",\"metadata\":{\"author\":\"me\",\"pr_number\":2}}" ;;
  *) echo "{}" ;;
esac'
  load_script
  run drain_once
  [ "$status" -eq 0 ]
  # MAX=1 -> the nudge (which carries SKILL_MD) is sent exactly once.
  [ "$(grep -c -- '/abs/SKILL.md' "$CALLS_LOG")" -eq 1 ]
}
```

- [x] **Step 2: Run the suite to verify the updated tests fail**

Run: `... -f "drain_once"`
Expected: FAIL — `drain_once` still calls `dispatch` (per-cycle `pf-` session), so the role-session and `kill-session` assertions don't match yet.

- [x] **Step 3: Rewire `work_one` and `drain_once`; remove `dispatch`/`session_name`**

In `pr-pool.sh`:

(a) **Delete** the `session_name` one-liner and the entire `dispatch` function.

(b) **Replace** the whole `work_one` function with:

```bash
# work_one drives one cycle to completion in the (reused) role session: ensure
# the session, name the conversation, nudge, wait for close, then /clear for the
# next item. Returns wait_done's exit code on success; 1 on any earlier failure.
# wait_done handles its own unclaim on failure; clear_context always runs so the
# session is left ready (and reusable).
work_one() {
  local cid="$1" rc
  ensure_session || return 1
  claude_rename "$(cycle_label "$cid")"
  if ! send_nudge "$ROLE_NAME" "$cid"; then
    unclaim "$cid"
    clear_context
    return 1
  fi
  wait_done "$cid" "$ROLE_NAME"
  rc=$?
  clear_context
  return "$rc"
}
```

(c) **Replace** the whole `drain_once` function with (it adds `teardown` after the loop):

```bash
# drain_once works up to MAX discoverable cycles per pass (serially) in one
# reused role session, then tears the session down. Returns 0 whether gated
# (paused) or after attempting up to MAX cycles.
drain_once() {
  gated && return 0
  local cid worked=0
  while read -r cid; do
    [ -z "$cid" ] && continue
    if [ "$worked" -ge "$MAX" ]; then
      log "pr-pool: reached MAX=$MAX cycle(s) this pass; stopping"
      break
    fi
    log "pr-pool: working cycle $cid"
    work_one "$cid" || log "pr-pool: cycle $cid did not complete (flagged)"
    worked=$((worked + 1))
  done < <(discover_cycles)
  teardown
  log "pr-pool: drain pass complete ($worked cycle(s) attempted)"
  return 0
}
```

- [x] **Step 4: Run the full suite to verify it passes**

Run: `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
Expected: PASS — all tests green, fast, no hang. (The `main: optional sentinel pauses` test still passes: `gated` returns before any session work, so no `new-session`.)

- [x] **Step 5: Commit**

```bash
git add packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh \
        packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
git commit -m "feat(pr-pool): role-session lifecycle (ensure/rename/clear/teardown); drop per-cycle dispatch"
```

---

## Task B7: Confirm the flake check still passes

**Files:** none (verification only).

- [x] **Step 1: Build the bats flake check**

Run: `nix build --no-link '.#checks.aarch64-darwin.test-pgii-pack-pr-support-bats'`
Expected: builds green (the whole suite runs in the sandbox).

- [x] **Step 2: Confirm the pack still builds**

Run: `nix build --no-link '.#checks.aarch64-darwin.check-pgii-pack-pr-support-layout' '.#pgii-pack-pr-support'`
Expected: both build. (No commit — nothing changed.)

---

## Task C1: Live smoke test (real bd/tmux/claude, MAX=1)

Validate the lifecycle and dedup on the live `zr` store, exactly as step 1's Task 9 did. **Mutates real bead state** — run from the monorepo with the `.envrc` taint scrubbed.

**Files:** none (manual verification; any fix gets a failing bats test first, per TDD).

- [x] **Step 1: Resolve the SKILL.md path and pick a target cycle**

```bash
SKILL=$(realpath ~/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-process-feedback/SKILL.md)
cd /Volumes/ziprecruiter/monorepo
env -u BEADS_DIR -u WORKSPACE_ROOT bash -c '
  pg-pr config show --json | jq -r "\"self_login: \(.self_login)\""
  bd list --type=task --status=open --json --limit 0 | jq -r "[.[]|select(.title|startswith(\"process-feedback:\"))][] | \"\(.id)  \(.title)\""
'
```

For dedup specifically, prefer a PR that **already has an open work bead** (so you can confirm the processor links instead of duplicating). To check a candidate cycle `<cid>`: `bd show <cid> --json | jq -r '.parent'` → `<PR>`, then `bd children <PR> --status=open` and look for an existing `task`/`bug`.

- [x] **Step 2: Run the full orchestrator against one cycle, watching the session**

```bash
cd /Volumes/ziprecruiter/monorepo
env -u BEADS_DIR -u WORKSPACE_ROOT \
  PR_POOL_MAX=1 \
  PR_POOL_SKILL_MD="$SKILL" \
  PR_POOL_READY_TIMEOUT=180 PR_POOL_MAX_WAIT=1200 PR_POOL_POLL_INTERVAL=15 \
  bash ~/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh
# Watch in another shell: tmux -L pgpool capture-pane -p -t "PR FEEDBACK PROCESSOR"
```

Expected: the session is named `PR FEEDBACK PROCESSOR`; the conversation is `/rename`d (`process-feedback <cid> PR #<n>`); the nudge submits; the worker creates/links a work bead as a **child of the PR bead**; the cycle closes; then the session is **torn down** (`exit` + `kill-session`) and the run exits.

- [x] **Step 3: Verify outcome in beads + no orphan session**

```bash
cd /Volumes/ziprecruiter/monorepo
env -u BEADS_DIR -u WORKSPACE_ROOT bash -c '
  bd show <cid> --json | jq "{status,assignee}"            # -> closed, pgii-pool__process-feedback
  bd children <PR> --json | jq -r ".[]|\"\(.id) [\(.status)] \(.issue_type) \(.title[0:60])\""  # work beads are PR children
'
tmux -L pgpool ls 2>&1   # expect: no server / no "PR FEEDBACK PROCESSOR" session
```

Confirm: cycle `closed`; the work bead is a **child of the PR bead**; if the PR already had an open work bead, the processor **linked/updated** it (no duplicate); no leftover `pgpool` session.

- [x] **Step 4: Commit any fixes surfaced by the smoke test** (each with its own failing bats test first, per TDD — e.g. if the `EXIT_CMD`/`/exit` incantation doesn't gracefully quit claude, adjust it; `kill-session` should still have closed the session).

---

## Out of scope (deferred, per spec)

Session pool (N>1); idle-timeout / self-terminating watchdog; daemonization; work triaging (`bd ready` over multiple work types); the worker agent (performing work beads); pg-pr producer correctness (reliable cycle reuse, PR-close cascade); the dispatched-env `node` hook error; epic-gluing of session names. All arrive with later chunks / the Go rewrite.
