# pr-pool work triaging — design

**Status:** Draft
**Date:** 2026-06-09
**Builds on:**

- `docs/superpowers/specs/2026-06-08-pr-feedback-orchestrator-design.md` (step 1, shipped)
- `docs/superpowers/specs/2026-06-09-pr-pool-session-lifecycle-and-dedup-design.md` (session lifecycle + dedup, shipped)

## Summary

Generalize `pr-pool.sh` from a hardcoded finder of "my open `process-feedback:`
cycles" into a **mechanical triager**: classify the `bd ready` queue into
**roles** and dispatch the matching agent through the (now role-generalized)
session lifecycle. This chunk registers **two roles**:

- **feedback-processor** (existing) — handles `process-feedback:` cycles I own.
- **worker** (new) — handles beads explicitly labeled `worker-ready`; creates a
  git worktree, implements the change, **commits but does not push**, and leaves
  the result for human review.

Everything else in `bd ready` is **skipped and logged**. The orchestrator stays
**on-demand, N=1, mechanical** (no extra LLM in the routing path). Epic/PR-gluing
of session names, N>1 pooling, idle-timeouts, daemonization, the Go rewrite,
auto-application of `worker-ready`, and worker **push** all remain deferred (see
Out of scope).

## Why "mechanical" and "two routes only"

`bd ready` against the `zr` store is **heterogeneous and whole-monorepo**
(verified live 2026-06-09: 48 ready beads — 35 `process-feedback:` cycles, 7 raw
`feedback`/comment beads, plus a handful of work beads like
`Fix failing check-gradle-jdk17 build on PR #89771` and the `zr-lweh.N` epic
children). Two facts shape the design:

1. **`bd ready` includes non-entry-points.** Raw `feedback` beads are cycle
   _children_ processed via their cycle, never dispatched directly. A naive
   "dispatch every ready bead" would double-work them.
2. **Labels are empty on every bead today.** So there is no pre-existing routing
   field; classification must use signals that already exist (`issue_type`,
   title prefix, the parent-ownership walk), and the worker route is gated on a
   _new, explicitly-applied_ label (`worker-ready`) so nothing routes to the
   worker until a human opts a bead in.

The triager is therefore **mechanical** (deterministic classification in bash,
no triage agent) and routes to exactly **two** roles. This is deliberately the
simplest useful generalization; more roles / smarter routing / an explicit
producer-stamped routing field are future changes (the config table below makes
them additive).

## Roles & bead model

Extends the shared model from the prior specs with the worker as a real,
in-scope role (previously deferred):

| Role                           | Route signal                                                  | Consumes     | Produces                                         | Mutates code?          | Terminal state                |
| ------------------------------ | ------------------------------------------------------------- | ------------ | ------------------------------------------------ | ---------------------- | ----------------------------- |
| **triager** (`pr-pool` itself) | the `bd ready` queue                                          | ready beads  | `role<TAB>bead-id` dispatches                    | no                     | queue drained (MAX/empty)     |
| **feedback-processor**         | `task` + title `process-feedback:` + parent MR `author == me` | a cycle bead | work beads (children of the PR)                  | no                     | cycle bead **closed**         |
| **worker** (new)               | bead has label `worker-ready`                                 | a work bead  | a **commit** in a worktree (unpushed) + a record | **yes** (own worktree) | bead gains label `needs-push` |

```
PR bead                                   (pg-pr: created on PR open; closed + cascades on PR close)
├── cycle bead  "process-feedback: …"     (pg-pr; feedback-processor claims + closes)
│   └── feedback bead                     (pg-pr; feedback-processor reviews + closes)
└── work bead                             (feedback-processor creates; human labels `worker-ready`; worker implements)
```

The producer (pg-pr) and feedback-processor contracts are **unchanged** by this
chunk. The only new agent contract is the worker SKILL.

## The triager (mechanical, in `pr-pool.sh`)

### Discovery — `discover` replaces `discover_cycles`

A single `bd ready --json --limit 0` call (one round-trip; partition in `jq`).
Iterate the ready array **in ready/priority order**, classify each bead, and emit
`role<TAB>bead-id` lines for matches:

- **feedback-processor**: `issue_type == "task"` ∧ title starts with
  `process-feedback:` ∧ — via the existing parent-walk — the parent
  merge-request's `metadata.author == self_login`. (Unchanged ownership logic
  and unchanged N+1 `bd show` cost from step 1's `discover_cycles`.)
- **worker**: `.labels` contains `worker-ready`. **No ownership walk** — the
  human applied the label deliberately, which _is_ the authorization. (A future
  change may additionally require it to resolve to my PR; out of scope now.)
- **anything else**: skip; log a one-line count of skipped ready beads.

Emitting in ready order means a single bounded pass (`MAX`) takes the
highest-priority eligible beads first, regardless of role.

### Per-role config table (approach A)

A declarative role → config mapping, implemented as **`case`-based resolver
functions** (not bash-4 associative arrays — macOS system bash is 3.2; the
prototype runs under `/usr/bin/env bash`). This is the "seed of the per-role
config table" the step-1 spec anticipated and the artifact the eventual Go pool
ports:

- `role_session <role>` → tmux session name
  (`"PR FEEDBACK PROCESSOR"` | `"WORKER"`).
- `role_actor <role>` → `BEADS_ACTOR`
  (`pgii-pool__process-feedback` | `pgii-pool__worker`).
- `role_skill <role>` → SKILL.md path
  (`$PR_POOL_SKILL_MD` | `$PR_POOL_WORKER_SKILL_MD`, a new top-of-script var).
- `role_nudge <role> <bead-id>` → the instruction line
  (`nudge_text_feedback` | `nudge_text_worker`).
- `role_label <role> <bead-id>` → the claude conversation name for `/rename`.

All session names / actors / skill paths stay top-of-script env-overridable
variables, as in step 1.

### Generalized lifecycle

Step 1's lifecycle functions already thread a `$sess` argument; this chunk
parameterizes them by **role** rather than the single `ROLE_NAME` global:

- `ensure_session <role>` — resolve session name + actor; `new-session` with
  that name and `-e BEADS_ACTOR=<actor>` (+ the `BEADS_DIR`/`WORKSPACE_ROOT`
  pins) if absent, else reuse; `wait_ready`.
- `work_one <role> <bead-id>` — `ensure_session` → `claude_rename` →
  `send_nudge` → `wait_done` → `clear_context`.
- `send_nudge` / `wait_done` / `clear_context` / `teardown_session` — take the
  resolved session name (mechanical change from the `ROLE_NAME` global).
- `drain_once` — iterate `discover` lines up to `MAX` **total** items (across
  both roles), calling `work_one <role> <id>`; **after** the loop, `teardown`
  **each** role's session that exists, so no role leaves an orphan.

### Per-role completion & failure (the subtle part)

`wait_done` becomes **role-aware**, because the worker **never closes its bead**
(no-push ≠ done):

- **feedback-processor** (unchanged): success = cycle `status == closed`.
  Failure (pane death or `MAX_WAIT`) → **unclaim** + flag (retryable next run).
- **worker**: success = the bead **gains label `needs-push`** (the worker's
  terminal action; see below). Failure → flag **without unclaim**. A dead worker
  may have left a half-built worktree and a partial commit, so we never blind-
  retry it; it is left `in_progress` for human inspection. `needs-push` doubles
  as the human review queue (`bd list --label needs-push`).

`bd ready` already excludes `in_progress` beads, and the worker route requires
`worker-ready` (which the worker removes), so a completed-or-stuck worker bead is
never re-dispatched.

## Worker SKILL (new) + nudge

New skill: `packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-work-bead/SKILL.md`
(name negotiable). Mirrors the thin-orchestrator pattern — `pr-pool` does no git;
the dispatched agent does, guided by the SKILL. Contract:

1. **Claim** the work bead (`bd update <id> --claim`).
2. **Resolve the PR + head branch**: walk to the PR bead (parent) for repo + PR
   number; obtain the head branch via `gh pr view <n> --json headRefName` (or
   `pg-pr`).
3. **Create an isolated git worktree** for that branch at a conventional path
   (top-of-script `PR_POOL_WORKTREE_DIR`, default
   `${XDG_STATE_HOME:-$HOME/.local/state}/pr-pool/worktrees/<repo>-pr<n>`, mirroring
   step 1's `LOG_DIR` placement outside `~/gc`); **reuse it if it already exists**
   (crash-safe re-entry).
4. **Implement** the change described in the work bead, in the worktree; run a
   cheap local check/build if feasible.
5. **Commit** with a conventional message referencing the bead + PR.
   **Never push, never force.**
6. **Record + signal**: `bd comment <id>` with the worktree path, commit SHA, and
   branch; swap the label `worker-ready` → `needs-push`; leave the bead
   **claimed / `in_progress`** — do **not** close it.
7. **Boundaries**: one bead per run; touch only files the bead implies; no push;
   no PR comments (the worker writes to the bead, not GitHub).

`nudge_text_worker <id>` states this in a single instruction line (read the
worker SKILL, implement bead `<id>` in a worktree, commit but do not push, record
path+SHA, swap `worker-ready`→`needs-push`, do not close).

The **feedback-processor SKILL and `nudge_text_feedback` are unchanged** (its
contract was finalized in the prior chunk).

## Components & isolation

| Unit                           | Responsibility                                           | Depends on                      |
| ------------------------------ | -------------------------------------------------------- | ------------------------------- |
| `discover`                     | `bd ready` → `role<TAB>id` (classify + ownership/label)  | `bd`, `pg-pr`, `jq`             |
| `role_*` resolvers             | role → session / actor / skill / nudge / label           | (pure)                          |
| `ensure_session <role>`        | create/reuse the role's named session                    | `tmux`, `claude`, `uuidgen`     |
| `work_one <role> <id>`         | drive one item through the lifecycle                     | the above, `bd`                 |
| `wait_done <role> <id> <sess>` | role-aware completion + failure (unclaim/flag)           | `bd`, `tmux`                    |
| `drain_once`                   | bounded multi-role pass + teardown-all                   | the above                       |
| worker (`claude` pane)         | per-bead worktree → implement → commit → record + signal | worker SKILL, `git`, `gh`, `bd` |

Each function stays small and independently stubbable on `$PATH`, as in step 1.

## Placement & packaging

Edits to the existing `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh`
and its `pr-pool.bats`. One **new** file: the worker SKILL under the pg-pr-plugin
skills tree. No new orchestrator files. The flake bats check
(`test-pgii-pack-pr-support-bats`) already runs the suite.

## Testing

**bats (`pr-pool.bats`, PATH-stubs):**

- `discover` tags `process-feedback:` cycles I own as `feedback-processor`,
  `worker-ready`-labeled beads as `worker`, **skips** the rest, and preserves
  ready order.
- `role_*` resolvers return the correct session / actor / skill / nudge per role.
- `ensure_session worker` emits
  `new-session … -s "WORKER" … -e BEADS_ACTOR=pgii-pool__worker`.
- `nudge_text_worker` references the worker SKILL, a worktree, commit-**without**-
  push, recording path+SHA, and the `worker-ready`→`needs-push` swap.
- worker `wait_done` succeeds when the bead gains `needs-push`, and on failure
  **does not unclaim** (asserts the divergence from the feedback path).
- `drain_once` tears down **both** role sessions (no orphan on the `pgpool`
  socket).
- **Regression**: all existing feedback-path tests stay green (the generalization
  must not change feedback behavior).

**Live smoke (manual; confirm the target bead with the user first — it mutates
real beads and creates a real worktree):** hand-label one of _my_ work beads
`worker-ready`, run `pr-pool.sh` with `PR_POOL_MAX=1`, and verify: the `WORKER`
session spawns, the agent creates a worktree and a commit (verify **unpushed**:
the PR branch's remote head is unchanged), the bead gains `needs-push` + a
path/SHA comment and stays `in_progress`, and the session is torn down at
end-of-run. Any TUI/claude-incantation surprises get a failing bats test first,
per TDD (as in step 1).

## Assumptions to verify in implementation

- The worker can resolve the PR head branch from the bead lineage (PR bead
  metadata) + `gh pr view`/`pg-pr` reliably.
- `git worktree add` for an already-checked-out branch fails; the SKILL's
  "reuse if exists" path handles re-entry without error.
- `bd` label add/remove (`worker-ready` → `needs-push`) and `bd list --label`
  behave as assumed in the `zr` store.
- `bd ready` reliably surfaces the `process-feedback:` cycles (verified: 35 are
  ready) and `worker-ready` beads, and excludes `in_progress` beads.

## Out of scope (additive later)

- **Epic/PR-gluing** of worker session names (`"WORKER: epic: <id>"` /
  `"WORKER: PR #<n>"`) — the session name is already a `role_session` function,
  so this is a pure config change. (Note: at N>1, two `worker-ready` beads on the
  same PR branch would collide on `git worktree add`; gluing is the fix and is
  why it is called out.)
- **N>1 pool**, **idle-timeout / watchdog**, **daemonization**, and the **Go
  rewrite** (port the config table + `TmuxSignaler`).
- **Auto-applying `worker-ready`** (manual by the user for now).
- **Worker push** (commit-only this chunk; human reviews + pushes).
- **Deeper worker crash / worktree reconciliation** beyond "reuse if exists".
- **pg-pr producer correctness** (cycle reuse, PR-close cascade) — separate
  producer ticket.
- **An explicit producer-stamped routing field** replacing the heuristic
  classification.
- The dispatched-pane `node: command not found` hook error.

## Related

- `docs/superpowers/specs/2026-06-08-pr-feedback-orchestrator-design.md` — step 1
  (taint-free bead access, the nudge/worker contract, dispatch invariants).
- `docs/superpowers/specs/2026-06-09-pr-pool-session-lifecycle-and-dedup-design.md`
  — the session lifecycle this chunk generalizes per role, and the unchanged
  feedback-processor dedup contract.
- `docs/superpowers/plans/2026-06-08-pr-pool-orchestrator.md` and
  `docs/superpowers/plans/2026-06-09-pr-pool-session-lifecycle-and-dedup.md` —
  the shipped plans + live smoke-test findings this builds on.
- `packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-process-feedback/SKILL.md`
  — the (unchanged) feedback-processor contract.
- `docs/adr/0009-pg-pr-bead-schema.md` — bead types, `discovered-from`, cascade.
