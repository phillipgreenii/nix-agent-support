# pr-pool orchestrator (step 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Also use superpowers:bash-scripting (workspace bash conventions) and superpowers:test-driven-development.

**Goal:** A standalone bash orchestrator (`pr-pool.sh`) that finds my open `process-feedback:` cycles in the `zr` bead db and, per cycle, dispatches an interactive `claude` into a tmux pane, send-keys a nudge to run the `pg-pr-process-feedback` skill, waits for the cycle to close, and loops — replacing the gas-city supervisor's materialize loop.

**Architecture:** One bash script of small, independently-testable functions (`precheck`, `discover_cycles`, `dispatch`, `wait_ready`, `send_nudge`, `wait_done`, `main`). External CLIs (`bd`, `pg-pr`, `tmux`, `claude`, `uuidgen`) are stubbed on `$PATH` in bats tests; the script ends with `main "$@"` which the test loader seds off so functions are callable in isolation. Run from the ziprecruiter monorepo root; `BEADS_DIR`/`WORKSPACE_ROOT` are unset once at the top so `bd`/`pg-pr` (here and in the dispatched pane, which inherits this env) resolve to the monorepo's `zr` `.beads`.

**Tech Stack:** bash, bats (tests), `tmux` (dedicated `-L pgpool` socket), `bd`/`pg-pr` CLIs, `jq`. Packaged into the `pgii-pr-support` nix pack; tested via a flake check mirroring `test-pgii-pack-dolt-hacks-bats`.

**Spec:** `docs/superpowers/specs/2026-06-08-pr-feedback-orchestrator-design.md`. **Deviation from spec:** the spec's per-call `env -u BEADS_DIR -u WORKSPACE_ROOT command bd` wrapper is broken (`env` cannot exec the `command` shell builtin); this plan unsets the two vars once at the top instead — simpler and also cleans the env the tmux pane inherits.

---

## File Structure

- **Create** `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh` — the orchestrator (functions + final `main "$@"`).
- **Create** `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats` — unit tests (stub CLIs on PATH; `load_script` sources with `main` suppressed). Models `packages/pgii-pack-dolt-hacks/pack-src/scripts/tests/hack-daily-summary.bats`.
- **Modify** `flake.nix` — add a `test-pgii-pack-pr-support-bats` check that runs the bats file from the built pack.

Local test command throughout: `bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats` (requires `bats`, `jq`, `bash` on PATH — available in the repo dev shell `nix develop`).

---

## Task 1: Script skeleton + bats harness + first behavior (precheck)

**Files:**

- Create: `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh`
- Create: `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`

- [ ] **Step 1: Write the failing test (harness + precheck cases)**

Create `pr-pool.bats`:

```bash
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
Expected: FAIL — `pr-pool.sh` does not exist yet (`sed: ... No such file`).

- [ ] **Step 3: Write the minimal script (skeleton + precheck)**

Create `pr-pool.sh`:

```bash
#!/usr/bin/env bash
# pr-pool.sh — standalone PR-feedback orchestrator (step 1).
# See docs/superpowers/specs/2026-06-08-pr-feedback-orchestrator-design.md.
set -uo pipefail

REPO_ROOT="${REPO_ROOT:-$PWD}"
SELF_LOGIN="${SELF_LOGIN:-}"
MAX="${PR_POOL_MAX:-1}"
SOCKET="${PR_POOL_SOCKET:-pgpool}"
SKILL_MD="${PR_POOL_SKILL_MD:-}"
READY_TIMEOUT="${PR_POOL_READY_TIMEOUT:-60}"
MAX_WAIT="${PR_POOL_MAX_WAIT:-1800}"
POLL_INTERVAL="${PR_POOL_POLL_INTERVAL:-10}"
READY_PROMPT="${PR_POOL_READY_PROMPT:-❯ }"
ACTOR="${PR_POOL_ACTOR:-pgii-pool__process-feedback}"
QUOTA_PAUSED="${PR_POOL_QUOTA_PAUSED:-}"
CICD_DOWN="${PR_POOL_CICD_DOWN:-}"
LOG_DIR="${PR_POOL_LOG_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/pr-pool}"

# Clean the ambient ~/phillipg_mbp/.envrc taint once, so bd/pg-pr resolve to
# the monorepo's own zr .beads — here AND in the dispatched tmux pane, which
# inherits this environment.
unset BEADS_DIR WORKSPACE_ROOT

log() { printf '[%s] %s\n' "$(date -Iseconds)" "$*" >&2; }

precheck() {
  if [ ! -d "$REPO_ROOT/.beads" ]; then
    log "ERROR: $REPO_ROOT/.beads not found — run from the monorepo root"
    return 1
  fi
  local prefix
  prefix="$(awk -F': *' '/^issue_prefix:/{print $2; exit}' "$REPO_ROOT/.beads/config.yaml" 2>/dev/null)"
  if [ "$prefix" != "zr" ]; then
    log "ERROR: bd prefix is '${prefix:-<none>}', expected 'zr' (wrong workspace?)"
    return 1
  fi
  if ! bd list --limit 1 --json >/dev/null 2>&1; then
    log "ERROR: bd/dolt not reachable (server down? it is gas-city-managed and cannot be auto-started)"
    return 1
  fi
  return 0
}

main() {
  mkdir -p "$LOG_DIR"
  precheck || exit 1
  log "pr-pool: precheck passed (REPO_ROOT=$REPO_ROOT)"
}

main "$@"
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
chmod +x packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh
git add packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh \
        packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
git commit -m "feat(pr-pool): script skeleton + precheck (zr workspace + bd reachable)"
```

---

## Task 2: discover_cycles (my open process-feedback cycles)

**Files:**

- Modify: `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh`
- Test: `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`

- [ ] **Step 1: Write the failing test**

Append to `pr-pool.bats`:

```bash
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bats ... -f discover_cycles`
Expected: FAIL — `discover_cycles: command not found` / no output.

- [ ] **Step 3: Write the implementation**

Add to `pr-pool.sh` (before `main`):

```bash
# resolve_self prints the configured GitHub login (cached in $SELF_LOGIN).
resolve_self() {
  if [ -z "$SELF_LOGIN" ]; then
    SELF_LOGIN="$(pg-pr config show --json 2>/dev/null | jq -r '.self_login // empty')"
  fi
  printf '%s' "$SELF_LOGIN"
}

# bd_obj runs `bd show <id> --json` and normalizes bd's array-or-object shape.
bd_obj() {
  bd show "$1" --json 2>/dev/null | jq 'if type=="array" then .[0] else . end'
}

# discover_cycles prints the IDs of open process-feedback cycles whose parent
# merge-request was authored by me. One id per line.
discover_cycles() {
  local self; self="$(resolve_self)"
  [ -z "$self" ] && { log "ERROR: could not resolve self_login from pg-pr config"; return 1; }
  bd list --type=task --status=open --json --limit 0 2>/dev/null \
    | jq -r 'if type=="array" then . else [] end
             | map(select(.title | startswith("process-feedback:")))
             | .[].id' \
    | while read -r cid; do
        [ -z "$cid" ] && continue
        local pid author
        pid="$(bd_obj "$cid" | jq -r '.parent // empty')"
        [ -z "$pid" ] && continue
        author="$(bd_obj "$pid" | jq -r '.metadata.author // ""')"
        [ "$author" = "$self" ] && printf '%s\n' "$cid"
      done
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `bats ... -f discover_cycles`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh \
        packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats
git commit -m "feat(pr-pool): discover_cycles filters to my PRs by parent author"
```

---

## Task 3: dispatch (spawn claude in a tmux pane)

**Files:** `pr-pool.sh`, `pr-pool.bats`

- [ ] **Step 1: Write the failing test**

```bash
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bats ... -f dispatch`
Expected: FAIL — `dispatch: command not found`.

- [ ] **Step 3: Write the implementation**

```bash
# session_name maps a cycle id to its tmux session name.
session_name() { printf 'pf-%s' "$1"; }

# dispatch starts a detached, interactive claude in a tmux pane for the cycle.
# Prints the session name. BEADS_DIR/WORKSPACE_ROOT were already unset at the
# top, so the pane inherits a clean env and its bd/pg-pr resolve to zr.
dispatch() {
  local cid="$1" sess; sess="$(session_name "$cid")"
  tmux -u -L "$SOCKET" new-session -d -s "$sess" -c "$REPO_ROOT" \
    -e "BEADS_ACTOR=$ACTOR" \
    claude --dangerously-skip-permissions --effort max --session-id "$(uuidgen)" \
    || { log "ERROR: tmux new-session failed for $cid"; return 1; }
  printf '%s\n' "$sess"
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `bats ... -f dispatch`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -u && git commit -m "feat(pr-pool): dispatch claude into a dedicated tmux socket"
```

---

## Task 4: wait_ready (poll for the claude prompt, with timeout)

**Files:** `pr-pool.sh`, `pr-pool.bats`

- [ ] **Step 1: Write the failing test**

```bash
@test "wait_ready: returns 0 once the ready prompt appears" {
  make_stub tmux 'echo "welcome to claude"; echo "❯ "'
  load_script
  PR_POOL_READY_TIMEOUT=2 run wait_ready pf-zr-mine
  [ "$status" -eq 0 ]
}

@test "wait_ready: times out (nonzero) when the prompt never appears" {
  make_stub tmux 'echo "still booting"'
  load_script
  PR_POOL_READY_TIMEOUT=1 PR_POOL_POLL_INTERVAL=1 run wait_ready pf-zr-mine
  [ "$status" -ne 0 ]
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bats ... -f wait_ready`
Expected: FAIL — `wait_ready: command not found`.

- [ ] **Step 3: Write the implementation**

```bash
# wait_ready polls the pane until the ready prompt appears, bounded by
# READY_TIMEOUT seconds. Returns nonzero on timeout so the caller can flag.
wait_ready() {
  local sess="$1" deadline; deadline=$(( $(date +%s) + READY_TIMEOUT ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if tmux -L "$SOCKET" capture-pane -p -t "$sess" 2>/dev/null | grep -qF "$READY_PROMPT"; then
      return 0
    fi
    sleep 1
  done
  log "wait_ready: $sess never reached the prompt within ${READY_TIMEOUT}s"
  return 1
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `bats ... -f wait_ready`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -u && git commit -m "feat(pr-pool): wait_ready polls for the claude prompt with a timeout"
```

---

## Task 5: send_nudge (direct the worker)

**Files:** `pr-pool.sh`, `pr-pool.bats`

- [ ] **Step 1: Write the failing test**

```bash
@test "send_nudge: sends a keys line naming the SKILL.md path and the cycle id" {
  export PR_POOL_SKILL_MD="/abs/SKILL.md"
  make_stub tmux 'exit 0'
  load_script
  run send_nudge pf-zr-mine zr-mine
  [ "$status" -eq 0 ]
  grep -q -- "send-keys -t pf-zr-mine" "$CALLS_LOG"
  grep -q -- "/abs/SKILL.md" "$CALLS_LOG"
  grep -q -- "zr-mine" "$CALLS_LOG"
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bats ... -f send_nudge`
Expected: FAIL — `send_nudge: command not found`.

- [ ] **Step 3: Write the implementation**

```bash
# nudge_text builds the instruction sent to the worker. Points at the clean
# SKILL.md only; overrides its "implement the change" step (step 1 creates an
# action bead instead of applying a fix) and forbids picking up new beads.
nudge_text() {
  local cid="$1"
  printf '%s' "Read $SKILL_MD and process the process-feedback cycle $cid: claim it, read its feedback children (bd children $cid), and for each create an action bead (task/bug, discovered-from the feedback) describing any needed code change — do NOT apply fixes and do NOT work the new action beads. Close non-actionable feedback. Then close the cycle with a one-line summary and exit."
}

# send_nudge types the nudge into the pane and presses Enter.
send_nudge() {
  local sess="$1" cid="$2"
  [ -z "$SKILL_MD" ] && { log "ERROR: PR_POOL_SKILL_MD unset (path to pg-pr-process-feedback SKILL.md)"; return 1; }
  tmux -L "$SOCKET" send-keys -t "$sess" "$(nudge_text "$cid")" Enter
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `bats ... -f send_nudge`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -u && git commit -m "feat(pr-pool): send_nudge directs the worker at the SKILL.md"
```

---

## Task 6: wait_done (completion, with flag + unclaim on failure)

**Files:** `pr-pool.sh`, `pr-pool.bats`

- [ ] **Step 1: Write the failing test**

```bash
@test "wait_done: returns 0 when the cycle closes" {
  make_stub bd 'echo "{\"id\":\"zr-mine\",\"status\":\"closed\"}"'
  make_stub tmux 'echo "pane alive"'
  load_script
  PR_POOL_MAX_WAIT=2 PR_POOL_POLL_INTERVAL=1 run wait_done zr-mine pf-zr-mine
  [ "$status" -eq 0 ]
}

@test "wait_done: on timeout, unclaims the cycle and returns nonzero" {
  make_stub bd 'echo "{\"id\":\"zr-mine\",\"status\":\"in_progress\"}"'
  make_stub tmux 'echo "pane alive"'
  load_script
  PR_POOL_MAX_WAIT=1 PR_POOL_POLL_INTERVAL=1 run wait_done zr-mine pf-zr-mine
  [ "$status" -ne 0 ]
  grep -q -- "update zr-mine --status=open --assignee=" "$CALLS_LOG"
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bats ... -f wait_done`
Expected: FAIL — `wait_done: command not found`.

- [ ] **Step 3: Write the implementation**

```bash
# cycle_status prints the cycle bead's status.
cycle_status() { bd_obj "$1" | jq -r '.status // ""'; }

# pane_alive returns 0 while the tmux session still exists.
pane_alive() { tmux -L "$SOCKET" capture-pane -p -t "$1" >/dev/null 2>&1; }

# unclaim returns a claimed-but-unfinished cycle to the open pool so the next
# run can see it (discover/list filters --status=open; a stranded in_progress
# cycle would be invisible otherwise).
unclaim() { bd update "$1" --status=open --assignee="" >/dev/null 2>&1 || true; }

# wait_done polls until the cycle closes (success) or MAX_WAIT elapses / the
# pane dies (failure). On failure it unclaims + flags; it NEVER auto-closes.
wait_done() {
  local cid="$1" sess="$2" deadline; deadline=$(( $(date +%s) + MAX_WAIT ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    case "$(cycle_status "$cid")" in
      closed) return 0 ;;
    esac
    if ! pane_alive "$sess"; then
      log "wait_done: $sess exited before closing $cid; unclaiming"
      unclaim "$cid"; return 1
    fi
    sleep "$POLL_INTERVAL"
  done
  log "wait_done: $cid not closed within ${MAX_WAIT}s; unclaiming + flagging"
  unclaim "$cid"
  return 1
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `bats ... -f wait_done`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -u && git commit -m "feat(pr-pool): wait_done with flag + unclaim on failure (never auto-close)"
```

---

## Task 7: main drain loop (cap, sentinels, drain)

**Files:** `pr-pool.sh`, `pr-pool.bats`

- [ ] **Step 1: Write the failing test**

```bash
@test "main: optional sentinel pauses before any work" {
  export PR_POOL_QUOTA_PAUSED="$TEST_DIR/PAUSED"; : > "$PR_POOL_QUOTA_PAUSED"
  make_stub bd 'exit 0'
  load_script
  run main
  [ "$status" -eq 0 ]
  ! grep -q "new-session" "$CALLS_LOG"  # nothing dispatched
}

@test "drain_once: dispatches, nudges and waits for one discovered cycle" {
  export SELF_LOGIN="me" PR_POOL_SKILL_MD="/abs/SKILL.md"
  make_stub tmux 'echo "❯ "'
  make_stub uuidgen 'echo uuid'
  make_stub bd '
case "$1 $2" in
  "list --type=task") echo "[{\"id\":\"zr-c\",\"title\":\"process-feedback: o/r#1\",\"status\":\"open\",\"issue_type\":\"task\"}]" ;;
  "show zr-c")  echo "{\"id\":\"zr-c\",\"parent\":\"zr-p\"}" ;;
  "show zr-p")  echo "{\"id\":\"zr-p\",\"metadata\":{\"author\":\"me\"}}" ;;
  *) echo "{\"id\":\"zr-c\",\"status\":\"closed\"}" ;;  # closes immediately
esac'
  load_script
  PR_POOL_MAX_WAIT=2 PR_POOL_POLL_INTERVAL=1 run drain_once
  [ "$status" -eq 0 ]
  grep -q -- "new-session -d -s pf-zr-c" "$CALLS_LOG"
  grep -q -- "send-keys -t pf-zr-c" "$CALLS_LOG"
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bats ... -f "sentinel|drain_once"`
Expected: FAIL — `drain_once: command not found`.

- [ ] **Step 3: Write the implementation**

Add `gated` + `drain_once` and rewrite `main`:

```bash
# gated returns 0 (pause) if an optional, read-only sentinel file is present.
# Paths default to empty (disabled). The script never creates/edits/removes them.
gated() {
  [ -n "$QUOTA_PAUSED" ] && [ -f "$QUOTA_PAUSED" ] && { log "QUOTA_PAUSED present; pausing"; return 0; }
  [ -n "$CICD_DOWN" ] && [ -f "$CICD_DOWN" ] && { log "CICD_DOWN present; pausing"; return 0; }
  return 1
}

# work_one dispatches + drives one cycle to completion. Returns wait_done's code.
work_one() {
  local cid="$1" sess
  sess="$(dispatch "$cid")" || return 1
  if ! wait_ready "$sess"; then unclaim "$cid"; return 1; fi
  send_nudge "$sess" "$cid" || { unclaim "$cid"; return 1; }
  wait_done "$cid" "$sess"
}

# drain_once works every currently-discoverable cycle (serially; MAX is the
# reserved concurrency knob, =1 in step 1). Returns 0 when the queue is empty.
drain_once() {
  gated && return 0
  local cid worked=0
  while read -r cid; do
    [ -z "$cid" ] && continue
    log "pr-pool: working cycle $cid"
    work_one "$cid" || log "pr-pool: cycle $cid did not complete (flagged)"
    worked=$((worked+1))
  done < <(discover_cycles)
  log "pr-pool: drain pass complete ($worked cycle(s) attempted)"
  return 0
}

main() {
  mkdir -p "$LOG_DIR"
  precheck || exit 1
  drain_once
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `bats packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
Expected: PASS (all tests).

- [ ] **Step 5: Commit**

```bash
git add -u && git commit -m "feat(pr-pool): main drain loop with read-only sentinel gates"
```

> **MAX/concurrency note:** step 1 works cycles serially (cap effectively 1). `MAX` is reserved as the knob a future daemon raises; implementing >1 concurrency (tracking N live panes) is out of scope here and is a later task.

---

## Task 8: Wire the bats suite into the flake check

**Files:** `flake.nix`

- [ ] **Step 1: Add the check (after `test-pgii-pack-dolt-hacks-bats`)**

```nix
            test-pgii-pack-pr-support-bats =
              pkgs.runCommand "test-pgii-pack-pr-support-bats"
                {
                  nativeBuildInputs = [
                    pkgs.bats
                    pkgs.bash
                    pkgs.jq
                  ];
                }
                ''
                  pack=${pkgs.pgii-pack-pr-support}
                  bats "$pack/scripts/tests/pr-pool.bats"
                  touch $out
                '';
```

- [ ] **Step 2: Run the check**

Run: `nix build --no-link '.#checks.aarch64-darwin.test-pgii-pack-pr-support-bats'`
Expected: builds (bats green inside the sandbox).

- [ ] **Step 3: Confirm the pack still builds (layout check unaffected — pr-pool.sh adds an exec script, no removed assertions)**

Run: `nix build --no-link '.#checks.aarch64-darwin.check-pgii-pack-pr-support-layout' '.#pgii-pack-pr-support'`
Expected: both build.

- [ ] **Step 4: Commit**

```bash
git add flake.nix && git commit -m "test(pgii-pr-support): run pr-pool bats in flake checks"
```

---

## Task 9: Live smoke test (real bd/tmux/claude, MAX=1)

**Files:** none (manual verification).

- [ ] **Step 1: Resolve the SKILL.md absolute path**

Run: `ls "$(nix build --no-link --print-out-paths '.#pgii-pack-pr-support' 2>/dev/null)"` is NOT it — the skill lives in the plugin. Use the source path:
`packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-process-feedback/SKILL.md` (absolute it with `realpath`).

- [ ] **Step 2: Dry contact — confirm `discover_cycles`'s inputs resolve**

Verify the building blocks (no script sourcing) from the monorepo root with the
`.envrc` taint scrubbed:

```bash
cd /Volumes/ziprecruiter/monorepo
env -u BEADS_DIR -u WORKSPACE_ROOT bash -c '
  echo "self_login: $(pg-pr config show --json | jq -r .self_login)"
  bd list --type=task --status=open --json --limit 0 \
    | jq -r "[.[]|select(.title|startswith(\"process-feedback:\"))]|length as \$n|\"open process-feedback cycles: \(\$n)\""
'
```

Expected: `self_login: phillipgziprecruiter` and a non-zero cycle count (these are exactly what `resolve_self` + `discover_cycles` consume). If `self_login` is empty, `discover_cycles` will error out by design.

- [ ] **Step 3: Full run against ONE cycle, watching the pane**

Run:

```bash
cd /Volumes/ziprecruiter/monorepo
PR_POOL_MAX=1 \
PR_POOL_SKILL_MD="$(realpath ~/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-process-feedback/SKILL.md)" \
  ~/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh
# In another terminal, watch:  tmux -L pgpool attach
```

Expected: a `pf-<id>` pane spawns running claude; the nudge lands; the worker reads feedback children, creates action bead(s), closes the cycle; `pr-pool` logs the drain pass and exits.

- [ ] **Step 4: Verify outcome in beads**

Run: `bd show <cycle-id> --json | jq .status` → `closed`; `bd list --type=task --json --limit 0 | jq '[.[]|select(.created_by=="'"$ACTOR"'" or (.title|test("action|fix")))]'` shows new action beads (spot check).

- [ ] **Step 5: Commit any fixes surfaced by the smoke test** (with their own failing bats test first, per TDD).

---

## Out of scope (later steps, per spec)

Self-fix (consume action beads); daemonization (launchd wrapper, continuous loop, raise `MAX`); escalation; PR-dedup of cycles (mooted now — the producer fix + cleanup leave 1 cycle per PR; add only if dupes recur); multi-role pool; nix-packaging `pr-pool.sh` as a first-class pack binary; migrating to a Go orchestrator.
