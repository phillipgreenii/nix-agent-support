# pr-pool worker contract Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the pr-pool worker's label-based terminal contract (`commit→swap worker-ready→needs-push→human pushes→never close`) with a status-based one: the worker ends by **closing** (resolved, incl. already-done) or **unclaiming to `open`** (hand-back), the orchestrator's `done_signal worker` succeeds when the bead **leaves `in_progress`**, and a single **`human`** label (replacing `worker-stuck`) is the needs-intervention surface.

**Architecture:** `pr-pool.sh` stays git-free; only its completion/discovery logic changes. `done_signal` takes the bead's **status** (computed once per poll by `wait_done`) plus a `seen_claimed` flag so a pre-claim `open` isn't mistaken for a hand-back. Discovery excludes `human`. The worker SKILL is rewritten as a rules-based contract. All git rules live in the SKILL.

**Tech Stack:** Bash (`/usr/bin/env bash`, must stay 3.2-compatible), `bats` (via `nix shell nixpkgs#bats`), `jq`, `bd` CLI, `tmux`, `git`. Spec: `docs/superpowers/specs/2026-06-11-pr-pool-worker-contract-design.md`.

---

## File Structure

- **Modify:** `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh` — rename `cycle_status`→`bead_status`; delete `bead_labels`/`bead_has_label`; rename `mark_stuck`→`mark_human` (label `human`); rewrite `done_signal` (status-arg + `seen_claimed`), `wait_done` (single status read/poll + claim tracking), `wait_done_fail` (worker→`mark_human`); add `--exclude-label human` to `discover_worker`; rewrite `nudge_text_worker`.
- **Modify:** `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats` — rewrite the worker `wait_done`/`nudge_text_worker`/`discover` tests; update two `drain_once` worker stubs; add hand-back + exclude-`human` tests.
- **Modify:** `packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-work-bead/SKILL.md` — full rewrite to the new rules-based contract.

### Conventions (apply throughout)

- **Work in the isolated worktree** already set up at `.claude/worktrees/pr-pool-worker-contract/` (branch `worktree-pr-pool-worker-contract`, based on `origin/main`). Run all commands from there. Do NOT touch the main checkout — another agent is active there.
- Follow the repo's `bash-scripting` skill conventions; bash must stay **3.2-compatible** (no associative arrays, `${var,,}`, etc.).
- Fast test loop (one file): `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
- **Don't trust `cmd | tail` exit codes** (the pipe masks them) — capture `${PIPESTATUS[0]}` or run unmasked.
- Full gate before declaring done: `nix flake check` (the `test-pgii-pack-pr-support-bats` check + the stricter `treefmt-check` — run `nix fmt -- <files>` to satisfy it).
- Path-scope every commit to the files the task names (`git commit -m "…" -- <paths>`).

---

## Task 1: Status-based worker terminal contract

Rewrite the worker completion/failure logic: `done_signal` keys on status (closed = done; open-after-claim = hand-back), `wait_done` tracks the claim transition, failure stamps `human` instead of `worker-stuck`. Feedback-processor behavior is unchanged.

**Files:**

- Modify: `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh`
- Test: `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`

- [ ] **Step 1: Rewrite the worker `wait_done` tests and update the two `drain_once` worker stubs**

Replace the existing test `@test "wait_done worker: succeeds when the bead gains the needs-push label" {…}` (currently at ~line 173) with these **two** tests:

```bash
@test "wait_done worker: succeeds when the bead is closed" {
  export PR_POOL_MAX_WAIT=2 PR_POOL_POLL_INTERVAL=1
  make_stub bd 'echo "{\"id\":\"zr-w1\",\"status\":\"closed\"}"'
  make_stub tmux 'echo "pane alive"'
  load_script
  run wait_done worker zr-w1 WORKER
  [ "$status" -eq 0 ]
  ! grep -q -- "update zr-w1 --status=open" "$CALLS_LOG"   # orchestrator never unclaims a worker
  ! grep -q -- "add-label human" "$CALLS_LOG"
}

@test "wait_done worker: succeeds on hand-back to open after being seen in_progress" {
  export PR_POOL_MAX_WAIT=5 PR_POOL_POLL_INTERVAL=1
  # in_progress on the first read (claimed), open thereafter (handed back).
  make_stub bd '
n="$(cat "$TEST_DIR/bd_n" 2>/dev/null || echo 0)"
echo $((n + 1)) > "$TEST_DIR/bd_n"
case "$1" in
  show) if [ "$n" -ge 1 ]; then echo "{\"id\":\"zr-w1\",\"status\":\"open\"}"; else echo "{\"id\":\"zr-w1\",\"status\":\"in_progress\"}"; fi ;;
  *) echo "{}" ;;
esac'
  make_stub tmux 'echo "pane alive"'
  load_script
  run wait_done worker zr-w1 WORKER
  [ "$status" -eq 0 ]
  ! grep -q -- "add-label human" "$CALLS_LOG"
}
```

Replace the existing test `@test "wait_done worker: on timeout, stamps worker-stuck and does NOT unclaim" {…}` with:

```bash
@test "wait_done worker: on timeout, adds the human label and does NOT unclaim" {
  export PR_POOL_MAX_WAIT=1 PR_POOL_POLL_INTERVAL=1
  make_stub bd 'echo "{\"id\":\"zr-w1\",\"status\":\"in_progress\"}"'
  make_stub tmux 'echo "pane alive"'
  load_script
  run wait_done worker zr-w1 WORKER
  [ "$status" -ne 0 ]
  grep -q -- "update zr-w1 --add-label human" "$CALLS_LOG"
  ! grep -q -- "update zr-w1 --status=open" "$CALLS_LOG"
}
```

Replace the existing test `@test "wait_done worker: pane dies just as needs-push lands -> success, no worker-stuck" {…}` with:

```bash
@test "wait_done worker: pane dies just as the bead closes -> success, no human" {
  export PR_POOL_MAX_WAIT=5 PR_POOL_POLL_INTERVAL=1
  # First read: still in_progress. Pane then reads dead; the re-check sees closed
  # and must succeed WITHOUT flagging human.
  make_stub bd '
n="$(cat "$TEST_DIR/bd_n" 2>/dev/null || echo 0)"
echo $((n + 1)) > "$TEST_DIR/bd_n"
case "$1" in
  show) if [ "$n" -ge 1 ]; then echo "{\"id\":\"zr-w1\",\"status\":\"closed\"}"; else echo "{\"id\":\"zr-w1\",\"status\":\"in_progress\"}"; fi ;;
  *) echo "{}" ;;
esac'
  make_stub tmux 'exit 1'
  load_script
  run wait_done worker zr-w1 WORKER
  [ "$status" -eq 0 ]
  ! grep -q -- "add-label human" "$CALLS_LOG"
}
```

In the two `drain_once` worker tests, the worker bead's `bd show` stub currently signals completion via the `needs-push` label; switch it to a closed status. In `@test "drain_once: works one worker bead in the WORKER session, then tears down"`, change:

```bash
      zr-w1) echo "{\"id\":\"zr-w1\",\"parent\":\"zr-p\",\"status\":\"in_progress\",\"labels\":[\"needs-push\"]}" ;;
```

to:

```bash
      zr-w1) echo "{\"id\":\"zr-w1\",\"parent\":\"zr-p\",\"status\":\"closed\"}" ;;
```

In `@test "drain_once: works a feedback cycle AND a worker bead in one pass (no starvation)"`, change:

```bash
      zr-w1) echo "{\"id\":\"zr-w1\",\"parent\":\"zr-pw\",\"status\":\"in_progress\",\"labels\":[\"needs-push\"]}" ;;
```

to:

```bash
      zr-w1) echo "{\"id\":\"zr-w1\",\"parent\":\"zr-pw\",\"status\":\"closed\"}" ;;
```

- [ ] **Step 2: Run the suite to confirm the rewritten tests fail**

Run: `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
Expected: the new/rewritten worker `wait_done` tests FAIL (old `done_signal` still keys on the `needs-push` label / `mark_stuck` still stamps `worker-stuck`).

- [ ] **Step 3: Rename `cycle_status`→`bead_status`**

Replace the comment+def (currently ~line 252):

```bash
# cycle_status prints the cycle bead's status.
cycle_status() { bd_obj "$1" | jq -r '.status // ""'; }
```

with:

```bash
# bead_status prints any bead's status (empty on error).
bead_status() { bd_obj "$1" | jq -r '.status // ""'; }
```

(Its only caller, `done_signal`, is rewritten in Step 6 to no longer call it directly — `wait_done` becomes the caller.)

- [ ] **Step 4: Delete the now-dead label helpers**

Remove these four lines (comments + defs, currently ~lines 263-267) entirely:

```bash
# bead_labels prints one label per line for a bead (handles labels==null).
bead_labels() { bd_obj "$1" | jq -r '(.labels // []) | .[]'; }

# bead_has_label returns 0 if the bead carries the exact label.
bead_has_label() { bead_labels "$1" | grep -qxF "$2"; }
```

- [ ] **Step 5: Rename `mark_stuck`→`mark_human`**

Replace the comment+def (currently ~lines 269-271):

```bash
# mark_stuck flags a worker bead the orchestrator could not see to completion so
# it surfaces in `bd list --label worker-stuck`. Best-effort.
mark_stuck() { bd update "$1" --add-label worker-stuck >/dev/null 2>&1 || true; }
```

with:

```bash
# mark_human flags a bead that needs human intervention so it surfaces in
# `bd list --label human` and is excluded from discovery. Best-effort.
mark_human() { bd update "$1" --add-label human >/dev/null 2>&1 || true; }
```

- [ ] **Step 6: Rewrite `done_signal`, `wait_done_fail`, and `wait_done`**

Replace the whole block from `# done_signal …` through the end of `wait_done` (currently ~lines 273-319) with:

```bash
# done_signal returns 0 when the role's completion is reached, given the bead's
# current status and (for the worker) whether it was ever seen in_progress.
#   feedback-processor -> the cycle bead is closed
#   worker             -> the bead has LEFT in_progress: closed (resolved) or, if
#                         it was seen claimed, open (handed back). seen_claimed
#                         guards the pre-claim startup race (a freshly-dispatched
#                         bead is still 'open').
done_signal() {
  local role="$1" status="$2" seen_claimed="${3:-0}"
  case "$role" in
  worker)
    [ "$status" = "closed" ] && return 0
    [ "$seen_claimed" = "1" ] && [ "$status" = "open" ] && return 0
    return 1
    ;;
  *) [ "$status" = "closed" ] ;;
  esac
}

# wait_done_fail performs the role-specific failure action:
#   feedback-processor -> unclaim (so the open pool resurfaces the cycle)
#   worker             -> add the `human` label, NEVER unclaim (a dead worker may
#                         hold a half-built worktree; blind retry is unsafe)
wait_done_fail() {
  case "$1" in
  worker)
    log "wait_done: worker $2 $3; flagging human"
    mark_human "$2"
    ;;
  *)
    log "wait_done: $2 $3; unclaiming"
    unclaim "$2"
    ;;
  esac
}

# wait_done polls until the role's completion signal fires (success) or MAX_WAIT
# elapses / the pane dies (failure). On failure it runs the role's fail action;
# it NEVER auto-closes. It reads the bead status once per poll, tracks whether the
# worker was ever seen in_progress, and re-checks after a pane death so a bead
# completed in the same instant the pane exited is not treated as a failure.
wait_done() {
  local role="$1" id="$2" sess="$3" deadline seen_claimed=0 s
  deadline=$(($(date +%s) + MAX_WAIT))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    s="$(bead_status "$id")"
    done_signal "$role" "$s" "$seen_claimed" && return 0
    [ "$role" = "worker" ] && [ "$s" = "in_progress" ] && seen_claimed=1
    if ! pane_alive "$sess"; then
      s="$(bead_status "$id")"
      done_signal "$role" "$s" "$seen_claimed" && return 0
      wait_done_fail "$role" "$id" "exited before completing"
      return 1
    fi
    sleep "$POLL_INTERVAL"
  done
  s="$(bead_status "$id")"
  done_signal "$role" "$s" "$seen_claimed" && return 0
  wait_done_fail "$role" "$id" "not complete within ${MAX_WAIT}s"
  return 1
}
```

- [ ] **Step 7: Run the suite to verify green**

Run: `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
Expected: ALL tests PASS (worker close/hand-back/timeout/pane-death + every existing feedback and drain test).

- [ ] **Step 8: Commit**

```bash
git add packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
git commit -m "feat(pr-pool): status-based worker terminal signal + human label" -- packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
```

---

## Task 2: Discovery excludes the `human` label

A bead flagged `human` must not be re-dispatched. Add the native `--exclude-label human` to the worker discovery query.

**Files:**

- Modify: `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh`
- Test: `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`

- [ ] **Step 1: Update the discover worker test + add an exclusion test**

In `@test "discover: worker route tags worker-ready beads (native --label)"`, change the final assertion:

```bash
  grep -q -- "ready --label worker-ready" "$CALLS_LOG"
```

to:

```bash
  grep -q -- "ready --label worker-ready --exclude-label human" "$CALLS_LOG"
```

Append a new test right after it:

```bash
@test "discover: worker route excludes human-flagged beads" {
  export SELF_LOGIN="me"
  make_stub bd '
case "$1" in
  ready)
    case "$*" in
      *"--exclude-label human"*) echo "[]" ;;          # human-flagged bead filtered out by bd
      *"--label worker-ready"*)  echo "[{\"id\":\"zr-w1\",\"title\":\"Fix X\",\"status\":\"open\",\"issue_type\":\"bug\"}]" ;;
      *) echo "[]" ;;
    esac ;;
  *) echo "{}" ;;
esac'
  load_script
  run discover
  [ "$status" -eq 0 ]
  [[ "$output" != *"worker	zr-w1"* ]]
  grep -q -- "ready --label worker-ready --exclude-label human" "$CALLS_LOG"
}
```

- [ ] **Step 2: Run the suite to confirm the discovery tests fail**

Run: `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
Expected: both `discover` worker tests FAIL (`discover_worker` does not yet pass `--exclude-label human`).

- [ ] **Step 3: Add `--exclude-label human` to `discover_worker`**

Replace the `bd ready` line in `discover_worker` (currently ~line 96):

```bash
  bd ready --label worker-ready --json --limit 0 2>/dev/null |
```

with:

```bash
  bd ready --label worker-ready --exclude-label human --json --limit 0 2>/dev/null |
```

Also update the function's comment (currently ~lines 92-94) to mention the exclusion:

```bash
# discover_worker prints "worker<TAB><bead-id>" for each worker-ready bead that is
# not flagged `human`, using bd ready's native label filters (the .labels field is
# null when unset, so a jq label check is avoided here).
```

- [ ] **Step 4: Run the suite to verify green**

Run: `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
Expected: ALL tests PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
git commit -m "feat(pr-pool): exclude human-flagged beads from worker discovery" -- packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
```

---

## Task 3: Rewrite `nudge_text_worker`

Replace the commit-no-push / swap-to-needs-push instruction with the new rules-based contract (claim → resolve bead-first → assert `phillipg.` branch + mine → clean worktree → implement → commit → push only if instructed → record then close-or-handback; hard-block → `human`).

**Files:**

- Modify: `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh`
- Test: `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`

- [ ] **Step 1: Rewrite the `nudge_text_worker` test**

Replace `@test "nudge_text_worker: worktree, commit-no-push, record-then-swap, abort-to-stuck" {…}` with:

```bash
@test "nudge_text_worker: phillipg-only, push-opt-in, close-or-handback, human-on-block" {
  export PR_POOL_WORKER_SKILL_MD="/abs/worker.md" PR_POOL_WORKTREE_DIR="/tmp/test-worktrees"
  load_script
  run nudge_text_worker zr-w1
  [ "$status" -eq 0 ]
  [[ "$output" == *"/abs/worker.md"* ]]
  [[ "$output" == *"zr-w1"* ]]
  [[ "$output" == *"worktree"* ]]
  [[ "$output" == *"phillipg."* ]]
  [[ "$output" == *"human"* ]]
  [[ "$output" == *"close"* ]]
  [[ "$output" == *"--force"* ]]
  [[ "$output" == *"/tmp/test-worktrees"* ]]
  [[ "$output" != *"needs-push"* ]]
}
```

- [ ] **Step 2: Run the suite to confirm the test fails**

Run: `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
Expected: the rewritten `nudge_text_worker` test FAILS (old text still says `needs-push` / `do NOT push`).

- [ ] **Step 3: Rewrite `nudge_text_worker`**

Replace the comment+function (currently ~lines 220-227) with:

```bash
# nudge_text_worker builds the worker's instruction line. The worker does all git
# work itself (pr-pool stays git-free): resolve PR+branch bead-first, assert the
# branch is phillipg.-prefixed and mine, work in a clean isolated worktree, commit,
# push only when the bead instructs, then record + close (or hand back). It never
# leaves the bead in_progress; on a hard block it adds the `human` label.
nudge_text_worker() {
  local id="$1"
  printf '%s' "Read $WORKER_SKILL_MD and implement work bead $id. Claim it (bd update $id --claim). Resolve its PR + head branch bead-first from the parent merge-request bead's metadata (repo, pr_number, branch — no gh needed); assert metadata.author is me AND the branch starts with 'phillipg.'. If you cannot resolve the PR, it is not mine, or the branch is not phillipg.-prefixed, make NO changes, comment why, and add the human label (bd update $id --add-label human). Otherwise work in a clean isolated git worktree for that branch under $WORKTREE_DIR (never start or leave it dirty), implement the change the bead describes, and commit it. Push ONLY if the bead's instructions say to (git push or git push --force-with-lease; NEVER git push --force). Record what you did with bd comment FIRST, then end by EITHER closing the bead (bd close $id — including when the work is already present at HEAD) OR, if handing it back, unclaiming it (bd update $id --status=open --assignee=\"\"). NEVER leave the bead in_progress; do not push by default."
}
```

- [ ] **Step 4: Run the suite to verify green**

Run: `nix shell nixpkgs#bats --command bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
Expected: ALL tests PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
git commit -m "feat(pr-pool): rewrite worker nudge to the status-based contract" -- packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
```

---

## Task 4: Worker SKILL rewrite

Rewrite the worker SKILL as the authoritative rules-based contract. No bats coverage for SKILL prose (the `nudge_text_worker` test covers the orchestrator side).

**Files:**

- Modify: `packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-work-bead/SKILL.md`

- [ ] **Step 1: Replace the SKILL with the new contract**

Overwrite `packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-work-bead/SKILL.md` with:

````markdown
---
name: pg-pr-work-bead
description: Implement a single worker-ready PR work bead in an isolated git worktree — claim, resolve the PR/branch bead-first, work only on phillipg.-prefixed branches, commit, push only if the bead instructs, then close it (or hand it back). Never leave the bead in_progress or the working tree dirty. Use when dispatched to work a bead labeled worker-ready.
---

# pg-pr work bead

You are the **worker**. You take ONE `worker-ready` work bead, make the code
change it describes in an isolated git worktree, and finish by **closing the
bead** (or cleanly handing it back). A human does not gate your work — but you
operate under strict rules.

## Roles (do only your part)

- **pg-pr (producer):** creates PR / cycle / feedback beads. Not you.
- **feedback-processor (someone else):** turns feedback into work beads. Not you.
- **You — the worker:** implement one `worker-ready` work bead end to end.

## Rules (non-negotiable)

1. You **may create** beads — but never label them `worker-ready` and never title
   them `process-feedback:` (so they don't auto-dispatch mid-run).
2. You **may update** the status / labels / metadata of the bead you own.
3. You **must claim** the bead when you start.
4. You **must end** by EITHER **closing** the bead (resolved) OR **unclaiming +
   returning it to `open`** (hand-back). **Never leave it `in_progress`.**
5. You **must never leave a dirty working directory.**
6. You **should not start** work in a dirty working directory.
7. You **may commit.**
8. You **must only work on branches starting with `phillipg.`**.
9. You **do not push by default.** Push **only when the bead's instructions tell
   you to**. When instructed you may `git push` or `git push --force-with-lease`.
10. You **may rebase** your branch if it helps keep it close to base.
11. You **must never merge unless fast-forward**; if ff isn't possible, rebase
    then ff-merge. (You normally just update the head branch — you rarely merge.)
12. You **may `git push` or `git push --force-with-lease`; NEVER `git push
--force`.** After rebasing an already-pushed branch, re-push with
    `--force-with-lease`.
13. If you **need a human** to intervene, add the `human` label and say why.

## Inputs

- A work-bead id (`task`/`bug`), a child of a PR (merge-request) bead, labeled
  `worker-ready`.
- The PR bead's `metadata` carries everything you need: `repo`, `pr_number`,
  `branch`, `base`, `author`, `state`. **Resolve bead-first; do not call `gh`
  unless the bead lacks a field.**

## Workflow

1. **Claim** the work bead:

   ```bash
   bd update <id> --claim
   ```

2. **Resolve the PR + head branch, bead-first:**

   ```bash
   bd show <id> --json | jq -r '.parent'                 # -> PR bead id
   bd show <PR-id> --json | jq -r '.metadata | {repo, pr_number, branch, base, author, state}'
   ```

3. **Needs-human guard — do this BEFORE editing anything** (you have already
   claimed). Add the `human` label and stop if any hold:
   - no parent PR bead, missing `branch`, or PR `state` is not `open`; **or**
   - `metadata.author` is not you (`pg-pr config show --json | jq -r '.self_login'`); **or**
   - the head `branch` does **not** start with `phillipg.`.

   ```bash
   bd comment <id> "Cannot proceed: <reason>. Needs a human."
   bd update <id> --add-label human
   ```

   Make no code changes. (The bead is now excluded from re-dispatch and surfaces
   in `bd list --label human`.)

4. **Create or reuse a CLEAN isolated git worktree** for the head branch.
   (`$WORKSPACE_ROOT` is the monorepo root, pinned into your session.)

   ```bash
   WT="${PR_POOL_WORKTREE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/pr-pool/worktrees}/<repo-slug>-pr<pr_number>"
   git -C "$WORKSPACE_ROOT" fetch origin "<branch>"
   if [ -d "$WT" ]; then
     # Reuse. If it is dirty, it is debris from a prior crashed run of THIS bead —
     # recover it (commit/reset what you recognize). If you cannot attribute the
     # dirt, treat it as needs-human (step 3) rather than blindly discarding work.
     git -C "$WT" status --porcelain
     git -C "$WT" checkout "<branch>" && git -C "$WT" pull --ff-only
   else
     git -C "$WORKSPACE_ROOT" worktree add "$WT" "<branch>"
   fi
   cd "$WT"
   ```

5. **Implement** the change the bead describes, scoped to only the files it
   implies. Run a cheap local check/build if feasible.

6. **Commit (scoped, never `-A`).** Conventional message referencing the bead + PR:

   ```bash
   git add -- <files the bead implies>
   git commit -m "<type>(<scope>): <change> (bead <id>, PR #<pr_number>)"
   ```

7. **Push ONLY if the bead's instructions say to.** Default is no push.

   ```bash
   git push                          # or: git push --force-with-lease   (NEVER --force)
   ```

   If a push is rejected (remote moved), `git fetch`, rebase, and retry once with
   `--force-with-lease`; if it still fails, hand back (step 8b) or needs-human.

8. **Finish — leave the working tree clean and the bead out of `in_progress`.
   Record FIRST, then transition LAST** (the orchestrator resets the session the
   instant the status leaves `in_progress`):
   - **8a. Resolved (incl. already-done):** if the work is complete — or you
     verified it is already present at branch HEAD — record and close:

     ```bash
     SHA="$(git -C "$WT" rev-parse HEAD)"
     bd comment <id> "<what changed> at $SHA on <branch>. <pushed? / already present at HEAD>."
     bd close <id>
     ```

   - **8b. Hand-back (e.g. low on context):** commit what you have, leave a note,
     then unclaim back to `open` so the next worker continues:

     ```bash
     bd comment <id> "Handing back: <what's done, what's left>."
     bd update <id> --status=open --assignee=""
     ```

## Boundaries

- One bead per run. Touch only files the bead implies.
- **Never `git push --force`.** Push at all only when the bead instructs.
- Do not comment on the PR (GitHub) — you write to the **bead**.
- Never leave the bead `in_progress`; never leave the working tree dirty.
````

- [ ] **Step 2: Verify formatting + structure**

Run: `nix fmt -- packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-work-bead/SKILL.md`
Then `prek run --all-files 2>&1 | tail -20` (or `pre-commit run --all-files`).
Expected: PASS (treefmt may reformat the SKILL — re-stage if so).

- [ ] **Step 3: Commit**

```bash
git add packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-work-bead/SKILL.md
git commit -m "feat(pg-pr-plugin): rewrite worker SKILL to status-based contract" -- packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-work-bead/SKILL.md
```

---

## Final verification (after all tasks)

- [ ] **Full gate:** `nix flake check` passes (incl. `test-pgii-pack-pr-support-bats` and `treefmt-check`); `prek run --all-files` passes. If `treefmt-check` fails, run `nix fmt -- <files>` and re-commit.
- [ ] **Grep sweep:** no stale references remain — `grep -rn "needs-push\|worker-stuck\|mark_stuck\|bead_has_label\|cycle_status" packages/pgii-pack-pr-support packages/pg-pr-plugin` returns nothing (the spec/handoff docs may still mention them historically; code/tests must not).
- [ ] **Live smoke (DEFERRED — P1 #2; confirm the target bead with the user first; blocked on the shared Dolt server being up on `127.0.0.1:25252`).** Exercises the real **commit → push a `phillipg.` branch (when the bead instructs) → close** happy path, the already-done close, and a forced needs-human (e.g. a closed PR or a non-`phillipg.` branch → `bd list --label human`). It mutates real `zr` beads and spawns `claude`.

---

## Self-Review

**1. Spec coverage** (spec `2026-06-11-pr-pool-worker-contract-design.md`):

- Status-based `done_signal worker` (not `in_progress`; closed or open-after-claim) → Task 1. ✓
- `human` replaces `worker-stuck`; orchestrator stamps it on failure, never unclaims → Task 1 (`mark_human`, `wait_done_fail`). ✓
- `needs-push` removed (label helpers deleted, nudge rewritten) → Task 1 + Task 3. ✓
- Discovery excludes `human` → Task 2. ✓
- Worker rules 1-13 (create beads, status authority, claim, close-or-handback, clean tree, `phillipg.`-only, push opt-in, rebase/ff guardrails, never `--force`, needs-human) → Task 4 SKILL + Task 3 nudge. ✓
- Startup-race guard (`seen_claimed`) for the open=hand-back signal → Task 1 `wait_done`. ✓ (refinement beyond the spec's prose; noted in spec assumptions.)
- Feedback-processor path unchanged → Task 1 keeps the `*)` branches (cycle closed / unclaim). ✓
- Test surface: worker `wait_done` (close/handback/timeout/pane-death), `discover` exclude, `nudge_text_worker`, two `drain_once` stubs → Tasks 1-3. ✓
- Live smoke deferred (blocked on 25252) → Final verification. ✓

**2. Placeholder scan:** No `TBD`/`TODO`/"add error handling"/"similar to Task N"; every code step shows complete function bodies and complete test bodies. ✓

**3. Type/signature consistency across tasks:**

- `bead_status <id>` (Task 1 Step 3) ↔ called in `wait_done` (Task 1 Step 6). ✓
- `done_signal <role> <status> [seen_claimed]` (Task 1 Step 6) ↔ all three call sites in `wait_done` pass `(role, s, seen_claimed)` (Task 1 Step 6). ✓ (No other callers — verified.)
- `mark_human <id>` (Task 1 Step 5) ↔ used only in `wait_done_fail` worker branch (Task 1 Step 6). ✓
- `discover_worker` emits `worker<TAB>id` (Task 2) ↔ `drain_once`'s `IFS=tab` read (unchanged). ✓
- `nudge_text_worker <id>` (Task 3) ↔ `role_nudge worker` (unchanged) ↔ test `role_nudge worker == nudge_text_worker` (unchanged, still passes). ✓
- `bead_labels`/`bead_has_label` removed (Task 1 Step 4) — no remaining callers (verified: only `done_signal`, which is rewritten not to use them). ✓
