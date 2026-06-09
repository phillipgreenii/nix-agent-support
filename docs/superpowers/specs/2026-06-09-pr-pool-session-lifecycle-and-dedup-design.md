# pr-pool session lifecycle + work-bead dedup — design

**Status:** Draft
**Date:** 2026-06-09
**Builds on:** `docs/superpowers/specs/2026-06-08-pr-feedback-orchestrator-design.md` (step 1, shipped) and its plan `docs/superpowers/plans/2026-06-08-pr-pool-orchestrator.md`.

## Summary

The next increment on the PR-feedback orchestrator (`pr-pool.sh`), in two independent parts:

1. **Session lifecycle** — give the dispatched tmux/claude session a real lifecycle managed _within_ a single on-demand run: create-if-absent, name the tmux session by **role**, `/rename` the claude conversation **per work item**, `/clear` between items, and tear down (`exit` + `kill-session`) at end-of-run. This replaces step 1's per-cycle `new-session` and its orphaned-pane behavior.

2. **Work-bead dedup** — make the **feedback processor** (the dispatched agent, not the orchestrator) consider a PR's existing **open work beads** before creating new ones, so multiple comments — or a later cycle's feedback — that refer to the same work **link/update** an existing work bead instead of creating a duplicate. This is a change to the feedback-processor _contract_ (its skill + nudge), not to `pr-pool.sh` or pg-pr.

Both keep the orchestrator **on-demand, N=1**. A true session pool (N>1), idle-timeouts, daemonization, work triaging, and the downstream worker agent remain deferred (see Out of scope).

## Roles & bead model (the shared mental model)

Three agents, each with a narrow role:

- **pg-pr (producer).** Creates/closes **PR beads**; creates **cycle** and **feedback** beads. Reuses an **open/unclaimed** cycle bead for new feedback; if the open cycle is **in-progress** (claimed), it leaves it alone and creates a **new** cycle. Closes the PR bead and all its children when the PR closes. Does not touch work beads.
- **Feedback processor (dispatched by `pr-pool`).** Claims a cycle, reviews + closes its **feedback** beads and the **cycle** bead, and **creates work beads** from the feedback — comparing against the PR's existing open work beads to avoid duplicates. Does **not** implement fixes.
- **Worker agent (future, out of scope).** Performs the work described in the work beads. Not part of this session.

Bead hierarchy:

```
PR bead                                   (pg-pr: created on PR open; closed + cascades on PR close)
├── cycle bead  "process-feedback: …"     (pg-pr: reuse if open/unclaimed; new if in-progress)
│   └── feedback bead                     (pg-pr: one per comment / CI failure; carries repo, pr, thread_id)
└── work bead                             (feedback processor: child of the PR bead; discovered-from a feedback bead)
```

The new structural fact this design relies on: **work beads are children of the PR bead** (and `discovered-from` the feedback that motivated them). That makes "the PR's existing open work" a single cheap query — `bd children <PR-id> --status=open` filtered to work types — which is exactly what the dedup check needs.

Multiple open cycles per PR is **legitimate** (in-progress cycle + a new cycle for newer feedback), so there is **no cycle-level dedup**. Dedup happens at the **work-bead** level, in the processor.

---

## Part 1 — Work-bead dedup (feedback-processor contract)

### What changes

Nothing in `pr-pool.sh` or pg-pr. Two artifacts that define the feedback processor's behavior are updated:

- `packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-process-feedback/SKILL.md` — refreshed to the live process.
- `nudge_text()` in `pr-pool.sh` — aligned to the refreshed skill.

### Refreshed feedback-processor workflow (SKILL.md)

The current SKILL.md is stale: it says feedback "lands in Phase 3 … prepped so it's ready when the surface is live," and step 3 tells the agent to _implement the change_ (which the nudge already has to override). Replace its workflow with:

1. **Claim** the cycle bead (`bd update <cycle-id> --claim`).
2. **Read** the cycle's feedback children (`bd children <cycle-id>`).
3. **Resolve the PR bead** (the cycle's parent) and **list the PR's existing open work beads** — `bd children <PR-id> --status=open` filtered to work types (`task`/`bug`), i.e. the action beads the processor itself creates (distinct from the cycle children, which are also PR children).
4. **For each feedback bead**, derive the work it implies, then:
   - If it matches an existing open work bead, **link/update** that bead — add this feedback as another `discovered-from` (and refine the bead's description if warranted) — rather than creating a duplicate. Multiple comments / a later cycle's feedback commonly map to the same work.
   - Otherwise **create a new work bead** as a **child of the PR bead**, `discovered-from` this feedback.
   - Then **close the feedback bead** with a short reason.
5. **Close the cycle bead** with a one-line summary.

Guardrails retained from today's nudge + SKILL.md boundaries: the processor **does not implement fixes** and **does not work the new work beads** (that's the future worker agent); never close the cycle before every feedback child is closed; author precedence on responses; don't strip the `🤖` marker.

### Nudge alignment

With SKILL.md corrected (create work beads, don't implement), the nudge no longer needs to _override_ the skill's "implement" step. `nudge_text()` is thinned to: point the agent at the SKILL.md, name the target cycle, and re-state the two guardrails (don't apply fixes, don't work the new work beads) and the dedup expectation (consider the PR's open work beads). The nudge remains the orchestrator's single instruction line.

### Why no cycle dedup / no pg-pr change here

The duplicate _work bead_ symptom observed in the step-1 smoke test (two #89771 gradle beads from cycles `zr-u2a` + `zr-7hms`) is solved by the processor consulting open work beads — the second cycle links to the first cycle's bead. Whether pg-pr correctly reuses cycles (reuse open/unclaimed, new-if-in-progress) is a **separate producer ticket**, not this chunk.

---

## Part 2 — Session lifecycle (`pr-pool.sh`)

### Model

One tmux session on the dedicated `-L pgpool` socket, named for its **role** and reused across work items within a run. Step 1's "one detached `pf-<cycle-id>` session per cycle, left running" is replaced by a managed lifecycle:

```
run:
  precheck
  for each discovered work item, up to MAX (=1 for now):
      ensure_session                 # create the role session (+ boot claude, wait_ready) if absent; else reuse
      claude_rename <per-item-name>  # name the claude conversation for findability
      send_nudge                     # the work instruction
      wait_done                      # poll until the cycle closes (unchanged from step 1)
      clear_context                  # /clear to reset claude for the next item; wait_ready
  teardown                           # no more work: exit claude, then kill-session
  app exits
```

At MAX=1 this is: create → `/rename` → nudge → `wait_done` → `/clear` → teardown. Reuse (skipping create + boot via `/clear`) only fires when MAX>1, which arrives with the Go pool — but the lifecycle is built now so that future is additive.

### Two names

- **tmux session name = role**, stable for the run: `PR_POOL_ROLE_NAME`, default `"PR FEEDBACK PROCESSOR"`. This is what external monitoring keys on (`tmux -L pgpool ls`). When sessions are later glued to epics, this becomes e.g. `"WORKER: epic: <epic-id>"` — a config change, not a code change. (Names contain spaces; all `-t` targeting must quote the exact name.)
- **claude conversation name = per work item**, set via `/rename "<name>"`, e.g. `"process-feedback <cycle-id> PR #<pr-number>"` (PR number from the parent MR bead's metadata). This makes an individual run findable later in claude's session history.

### Functions (refactor of step 1)

- **`submit_line <session> <text>`** — _new shared helper._ Types `<text>` into the pane, sleeps `SEND_SETTLE`, then sends a **separate** `Enter`. This generalizes the step-1 `send_nudge` paste-mode fix; `/rename`, the nudge, `/clear`, and the exit line all go through it.
- **`ensure_session`** — if the role session does not exist, `new-session -d -s "<role>" -c "$REPO_ROOT" -e BEADS_ACTOR=… -e BEADS_DIR=… -e WORKSPACE_ROOT=…` running claude, then `wait_ready`. If it exists, reuse it. Replaces step 1's `dispatch`/`session_name` (which were per-cycle).
- **`claude_rename <name>`** — `submit_line "<role>" "/rename \"<name>\""`.
- **`send_nudge`** — unchanged in spirit (now uses `submit_line`); nudge text per Part 1.
- **`wait_ready` / `wait_done`** — unchanged from step 1 (prompt-glyph match + close-race guard already shipped). `wait_ready` is also used after `/clear`.
- **`clear_context`** — `submit_line "<role>" "/clear"`, then `wait_ready` (so the session is ready for the next item).
- **`teardown`** — if the role session exists: `submit_line "<role>" "<exit incantation>"`, then `kill-session` (best-effort, `|| true`). `kill-session` is the **guaranteed** teardown even if the graceful exit doesn't land.
- **`work_one`** — `ensure_session` → `claude_rename` → `send_nudge` → `wait_done` → `clear_context`. (Unclaim-on-failure paths from step 1 retained.)
- **`drain_once`** — loop over discovered cycles up to `MAX` calling `work_one`; **after** the loop (queue empty or `MAX` hit), call `teardown`. Always returns 0 (per step 1).

### Claude interaction incantations

- `/rename "<name>"` — **confirmed** available in Claude Code (sent via `submit_line`).
- `/clear` — standard Claude Code context reset.
- **exit** — incantation to be **verified in implementation** (`/exit` vs `Ctrl-C Ctrl-C` vs `Ctrl-D`); `kill-session` is the guaranteed fallback regardless. This is the same class of claude-TUI detail that produced the step-1 smoke-test fixes (prompt glyph, Enter-as-submit), so it is verified live, not assumed.

### Error handling

- `ensure_session` create failure → log + `return 1` (drain logs the cycle as not completed, continues/stops per `MAX`).
- `wait_ready` timeout (claude never reached the prompt) → existing behavior: unclaim + skip the item.
- `teardown` is best-effort and never fails the run: attempt graceful `exit`, then `kill-session || true`.
- A **leftover** role session from a previously crashed run is simply reused by `ensure_session` (a fresh `/clear` precedes the first nudge if needed); normal runs end with `teardown`, so accumulation is bounded to at most one stray session.

---

## Components & isolation

- `pr-pool.sh` — orchestrator; gains `submit_line`, `ensure_session`, `claude_rename`, `clear_context`, `teardown`; `dispatch`/`session_name` removed/folded. Each function stays small and independently stubbable on `$PATH` (as in step 1).
- `SKILL.md` (pg-pr-plugin) — the feedback processor's authoritative contract; now current and includes the dedup step.
- pg-pr, Gas City — untouched (the step-1 hard constraint holds: own `-L pgpool` socket, no `gc` commands, reads/writes only the `zr` bead data).

## Placement & packaging

No new files. Edits to the existing `pr-pool.sh`, its `pr-pool.bats`, and the plugin's `SKILL.md`. The flake bats check (`test-pgii-pack-pr-support-bats`) already runs the suite.

## Testing

**bats (`pr-pool.bats`)** — extend the PATH-stub suite to cover the lifecycle:

- `submit_line` sends the text and then a **separate** `Enter` (generalized from the step-1 send_nudge assertion).
- `ensure_session` **creates** the role session when absent (asserts `new-session … -s "PR FEEDBACK PROCESSOR"` + role `-e` env), and **reuses** (no second `new-session`) when the session already exists.
- `claude_rename` submits `/rename "<name>"` with the per-item name.
- `clear_context` submits `/clear` and then waits for the prompt.
- `teardown` submits the exit line **and** calls `kill-session` for the role session.
- `drain_once` calls `teardown` exactly once after the pass, and leaves **no** role session afterward (no-orphan assertion).
- Dedup (shallow, since it's a prompt change): assert `nudge_text` references considering the PR's open work beads and creating work beads as children of the PR bead.

**Live smoke (manual, one cycle)** — re-run `pr-pool.sh` (`PR_POOL_MAX=1`) against a real cycle and verify: the tmux session is role-named; the claude conversation is `/rename`d; the nudge submits (shared `submit_line`); and at end-of-run the session is **torn down** (no orphan on the `pgpool` socket). Dedup is validated by choosing a cycle whose PR already has an **open work bead** and confirming the processor **links** to it rather than creating a duplicate. Fixes surfaced get a failing bats test first, per TDD (as in step 1).

## Assumptions to verify in implementation

- Claude Code **exit** incantation (graceful) — verified live; `kill-session` is the guaranteed fallback.
- `/clear` returns the conversation to the ready prompt such that `wait_ready` re-detects it (relevant only for MAX>1 reuse; still asserted).
- tmux session names with spaces target correctly under `-t "<name>"` for `capture-pane`/`send-keys`/`kill-session` (quote everywhere).

## Out of scope (deferred)

- **Session pool (N>1)** and **idle-timeout / self-terminating watchdog** — arrive with the **Go** rewrite; this chunk is N=1, on-demand, lifecycle-within-a-run only.
- **Daemonization** (launchd, continuous loop).
- **Work triaging** — generalizing discovery from "my process-feedback cycles" to `bd ready` over multiple work types (process feedback / close PR / PR fix / other). The role-named session is forward-compatible with it, but it is a separate chunk.
- **Worker agent** — performing the work in work beads (a distinct agent/prompt).
- **pg-pr producer correctness** — reliable cycle reuse (reuse open/unclaimed, new-if-in-progress) and PR-close cascade — a separate producer ticket.
- **Dispatched-env hook error** (`node: command not found` in the pane's `SessionStart`/`UserPromptSubmit` hooks) — explicitly out of scope.
- **Epic-gluing** of sessions (role name `"WORKER: epic: <id>"`) — a later config/triaging change.

## Related

- `docs/superpowers/specs/2026-06-08-pr-feedback-orchestrator-design.md` — step 1 design (roles, taint-free bead access, the nudge/worker contract, producer-side investigation).
- `docs/superpowers/plans/2026-06-08-pr-pool-orchestrator.md` — step 1 plan + live smoke-test results (the duplicate-work-bead and orphaned-pane findings this chunk addresses).
