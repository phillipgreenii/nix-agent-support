# pr-pool spec C Phase 2 — extract the `Executor` interface

**Status**: Accepted
**Date**: 2026-06-18
**Deciders**: Phillip Green II (with Claude)
**Beads**: `pg2-hnz7` (extract Executor), folds in `pg2-kj7j` (report failure-verb fidelity)
**Supersedes/extends**: `docs/superpowers/specs/2026-06-16-pr-pool-externalize-roles-prompts-queries-toml-design.md` §5/§10 (Phase 2 was deferred there)

## Context

Spec C Phase 1 (`pg2-kplb`, merged) made roles/queries config-driven via TOML and
dispatched per-role with a **thin `switch role.Type`** inside
`orchestrator.workOneWithID` (`runCCPool` / `runCommand`). Phase 1 deliberately left
the `pg2-c1vp` watchdog/single-terminal race code **inline and untouched** in the
orchestrator.

Phase 2 extracts the dispatch mechanics behind an `Executor` interface so the
race-critical code lives in one cohesive unit with one clear purpose, the orchestrator
shrinks to its drive loop (discover → drain → teardown + result assembly), and command
vs ccpool dispatch are independently testable concrete types.

This is a **pure structural refactor** — no bead-mutation / dispatch-semantics change.
The one deliberate behavior delta is the narrow **`pg2-kj7j`** report-fidelity fix
(failure _verb_ only; bead mutations are already correct).

### Constraints discovered while exploring the Phase 1 code

- The bead phrases the interface as `roles.Executor`, but `DispatchContext` lives in
  `internal/discover`, and `discover → roles`. Declaring the interface in `roles` with
  a `DispatchContext` parameter would make `roles → discover → roles` — an **import
  cycle**. (The original spec's §10 DAG predates Phase 1, which placed `DispatchContext`
  in `discover`.)
- The race tests (`TestWaitDone_*`, incl. both `TestWaitDone_lostRace_*`) and
  `TestActive_stateMapping` are **white-box** (`package orchestrator`) and call
  `o.waitDone(...)` / `o.active(...)` directly. Wherever those methods land, the call
  sites are affected.
- The golden test (`TestGolden_workerDispatchShape`) exercises only role config +
  prompt rendering — it is **not** touched by the dispatch move.
- `RunOne` resolves the per-attempt `externalID` **once** (single `stamp()` call) and
  reuses it for both `Ensure` and the deferred teardown `Close`
  (`TestRunOne_feedbackClosesSession` asserts the exact stamped id is closed). That
  single-stamp invariant must survive the move.
- Today the dispatch returns only `error`; `buildResult` _separately_ computes the
  whole `report.Result`, deriving the failure verb from `role.CCPool.OnFailure`. That
  is the `pg2-kj7j` bug: on ensure-fail the action is launch-fail-escalate, on send-fail
  it is `OnDispatchFail` — neither necessarily matches `OnFailure`.

## Decision

### 1. New package `internal/executor`

The `Executor` interface, the `Deps` seam bag, and the concrete `ccpoolExecutor` /
`commandExecutor` types all live in a new `internal/executor` package that imports
`discover` for `DispatchContext`. This sidesteps the cycle (no `DispatchContext`
rename) and delivers the spec's "concrete executors in their own downward-importing
package" intent. The interface is `executor.Executor`, not `roles.Executor`.

```go
package executor

type Executor interface {
    Dispatch(ctx context.Context, d discover.DispatchContext, deps Deps) (report.Result, error)
}

// Deps is the seam bag the executor needs from the orchestrator. Fields are
// EXPORTED (cross-package) — unlike today's unexported orchestrator fields.
type Deps struct {
    CC          ccpool.Runner
    BD          beads.Runner
    Cmd         query.Commander
    Log         *eventlog.Writer // may be nil (no-op)
    Cfg         config.Config
    Now         func() time.Time
    Tick        func(context.Context, time.Duration) error
    Stamp       func() string
    UsageReader usage.Reader
    ExternalID  string // resolved ONCE by the orchestrator (see §5)
}
```

`ccpoolExecutor{}` and `commandExecutor{}` are stateless value types; the orchestrator
selects one by `role.Type` and calls `Dispatch`. The nil-default accessors that today
live on `*Orchestrator` (`clock`/`waitPoll`/`reader`/`commander`, the `Now`/`Tick`/
`Stamp`/`UsageReader` defaulting) move to `Deps` methods so the defaulting behavior is
preserved **verbatim** (a nil `Now` still falls back to `time.Now`, a nil `Tick` to the
`select{<-ctx.Done()|<-time.After}` poll, etc.).

**Import DAG (acyclic):**

```
executor → discover, roles, report, ccpool, watchdog, complete,
           beads, budget, prompt, config, usage, eventlog, query
orchestrator → executor, discover, roles, report, ccpool, complete,
               eventlog, config, beads, usage, ...
```

Nothing in the `executor` closure imports `executor`, so there is no cycle. `report`
remains a pure-value leaf.

### 2. Code that moves into `internal/executor` — verbatim vs. modified

All of `runCCPool`, `workerWaitWithWatchdog`, `waitDone`, `renderNudge`,
`escalateLaunchFailure`, `fail`, `active`, `budgetUnlimited` move into
**`ccpoolExecutor`**; `runCommand`, `renderArgv` move into **`commandExecutor`**. But
the move is two distinct categories, and the "verbatim" claim applies only to the first:

- **Moved byte-for-byte (the `pg2-c1vp` race machinery):** `waitDone` and
  `workerWaitWithWatchdog` keep their **`error`-only return signatures** and every line
  of the atomic-owner-claim logic (`owner atomic.Bool`, `claimTerminal`, the buffered
  `done` channel, `cancel()`/drain ordering, every `won()`/`lose()` gate). `active`,
  `budgetUnlimited`, `renderNudge` likewise. Only the receiver changes
  (`*Orchestrator` → `*ccpoolExecutor`) and field refs rebind (`o.CC` → `deps.CC`,
  `o.clock()` → `deps.clock()`, …). **This is the load-bearing race code; it must not be
  re-shaped.**
- **Moved AND modified (the `pg2-kj7j` failure-verb plumbing, §4):** `escalateLaunchFailure`
  gains a **`bool` return** (did it escalate to `human`, vs. only label?). The
  `Dispatch` wrapper (today's `runCCPool` body) is the one place that changes shape: at
  each failure return it attaches the `report.Action` for the verb actually taken (see
  §4 for the derivation). `fail`, `waitDone`, and `workerWaitWithWatchdog` are **not**
  re-signatured for this — the verb is derived in `Dispatch` from the returned error +
  role config (§4), so the race code stays verbatim.

### 3. Code that **stays** in `internal/orchestrator`

`DrainOnce`, `drain`, `RunOne`, `workOne` / `workOneWithID`, `snapshotIDs`,
`createdByActor`, `buildResult`, `emitResult`, `teardownAll`, `queryEnv`, `gated`,
`fileExists`, `actorOf`, `beadRefs`. `workOne` / `workOneWithID` become the thin seam:
build `Deps` from the orchestrator, select `ccpoolExecutor{}` / `commandExecutor{}` by
`role.Type`, and call `Dispatch`.

**Return-type ripple (required, not optional):** today `workOne` / `workOneWithID`
return `error`; they must change to **`(report.Result, error)`** so the executor's
`Result` reaches `buildResult` for the §4 merge. This ripples to the two call sites:
`drain` (`orchestrator.go:153`) and `RunOne` (`:137`), which already call
`buildResult` right after — they now pass the executor's `Result` in instead of
recomputing the failure verb. `error` still independently drives the complete/flagged
tally (`drain:154`) — unchanged.

### 4. `report.Result` integration + the `pg2-kj7j` fix

Key insight: **the executor only ever performs _failure_ actions on the bead**
(unclaim / add-human / launch-fail label). Bead _closes/hand-backs_ and _created_
sub-beads are performed by the worker session and merely **observed** by the
orchestrator (post-status read + pre/post snapshot diff). So:

- `Dispatch` returns the **failure action it actually performed** in `report.Result`
  (empty `Actions` on success).
- `buildResult(ctx, role, d, pre, preOK, execRes, err)` merges:
  - snapshot diff → `Created` / `Indeterminate` — **unchanged**
  - `err == nil` → post-status read → `Closed` / `HandedBack` — **unchanged** (already
    correct today)
  - `err != nil` → use **`execRes.Actions`** for the failure verb, replacing the
    `switch role.CCPool.OnFailure { … }` guess.

Failure-verb mapping (the `pg2-kj7j` fix). The "today" column is what `buildResult`
emits now (the bug: it keys off `role.CCPool.OnFailure` for **any** `dispatchErr != 0`,
so it mis-reports the launch-fail / dispatch-leave / budget cases); "after" is the
verb matching the action actually taken:

| failure path                          | bead action actually taken        | today (buggy)      | after (this fix)          |
| ------------------------------------- | --------------------------------- | ------------------ | ------------------------- |
| ensure-fail, 1st time                 | `pool-launch-fail` label only     | `OnFailure`-verb   | _(none)_                  |
| ensure-fail, repeat                   | add `human`                       | `OnFailure`-verb   | `Escalated`               |
| send-fail, `on_dispatch_fail=unclaim` | unclaim                           | `OnFailure`-verb   | `Unclaimed`               |
| send-fail, `on_dispatch_fail=leave`   | nothing                           | `OnFailure`-verb   | _(none)_                  |
| waitDone fail (timeout / death)       | `complete.OnFailure(OnFailure)`   | `OnFailure`-verb ✓ | `Unclaimed` / `Escalated` |
| **watchdog hard-stop wins** (budget)  | `watchdog.terminal` → **unclaim** | `OnFailure`-verb   | `Unclaimed`               |

The last row is a **distinct third failure path**: when the budget watchdog wins the
single-terminal race it unclaims the bead in `watchdog.Run` (`watchdog.go:80-83`,
always an unclaim), and `workerWaitWithWatchdog` returns `watchdog.ErrBudgetExceeded`.
This is NOT the `waitDone`-fail path, and today's `OnFailure`-verb guess mis-labels a
worker (`OnFailure=add-human`) budget stop as `Escalated` when it was actually unclaimed.

**Derivation mechanism (in `ccpoolExecutor.Dispatch`, no race-code re-signaturing):**

- ensure-fail → `report.Action{Escalated}` iff `escalateLaunchFailure` returned
  `true` (escalated to human); else no action.
- send-fail → `report.Action{Unclaimed}` iff `cc.OnDispatchFail == DispatchUnclaim`;
  else no action.
- wait-fail → if `errors.Is(err, watchdog.ErrBudgetExceeded)` → `Unclaimed` (watchdog
  unclaimed); else map `cc.OnFailure` → `Unclaimed` (unclaim) / `Escalated` (add-human),
  matching what `complete.OnFailure` applied inside `fail`.

This stays inside the **closed verb vocabulary** (`Created`, `Closed`, `HandedBack`,
`Unclaimed`, `Escalated`, `Indeterminate`) — no new verb. "(none)" is the faithful
report for a label-only flag or a leave: nothing terminal happened to the bead's
claim/human state, so we must not claim `Escalated`/`Unclaimed`.

`commandExecutor.Dispatch` returns an **empty `report.Result{}`** on both success and
failure: a command role performs no bead mutation (`runCommand`), and `actorOf` is `""`
for command roles so the snapshot diff finds nothing. The `buildResult` merge for a
command role is therefore a no-op on the executor's side (created/indeterminate from the
snapshot still apply).

### 5. `ExternalID` threading

The orchestrator resolves `externalID` **once** (single `Stamp()` call) per dispatch
and sets `Deps.ExternalID`. `ccpoolExecutor.Dispatch` uses `deps.ExternalID` for
`Ensure`; `RunOne` reads the same value for its deferred teardown `Close`. The stamp
cannot drift between ensure and close, preserving `TestRunOne_feedbackClosesSession`.
(The drain path tears down by prefix in `teardownAll`, so it is unaffected either way.)
`Deps.ExternalID` is consumed **only by `ccpoolExecutor`**; `commandExecutor` ignores it
(command roles launch no ccpool session), so no `Stamp()`/`Ensure` wiring leaks into the
command path.

### 6. Test strategy

- **Move into `package executor` (they call executor-internal `waitDone`/`active`
  directly):** **all 14** such tests, not just the `lostRace` pair —
  `TestWaitDone_workerCloses`, `_workerHandbackToOpen`, `_workerTimeoutAddsHumanNoUnclaim`,
  `_paneDiesAsBeadCloses_success`, `_paneDiesStillInProgress_failure`,
  `_feedbackTimeoutUnclaims`, `_transientStatusErrorKeepsPolling`, `_ctxCancelDoesNotFail`,
  `_ctxCancelledBeforeDeathPathNoFail`, `_lostRace_deathPathNoFail`,
  `_lostRace_openNotReportedSuccess`, `_workerDoneStopsFast_failure`,
  `_feedbackDoneStopsFast_unclaims`, `_doneStopsFast_successRace`,
  `_needsInputWaitsUntilMaxWait`, plus `TestActive_stateMapping`. Test _logic_ is
  unchanged; call sites become `exec.waitDone(...)` / `exec.active(...)` on a
  `ccpoolExecutor` built with a `Deps`. This requires a **`newExec(...)` helper** in the
  executor test package (parallel to `newOrch`) that builds `ccpoolExecutor{}` + a `Deps`
  with the injected `manualClock` clock/tick, fixed `stamp`, and fakes — the moved tests
  cannot use `newOrch` (no `*Orchestrator`).
- **Stay in `package orchestrator` (they go through `workOne`/`drain`/`RunOne` — now
  integration tests across the orch↔executor↔watchdog seam):** `TestWorkOne_*` (incl.
  the budget tests `TestWorkOne_workerSuccessWithWatchdogArmed`,
  `TestWorkOne_workerBudgetHardStopUnclaimsNoHuman`, which drive
  `workOne → Dispatch → workerWaitWithWatchdog` and set `usageReader`/`BudgetTokens`),
  `TestStuckBead_*`, `TestRunOne_*`, `TestDrainOnce_*`, `TestDrain_*`,
  `TestTeardownAll_*`, `TestCreatedByActor*`. The **golden test is untouched**.
- **Shared fakes:** extract `fakeCC`, `scriptBD`, `manualClock`, `rampReader` (and the
  `hasUpdate`/`contains`/`join` helpers, `testStamp`) into a new **`internal/dtest`**
  package as exported types (`dtest.FakeCC`, `dtest.ScriptBD`, …), imported by both test
  packages. Only the identifiers change (`fakeCC` → `dtest.FakeCC`); the fake behavior
  is unchanged.

### 7. Verification gates

- `go test ./...` and `go test -race ./...` green (the race tests + golden are the
  behavioral guard that the verbatim move did not regress `pg2-c1vp`).
- `nix build .#pr-pool` green.
- `nix flake check` green.
- `prek run --all-files` (or `pre-commit run --all-files`) green.

## Consequences

### Positive

- The race-critical dispatch mechanics live in one cohesive package with one purpose;
  the orchestrator file shrinks to its drive loop + result assembly.
- `pg2-kj7j` is fixed as a natural by-product: the failure verb now reports what the
  executor _did_, not what `OnFailure` _would_ be.
- `ccpool` and `command` dispatch are independently testable concrete types behind a
  small interface; future role types implement `Executor` without touching the
  orchestrator's drive loop.

### Negative

- One genuinely non-mechanical chunk: extracting the shared test fakes into
  `internal/dtest` (otherwise `executor` and `orchestrator` test packages would
  duplicate ~150 lines of fakes), plus a parallel `newExec` test helper.
- The `pg2-kj7j` plumbing is **not** a pure verbatim move: `escalateLaunchFailure` gains
  a `bool` return and `workOneWithID`/`workOne` change to `(report.Result, error)`,
  rippling to `drain`/`RunOne`. The `pg2-c1vp` race code itself (`waitDone`,
  `workerWaitWithWatchdog`) stays byte-for-byte; the verb is derived in `Dispatch` from
  the error + role config, so the failure-verb fix never touches the race-critical
  loop.
- `Deps` exports seams that were previously unexported orchestrator internals — a
  slightly wider surface, justified by the cross-package boundary.

### Neutral

- The interface is `executor.Executor`, not `roles.Executor` as the bead's shorthand
  suggested — a naming consequence of the real `roles ↔ discover` cycle, not a scope
  change.
- `DispatchContext` stays in `discover` (no rename).

## Alternatives Considered

- **Move `DispatchContext` → `roles`** so the interface can be `roles.Executor`
  literally. Rejected: a wide mechanical rename across `discover`/`orchestrator`/`cmd`/
  tests for a naming nicety, against the "minimal, pure-structural" goal.
- **Keep executors in the `orchestrator` package.** Lowest blast radius, but under-
  delivers the spec's separate-package seam and leaves the orchestrator file large.
- **Full `Result` from the executor** (move the pre/post snapshot bracket into the
  executor via `Deps`). More faithful to the original spec §5a, but moves the snapshot
  seam + timing-relative-to-teardown into the executor — larger change, more behavior-
  shift risk on a pure-structural task. Rejected in favor of the partial-Result split.

## Related Decisions

- `docs/superpowers/specs/2026-06-16-pr-pool-externalize-roles-prompts-queries-toml-design.md`
  (spec C; Phase 1 core + Phase 2 deferral)
- `pg2-c1vp` (the single-terminal race fix being moved verbatim)
- `pg2-kj7j` (report failure-verb fidelity — folded in here)
