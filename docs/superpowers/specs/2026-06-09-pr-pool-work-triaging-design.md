# pr-pool work triaging — design

**Status:** Draft (revised 2026-06-09 after a subagent design review + live re-verification)
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
**on-demand, mechanical** (no extra LLM in the routing path), with a small
**per-role concurrency cap** (default 1 each). Epic/PR-gluing of session names,
N>1 within a role, idle-timeouts, daemonization, the Go rewrite, auto-application
of `worker-ready`, and worker **push** all remain deferred (see Out of scope).

## Why "mechanical" and "two routes only"

`bd ready` against the `zr` store is **heterogeneous and whole-monorepo**
(re-verified live 2026-06-09: 49 ready beads, **all `status: open`** — 35
`process-feedback:` cycles, 7 **open/unhooked** `feedback`/comment beads, plus a
handful of work beads like `Fix failing check-gradle-jdk17 build on PR #89771`
and the `zr-lweh.N` epic children). Two facts shape the design:

1. **`bd ready` includes non-entry-points.** Most `feedback` beads are
   `status: hooked` (excluded from `bd ready`), but a minority are **open** and
   _do_ surface in `bd ready` (verified: 7). Those are cycle _children_ processed
   via their cycle, never dispatched directly. A naive "dispatch every ready
   bead" would double-work them, which is why the triager classifies and
   **skips** anything that is not an explicit entry point.
2. **Labels are empty on every bead today**, and on `bd ready --json` the
   `labels` field is **`null`** (not `[]`) when unset (verified). So there is no
   pre-existing routing field, and a jq label check must not assume an array. The
   worker route is therefore gated on a _new, explicitly-applied_ label
   (`worker-ready`), queried via `bd ready`'s **native `--label` filter** (which
   exists — verified) rather than fragile jq over a possibly-null field. Nothing
   routes to the worker until a human opts a bead in.

The triager is **mechanical** (deterministic classification in bash, no triage
agent) and routes to exactly **two** roles. This is deliberately the simplest
useful generalization; more roles / smarter routing / a producer-stamped routing
field are future changes that the per-role config table makes additive.

## Roles & bead model

Extends the shared model from the prior specs with the worker as a real,
in-scope role (previously deferred):

| Role                           | Route signal                                                  | Consumes     | Produces                                         | Mutates code?          | Terminal state                |
| ------------------------------ | ------------------------------------------------------------- | ------------ | ------------------------------------------------ | ---------------------- | ----------------------------- |
| **triager** (`pr-pool` itself) | the `bd ready` queue                                          | ready beads  | `role<TAB>bead-id` dispatches                    | no                     | per-role caps reached / empty |
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

The shipped `discover_cycles` queries `bd list --type=task --status=open`. This
chunk **switches discovery to `bd ready`** — the queue the whole chunk is about —
issued **per role** so the two routes stay independent and cannot starve each
other (see per-role caps below):

- **feedback-processor**: `bd ready --json --limit 0`, kept to beads where
  `issue_type == "task"` ∧ title starts with `process-feedback:` ∧ — via the
  existing parent-walk — the parent merge-request's
  `metadata.author == self_login`. (Same ownership logic and same N+1 `bd show`
  cost as step 1.)
  - _Behavior note:_ `bd ready` excludes `blocked`/`deferred`/`hooked`/`in_progress`
    beads, so a blocked cycle is intentionally not picked (it is not ready). The
    step-1 **unclaim-on-failure** contract is still required: a cycle the worker
    claimed-then-died on becomes `in_progress`, which `bd ready` excludes, so it
    must be unclaimed to resurface (unchanged from step 1).
- **worker**: `bd ready --label worker-ready --json --limit 0` (native label
  filter — no jq over the null-able `.labels` field). Every returned bead routes
  to the worker. **No ownership walk in the triager** — the human applied the
  label deliberately, which _is_ the authorization (the worker SKILL adds a
  belt-and-suspenders author assertion before it commits; see below).

`discover` emits `role<TAB>bead-id` lines. Each role's beads stay in `bd ready`
order within that role.

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
- `role_convo_name <role> <bead-id>` → the claude conversation name for
  `/rename` (renamed from a draft `role_label` to avoid confusion with bd
  _labels_, which this chunk uses heavily).
- `role_max <role>` → the per-role concurrency cap (`$PR_POOL_MAX_FEEDBACK` |
  `$PR_POOL_MAX_WORKER`, default 1 each).

All session names / actors / skill paths / caps stay top-of-script
env-overridable variables, as in step 1.

### Generalized lifecycle

Step 1's lifecycle functions already thread a `$sess` argument; this chunk
parameterizes them by **role** rather than the single `ROLE_NAME` global:

- `ensure_session <role>` — resolve session name + actor; `new-session` with that
  name and `-e BEADS_ACTOR=<actor>` (+ the `BEADS_DIR`/`WORKSPACE_ROOT` pins) if
  absent, else reuse; `wait_ready`.
- `work_one <role> <bead-id>` — `ensure_session` → `claude_rename` →
  `send_nudge` → `wait_done` → `clear_context`.
- `send_nudge` / `wait_done` / `clear_context` / `teardown_session` — take the
  resolved session name (mechanical change from the `ROLE_NAME` global).
- `drain_once` — for each role, drain its discovered beads up to `role_max
<role>` (so a worker-ready bead can never starve feedback processing, and vice
  versa). **After** all roles, **`teardown` every known role's session** —
  iterate the role list and `kill-session` each (not only sessions this pass
  created), so a stray session from a previously-crashed run, or from a role no
  longer being dispatched, is always reaped (tightening spec 2's "at most one
  stray session" to hold per role).

### Per-role completion & failure

`wait_done` becomes **role-aware**, because the worker **never closes its bead**
(no-push ≠ done):

- **feedback-processor** (unchanged): success = cycle `status == closed`. Failure
  (pane death or `MAX_WAIT`) → **unclaim** + flag (retryable next run).
- **worker**: success = the bead **gains label `needs-push`** (the worker's
  terminal signal; see below), surfacing it as the human review queue
  (`bd list --label needs-push`). Failure (pane death, `MAX_WAIT`, or the worker
  aborting because it cannot resolve the PR) → the orchestrator stamps a
  **`worker-stuck`** label and flags. It does **not** unclaim: a dead worker may
  have a half-built worktree + partial commit, so blind retry is unsafe; the bead
  is left `in_progress` for human inspection. **`worker-stuck` is the queryable
  surface** (`bd list --label worker-stuck`) so a stuck worker is never a
  silent black hole. (Both `bd ready` excluding `in_progress` and the
  `worker-ready` label still being present mean it is never auto-re-dispatched;
  the human clears `worker-stuck`/unclaims to retry.)

## Worker SKILL (new) + nudge

New skill: `packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-work-bead/SKILL.md`
(name negotiable). Mirrors the thin-orchestrator pattern — `pr-pool` does no git;
the dispatched agent does, guided by the SKILL. Contract:

1. **Claim** the work bead (`bd update <id> --claim`).
2. **Resolve the PR + head branch, bead-first**: walk to the PR (merge-request)
   bead (parent) and read `metadata.repo`, `metadata.pr_number`, and
   **`metadata.branch`** (verified present on MR beads). Live `gh`/`pg-pr` is a
   _fallback_ only. **Abort safely if unresolvable** (no parent MR, missing
   branch, PR `state` not open): do not edit anything; surface the failure so the
   orchestrator stamps `worker-stuck` (the worker has already `--claim`ed, so it
   must not die silently).
3. **Assert ownership** before any mutation: `metadata.author == self_login`
   (`pg-pr config show .self_login`). If not mine, abort → `worker-stuck`. (The
   triager trusts the label; this is the worker's defense-in-depth so a mislabel
   can't commit to someone else's branch.)
4. **Create an isolated git worktree** for that branch at a conventional path
   (top-of-script `PR_POOL_WORKTREE_DIR`, default
   `${XDG_STATE_HOME:-$HOME/.local/state}/pr-pool/worktrees/<repo>-pr<n>`,
   mirroring step 1's `LOG_DIR` placement outside `~/gc`); **reuse it if it
   already exists** (`git worktree add` refuses an already-checked-out branch —
   verified — so re-entry must reuse, not re-add).
5. **Implement** the change described in the work bead, in the worktree; run a
   cheap local check/build if feasible.
6. **Commit** with a conventional message referencing the bead + PR. **Never
   push, never force.**
7. **Record, then signal (order matters):** first `bd comment <id>` with the
   worktree path, commit SHA, and branch; **then** atomically swap the label in a
   single call: `bd update <id> --add-label needs-push --remove-label
worker-ready`. The comment must land **before** the swap because the
   orchestrator's `wait_done` returns the instant `needs-push` appears and then
   `/clear`s the session — a swap-before-comment race could wipe the agent
   mid-record. Leave the bead **claimed / `in_progress`** — do **not** close it.
8. **Boundaries**: one bead per run; touch only files the bead implies; no push;
   no PR comments (the worker writes to the bead, not GitHub).

`nudge_text_worker <id>` states this in a single instruction line (read the
worker SKILL, implement bead `<id>` in a worktree, commit but do not push, record
path+SHA then swap `worker-ready`→`needs-push`, do not close, abort to
`worker-stuck` if the PR can't be resolved or isn't mine).

The **feedback-processor SKILL and `nudge_text_feedback` are unchanged** (its
contract was finalized in the prior chunk).

## Components & isolation

| Unit                           | Responsibility                                              | Depends on                         |
| ------------------------------ | ----------------------------------------------------------- | ---------------------------------- |
| `discover`                     | per-role `bd ready` → `role<TAB>id`                         | `bd`, `pg-pr`, `jq`                |
| `role_*` resolvers             | role → session / actor / skill / nudge / convo / cap        | (pure)                             |
| `ensure_session <role>`        | create/reuse the role's named session                       | `tmux`, `claude`, `uuidgen`        |
| `work_one <role> <id>`         | drive one item through the lifecycle                        | the above, `bd`                    |
| `wait_done <role> <id> <sess>` | role-aware completion + failure (unclaim / `worker-stuck`)  | `bd`, `tmux`                       |
| `drain_once`                   | per-role bounded drain + teardown-all-roles                 | the above                          |
| worker (`claude` pane)         | bead-first resolve → worktree → implement → commit → signal | worker SKILL, `git`, `bd`, `pg-pr` |

Each function stays small and independently stubbable on `$PATH`, as in step 1.

## Placement & packaging

Edits to the existing `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh`
and its `pr-pool.bats`. One **new** file: the worker SKILL under the pg-pr-plugin
skills tree. No new orchestrator files. The flake bats check
(`test-pgii-pack-pr-support-bats`) already runs the suite.

## Testing

**bats (`pr-pool.bats`, PATH-stubs):**

- `discover` (feedback) tags `process-feedback:` cycles I own as
  `feedback-processor` and excludes others' / non-cycle beads.
- `discover` (worker) invokes `bd ready --label worker-ready` and tags the
  results `worker`; assert it does **not** rely on parsing `.labels` from a plain
  `bd ready`.
- `role_*` resolvers return the correct session / actor / skill / nudge / convo /
  cap per role.
- `ensure_session worker` emits
  `new-session … -s "WORKER" … -e BEADS_ACTOR=pgii-pool__worker`.
- `nudge_text_worker` references the worker SKILL, a worktree, commit-**without**-
  push, the record-then-swap order, and the `worker-stuck` abort.
- worker `wait_done` succeeds when the bead gains `needs-push`; on failure it
  stamps **`worker-stuck`** and **does not unclaim** (asserts the divergence from
  the feedback path).
- `drain_once` honors **per-role caps** (a single `worker-ready` bead does not
  prevent a feedback cycle from being worked in the same pass) and tears down
  **both** role sessions, including a pre-existing stray session for a role not
  dispatched this pass (no-orphan).
- **Regression**: all existing feedback-path tests stay green (the generalization
  must not change feedback behavior beyond the `bd list`→`bd ready` switch, which
  is asserted).

**Live smoke (manual; confirm the target bead with the user first — it mutates
real beads and creates a real worktree):** hand-label one of _my_ work beads
`worker-ready`, run `pr-pool.sh` with default caps; verify: the `WORKER` session
spawns, the agent resolves the branch from the MR bead (no `gh` needed), creates a
worktree and a commit (verify **unpushed**: the PR branch's remote head is
unchanged), the bead gains `needs-push` + a path/SHA comment and stays
`in_progress`, and the session is torn down at end-of-run. Force a failure (e.g.
label a bead whose PR is closed) and confirm it lands in `bd list --label
worker-stuck`. TUI/claude-incantation surprises get a failing bats test first, per
TDD (as in step 1).

## Assumptions to verify in implementation

- **Confirmed live (2026-06-09):** MR beads carry `metadata.branch` (+ `repo`,
  `pr_number`, `author`, `state`, `url`); `bd ready` is all-`open` and excludes
  `hooked`/`in_progress`; `bd ready --json` returns `labels: null` when unset;
  `bd ready` supports `--label`/`--label-any`/`--exclude-label`; `git worktree
add` refuses an already-checked-out branch.
- **Still to verify:** the **populated** shape of `.labels` (array of strings vs
  objects) for any code path that reads labels off JSON; `bd update --add-label
… --remove-label …` performs the swap atomically in one call; `bd list --label`
  returns `in_progress` beads (so the `worker-stuck` / `needs-push` review queues
  actually list claimed beads).

## Out of scope (additive later)

- **Epic/PR-gluing** of worker session names (`"WORKER: epic: <id>"` /
  `"WORKER: PR #<n>"`) — the session name is already a `role_session` function,
  so this is a pure config change. (Note: at a worker cap >1, two `worker-ready`
  beads on the same PR branch would collide on `git worktree add`; gluing is the
  fix and is why it is called out. The default cap of 1 avoids it for now.)
- **Per-role cap >1**, **idle-timeout / watchdog**, **daemonization**, and the
  **Go rewrite** (port the config table + `TmuxSignaler`).
- **Auto-applying `worker-ready`** (manual by the user for now).
- **Worker push** (commit-only this chunk; human reviews + pushes).
- **Auto-recovery of `worker-stuck` beads** beyond the human clearing the label /
  unclaiming, and worktree reconciliation beyond "reuse if exists".
- **A triager-side ownership walk for the worker route** (the worker SKILL's
  author assertion covers safety for now).
- **pg-pr producer correctness** (cycle reuse, PR-close cascade) — separate
  producer ticket.
- **An explicit producer-stamped routing field** replacing the heuristic
  classification.
- The dispatched-pane `node: command not found` hook error.

## Related

- `docs/superpowers/specs/2026-06-08-pr-feedback-orchestrator-design.md` — step 1
  (taint-free bead access, the nudge/worker contract, dispatch invariants,
  unclaim-on-failure rationale).
- `docs/superpowers/specs/2026-06-09-pr-pool-session-lifecycle-and-dedup-design.md`
  — the session lifecycle this chunk generalizes per role, and the unchanged
  feedback-processor dedup contract.
- `docs/superpowers/plans/2026-06-08-pr-pool-orchestrator.md` and
  `docs/superpowers/plans/2026-06-09-pr-pool-session-lifecycle-and-dedup.md` —
  the shipped plans + live smoke-test findings this builds on.
- `packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-process-feedback/SKILL.md`
  — the (unchanged) feedback-processor contract.
- `docs/adr/0009-pg-pr-bead-schema.md` — bead types, `discovered-from`, cascade.
