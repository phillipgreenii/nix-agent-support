# pr-pool: stop-on-done + role smoke harness

**Status**: Draft
**Date**: 2026-06-15
**Deciders**: Phillip Green II

## Context

A live `pr-pool drain` against the ZipRecruiter monorepo on 2026-06-15 dispatched a
feedback session for cycle `zr-prhuj`, then **waited the full 30-minute `MaxWait`**
before flagging the bead `not complete within 30m0s`. Investigation showed the
dispatched Claude session reached ccpool state `done` (its turn finished) without
ever claiming or working the bead, and produced no transcript. pr-pool kept polling
the bead status for 30 minutes for a bead that nothing was ever going to close.

Root cause is in the orchestrator's wait loop:

- `live()` (`internal/orchestrator/orchestrator.go:266`) returns, for a session
  found in `ccpool list`, `s.Live && s.State != StateFailed` (`orchestrator.go:273`);
  it assumes alive on a `List` error (`orchestrator.go:269`) and reports not-alive
  only when the session is absent (`orchestrator.go:276`). So a session sitting at
  `StateDone` — turn finished, tmux still alive, claude idle — reads as "alive," and
  `waitDone` (`orchestrator.go:194`) polls until `MaxWait`.
- The existing doc comment on `live()` (`orchestrator.go:262-265`) encodes the old
  intent explicitly: _"ccpool store states done/needs_input (a finished/paused TURN)
  are normal multi-turn operation, NOT death."_ This spec changes that for `done`.
- Nothing re-nudges a `done` session. Feedback dispatches have no watchdog at all;
  the budget watchdog (workers only) reacts to budget thresholds, never to
  `done`-state. So once a session is `done` without a done-signal on the bead, the
  remaining wait is pure waste.

The bug slipped through because every `StateDone` case in `orchestrator_test.go`
pairs it with `Live: false` (line 232 — the only `StateDone` in the file) — "done
AND tmux gone," which already fast-paths as death. The real-world
`Live: true, State: StateDone` (idle after a finished turn) has no test, and no
existing test uses `StateNeedsInput` at all.

Separately, there is **no smoke harness**: all 16 `*_test.go` files are unit tests
with fake `ccpool`/`bd`. There is no way to (a) run a single role's discovery query
and see what it would pick up, or (b) dispatch a single role against a single bead
end to end. Such a harness would have surfaced this behavior immediately and must
remain usable after the planned TOML config extraction.

## Decision

Ship two things, scoped narrowly:

1. **Stop-on-`done`**: the orchestrator stops waiting as soon as a dispatched
   session can no longer make progress, instead of always waiting to `MaxWait`.
2. **A role smoke harness**: two new read-only/dispatch subcommands —
   `pr-pool run-query <role>` and `pr-pool run-role <role> <context>` — plus the
   deterministic tests that lock the behavior in.

### 1. Stop-on-`done` (the wait-loop fix)

Rename `live()` to `active()` and change its meaning from "is the session process
alive" to **"is it still worth waiting on this session."** Rewrite the doc comment
at `orchestrator.go:262-265` to match: `done` is now terminal, and `needs_input`
must be split out of that sentence — it stays "keep waiting."

| ccpool `SessionState`            | `active()` | Wait-loop behavior                            |
| -------------------------------- | ---------- | --------------------------------------------- |
| `starting` / `ready` / `working` | `true`     | keep polling (bounded by `MaxWait`)           |
| `needs_input`                    | `true`     | keep polling (bounded by `MaxWait`)           |
| `done`                           | `false`    | stop now → final bead check → outcome         |
| `failed`                         | `false`    | stop now → final bead check → outcome         |
| absent from `ccpool list`        | `false`    | stop now → final bead check → outcome         |
| `ccpool list` errors             | `true`     | keep polling (unchanged; `MaxWait` bounds it) |

`needs_input` is deliberately **NOT** terminal: it means the agent is paused
awaiting human input, not that it finished. A human is expected to attach and move
it along, so pr-pool keeps waiting (still bounded by `MaxWait`). Surfacing a
`needs_input` session to the operator and preserving it from teardown is tracked
separately (see Deferred work, `pg2-th35`).

`done`/`failed`/absent reuse the **existing** re-check-after-stop branch in
`waitDone` (`orchestrator.go:221-234`): re-read the bead status once (this still
catches the "bead closed in the same instant the turn ended" race via
`complete.DoneSignal`), and if it is not a done-signal, apply the role's
`complete.OnFailure` — feedback unclaims, worker adds the `human` label. The
watchdog/`claimTerminal` single-terminal race (`workerWaitWithWatchdog`) is
untouched, because `active()` is consulted at the same call site (`orchestrator.go:221`)
that already feeds the arbitrated branch; a `done` worker simply stops fast rather
than idling to `MaxWait`. Only `active()`, its one call site, and the doc comment
change.

**Behavioral nuance to call out (not just a timing win):** with
`BudgetTime = 25m < MaxWait = 30m` (`config.go:64,76`), a stalled worker today is
usually hard-stopped by the time budget at 25m, which **unclaims**. After
stop-on-`done`, a worker that reaches `done` without closing the bead stops in one
poll and gets **`AddHuman`** instead. That is intended — a `done`-without-close
worker is a human-inspection case, not a blind-retry case — but the failure
_action_ (human vs unclaim) now depends on which terminal fires first, so it is a
real behavior change, not purely faster.

### 2. `DispatchContext` (forward-compatible dispatch payload)

**Rename** `discover.Dispatch` → `discover.DispatchContext` (it already lives in
package `discover`, which imports `roles`, so there is no import-cycle concern). It
is the explicit growth point for what a dispatch carries:

```go
// DispatchContext is everything one dispatch needs. Today: role + bead. It is the
// explicit growth point for future fields (repo, self_login, template variables);
// keeping it a struct means run-role's call shape is stable as it accretes fields,
// and spec C's prompt interpolation reads from this same value.
type DispatchContext struct {
    Role   roles.Role
    BeadID string
}

// Validate reports every required field that is missing, so run-role can fail fast
// with a complete diagnostic rather than dispatching on a half-filled context.
func (d DispatchContext) Validate() error
```

This is a mechanical rename plus a `Validate()` method. `workOne`, `waitDone`, and
`fail` take a `DispatchContext`; the rename touches the five orchestrator call sites
(`orchestrator.go:82,105,149,194,257`) and every `discover.Dispatch{...}`
construction in the tests (20+ in `orchestrator_test.go`). No behavioral change.

The eventual richer shape — and whether `run-role` is ultimately driven by a
self-contained "event" versus a runtime "context" — is decided in the deferred
event-model design (`pg2-r6cf`), not here.

### 3. `pr-pool run-query <role>` (read-only)

Runs **only the named role's discovery query** and prints the matching beads
(id, type, title). It does not dispatch and does not tear anything down.

Flow: `config.Load → precheck → resolveSelf → discover.ForRole(role) → print`.

This needs a small exported per-role discovery seam, `discover.ForRole(ctx, br, reg,
role, selfLogin) ([]DispatchContext, error)`, which the existing `discover.Discover`
then composes for both roles. Two behaviors must be specified, because `ForRole`
differs from `Discover`:

- **`selfLogin` guard**: `discoverFeedback` requires a non-empty `selfLogin` (for the
  parent-author join, `discover.go:71`); `discoverWorker` ignores it. `ForRole`
  keeps the non-empty-`selfLogin` check **only on the feedback path** (it would be
  spurious for worker). The harness resolves `selfLogin` via `resolveSelf` regardless.
- **`Enabled` gate is bypassed**: `Discover` skips a role whose `Enabled` flag is
  false (`discover.go:29-46`). `ForRole` does **not** — the whole point of the
  harness is to smoke-test a specific role even when it is disabled in config.

For `feedback` today, `run-query` exercises and **shows the join** (`bd ready` →
per-candidate `bd show <parent>` → `metadata.author == self_login`,
`discover.go:66-72`), which makes the simplification in the bead-structure work
(spec B) visible and verifiable.

### 4. `pr-pool run-role <role> <context>` (dispatch)

Dispatches one item through the named role's full `workOne` path, then tears down
that one session. Accepts the dispatch **context** (today: a bead id, assembled into
a `DispatchContext`).

Flow: `config.Load → precheck → resolveSelf → resolve role from registry by name →
build DispatchContext → DispatchContext.Validate() → Orchestrator.RunOne(ctx, dctx)
→ report outcome + exit code`.

`Orchestrator.RunOne(ctx, dctx)` is a new exported single-dispatch entry that runs
`workOne` for one context and closes **only that one session** (the drain's
pass-level `teardownAll` is not involved). It reports the outcome (closed / flagged /
timed-out) and a reason string. Note: `waitDone`/`fail` today return a generic
`error` whose reason text distinguishes the death/`done` branch ("session exited
before completing", `orchestrator.go:231`) from the timeout branch ("not complete
within …", `orchestrator.go:245`). `run-role` reports that reason string as-is;
surfacing a distinct typed "stopped-on-`done`" terminal state would require threading
a typed result out of `waitDone` and is **not** in scope for spec A.

**Fail fast on missing context** (per review): `run-role` must refuse to dispatch
if any required context value is absent or the role name is unknown. Validation
happens in two pure, side-effect-free stages, mirroring `args.go`'s existing
"never fall through to a real dispatch on bad input" guarantee (`pg2-52rn`):

1. `parseRunRoleArgs` (pure, in `args.go`): a missing/unknown role name or a missing
   bead positional yields `routeUsageErr` (exit 2) before any config load, precheck,
   or dispatch.
2. `DispatchContext.Validate()`: a second guard inside `runRunRole`, after the
   context is assembled, that returns an error naming **every** missing required
   field. This is the guard that keeps holding as `DispatchContext` grows new
   required fields. A validation failure exits non-zero **without dispatching**.

Like `run-query`, `run-role` bypasses the `Enabled` gate (you may want to smoke-test
a role disabled in prod config). It performs a live ccpool/claude dispatch, so it is
meant to be run from a normal shell — not nested inside another Claude Code session
(a nested spawn was the confound behind the 2026-06-15 run producing no transcript).

### CLI routing

Both subcommands extend the pure `route()` / `routeKind` machinery in
`cmd/pr-pool/args.go`. Bare `pr-pool` and `pr-pool drain` are unchanged. New routes
(`routeRunQuery`, `routeRunRole`) are added to the `route()` switch
(`args.go:77-84`), with pure `parseRunQueryArgs` / `parseRunRoleArgs` mirroring
`parseDrainArgs`. The current `routeResult` struct (`args.go:55-59`) carries only
`rest []string` and `msg string`, so it gains fields to carry the parsed
role/bead (or a small dedicated result type). Like `drain`, parsing is pure and a
parse error or help request can never fall through to a live dispatch. `helpText`
and `usageLine` gain the two subcommands.

## Testing

### Deterministic (CI safety net)

These run with fake `ccpool`/`bd` and are the real regression net:

1. **`done` stops fast (failure)**: `waitDone` with ccpool returning
   `{Live: true, State: StateDone}` and a bead status that never reaches a
   done-signal → asserts the loop returns **quickly** (does not approach `MaxWait`)
   and applies the correct `OnFailure` per role (feedback unclaims; worker adds
   `human`). This is the exact gap the 2026-06-15 bug slipped through.
2. **`done` stops fast (success race)**: `{Live: true, State: StateDone}` where the
   final re-read status reads `closed` (or worker hand-back) → asserts **success
   with no bead mutation**. This guards the re-check-after-stop branch that
   stop-on-`done` now routes `done` through; without it, a regression that skipped
   the re-read (failing a just-closed bead) would pass CI.
3. **`needs_input` waits to `MaxWait`**: `waitDone` with ccpool returning
   `{Live: true, State: StateNeedsInput}` and a non-closing bead → asserts the loop
   **waits until `MaxWait`**, times out, **and applies the role's `OnFailure`**
   (asserting the action, not just the timeout). Locks in that `needs_input` is not
   terminal and that its timeout action will not regress silently.
4. **`RunOne` smoke table**: per role, script ccpool state + bead-status sequences
   (`claim → close`, `claim → stall → timeout`, `done → no claim`) and assert both
   the terminal action **and** that `RunOne` closed the single session (its
   distinguishing behavior vs `DrainOnce`). Runs through the full `workOne` /
   `workerWaitWithWatchdog` path, so it is not redundant with the direct-`waitDone`
   tests above.
5. **`DispatchContext.Validate()`**: table of incomplete contexts → asserts each
   missing required field is named and that `run-role` exits non-zero without
   dispatching.
6. **`parseRunRoleArgs` / `parseRunQueryArgs` / `route()`**: unknown role, missing
   bead, help, and unknown flag → asserts `routeUsageErr` / `routeHelp` and never a
   run route.

### Live (manual, full-stack)

`pr-pool run-query <role>` and `pr-pool run-role <role> <bead>` against the real
store, run from a normal shell, for ad-hoc confidence (the kind of check the
2026-06-15 verification needed). Non-deterministic and (for `run-role`) mutating, so
not part of CI.

## Consequences

### Positive

- A stalled/`done` session is reaped in one poll interval instead of 30 minutes.
- The exact untested states (`Live: true, State: StateDone` and `StateNeedsInput`)
  gain coverage.
- An operator can inspect a role's query (`run-query`) and dispatch a single bead
  (`run-role`) without running a full drain — and these survive the TOML extraction
  (role resolved by name from config).
- `run-role` cannot dispatch on an incomplete context.

### Negative / risks

- `active()`'s semantics shift from "process alive" to "worth waiting on." The
  rename, the updated `orchestrator.go:262-265` comment, and splitting `needs_input`
  out of the old sentence must be explicit, or a future reader may reintroduce the
  old meaning.
- A `done`-before-25m worker now gets `AddHuman` rather than the watchdog's unclaim
  (see the behavioral nuance in §1). Intended, but a visible change.
- `RunOne` adds a second dispatch entry point beside `DrainOnce`. It must route
  through the same `workOne` so behavior cannot drift; it differs only in skipping
  discovery and tearing down a single session.
- `run-role` live dispatch shares the nested-session constraint; documented, not
  enforced.

### Neutral

- `discover.ForRole` is a refactor of existing logic into a per-role seam; no query
  behavior changes in spec A.

## Out of scope / deferred

- **Event-model split of role and query** — typed events emitted by queries, roles
  binding to one-or-more event types, per-role-type queues, event TTL, query
  triggers/periods. Tracked in `pg2-r6cf`. The `context`-vs-`event` shape of a
  dispatch is decided there.
- **`needs_input` operator notification + teardown survival** — alert the operator
  to attach, and avoid tearing down a `needs_input` session at pass end. Tracked in
  `pg2-th35`.
- **Bead-structure redesign (spec B)** and **TOML config extraction (spec C)** are
  the next cycles; spec A's `active()` fix and harness are prerequisites for both.

## Alternatives considered

- **Nudge a `done` worker to continue/hand-back instead of stopping.** Rejected for
  spec A: the decision is "if the agent says it is done, stop — waiting/coaxing adds
  nothing." A completion nudge is a future option, not this change.
- **Treat `needs_input` as terminal too.** Rejected: `needs_input` is a paused,
  human-attendable state, semantically distinct from `done`.
- **A single combined `run-role` that also runs the query.** Rejected: separating
  `run-query` (read-only, show results) from `run-role` (dispatch one item) keeps
  each verifiable in isolation and matches the future direction where queries and
  roles are distinct (`pg2-r6cf`).
