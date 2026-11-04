# Plan: pg-claude-code-recorder phase 2, event-driven settle

Repo root: `/Volumes/gitrepos/phillipg/nix-agent-support` (public repo: no employer-specific detail). Package:
`packages/pg-claude-code-recorder`. Spec: `docs/superpowers/specs/2026-10-30-pg-claude-code-recorder-design.md`.
Earlier plans: `docs/superpowers/plans/2026-11-03-recorder-phase0-cpu-and-observability.md` and the phase 1 plan
(`2026-11-04-recorder-phase1-observability-completeness.md`, which also holds the re-ranking of all six phases).
Paths below are relative to the package unless they start with `docs/` or `grafana/`.

## Why (measured, post phase 0, build `0.0.0-8ab32e31`, 2026-11-04)

- `Settler.Tick` is still 39.0% of profile samples (about 0.17 cores), with `changedSinceSettle` 18.5%,
  `store.Generation` 13.5% and `store.files` 10.1%. Settle tick p99 is about 59 s in a 500 s window (lifetime p99
  about 107 s); the last tick took 37 s, so ticks still run most of the minute and the 60 s interval is nearly
  saturated. The store has about 58.5k files and 7.8k settled conversations.
- The shape of the cost: every minute `Tick` calls `groups()` (reads every file and every path), then
  `settledPrefilter` (one read transaction: every stage row, then for each of about 7.8k settled conversations a
  manifest decode plus one `Generation` read per file), then `evaluate` for each non-settled conversation, then
  `refresh` (stages, all generations, all archive ranges, torn tails, all files again), then `refreshRemovalGauges`.
  Nothing in this work depends on whether anything changed since the last tick.
- A conversation settles only after a quiet period (`settle.after`) with no write, so the set that can change
  state in a minute is the conversations that were written to since the last tick, plus those whose quiet deadline
  falls in the minute, plus held conversations at their recheck time. That set is tiny against 7.8k.

## Scope

In scope: replace the per-minute full scan with a dirty set plus a due-time schedule; maintain stage counts and
held and pending-byte figures incrementally; move `refreshRemovalGauges` off the per-tick path; state the settle
cadence in the spec. Out of scope: gauge refresh outside the settler (phase 3), the sweep (phase 4).

Behavioural contract (MUST NOT change): which conversations settle, when, with what manifest; the stage machine and
its events (`stage_changed`, `conversation_settled`); `held` reasons and `held.recheck`; removal rules. The settle
decision is a pure function of persisted state and the clock, so the event-driven version MUST reach exactly the
same decisions as the full scan. That equivalence is the central test (T3).

## Ground rules

Inherited from the phase 0 plan: real SQLite and files, read-count assertions through the counting driver (never
wall-clock), injected clock, a metric is a constant plus a catalogue line, no bead id in the repo, no new
dependency, no slow test in the unit gate (V-3, V-4), unit gate timeout never raised. Run Go tests as `nice -n 10 go
test -p 4 ./internal/<pkg>/...` (`-race` for any task that adds a goroutine or shared state).

Additional: the safety net MUST remain. A full reconciliation scan runs at start and every `settle.reconcile`
interval (default 1 h, a new config key), so a missed event can delay a settle by at most that interval and never
loses it. The reconciliation is the old `Tick` body unchanged, which also makes it the oracle in T3.

## Design (Observer plus a due-time queue)

State, all in memory, rebuilt by the start-up reconciliation (no schema change):

- `dirty`: a set of session names. Fed by the copier after each commit that changes a file (the `Notify(fileID)`
  seam the parser already uses), mapped from file id to session through a `fileSession` map the settler maintains
  from `groups()` (the same `SessionOf` rule). A file not yet in the map causes a one-off `groups()` refresh.
- `due`: a min-heap of `(deadline, session)`. A conversation that is not settled gets a deadline of
  `newest write + settle.after`; a held conversation gets its in-memory `checkedAt + held.recheck` (the code keys the recheck on `checkedAt`, set on every evaluation and lost on restart, where a zero value means evaluate immediately; the start-up reconcile reproduces that); a settled conversation has
  none until it is dirtied; a live-process conversation is re-queued when its process exits (the tracker already
  tells the engine; `Live()` is polled once per wake, not per session).
- `Wake`: the loop sleeps until the earliest of: a dirty-set notification, the head of `due`, the reconcile timer.
  A wake processes the dirty sessions and every due session, then publishes metrics. The loop never wakes more than
  once per `settle.min_wake` (default 5 s) so a burst of writes coalesces.
- Stage counts: a `map[stage]int` updated on each transition (the settler already knows prior and next stage in
  `evaluate` and `TickStats.add`); the gauges are set from the map at the end of each wake, not recomputed by
  scanning every group. Pending bytes and held ages are maintained per session in the same pass.

The one-minute `time.NewTicker` in `settleLoop` is replaced by this loop; `runTick` keeps its metrics but its
`examined` counter now means sessions processed in the wake, so the existing `settle_tick_duration_seconds` is
renamed in meaning only (its help text changes); the dashboards' threshold line at 60 s is replaced by one at the
`settle.min_wake`-relative value in T6.

## State inputs the event-driven version MUST cover (review findings, folded in)

The settle decision is NOT a pure function of persisted state and the clock; the full scan reads these inputs
implicitly, so each needs an explicit source in the event-driven loop, a test, and a negative control:

1. Quiet deadline source: `newest` is the original file's mtime (through `cfg.Mtime`) falling back to the recorded
   last-seen times. An mtime change with no copier commit moves the deadline and sends no `Dirty`. The loop MUST
   re-derive `newest` when a heap entry pops and re-push if the deadline moved, never trust the stored deadline.
2. Low-space mode: `checkLow()` flips the gate that bypasses `settle.after` for quiet and held conversations. A
   flip into low space MUST enqueue every quiet and held session at once; a flip out MUST re-derive deadlines.
   `checkLow` runs once per wake and on a timer (see 4).
3. Live-process entry and exit: a session becoming live (a registry file appears) clears its stage with reason
   `process` and sets the gauge, with no write event; an exit makes it evaluable. The loop MUST subscribe to the
   tracker's engine change (add a `Settler.LiveChanged()` callback from the lifecycle engine, an explicit task in
   T1, not assumed) and, as a fallback, poll `Live()` every `settle.min_wake`.
4. Timer sources besides `due`: the `checkLow` poll, the `Live()` fallback poll, removal due times and the
   reconcile timer. The loop sleeps until the earliest of them all.
5. Removal pass: `Tick` runs `removeLocked` every tick when removal is enabled. Removal eligibility is `newest +
remove.after` (`blockers` returns the next deadline). The wake MUST run `removeLocked` when a removal deadline is
   due (deadlines pushed into `due` with kind `removal`) and on every reconcile, and MUST re-run `groups()` and
   `Live()` after it as `Tick` does. The oracle fixture includes conversations at each removal state.
6. Events: `stage_changed` events today come from `refresh` diffing `s.last` over all groups (not from
   `evaluate`). T4 replaces that loop, so `transition()` MUST emit the event, carry the `s.reasons` hand-off, and
   apply the same first-seen defaults (quiet, or live when live). Sessions with persisted `removal_pending` or
   `removed` stages and no files are counted from the stage rows, as `refresh` does; T4 maintains those counts too.
7. Dirty callers: besides `afterIngest` (skipped when `res.File == 0`), `Dirty` MUST be called from the backfill
   batch path, tombstones, snapshot-generation changes and the settler's and doctor's own `NewGenerationLocked` and
   archive writes (`flushArchive`, `reclaim`). The task lists each call site found with `rg -n 'Commit\(|
NewGenerationLocked' internal/copier internal/settle` and tests each. `Dirty(file, copyOffset)` carries the
   offset so pending bytes can be maintained.
8. Start sequencing: the first wake runs after the first parse completes, as `settleLoop` does today.

The reconcile holds `s.run` like `Tick`, so a wake waits for a running reconcile (hourly, up to a tick's duration);
the README says so. Reconcile healing is metered: `settle_reconcile_rescued_total` (sessions whose stage or
counts changed during a reconcile that no event caused) and `settle_counts_drift_total` (counter, as well as the
WARN). New gauges: `settle_dirty_sessions`, `settle_due_queue_depth`, and a counter `settle_wakes_total{reason}`
with `reason` in `dirty due reconcile poll`. T6 lists the existing `runtick_test.go` assertions on `examined` that
change meaning.

Oracle tie-break: events within one simulated minute are applied in session-name order on both sides; compare
final persisted stages and the multiset of `stage_changed` events. `checkedAt` is part of the comparison fixture
(cleared on a simulated restart). Added negative controls: ignore the low-space flip, ignore an mtime-only change,
drop a `Dirty` feed, clear `checkedAt` without reconcile. Also added oracle cases: removal firing, `path_reappeared`
(removed to quiet), settled-then-mode-change. The count test's bound of 20 queries is derived, not arbitrary: it is
the measured wake cost (one dirty session) plus a margin written in the test with the measured value, and the test
also asserts the scan path exceeds the bound by 100x so the assertion stays meaningful.

Dashboard note: the `settle tick duration` panel line stays at 60 s while the scan reconcile exists; T6 adds a
second line for the wake budget (`settle.min_wake`), replacing the inconsistent wording above. `settle.mode` can be
switched only by restart (stated in the README).

## Task list and order

- T1 (M): the dirty-set feed and the session map (no behaviour change; unused until T3).
- T2 (M): the due-time heap, with its own unit tests.
- T3 (L): the event-driven loop, behind `settle.mode = "scan" | "event"` (default `scan` in this task), with the
  equivalence test against the scan oracle.
- T4 (M): incremental stage counts and gauges; `refresh` stops scanning every group on a wake.
- T5 (S): `refreshRemovalGauges` off the per-wake path (run it on its own 5-minute cadence and on a removal event).
- T6 (S): `settle.reconcile`, `settle.min_wake`, `settle.mode` config keys, README, spec text, dashboard threshold.
- T7 (S, gated follow-up): flip the default to `event`, verify after apply, then remove `scan` mode in a later
  bead if the verification holds for a week.

Dependencies: T1, T2 independent; T3 after T1, T2; T4 after T3; T5 after T3; T6 after T3; T7 after T3, T4, T5, T6 and
is gated on their apply (`pb:pb-gate-lifecycle`, keyed on patch-ids). T3 touches `internal/settle/settle.go` and
`internal/serve/daemon.go`; so does phase 1's T2 (loop wrapper): land phase 1's T2 first.

## T1. Dirty-set feed

Files: `internal/settle/settle.go` (new `Settler.Dirty(file int64)`), `internal/serve/daemon.go` (`afterIngest`
calls it next to `d.prs.Notify`), `internal/copier` unchanged. `Dirty` is non-blocking and idempotent: it looks up
the session through the `fileSession` map and inserts into a mutex-guarded set, signalling a one-slot channel.
Tests: `Dirty` on a known file adds exactly its session; an unknown file triggers one `groups()` rebuild and then
resolves; ten calls for the same session leave one entry; concurrent calls under `-race`. The counting driver
asserts a `Dirty` on a known file issues zero store queries.

## T2. Due-time heap

Files: new `internal/settle/due.go`. Operations `Push(session, at)`, `Pop(now) []string`, `Next() (time.Time, bool)`,
and replace-on-push so a session has at most one entry. Pure data structure, injected times. Tests: ordering,
replace, pop-all-due, empty, duplicates collapse, a million-push benchmark gated behind a build tag is NOT added.
Negative control: pop with `<` instead of `<=`.

## T3. The event-driven loop and its oracle

Files: `internal/settle/settle.go` (`Wake` next to `Tick`), `internal/serve/daemon.go` (`settleLoop`).

`Wake(now)` evaluates the union of dirty sessions and due sessions with the existing `evaluate` (unchanged), then
re-derives each processed session's next deadline. `Tick()` stays and becomes the reconcile.

Oracle test (the main test of the phase): build a store fixture of 200 conversations in a mix of states (live, quiet
with various ages, held for each reason, settled-unchanged, settled-then-appended, removed). Drive two settlers
over copies of the same store with a scripted event sequence on an injected clock (writes, process exits, clock
advances past `settle.after` and `held.recheck`): one calls `Tick` every simulated minute, the other `Wake` on
events and due times. After every simulated minute assert equal persisted stage rows and equal emitted
`stage_changed` events (as sets keyed by session with times compared at minute granularity: the event-driven
version MAY act earlier than the next minute boundary, never later). A property-style test repeats this with a
fixed set of seeded random scripts (seeds listed in the test, not generated at run time). Negative controls: drop
the due-heap push for held conversations (the test must fail); ignore `Live()` exits (must fail).

Count test: with 8,000 settled-unchanged conversations and one dirtied, `Wake` issues a number of store queries
that does not depend on 8,000 (assert an absolute bound of 20 queries, and assert the scan `Tick` on the same
store issues more than 8,000 rows read, so the test fails if the bound ever stops being meaningful).

Done when: oracle and count tests pass with `-race`.

## T4. Incremental stage counts

Files: `internal/settle/settle.go` (`refresh`), `internal/obs`. `refresh` currently reads every stage, every
generation, every archive range, torn tails and all files on each tick. New: a `stageCounts` structure updated by
`transition(session, from, to)`; per-session pending bytes updated when the copier commits (`Dirty` carries the
new copy offset) and when an archive range is written; `bytes_pending_settle`, `held_conversations`,
`oldest_held_age_seconds` and `torn_tail_age_seconds` are derived from the maintained maps. The reconcile
recomputes them from scratch and logs a WARN `settle_counts_drift` with both values when they differ (the drift
check is the safety net and the test oracle). Test: after the oracle script, incremental gauges equal the
from-scratch values for every stage; injected drift (corrupt one counter) is detected and healed by the next
reconcile and logs once. Count test: a wake with one dirty session issues no `AllGenerations`, `AllRanges` or
`AllFiles` query (counting driver, by statement text).

## T5. Removal gauges off the wake path

Files: `internal/settle/remove.go` (`refreshRemovalGauges`). Run it on a 5-minute timer and after any removal,
quarantine restore or doctor run, not on every wake. Test: two wakes within a minute with no removal event call it
zero times; one removal event calls it once; the gauges after a removal equal the old per-tick values (golden
comparison against the scan path on the same fixture).

## T6. Config, docs, dashboard

Config keys `settle.mode`, `settle.reconcile` (default 1h), `settle.min_wake` (default 5s) in `internal/config`
with validation (reconcile at least 1 minute, min_wake at least 1 second); the Nix module option and the rendered
`config.toml` (the module's build-time check runs the real binary; extend `tests/pg-claude-code-recorder-hm-
launchd.nix` for the new keys). Spec: replace "the settle cadence (a hard-coded one minute) is not specified"
with the dirty-set, due-time and reconcile description and the equivalence requirement. README: settle cadence
paragraph, runbook entry "a conversation did not settle when expected" (`where <session>`, then check
`settle.after`, then the next reconcile). Dashboard: the `settle tick duration` panel's 60 s line becomes the
`settle.reconcile` budget; `settle tick work` renamed in its description to "sessions processed per wake".

## T7. Flip and verify (gated)

After T3 to T6 are applied: set the default to `event`. Verify (record as a comment): CPU cores over one hour
(target: `Settler` below 5% of samples in a 30 s profile; the phase 0 figure was 39%), wake duration p99 under
5 s, no `settle_counts_drift` WARN, and `conversations_by_stage` equal to the same query run by the `scan` reconcile.
If any check misses, set `settle.mode = "scan"` in the host config (no rebuild of the index, no data change) and
file a bead with the profile.

## Risks and rollback

- A missed event delays a settle: bounded by `settle.reconcile`; alert on `loop_in_flight_seconds` and on the
  reconcile's drift WARN (phase 1 metrics).
- Memory: the dirty set and heap hold session names only (about 7.8k entries, under 1 MB).
- Rollback is the config key, then a revert; no schema, wire or data change.
- The stage machine is untouched, so the equivalence test is the whole behaviour guarantee: if it cannot be made to
  pass for some state, stop and report instead of loosening it.

## Proposed bead breakdown

Epic with seven children T1 to T7 and the edges above. Sizes: T1 M, T2 M, T3 L, T4 M, T5 S, T6 S, T7 S (gated
verification). Labels `agent-support`, `pg-claude-code-recorder`. T3 is the only task that SHOULD be given to a
strong model; T1, T2, T5, T6 are routine.
