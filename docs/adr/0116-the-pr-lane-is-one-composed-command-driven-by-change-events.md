# The PR lane is one composed command driven by change events, and the legacy sync and reconcile lane is deleted

**Status**: Accepted (resolves `pg2-6mo45`; completes the PR half of [0077](0077-entity-change-flow.md); the operator's approval of the design, including the deferral of thread support, is recorded on `pg2-6mo45.2`)
**Date**: 2026-10-30
**Deciders**: phillipg

## Context

Pull request work (anchor, feedback, review, fix-ci and resolve-conflict work items) stopped being
opened or closed on 2026-10-19 at 16:12Z. `pg-desk migrate --cutover` moved the live store to schema
2 between 16:10Z and 16:15Z. That migration drops `interpretation.sync_error` and the `ledger` table,
because [0077](0077-entity-change-flow.md) removes the sync stage that wrote them. The legacy PR lane
was still deployed and still needed both: the `desk-pr` role failed at stage `record_sync_error` on
every run, the `desk-reconcile` role failed with `no such table: ledger`, and the plan-form
`pr-decider` role failed on every PR it could not resolve to a stored entity.

### Root cause

An irreversible, store-wide migration was separated from the lane it breaks, no bead owned "the PR
lane survives schema 2", and nothing mechanical checked it. [0077](0077-entity-change-flow.md)
assumed one stop-the-world release (migrate, ship the PR decider, delete the sync machinery
together). The daemon program then kept the PR lane and struck every PR step from its last phase; on
2026-10-14 the operator ruled "collapse, defer deletions" (`pg2-15ja3`), so the lane stayed until a
decider replaced it. The issue half of the deployment config landed on 2026-10-15 under the order
"migrate the store, then apply", and its scope excluded PR, so nobody checked what the same migration
removes from the PR lane. No test in either repository tied a deployed role to the schema it needs.

### Citation rule

An ADR of this repository cites an owning repository by name unless the target is another ADR in this
directory, names a section by its prose heading, and does not cite an ephemeral spec under the
workspace root `docs/superpowers/specs/`. The PR lane refactor design that this ADR records is such a
spec, so it is not cited by path; this ADR and [0077](0077-entity-change-flow.md) are its durable
record.

## Decision

Fix forward. The PR lane becomes one packaged command, run by one role, driven by change events from
the connector's daemon, and the legacy sync, ledger, reconcile and sweep machinery is deleted.

### The lane as built

- **One composed command, one role.** The `pr-change` role runs `pg-desk pr refresh <id>` and then
  `pg-decider` in one packaged command (ruling 11). `pg-decider` keeps its name and stays one command
  with `plan` and `apply`. The command locks per PR, marks a retryable failure for the router, fails
  closed, and writes one run record (a JSON-lines file, never rotated away before the plan summary is
  consumed).
- **Tracking and watching are separate** (rulings 4, 8, 9, 17). Every PR the connector references is
  kept and refreshed until it closes, then evicted by `terminal_ttl` and `expiry`. A query in the
  connector configuration carries a `watched` flag; `pr.changed` events are emitted only for watched
  PRs, and pg-desk consumes only change events. A PR watched in any way stays watched until it closes.
- **No reconcile and no sweep** (ruling 10). There is no `pr.reconcile` event, no `pr-sweep`, no
  `pr-sweep-full`, no anchor sweep. The revisit conditions are in the behavior docs of pg-desk.
- **Work-item visibility.** The per-PR work-item lookup is an all-status, named query in the
  deployment's connector configuration, filtered to the PR inside the decider. It fails closed only
  when the answer cannot be trusted (truncated, unavailable, or inconsistent with the view); there is
  no size ceiling. V6-4 first set a fixed `work_items_ceiling`; the operator retired it on
  2026-10-30 (pg2-eclw3j.9) once the lookup became per PR, and the decider reports `lookup_items` on
  every run instead.
- **Several review items per PR** (ruling 16): a PR whose head advances with no open, unclaimed review
  item gets one; an existing one has its head updated; a closed item for the same head is never
  recreated. There is no relation link between review items (ruling 18).
- **First apply.** The role runs in `plan` mode first; the operator reviews the recorded plans; one
  flip then switches to `apply` for every PR (ruling 12). The procedure is the operator runbook
  [`docs/runbooks/pg-pr-first-apply.md`](../runbooks/pg-pr-first-apply.md).
- **Rollback.** ADR 0077's consequence "rollback by restoring the store backup" is REPLACED. Fix
  forward rules a store rollback out (ruling 1), and "close the beads created since the flip"
  describes the creates only. `pg-pr-backfill rollback` lists every write since the flip marker by
  timestamp from the decider's audit comments (create, close, reopen, update) and reverts each by its
  inverse, using a tracker before-image taken just before the flip (V6-3). Revert is by write, not a
  whole-item restore, so a change a person made since the flip is left alone.
- **Deleted.** The sync stage, the ledger, `pg-desk reconcile`, the sweep verbs, `run pr`, the legacy
  `sync` configuration keys and home-manager options, the old parity side, the posted-review sidecar
  of the old backend and the old PR backend packages. `run issue` and `run thread` stay (V6-1), and
  `sweep.max_age`, `sweep.max_per_poll` and `sweep.reconcile_age` stay because the non-PR change flow
  still reads them.

### What guards against a repeat

| Guard                                                 | What it prevents                                                                                                                          |
| ----------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| An owner: epic `pg2-6mo45` and its traceability table | "Nobody owned the PR lane surviving a schema change". Every ruling and every source decision maps to a phase or is recorded as dropped.   |
| The verb-exhaustiveness test and the deployment check | A deployed role or query that needs a verb the store's schema no longer supports (the safe-verb list is a file in the package output).    |
| The cross-repo contract test on the rendered roles    | A role's argv or mode drifting from the composed command, and the lane mode key disagreeing with the role mode word.                      |
| Outcome alerts that read the store and the run record | A lane that is wired up but produces nothing: the primary never-hydrated alert, the residual-after-apply, stale-anchor and similar rules. |

The alerts read the outcome (an open anchor that is never hydrated, a stale anchor, a hidden PR with
an open anchor, a tracked set above its limit, no watched PRs) rather than
the machinery.

### Operator rulings recorded

Rulings of the interactive session of 2026-10-20, and of 2026-10-22 (recorded on `pg2-6mo45`):

1. Fix forward; no store rollback.
2. Take the daemon path as quickly as possible; the cutover does not wait for the shadow run.
3. Do the full refactor, no stop-gap code fix; what is constantly failing stays off until expected to
   work.
4. A PR with an open work item stays tracked until that item closes (generalised by 8).
5. Mass adoption of existing anchors and a hydration burst are acceptable.
6. Threads of tracked PRs MUST be supported (departed from for acceptance by V6-1; see below).
7. Keep the existing alerts, remove the four `sync_error` alerts with the lane, add the outcome
   alerts; dashboards updated in the same change.
8. Every PR the connector references is kept and refreshed until it closes.
9. Tracking and change events are separate; nothing is watched by default; a query flag or a call-time
   option opts in; `pr.changed` only for watched PRs.
10. Drop reconcile; no sweeps until problems appear without them.
11. One role, `pr-change`, one packaged command; `pg-decider` unchanged.
12. The first flip replaces the staged apply (plan first, review, then apply); no stage selector, no
    suppression switch, no distinct-actor tooling, no creates cap; rollback refined by V6-3.
13. The flip MAY precede the hoist consumers and the daemon cutover; the gap for a PR that left both
    old queries while open is accepted.
14. Threads: existing thread-to-PR links first, a thread feed second (deferred with V6-1).
15. The tracked-set alert threshold starts at about twice the post-backfill count and is revisited
    with data.
16. Several `review-pr` items per PR over time, as above.
17. Open-anchor PRs are marked watched directly at cutover; no per-run re-assert.
18. No relation link between review items; the tracker query finds them.
19. No independent canary; the decider's decisions are exported as a metric with a panel and no alert
    until a silent-empty result is observed.

### Design calls of the v6 revision

These were recommendations of the design's author, accepted by approving the design (`pg2-6mo45.2`).

| Id    | Call                                                                                                                                                                                                                  |
| ----- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| V6-1  | P7 (thread support) is DEFERRED out of the program's acceptance: no decider or interpretation rule consumes thread data. The follow-up bead is `pg2-9eec8`. `run thread` and `run issue` stay.                        |
| V6-2  | The `migrate --cutover` preflight is dropped: the verb is idempotent and does nothing at schema 2. The exhaustiveness test, the safe-verb list, the deployment check, the `sync.mode` pin and the lane key stay.      |
| V6-3  | Rollback is a by-timestamp listing of every write since the flip marker, not only closing created beads.                                                                                                              |
| V6-4  | The work-item lookup is an all-status named query, filtered in the decider; its ceiling was retired (pg2-eclw3j.9).                                                                                                   |
| V6-5  | The router attempt key: the sequence, else the integer value of the item id's `@<12 hex>` suffix, else no `--from-item`; 0 is never passed. `retry_exhausted` is dropped.                                             |
| V6-6  | The failure-rate rule keeps its `upstream-killed` exclusion; no scoped exclusion for `pr-change`.                                                                                                                     |
| V6-7  | The daemon cutover resets the new consumer with a replay after the marking step; the old ledger cursor cannot carry over.                                                                                             |
| V6-8  | The lane state lives in pg-desk's existing key and value table, with a single-statement atomic counter, never a schema bump.                                                                                          |
| V6-9  | The `mark-watched` mode moves to the daemon cutover phase, because it needs the connector's watch read.                                                                                                               |
| V6-10 | The connector-side flip to emission by watched PRs and delivery by the flag takes two deployment applies (declare watched while the modes are legacy, then flip once every consumer is at or past the recorded head). |
| V6-11 | The PR rule conditions are promoted into the decider's work-item behavior doc, and the README's single-home statement names it, because the entity-change-flow design is an ephemeral spec.                           |
| V6-12 | There is no standing stream-silence alert on the run record; the plan summary's coverage check detects the plan period and the outcome alerts read the store.                                                         |

## Consequences

### Positive

- The PR lane has one owner, one command and one set of outcome alerts. A schema change that breaks
  a deployed role fails a check before an operator runs it.
- Rollback is rehearsed by an automated end-to-end drill against a real tracker and the real composed
  command, not by restoring a store.
- The legacy lane's failure modes (second-error masking, the dropped ledger) cannot recur because the
  code is gone.

### Negative

- Thread support for tracked PRs is not delivered. V6-1 departs from ruling 6 ("MUST be supported");
  `pg2-9eec8` holds the follow-up and is deferred until the stateful connector redesigns land and the
  operator names a consuming rule.
- No reconcile and no sweep: if the daemon misses a change, nothing re-asserts it (risk R5). The
  stale-anchor, hydrate dead-man, store-aside and no-watched-PRs alerts are the detectors.
- History: while the lookup read the whole all-status set on every run, a fixed ceiling
  (`work_items_ceiling`) WILL have been reached as closed items accumulate. The per-PR query removes
  that growth, and the ceiling and its alert (`pg-desk-pr-lookup-ceiling`) were retired
  (pg2-eclw3j.9); an abnormal per-PR item count shows in `lookup_items`, without stopping the lane.
- Several review items per PR can waste a review on an old head (risk R7).

### Neutral

- Alert thresholds of the tracked-set alert are provisional (ruling 15) and are revisited with data.
- The consolidated runbook keeps the file name `pg-pr-first-apply.md` because a test reads it.
