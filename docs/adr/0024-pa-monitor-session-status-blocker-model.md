# pa-monitor session status/blocker model, authoritative usage-limit signalling, and non-stale state metrics

**Status**: Accepted
**Date**: 2026-07-09
**Deciders**: Phillip Green II

## Context

`pa-monitor` reports session state on three surfaces that MUST agree (bead `pg2-r1f1j` acceptance criteria): the `status` CLI, the `tui`, and the OTEL/Grafana dashboards. When the Claude 5-hour rate-limit window is hit, they disagree — most visibly, `status` reports all sessions `idle` while the OTEL "Sessions by state" panel reports them `working`, and a spread of error/limit/nudge panels show "no data".

Investigation (5 parallel research agents, cross-confirmed) established:

1. **State is already single-sourced.** `session.Status` is derived once per tick in the poller (`internal/core/poller/poller.go:218-255`, registry `busy` + activity recency); `status`/`tui` read it via a DB round-trip, OTEL reads the same `sv.Status` off the live tree. There is **no computation divergence**.

2. **The `Status` enum conflates two questions.** The current values (`Working`/`Idle`/`Dormant`/`WaitingForHuman`) answer "does the session have work to do (assuming it could run)?" — registry `busy` is deliberately trusted and never demoted. But every dashboard _reads_ the value as "is the session actually running right now?". During a window hit a session with work but blocked on the exhausted API is registry-`busy` → reported `Working`, which is wrong for the operational question.

3. **OTEL mutable-label staleness.** `pa_monitor.sessions.count` (and `.sessions.errored`, `.session.info`) model `state`/`kind` as **mutable labels** on observable gauges. When a session flips state the daemon stops reporting the old label series but **never zeroes it**; Prometheus retains the orphaned series' last value (OTLP has no staleness markers). The state panels `sum by (state)` with no freshness guard, so a stale `state="working"` series persists — this is the actual "all working" artifact. The codebase already fixed this for the `session.info` table via `topk(1, timestamp())` and documents the mechanism (`grafana/pa-monitor-overview.json` panel description) but never applied it to the count panels.

4. **Two unreconciled "limit" notions.** The "block limit hit" counter fires on ccusage _native cost ≥ dollar cap_ (`internal/core/block/tracker.go:36`), edge-triggered once per block per process. The 436% the operator sees is the _authoritative_ `FiveHourPct` — a different source that `applyLimits` never reconciles into cost. **No instrument fires when the authoritative percentage crosses 100%.** ADR 0021 §5 intended the authoritative percentage to replace the `CostUSD/capUSD` derivation; that audit was never carried out.

5. **Nudge deferral is invisible.** During a reset-bearing rate-limit window no producer enqueues (disrupted excludes non-retryable rate*limit; window_reset waits \_silently* until reset; limit_pause handles only reset-less limits). Behavior is correct but emits zero telemetry, so "auto-resume deliberately waiting" is indistinguishable from "broken". Several dashboard filters also mismatch the emitted attributes (e.g. "Stuck on retryable error" filters `kind=~"unknown|server_error"`, which never matches `rate_limit`).

6. **`workspace.scope` all "personal".** No built-in path→scope classifier exists by design; scope is meant to come from an external shell-out decorator, and this host's `~/.config/pa-monitor/config.toml` has no `[[decorator]]` block.

## Decision

### D1 — Replace the single `Status` enum with an orthogonal `status` + `blocker` model

`session` state is represented by:

- `status`: a closed enum `working | blocked | idle`, answering "can this session make progress right now, and does it have work?"
  - `working` — has work and is (or can be) actively running turns.
  - `blocked` — has work but cannot proceed; the reason is the `blocker`.
  - `idle` — no work to do.
- `blocker`: present **only** when `status == blocked`; absent otherwise (no `none` sentinel). Values:
  - `human_input` — awaiting human input (`AskUserQuestion`/permission prompt).
  - `human_authn` — awaiting human re-authentication (e.g. HTTP 401).
  - `usage_limit` — a terminal usage-limit error on the session (rate-limit 429 / spend-limit) or a non-zero, still-in-future `RateLimitResetsAt`. Derived from **per-session** inputs only, NOT the account-global `FiveHourPct` (see Review Correction R2).
  - `error` — any other terminal blocking error (server/network/model-unavailable); retryability carried by `last_error`.
- `last_error`: the raw error record + retryable flag, always tracked (unchanged in spirit from today's `LastError`/`LastErrorRetryable`).

The `human_` prefix marks the blockers that require a human to clear.

Derivation MUST live in one place (the poller), preserving today's registry-`busy`-is-trusted semantics for the "has work" question, and **overriding registry-`busy` to `blocked` + the appropriate `blocker` when the session carries a current terminal blocking condition**. This override is confirmed necessary by live observation: a session (`findev-deep-dive`) reads `working` while carrying a terminal `rate_limit` error, which is precisely the mis-report to correct. The error must be _current_ (not superseded by newer transcript activity after the error), so the override predicate combines `LastError` with activity recency / `RateLimitResetsAt`. `Dormant` ceases to be a top-level status: a long-idle session is `idle` with an age refinement retained for TUI display and the `session.info` exclusion.

`awaitsHuman(blocker)` is a **derived predicate**, not a stored value: `true` when `blocker` matches `human_*` OR (`blocker == error` AND not retryable). It is computed where needed (keepAwake, "blocked on human" rollups) so the granular blocker identity is never lost. Merging `human_input` and `human_authn` into one value is explicitly rejected (see Alternatives).

All three surfaces (`status`, `tui`, OTEL) MUST carry and display both `status` and `blocker` from the same fields; the DB schema, proto, and RPC MUST carry both.

### D2 — keepAwake policy over the new model

`keepAwake` is redefined as a predicate over the new fields, preserving the existing D5 unattempted-disrupt behavior:

- Keep the Mac awake when any session is `working`, OR `blocked` on a _machine-recoverable_ blocker: `usage_limit` (auto-resume fires at window reset), or `error` that is retryable.
- Allow sleep when every session is `idle` or `awaitsHuman` (`human_input`, `human_authn`, or a non-retryable `error`).

This generalizes today's `keepAwake = anyWorking`; the nudger/caffeinate gates that key off `WaitingForHuman` today MUST key off `blocker ∈ human_*` after the refactor.

### D3 — Authoritative usage-limit-hit signalling

A "usage limit hit" MUST be detected from the authoritative signal, not ccusage cost:

- Fire the limit-hit when the authoritative `FiveHourPct >= 100` (weekly: `SevenDayPct >= 100`) **or** a terminal usage-limit error is observed on any session.
- Retire the ccusage `CostUSD >= dollar-cap` trigger for limit-hit detection (ccusage cost is not accurate enough for this purpose).
- Carry out ADR 0021 §5: audit `WindowPct = CostUSD/capUSD`; the `status` CLI and dashboards report the authoritative percentage, not the cost ratio.
- The account-global window-hit is an account-level signal + event; it does NOT by itself force work-less sessions into `blocked`. A session becomes `blocked`/`usage_limit` from its own terminal usage-limit condition.

### D4 — Non-stale snapshot-gauge metric model

Observable gauges that carry mutable labels (`pa_monitor.sessions.count`, `pa_monitor.sessions.errored`) MUST explicitly observe `0` for every FULL emitted label-tuple (`{state, workspace.*, agent.*, plan_tier}`) that was present on a prior tick but is absent this tick (carry-forward-zero, zeroed once), so orphaned series drop to `0` instead of persisting at their last value. `pa_monitor.sessions.count` (aggregate) and `pa_monitor.session.info` (per-`session_id`) intentionally count DIFFERENT populations — the latter excludes long-idle sessions to bound `session_id` cardinality (`lifecycle.go:749-752`); this is correct and is NOT reconciled. Dashboard state panels are reworked to the `status`/`blocker` model and, where a query-side guard is still warranted, use the `session.info`-style freshness guard.

### D5 — Observability + scope (carried by child beads, governed by this ADR)

- Producers MUST emit a deferral signal when intentionally waiting on a window (so "auto-resume waiting" is visible). Dashboard filters MUST match emitted attributes (`is_terminal` on `sessions.errored`, retryable set includes `usage_limit`/network, `error_kind` panels).
- `workspace.scope` is set by an external decorator wired via the nix module; the label cache MUST NOT cache a failed/nil decorator result.
- The OTLP log stream gains a periodic heartbeat so "pa-monitor events" is never empty on a healthy daemon; the logs→Loki routing + datasource are verified.

## Consequences

### Positive

- The three surfaces answer the _same_ operational question and agree; a window-hit reads `blocked`/`usage_limit`, never `working`.
- `status` stays a small closed enum (stable dashboards); new failure modes are new `blocker` values, not new statuses.
- keepAwake becomes correct-by-construction: the machine sleeps exactly when only a human can act.
- Limit-hit reflects reality (authoritative %), not a lagging cost estimate.
- Snapshot metrics stop lying after a state transition.

### Negative

- Breaking change to the DB schema, proto, and RPC (migration + version-skew handling between client and daemon required).
- Touches the poller, aggregate tree, both stores, proto, service, render (status + tui), otel emitter, nudger gating, and dashboards — large blast radius.
- Carry-forward-zero adds per-tick bookkeeping of prior label-sets in the emitter.

### Neutral

- `Dormant` moves from a status to an idle age-refinement; the TUI keeps its dormant rendering via the age attribute.
- Weekly limit-hit gets the same authoritative treatment as the 5h.

## Alternatives Considered

### Flatten blocker into the status enum (`blocked_on_usage_limits`, `blocked_on_authn`, …)

Rejected: encodes a status×reason cross-product into one field, so the enum churns on every new failure mode and "how many blocked?" must OR N values. The orthogonal `status`+`blocker` representation is the normalized form; a flattened label is a lossy display view derivable from it.

### Merge `human_input` + `human_authn` into one `human_authn`/"blocked on human" value

Rejected: the two behave identically only for keepAwake; `authn` is an actionable, alert-worthy failure that MUST stay callable-out, while `human_input` is normal operation. The shared "needs a human" grouping is captured by the `awaitsHuman` predicate + the `human_` naming convention without discarding the distinction.

### Two separate dimensions (Demand vs Runtime), each working/blocked/idle

Rejected during design: heavy overlap between the dimensions; the single `status` + `blocker` collapses it — `blocked` already means "has work but can't run", subsuming "demand=working, runtime=errored".

### Dashboard-only fix (apply `topk(timestamp())` guard to the state panels)

Rejected as the sole fix: only partially effective (concurrent same-session series share near-identical timestamps), untestable in Go, and leaves the semantic conflation (D1) unaddressed. Used only as a supplementary guard where warranted.

### Keep the ccusage cost-cap limit-hit trigger

Rejected: ccusage native cost is not accurate enough; it produces "0 block limit hits" while the account is at 400%+ and is a different signal than the authoritative percentage the operator reasons about.

## Review Corrections (binding; from independent code-verified critique)

These supersede any conflicting detail above.

- **R1 (was BLOCKER B1) — divergence mechanism is staleness, not live mis-derivation.** `status`/`tui`/`IsAnyBusy` read the DB-materialized tree (`server.go:100`, `state.go:65-74`, `tree_builder.go:40-49`); OTEL emits from the live tree (`lifecycle.go:577`). `poller.go:387` `UpsertSession` writes the same `sv.Status.String()` that OTEL emits that same tick, so the two can only diverge via observable-gauge orphan-series staleness. **D4/.2 alone closes the literal acceptance criterion.** D1 is retained NOT to fix agreement but to fix the _semantic_ value (a terminal-rate-limited `working` session should read `blocked`/`usage_limit`), which is separately confirmed live. Ship `.2` first; it is independently valuable.
- **R2 (was BLOCKER B2) — poller lacks `FiveHourPct`.** `FiveHourPct` is populated post-`Snapshot` via `applyLimits` (`lifecycle.go:434-438,1015-1034`). The per-session `usage_limit` blocker MUST derive only from `snap.LastError` (`ErrRateLimit`/terminal) and `RateLimitResetsAt` (`poller.go:196-216`). `FiveHourPct>=100` is used ONLY for the D3 account-level hit event, never per-session.
- **R3 (S1) — five surfaces, not three.** `IsAnyBusy` (`server.go:135-144`) and the `agents-busy-check` exit-code contract sum `WorkingN`; and `cmuxstatus/reporter.go:15-20` has states `{Unknown,Dormant,Idle,Working,Paused}` with no `Blocked`. Decision: `IsAnyBusy`/busy = `status==working` only (preserves current semantics; a `blocked` session is not "actively progressing"). `cmuxstatus` maps `blocked`→`Paused` (extending its existing `Paused` notion) rather than losing it to `Idle`.
- **R4 (S2) — audit `session.Working` consumers, not just `WaitingForHuman`.** Sites at `nudger/{window_reset:38,limit_pause:53,dispatcher:175}`, `tui/{keybindings:249,model:283}`, `lifecycle.go:479-484`. Former-`Working`-now-`blocked` sessions becoming nudge-eligible is an intended behavior change and MUST be called out + tested.
- **R5 (S3) — keepAwake power cost is intentional.** `usage_limit`-keeps-awake can hold the Mac awake up to a full window reset. This is deliberate (auto-resume must fire at reset) and the ADR's "sleeps when only a human can act" is consistent (usage_limit is machine-recoverable). Documented, not a regression.
- **R6 (S4) — CORRECTED (re-review found the first correction wrong).** The original D4 mechanism is right and stands: carry-forward-zero over the FULL emitted label-tuple `{state, workspace.*, agent.*, plan_tier}`. `state` is one label among several on a single gauge (`emitter.go:414-448`, `lifecycle.go:731-746`), so it CANNOT be zeroed independently — a bare `{state=working}` series is a different series and does not zero the orphan `{state=working, workspace.X, agent.Y}`. Mechanism: the emitter remembers the set of tuples emitted last tick; on the next `Record*`, `Observe(0)` every tuple present last tick but absent now, then forgets it (zero once). Bounded by last tick's live-session tuple count (≤ #sessions × small constant); the existing `labels.CardinalityCap` (`lifecycle.go:402`) already bounds per-key values, so no additional cap is needed.
- **R7 (S5) — limit-hit counter hygiene.** `Add(0)` the limit-hit counters at startup to birth the zero series (fixes born-nonzero/`increase()`), and latch once-per-window keyed on `FiveHourResetsAt` (pattern: `block.Tracker.hitFired` `tracker.go:32-41`, `LimitPauseFiredFor` `limit_pause.go:46`).
- **R8 (S6) — version-skew.** Status is a wire/DB string (`pa_monitor.proto:171`, `from_proto.go:186-197`, `migrations/001`). Map unknown status → `idle` (visible), NOT the current `default→Dormant` (hidden). Keep parsing the old vocab ("dormant"/"waiting") during transition. Add a NEW `blocked_n` proto field (verified free: `Directory` next=11, `SessionView.blocker` next=30; `dormant_n=7` untouched) + a nullable `blocker` DB column WITH DEFAULT (forward-only `ADD COLUMN`; next migration file is `005`, exemplar `003`/`004`); do not repurpose `dormant_n`. Residual (self-healing, not a blocker): an OLD client binary reading a NEW daemon still maps `"blocked"`→`Dormant` until the client is upgraded.
- **R9 (S7) — three bucketers + three parsers must move in lockstep** (`aggregate.go:32-41`, `pathtree.go:106-117`, `tree_builder.go:40-49`; `state_convert.go:216`, `from_proto.go:186`, `BuildDirectories`). The DB path does not persist `RateLimitResetsAt`, which is a second reason to persist a `blocker` column so the DB-path bucketer can render `usage_limit`.
- **R10 (C2) — dashboard edits consolidated into `.7`** (remove them from `.2`). The `.2` Dormant-population sub-item is DROPPED as a non-bug: the `sessions.count` (aggregate) vs `session.info` (per-`session_id`) population difference is a deliberate `session_id`-cardinality guard (`lifecycle.go:749-752`), NOT a mismatch to reconcile. It survives the Dormant→idle-age rename and is intentional.
- **R11 (C3) — weekly hit is 5h-only-testable here.** `SevenDayPct` is a commonly-nil `*float64` (`tree.go:83-90`); nil-guard it and mark weekly untestable on this account.
- **R12 (C4) — define the test seam.** "Surfaces agree" is asserted in Go by deriving `status`+`blocker` once and checking the emitter's buffered `SessionGroup`/`sessionInfoObs` against the DB-materialized buckets — both against the same derived value. OTEL→Grafana rendering is out of Go-test scope.

## Related Decisions

- Supersedes/extends `docs/adr/0021-pa-monitor-plan-model-and-rate-limit-source.md` §5 (authoritative `used_percentage` replacing `WindowPct`; the audit this ADR finally carries out).
- Relates to `docs/adr/0011-pa-monitor-daemon-otel-split.md` (metric emission split) and `docs/adr/0013-pg-pr-otlp-logs-via-otelslog.md` (OTLP logs → Loki, relevant to the heartbeat/log-stream item).
- Relates to `docs/adr/0022-nudge-delivery-via-cmux-bridge.md` (nudge delivery; the deferral-visibility item).
