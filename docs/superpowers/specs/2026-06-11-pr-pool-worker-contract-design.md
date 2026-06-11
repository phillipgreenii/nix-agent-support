# pr-pool worker contract — design

**Status:** Draft (2026-06-11; reviewed by an adversarial subagent before write-up)
**Date:** 2026-06-11
**Supersedes:** the **worker terminal contract** in
`docs/superpowers/specs/2026-06-09-pr-pool-work-triaging-design.md` (the
two-role triager, per-role config table, discovery, and feedback-processor
contract from that spec are **unchanged**; only the worker role's terminal
signal, SKILL, and failure surface change here).
**Builds on:**

- `docs/superpowers/specs/2026-06-09-pr-pool-work-triaging-design.md` (the
  two-role mechanical triager — shipped + merged to `main`).
- `docs/superpowers/2026-06-10-pr-pool-triaging-handoff.md` (the live-verification
  findings, including the "already-done contract gap" that motivated this).

## Summary

Replace the worker role's loose, **label-based** terminal contract
("commit-but-never-push → swap `worker-ready`→`needs-push` → a human reviews and
pushes → never close") with a strong, **rules-based, status-driven** contract.
The worker becomes an autonomous agent with an explicit permission/prohibition
set; its completion signal is the **bead leaving `in_progress`** (`closed` =
resolved, `open` = progress hand-back), and a single **`human`** label is the
"needs human intervention" surface (replacing `worker-stuck`). The `needs-push`
mechanism is removed.

This **dissolves** the already-done deadlock rather than patching it: when a
`worker-ready` bead's work is already present at branch HEAD, the worker just
**closes** it. There is no special "already-done" state.

## Motivation

The shipped worker contract has no terminal success signal for work that is
**already done**. A worker can't legitimately swap to `needs-push` (no commit),
the SKILL forbids closing, so the orchestrator's `wait_done` polls until
`MAX_WAIT` and falsely stamps `worker-stuck` (verified live against `zr-lweh.4`:
the worker correctly recognized the fix was already at branch HEAD, but had no
clean exit and paused for human input).

Rather than add an "already-done" special case to a deliberately-minimal worker,
this chunk **redefines the worker as a trusted, rule-bounded agent**. A clear
"end by closing or handing back, never dangling `in_progress`" rule makes
already-done a non-event (just close), and the broader rule set (commit, push
when instructed, rebase, `phillipg.`-only, clean working tree) lets the worker do
real end-to-end work instead of stopping at an unpushed commit.

## Worker rules (the contract)

The dispatched worker agent operates under these rules (the SKILL is their
authoritative statement; the orchestrator enforces only the terminal signal):

1. **May create beads** (e.g. split off children) — but created beads must **not**
   carry `worker-ready` and must not be `process-feedback:`-titled, so they never
   auto-dispatch mid-run.
2. **May update the status / labels / metadata** of the bead it owns.
3. **Must claim** the bead when starting (`bd update <id> --claim`).
4. **Must end by either closing it (resolved) or unclaiming + returning it to
   `open` (progress hand-back)** — it must never leave the bead dangling
   `in_progress` on a path it controls. A low-context "commit what I have,
   unclaim, reopen" exit is explicitly allowed (a hand-back note is a _should_,
   not a _must_).
5. **Must never leave a dirty working directory.**
6. **Should not start** work in a dirty working directory.
7. **May commit.**
8. **Must only work on branches starting with `phillipg.`.**
9. **Does not push by default.** It pushes **only when the bead's
   description/instructions tell it to** (push is opt-in per bead; there is no
   separate metadata key or label — the instruction lives in the work text). When
   instructed, it may `git push` or `git push --force-with-lease`.
10. **May rebase** its branch if it believes that helps (e.g. to stay close to
    base). Soft guideline, not a required step.
11. **Must never merge unless fast-forward**; if ff isn't possible, rebase then
    ff-merge. Guardrail, not a scripted step — the PR-worker normally just updates
    its head branch and does not merge.
12. **May `git push` or `git push --force-with-lease`; must never `git push
--force`.** (After a rebase of an already-pushed branch, the re-push is
    `--force-with-lease`.)
13. **Needs-human:** if the worker needs a human to intervene (hard block), it
    adds the **`human`** label and records why. The orchestrator does the same on
    a detected error/failure during the run.
14. **Completion sentinel (optional):** the worker may print a recognizable
    completion sentinel as its final pane output. This is a best-effort
    _secondary_ done-signal; the bead status remains authoritative.

## Label model

| Label            | Meaning                                   | Applied by                                    | Effect on discovery                             |
| ---------------- | ----------------------------------------- | --------------------------------------------- | ----------------------------------------------- |
| `worker-ready`   | dispatch-arming; opt-in to the worker     | a human                                       | **gates** worker discovery (`--label`)          |
| `human`          | needs human intervention                  | worker (hard block) or orchestrator (failure) | **excluded** from discovery (`--exclude-label`) |
| ~~`needs-push`~~ | **removed** (was the old terminal signal) | —                                             | —                                               |

`human` replaces `worker-stuck` as the queryable intervention surface
(`bd list --label human`). Because it is excluded from discovery, a bead needing
a human is never auto-re-dispatched; a human removes `human` (and re-arms
`worker-ready` if appropriate) to retry.

## Terminal contract (state machine)

The orchestrator's worker completion signal flips from **label-present**
(`needs-push`) to **status-left-`in_progress`**:

| Worker outcome                              | Bead end state                                                  | `done_signal worker`                       | Re-dispatched?                                                       |
| ------------------------------------------- | --------------------------------------------------------------- | ------------------------------------------ | -------------------------------------------------------------------- |
| **Resolved** (incl. already-done)           | `closed`                                                        | success (not `in_progress`)                | no (closed)                                                          |
| **Progress hand-back** (low context)        | `open`, assignee cleared, no `human`                            | success (not `in_progress`)                | **yes** — next worker continues (permissive; see Risks)              |
| **Needs human** (hard block)                | `open` (or `in_progress`) + `human`                             | success if not `in_progress`, else failure | no (`--exclude-label human`)                                         |
| **Crash / timeout** (orchestrator-detected) | `in_progress` + `human` (orchestrator-stamped, never unclaimed) | failure (`in_progress`)                    | no (`bd ready` excludes `in_progress` _and_ `--exclude-label human`) |

**Why "not `in_progress`" is a safe success signal:** the only ways a bead stays
`in_progress` are (a) the worker is still working, or (b) the worker crashed —
both correctly read as "not done." A worker that hard-blocks does **not** silently
re-arm itself: it stamps `human`, which both flags it and excludes it from
discovery. This preserves the prior spec's "never a silent black hole" guarantee
(the review's primary concern, B1) without keeping the `needs-push` label.

**Ordering (race-safety):** the worker records its comment/note **first** and
performs the status transition (close / unclaim) **last**, because the transition
_is_ the done-signal and `work_one` `/clear`s the session the instant it fires; a
transition-before-note race would wipe the note.

## Orchestrator changes (`pr-pool.sh`)

`pr-pool.sh` stays **git-free** — every git rule (clean tree, `phillipg.`-only,
push flags, rebase/ff-merge, fetch) lives in the worker SKILL, not the
orchestrator.

- **`done_signal`** — worker branch becomes "bead is **not `in_progress`**"
  (replacing the `bead_has_label "$id" needs-push` check). The feedback-processor
  branch (cycle `closed`) is unchanged.
- **`wait_done` (secondary sentinel, optional):** in the poll loop, an additional
  best-effort check for the worker's completion sentinel in the captured pane may
  short-circuit the wait; the bead status remains authoritative for
  classification. May be deferred to a follow-up if it complicates the
  implementation — the status poll already catches the transition within
  `POLL_INTERVAL`.
- **`wait_done_fail`** — worker branch adds the **`human`** label (rename
  `mark_stuck` → `mark_human`, label `worker-stuck` → `human`), **never
  unclaims**, leaves the bead `in_progress`. Feedback-processor branch (unclaim)
  unchanged.
- **`discover_worker`** — `bd ready --label worker-ready --exclude-label human
--json --limit 0` (the native exclude keeps needs-human beads out of the
  queue). The progress-hand-back loop is intentionally **not** excluded — a
  re-armed `worker-ready` bead is re-dispatched so the next worker continues.
- **`nudge_text_worker`** — full rewrite to the new contract (see SKILL). No
  more "swap `worker-ready`→`needs-push`" / "do NOT push" text.
- **Remove** the now-dead `needs-push` paths: `bead_has_label` / `bead_labels`
  become unused once `done_signal` reads status instead of labels — remove them
  (or keep only if still referenced).

## Worker SKILL rewrite (`pg-pr-work-bead/SKILL.md`)

The SKILL becomes a **rules-based contract**, not a rigid linear script. It
states the rules above and the concrete commands for each terminal path:

- **Lifecycle:** claim → resolve PR/branch bead-first → assert branch is
  `phillipg.`-prefixed and the PR is mine (author-assert as backup) → ensure a
  clean worktree → implement → commit → (push only if the bead instructs) →
  **record note first, then transition last**: `bd close` (resolved) **or**
  `bd update <id> --status=open --assignee=""` (progress hand-back — clears the
  claim exactly like the orchestrator's `unclaim()`; a bare `bd reopen` is wrong,
  it leaves a zombie `open`+assigned bead).
- **Already-done:** verify the work is present at branch HEAD, `bd comment` the
  SHA + why, then `bd close`. No special label.
- **Needs-human (hard block):** unresolvable/closed PR, branch not
  `phillipg.`-prefixed, not my PR, an unrecoverable dirty worktree, or a push
  rejected after one fetch-rebase-retry → `bd comment` why, then `bd update <id>
--add-label human`. Make no further code changes.
- **Working dir:** never leave dirty (commit or intentionally discard);
  on dirty **entry**, recover only if the dirt is attributable to this bead's own
  prior crashed run, otherwise treat as needs-human (don't blind-nuke another
  bead's WIP — the worktree is shared per-PR).
- **Git:** isolated worktree (create or reuse; `git worktree add` refuses an
  already-checked-out branch); fetch + `pull --ff-only` before work; scoped
  `git add -- <files>` (never `-A`); commit referencing the bead + PR; push only
  when instructed (`git push` / `--force-with-lease`, **never `--force`**);
  rebase/ff-merge per the guardrails; push-rejected → fetch, rebase,
  `--force-with-lease` once, else hand-back/needs-human.
- **Boundaries:** one bead per run; touch only files the bead implies;
  worker-created beads must not carry `worker-ready` / `process-feedback:` titles;
  optionally print the completion sentinel as the final action.

## Testing (`pr-pool.bats`)

Net change surface is **larger than the two `wait_done` tests** — `needs-push`
appears in ~5 sites:

- **Rewrite** worker `wait_done` tests: success when the bead is **closed**;
  success when it is **handed back** (`open`, **assignee cleared**); **not** done
  while `in_progress` regardless of labels; failure (pane death / `MAX_WAIT` while
  `in_progress`) → adds **`human`**, **no unclaim**.
- **Rewrite** the two `drain_once` worker tests (they currently stub
  `bd show … labels:["needs-push"]` as the done signal → switch to a status-based
  stub).
- **Rewrite** `nudge_text_worker` assertions to the new contract
  (`phillipg.`-only, push opt-in / `--force-with-lease` / never `--force`,
  clean-tree, close-or-handback, never-leave-`in_progress`) and drop the
  `needs-push` / "do NOT push" asserts.
- **New** tests: `discover_worker` issues `--exclude-label human`; a `human`-labeled
  bead is not discovered; `done_signal worker` true on close AND on
  reopen-with-assignee-cleared; `wait_done_fail worker` adds `human` not
  `worker-stuck`.
- **Regression:** feedback-processor path untouched (`done_signal` default branch
  = cycle `closed`; failure = unclaim).

**Authoritative gate:** `nix flake check` (its `test-pgii-pack-pr-support-bats`
check + stricter `treefmt-check` — run `nix fmt -- <files>` to satisfy it). The
SKILL prose has no bats coverage; the `nudge_text_worker` test covers the
orchestrator side.

## Live smoke (deferred — confirm with the user first)

Exercises the real **commit → push a `phillipg.` branch (when the bead instructs)
→ close** happy path, plus the already-done close and a forced needs-human. This
is **P1 #2** from the handoff. It mutates real `zr` beads and spawns `claude`, and
depends on the shared Dolt server being up on `127.0.0.1:25252` (zr + pg2) — both
**blocked** until that migration settles and the target bead is confirmed.

## Out of scope (additive later)

- **`human`-label auto-recovery / retry policy** beyond a human clearing the label.
- **Re-dispatch-loop guard** for repeated no-progress hand-backs (permissive for
  now; `bd ready` sorts by priority, so revisit if a livelock is observed).
- **A structured per-bead push directive** (metadata key / label) — for now the
  push instruction lives in the bead's work text.
- **Worker push of non-`phillipg.` branches**, PR merging, and N>1 per-role cap
  (the prior spec's deferrals stand; worktree-per-PR reuse means concurrent
  same-PR workers would still collide).
- **Generalizing the `human` label** to the feedback-processor failure path (it
  keeps its unclaim-and-retry semantics).

## Assumptions to verify in implementation

- `bd update <id> --status=open --assignee=""` clears both status and assignee
  in one call (matches the existing `unclaim()`); a `human`-labeled,
  assignee-cleared bead does not resurface in `bd ready --label worker-ready
--exclude-label human`.
- `bd ready` supports `--exclude-label` alongside `--label` (the prior spec
  verified `--exclude-label` exists).
- Every routable worker bead's `metadata.branch` is `phillipg.`-prefixed (else
  the rule-8 guard would abort legitimate work — confirm before making it a hard
  abort vs a skip).
- Capturing the pane reliably surfaces the completion sentinel (if the secondary
  signal is implemented and not deferred).

## Related

- `docs/superpowers/specs/2026-06-09-pr-pool-work-triaging-design.md` — the
  triager + the worker contract this supersedes.
- `docs/superpowers/2026-06-10-pr-pool-triaging-handoff.md` — live findings +
  remaining work (P1 #1 = this; P1 #2 = the deferred live smoke).
- `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh` — the orchestrator.
- `packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-work-bead/SKILL.md` — the
  worker contract being rewritten.
- `docs/adr/0009-pg-pr-bead-schema.md` — bead types, `discovered-from`, cascade.
