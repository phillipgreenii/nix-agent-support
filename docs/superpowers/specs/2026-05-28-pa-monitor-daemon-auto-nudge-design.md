# pa-monitor Daemon-Owned Auto-Nudge + API-Error Detection Design

**Status**: Draft
**Date**: 2026-05-28
**Deciders**: phillipg
**Related**: `docs/superpowers/specs/2026-05-20-pa-monitor-daemon-otel-design.md`,
`docs/adr/0011-pa-monitor-daemon-otel-split.md`
**Beads**: `beads_pg2-eoq3`

## Summary

Move the auto-resume scheduler from the TUI into the daemon, and add detection
for transient API failures (synthetic `isApiErrorMessage` events with
`error ∈ {"unknown","server_error"}`) so the daemon nudges a stuck session
back into work after a network disruption — the same way it nudges after a 5h
billing window resets.

Replace the existing single-purpose scheduler with a producer/dispatcher
architecture: independent producers (`window_reset`, `disrupted`, `manual`)
emit per-session intents into a shared pending store; one dispatcher fires the
nudge, suppresses if the session is already working, and clears all intents
for that session afterwards.

Surface all observed API-error events (retryable and non-retryable) in OTel,
the TUI session details, the CLI `info`/`status` output, and a new Grafana
panel.

## Goals

- Daemon is the sole owner of auto-nudge logic. The TUI is a viewer + toggle
  client. Closing the TUI does not stop auto-resume.
- Nudge a session after a transient API failure (socket closed, ECONNRESET,
  ConnectionRefused, stream idle timeout, 5xx server error) once Claude
  Code's own retries have given up.
- Decouple producers (what conditions warrant a nudge) from the dispatcher
  (how/when a nudge is delivered). Add new producers later without touching
  the dispatcher.
- Manual nudge from TUI (single keybind, scoped by cursor position) and CLI
  flows through the same path as auto producers.
- Track every observed `isApiErrorMessage` kind in OTel, including the
  non-retryable kinds (`invalid_request`, `authentication_failed`) that are
  not nudged.
- Daemon restart re-derives in-flight state from snapshots + persisted
  watermarks; no double-nudging.
- TUI shows whether a session is currently stuck on an error and whether a
  nudge is queued for it. CLI mirrors this in `status` and `info`.

## Non-Goals

- Re-classifying which API-error kinds are retryable. The retryable set is
  fixed at `{"unknown","server_error"}` based on the data sampled (see
  Detection §).
- Distinguishing nudge-source in the TUI session-row glyph. The "nudge
  queued" indicator does not differentiate `window_reset` vs `disrupted` vs
  `manual` on the row; the session details pane shows the source list.
- Cross-machine state sync. Same single-host constraint as
  `2026-05-20-pa-monitor-daemon-otel-design.md`.
- Replaying nudges for resolved errors (i.e. surfacing history in the TUI).
  History lives in OTel/Grafana.

## Background

Today (`internal/tui/update.go:64-101`), the TUI watches
`tree.WindowResetsAt`, schedules an `autoResumeFireMsg` for
`WindowResetsAt + auto_resume_delay_s`, then iterates non-Working sessions
via `signalNonWorkingAndCount` and calls `signaler.Send(pid,
auto_resume_message)`. The signal layer (`internal/signal/*.go`) and the
session list are already daemon-resident; only the scheduling and the
"non-Working iteration" live in the TUI.

This is a bug: if the TUI is closed, auto-resume stops. If two TUI clients
are open, each fires its own nudge. The daemon already owns the equivalent
state for caffeinate (`internal/daemon/runtime.json`) and the gRPC `Nudge`
RPC.

Disrupt detection does not exist today. Real transcripts (`~/.claude/projects/`)
contain synthetic `isApiErrorMessage` events with five distinct `error`
values:

| `error`                 | Count | Example text                                                                                                                                                                                  |
| ----------------------- | ----- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `rate_limit`            | 519   | "You've hit your limit · resets 7:10pm (America/New_York)"                                                                                                                                    |
| `server_error`          | 386   | "API Error: 529 Overloaded. ..."                                                                                                                                                              |
| `invalid_request`       | 166   | "Prompt is too long"                                                                                                                                                                          |
| `unknown`               | 46    | "API Error: The socket connection was closed unexpectedly...", "API Error: Stream idle timeout...", "API Error: Unable to connect to API (ECONNRESET\|ConnectionRefused\|FailedToOpenSocket)" |
| `authentication_failed` | 20    | "Not logged in · Please run /login"                                                                                                                                                           |

`rate_limit` already has a dedicated path (`transcript/ratelimit.go`).
The new disrupt path covers `unknown` and `server_error` — every variant
of those is transport-layer or transient 5xx. `invalid_request` and
`authentication_failed` are recorded for observability but never nudged.

## Architecture

```
                   ┌──────────────────────────────────────────────┐
                   │                  pa-monitor daemon            │
                   │                                                │
   poller tick ──► │   ┌───────────────┐   ┌──────────────────┐   │
                   │   │ snapshot +    │──►│ producers        │   │
                   │   │ aggregate     │   │  - window_reset  │   │
                   │   │ (tree.go)     │   │  - disrupted     │   │
                   │   └───────────────┘   │  - manual (RPC)  │   │
                   │                       └────────┬─────────┘   │
                   │                                │              │
                   │                                ▼              │
                   │                       ┌──────────────────┐   │
                   │                       │ pendingStore     │   │
                   │                       │ map[IntentKey]   │   │
                   │                       └────────┬─────────┘   │
                   │                                │              │
                   │                                ▼              │
                   │                       ┌──────────────────┐   │
                   │                       │ dispatcher       │   │
                   │                       │  - suppress?     │──┼──► signaler.Send
                   │                       │  - fire once     │   │      (tmux/cmux/ghostty/vscode)
                   │                       │  - ClearSession  │   │
                   │                       └────────┬─────────┘   │
                   │                                │              │
                   │                                ▼              │
                   │                          runtime.json         │
                   │                          (watermarks +        │
                   │                           pending intents)    │
                   └──────────────────────────────────────────────┘
```

Producers, the store, and the dispatcher all live in a new package
`internal/daemon/nudger/`. The daemon's existing tick loop calls
`nudger.Tick(state)` after the poller has refreshed the aggregate tree.

### Intent store

```go
type IntentKey struct {
    SessionID string
    Source    string // "window_reset" | "disrupted" | "manual"
}

type NudgeIntent struct {
    Key        IntentKey
    Text       string         // default: config.AutoResumeMessage; manual may override
    EmittedAt  time.Time
    Cause      *ErrorRecord   // for "disrupted"; nil for window_reset/manual
}

// pendingStore is goroutine-safe. All mutations rewrite runtime.json.
type pendingStore interface {
    Add(NudgeIntent)                      // idempotent on Key
    Cancel(IntentKey)                     // no-op if absent
    ClearSession(sessionID string)        // remove all keys for sessionID
    HasAny(sessionID string) bool
    SourcesFor(sessionID string) []string
    List() []NudgeIntent                  // for dispatcher iteration; ordered by EmittedAt
}
```

A session may simultaneously hold up to three intents (one per source). No
source ever overwrites another source's intent; each source manages only its
own `IntentKey`.

### Producers

All producers run on every daemon tick, after the snapshot/aggregate refresh.
Each producer reconciles its own intent state from the current aggregate +
watermarks:

1. Cancel its own intent if the precondition no longer holds.
2. Then add its own intent if the precondition newly holds.

Cancel-then-add ordering is mandatory so transitions (e.g. new error event
replaces an in-flight disrupt intent) clear the old intent's `EmittedAt`
before re-emitting.

#### WindowResetProducer

Per-tick rule (applied across all sessions):

- If `tree.WindowResetsAt == zero` → cancel any `window_reset` intent for
  every session; clear `window_reset_fired_for` watermark.
- Else if `now < tree.WindowResetsAt + auto_resume_delay_s` → no-op (the
  daemon scheduler picks the right tick to fire).
- Else if `window_reset_fired_for == tree.WindowResetsAt` → already fired
  this window; no-op.
- Else → for every non-Working session in the tree, `Add` a
  `window_reset` intent. Set `window_reset_fired_for = tree.WindowResetsAt`.

`auto_resume_enabled = false` short-circuits the producer to a no-op (and
cancels any leftover intents).

#### DisruptProducer

Per-session rule (sid is the session identifier):

| Current observation                                                                                            | Action on `disrupted` intent for sid | Watermarks                                             |
| -------------------------------------------------------------------------------------------------------------- | ------------------------------------ | ------------------------------------------------------ |
| `LastError == nil` OR `LastError.IsTerminal == false` (active)                                                 | `Cancel`                             | `firstSeen[sid] = zero`                                |
| `LastError.IsTerminal && IsRetryable`, `LastError.At` is new                                                   | `Cancel`, then re-evaluate           | `firstSeen[sid] = now`; clear `disrupt_escalated[sid]` |
| `LastError.IsTerminal && IsRetryable`, same `LastError.At`, within grace (`now - firstSeen < disrupt_grace_s`) | no-op                                | —                                                      |
| `LastError.IsTerminal && IsRetryable`, same `LastError.At`, grace elapsed, not escalated                       | `Add` (idempotent)                   | —                                                      |
| `LastError.IsTerminal && IsRetryable`, escalated                                                               | `Cancel`                             | — (escalation handled below)                           |
| `LastError.IsTerminal && !IsRetryable`                                                                         | `Cancel`                             | —                                                      |

"`LastError.At` is new" means newer than the persisted watermark
`last_disrupt_nudge_for[sid]` (the error event the dispatcher last nudged
for, or zero if none). This survives restart, so a daemon restart does not
re-grace + re-nudge a same-event error we already handled. If
`LastError.At <= last_disrupt_nudge_for[sid]` the producer treats it as
"same error", routing to the escalation evaluation rather than re-graceing.
Cancel-then-add ensures stale intents from prior error events do not
persist.

`auto_resume_enabled = false` short-circuits to no-op + cancel-all.

Escalation:

- After the dispatcher fires a `disrupted` intent for sid, it sets
  `last_disrupt_nudge_at[sid] = now` and `last_disrupt_nudge_for[sid] =
LastError.At` (the error event that triggered the nudge).
- On a later tick, if `LastError.At == last_disrupt_nudge_for[sid]` (same
  error, still terminal, still retryable) and `now - last_disrupt_nudge_at[sid]
  > = escalation_after_s`, set `disrupt_escalated[sid] = true`. The producer
stops re-arming. The aggregate view exposes `IsRetryable = false` for this
  > session/error pair so the TUI swaps to the non-retryable glyph.
- A fresh error event (`LastError.At > last_disrupt_nudge_for[sid]`)
  re-arms: clear `disrupt_escalated[sid]`, reset the watermarks, treat as a
  new disruption.

Defaults: `disrupt_grace_s = 30`, `escalation_after_s = 60`.

#### ManualProducer

Driven by gRPC. Two RPC variants:

```proto
service PaMonitor {
  rpc NudgeQueue(NudgeQueueRequest)   returns (NudgeQueueResponse);
  rpc NudgeCancel(NudgeCancelRequest) returns (NudgeCancelResponse);
  rpc SetAutoResume(SetAutoResumeRequest) returns (SetAutoResumeResponse);
}

message NudgeQueueRequest     { string selector = 1; string text = 2; }
message NudgeCancelRequest    { string selector = 1; }
message SetAutoResumeRequest  { bool   enabled  = 1; }
```

`SetAutoResume` toggles the daemon's `auto_resume_enabled` flag; the new
value is persisted in `runtime.json` and overrides the config default. It
affects the `window_reset` and `disrupted` producers only.

Selector semantics match the existing `pa-monitor nudge` selector
(`session:`, `path:`, `cmux:`, bare value). Both RPCs expand the selector
to a session set before mutating the store.

- `NudgeQueue`: for each expanded sid, `Add({sid, "manual"}, Text)`. No
  session-status check at queue time.
- `NudgeCancel`: for each expanded sid, `Cancel({sid, "manual"})`.
- Toggle is a TUI concern only (see TUI Manual Nudge §).

`auto_resume_enabled = false` does NOT disable manual.

### Dispatcher

Runs as the final phase of each daemon tick, after all producers have
reconciled.

```
for each sid with HasAny(sid):
    view := aggregate.Session(sid)
    sources := pending.SourcesFor(sid)

    if view == nil:                          // session disappeared
        pending.ClearSession(sid)
        log.Debug("nudge: session vanished", "sid", sid, "sources", sources)
        continue

    if view.Status == Working:
        OTel: pa.nudge.suppressed{cause=session_active, sources=sources}
        log.Warn("nudge suppressed; session active", ...)
        pending.ClearSession(sid)
        continue

    text := resolveText(pending, sid)        // manual.Text wins, else autoResumeMessage
    err := signaler.Send(view.PID, text)
    if err != nil:
        log.Warn("nudge send failed", ...)
        // leave intents in place; retry next tick
        continue

    OTel: pa.nudge.sent{sources, error_kind, escalated}
    update watermarks:
        last_nudged_at[sid] = now
        if "disrupted" in sources: last_disrupt_nudge_at[sid] = now;
                                   last_disrupt_nudge_for[sid] = LastError.At
    pending.ClearSession(sid)
```

One signal-send per session per dispatch pass. ClearSession runs on both
success and suppression. On send failure, intents remain — producers don't
re-add (idempotent) and the dispatcher retries next tick.

## Detection: `transcript/disrupt.go`

New file alongside `internal/core/transcript/ratelimit.go`.

```go
type ErrorKind string

const (
    ErrRateLimit      ErrorKind = "rate_limit"
    ErrUnknown        ErrorKind = "unknown"
    ErrServerError    ErrorKind = "server_error"
    ErrInvalidRequest ErrorKind = "invalid_request"
    ErrAuthFailed     ErrorKind = "authentication_failed"
)

type ErrorRecord struct {
    Kind        ErrorKind
    Text        string    // raw message text
    At          time.Time
    IsTerminal  bool      // true ⇔ no non-synthetic user/assistant event follows
    IsRetryable bool      // true ⇔ Kind ∈ {ErrUnknown, ErrServerError}
}

// LastAPIError returns the most recent isApiErrorMessage event in the
// transcript regardless of kind. IsTerminal is true iff no non-synthetic
// user/assistant event follows. Returns (zero, nil) if none found.
func LastAPIError(path string) (ErrorRecord, error)
```

Detection rule (operating on the same single-pass file walk used by
`Snapshot()`):

- Match: `type == "assistant"` AND `isApiErrorMessage == true` AND
  `error ∈ {rate_limit, unknown, server_error, invalid_request,
authentication_failed}`. Capture `error`, the first `text` content block,
  and the event timestamp.
- Resume detection mirrors `ratelimit.go:186-201`: any subsequent event
  with `type ∈ {user, assistant}` that is NOT itself an `isApiErrorMessage`
  flips `IsTerminal` to false for the captured record.

`Snapshot.LastError *ErrorRecord` is populated inside the existing event
loop in `transcript/snapshot.go` (no second pass). When a record is found
and `Kind == rate_limit`, the existing `RateLimitResetsAt` path still
runs — the two are not exclusive. `LastError` is informational/observability
state; `RateLimitResetsAt` continues to drive `tree.WindowResetsAt`.

`IsRetryable` is computed at construction.

`aggregate/tree.go`: `SessionEntry` gains `LastError *ErrorRecord` and
`PendingNudge struct { Sources []string }`. Both nil when not applicable.
The aggregate view also flips `LastError.IsRetryable` to false when the
nudger's `disrupt_escalated[sid]` is set (see Escalation above).

## Configuration

Extend `internal/config/config.go`:

```toml
[auto_resume]
enabled         = true   # gates window_reset + disrupted producers (NOT manual)
delay_s         = 120    # existing: post-window-reset firing delay
message         = "continue"  # existing
disrupt_grace_s = 30     # NEW: pre-enqueue debounce for disrupt producer
escalation_after_s = 60  # NEW: time after disrupt-nudge to declare escalation
```

`auto_resume_enabled` is also persisted in runtime.json (set via gRPC
toggle) and overrides the config value when present. Persistence keeps
the toggle sticky across daemon restarts.

## Persistence: `runtime.json`

Extends the existing file. Schema (additive, backwards-compatible):

```json
{
  "caffeinate_active": true,
  "auto_resume_enabled": true,
  "nudger": {
    "pending_intents": [
      {
        "session_id": "abc-def",
        "source": "manual",
        "text": "continue",
        "emitted_at": "2026-05-28T14:30:00Z",
        "cause": null
      }
    ],
    "sessions": {
      "abc-def": {
        "last_nudged_at": "2026-05-28T14:25:00Z",
        "last_disrupt_nudge_at": "2026-05-28T14:25:00Z",
        "last_disrupt_nudge_for": "2026-05-28T14:24:50Z",
        "disrupt_escalated": false
      }
    },
    "window_reset_fired_for": "2026-05-28T14:30:00Z"
  }
}
```

- Pending intents are persisted (all sources). Restart re-emits idempotently
  via producers, and persisted intents prevent a manual nudge from being
  lost across restart.
- Per-session watermarks GC'd when sid absent from snapshots for >1h.
- Write strategy: rewrite-on-mutation, same approach as the existing
  caffeinate toggle. File is small.

## TUI

### Toggle removal and re-wire

Remove from `internal/tui/`:

- `m.autoResume`, `m.autoResumeFired`, `m.autoResumeDelay`, `m.autoResumeMessage`
- `m.countdownTick`, `autoResumeFireMsg`, `autoResumeFireCmd`, `countdownTickCmd`
- `signalNonWorking`, `signalNonWorkingAndCount`
- The signaler import from the TUI

The `a` keybind (auto-resume toggle) becomes a gRPC call to a new
`SetAutoResume(enabled bool)` RPC on the daemon. Visible state is read
from `tree.AutoResumeEnabled` (new field on the state RPC response).

The countdown ("nudging in 23s") is rendered from
`tree.WindowResetsAt + auto_resume_delay_s` (both already wire-visible).
`auto_resume_delay_s` is sent on the state RPC alongside `WindowResetsAt`.

### Session-row glyphs

`internal/render/` rules for the session row, in priority order:

1. `view.Status == Working` → existing working glyph.
2. `view.LastError != nil && view.LastError.IsTerminal && view.LastError.IsRetryable` → retryable-error glyph (e.g. `⚠`).
3. `view.LastError != nil && view.LastError.IsTerminal && !view.LastError.IsRetryable` → non-retryable-error glyph (e.g. `✗`). Includes escalated retryables (aggregate has flipped IsRetryable to false).
4. Otherwise → existing idle glyph.

When `view.PendingNudge != nil` AND the row's primary glyph is not Working,
append a "nudge queued" marker (e.g. `↪`). The marker is independent of
which source contributed; the details pane lists sources.

Display rule: error glyphs and `view.LastError` text only appear when
`IsTerminal == true`. As soon as a non-synthetic user/assistant event lands
after the error, the snapshot flips IsTerminal=false and the glyph reverts
to idle/working.

### Session details pane

When `view.LastError != nil && view.LastError.IsTerminal`:

```
Last error:    server_error  (escalated)
               API Error: 529 Overloaded. ...
               2 minutes ago
Pending nudge: [disrupted, manual]
```

`(escalated)` annotation appears when the aggregate has flipped IsRetryable
to false due to escalation.

### Manual nudge keybind `N`

Single key, scope-by-cursor:

- Leaf row (single session) → toggle `manual` intent on that sid.
- Directory/group row → toggle across every sid under that node.
- Root row → toggle across every sid in the tree.

Toggle in multi-session scope:

- If ALL selected sids already have a `manual` intent → call `NudgeCancel`
  on the scope selector.
- Else (some are missing) → call `NudgeQueue` on the scope selector with
  the default text (`auto_resume_message`).

Per the agreed semantic: queue-time is unconditional on session status.
The dispatcher does the active-session suppression check.

The existing "send to all" key is removed. Move cursor to root, press `N`
to achieve the same.

## CLI

`pa-monitor` subcommands:

- `pa-monitor nudge <sel>` — `NudgeQueue` RPC. Idempotent. Replaces the
  current synchronous send-and-confirm behavior. Response indicates
  "queued"/"already queued" per session.
- `pa-monitor nudge <sel> --cancel` — `NudgeCancel` RPC.
- `pa-monitor status` — gains a `NUDGE` column when any session has
  pending intents; otherwise omitted. Error rendering uses the same
  `IsTerminal` gate as the TUI.
- `pa-monitor info <sel>` — appends a `Last error` section (gated on
  IsTerminal) and a `Pending nudge` section listing sources.
- `pa-monitor auto-resume on|off|toggle` — new subcommand controlling
  the daemon-side toggle via gRPC. Persisted in runtime.json.

Existing subcommands (`status`, `info`, `caffeinate`, `agents-busy-check`,
`wait-until-agents-finished`, `cmux-bridge`, `config show`) unchanged
otherwise.

## OTel

Extends `internal/otel/`:

| Instrument                      | Type    | Labels                           | When                                                                                                                                               |
| ------------------------------- | ------- | -------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pa.session.api_error.observed` | counter | `kind`                           | Snapshot first sees a new `LastError` event (delta on `LastError.At`). All kinds.                                                                  |
| `pa.sessions.errored`           | gauge   | `kind, is_terminal`              | Per scrape: count of sessions with `LastError != nil` matching.                                                                                    |
| `pa.nudge.queued`               | counter | `source`                         | Producer `Add` succeeds (new key, not idempotent re-add).                                                                                          |
| `pa.nudge.suppressed`           | counter | `cause, sources` (comma-joined)  | Dispatcher suppression branch.                                                                                                                     |
| `pa.nudge.sent`                 | counter | `sources, error_kind, escalated` | Extends existing counter. `escalated=true` when the dispatched intent set includes `disrupted` and `disrupt_escalated[sid]` was true at fire time. |

Log events (structured records):

- `session.api_error.observed` — kind, text (truncated to 256 chars), at,
  is_terminal, session/workspace labels.
- `nudge.queued` — sid, source, text-hash.
- `nudge.suppressed` — sid, sources, cause.
- `nudge.sent` — extends existing; add `sources`, `error_kind`,
  `escalated`.

Cardinality: `sources` label uses a comma-joined canonical form
(`disrupted,manual` etc.); maximum 7 distinct combinations across 3
sources.

## Grafana

`grafana/pa-monitor-overview.json` adds a new "API Errors & Nudges" row:

1. **Time series** — `rate(pa.session.api_error.observed[5m])` stacked by
   `kind`. Shows when errors fire across the fleet.
2. **Time series** — `pa.sessions.errored` stacked by `kind`; series
   filtered to `is_terminal=true`. Shows currently-stuck sessions.
3. **Stat** — current count of sessions stuck on retryable errors
   (`pa.sessions.errored{kind=~"unknown|server_error",is_terminal="true"}`).
4. **Stat** — `sum(increase(pa.nudge.suppressed[1h]))` — suppression rate
   indicates dispatcher catching active sessions.
5. **Time series** — `rate(pa.nudge.sent[5m])` stacked by `sources`. Shows
   what triggered nudges over time.

Existing nudge panel updated to use the `sources` and `error_kind` labels.

## Tests

Unit:

- `transcript/disrupt_test.go` — golden fixtures for each `error` kind,
  resume case (non-synthetic event after), IsTerminal flip on second
  synthetic error.
- `transcript/snapshot_test.go` — extended fixture sets `LastError`
  population alongside existing `RateLimitResetsAt` cases.
- `daemon/nudger/store_test.go` — multi-source Add/Cancel/ClearSession
  semantics; persistence round-trip.
- `daemon/nudger/producer_test.go` — table-driven state transitions per
  the DisruptProducer table; window-reset firing latch; manual queue/cancel.
- `daemon/nudger/dispatcher_test.go` — suppression, multi-source merge,
  send-failure-retry, watermark update on success.

Integration:

- `daemon/server_test.go` — gRPC `NudgeQueue`/`NudgeCancel`/`SetAutoResume`
  end-to-end.
- TUI smoke: `a` and `N` keybinds round-trip with a fake daemon.

Manual: live-fire test using a real Claude Code session — simulate a
disconnect by killing the network briefly while a session is mid-request,
verify the disrupt event lands in the transcript, the grace window
elapses, an intent gets queued, and the dispatcher fires the nudge.

## Migration

- No on-disk format break. `runtime.json` is extended additively; older
  pa-monitor versions ignore unknown keys.
- gRPC service additions are backwards-compatible (new RPCs, new fields
  on existing responses).
- TUI behavior changes are visible to users — the `a` key still toggles
  but now mutates daemon state, and `N` semantics change from "send to
  all" to "toggle scope-by-cursor". Document in `README.md`.

## Open Questions

None at spec time. Three items confirmed in brainstorm:

1. Multi-session manual-toggle semantics: "all-have → cancel all; any-missing → queue for missing."
2. `escalation_after_s` default: 60s.
3. Persist all pending intents (not just manual).

## Out of Scope (Follow-ups)

- A "nudge history" view in the TUI (history lives in OTel/Grafana).
- Per-kind glyphs in the TUI (we have two: retryable / non-retryable).
- A configurable "max nudges per session per hour" cap. The
  escalation mechanic already bounds retryable auto-nudges to one per
  fresh error event, which is the most likely upper bound that matters.
