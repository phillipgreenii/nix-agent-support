# ccpool transient-error retry + shared error classifier

**Status**: Draft
**Date**: 2026-06-16
**Deciders**: Phillip, Claude

## Context

On 2026-06-16 a `pr-pool drain` pass flagged both dispatched beads:

```
WARN bead flagged role=feedback-processor bead=zr-esnj err="zr-esnj: session exited before completing"
WARN bead flagged role=worker bead=zr-245.1 err="zr-245.1: session exited before completing"
```

Root-cause investigation (ccpool event log + Claude transcripts) showed **neither was a
pr-pool/ccpool defect**. Both Claude sessions hit the identical transient upstream error
within ~65s of each other:

> `API Error: 500 Internal server error. This is a server-side issue, usually temporary — try again in a moment.`

- `zr-esnj` (feedback) ran ~9 minutes of productive work — it had diagnosed the
  `budgets/malachi` build failure and was about to create child beads — when the 500 hit
  mid-tool-call. ccpool transitioned the session `working → errored` (the Claude
  StopFailure hook), pr-pool's `active()` poll saw the non-active session, and flagged
  the bead.
- `zr-245.1` (worker) died on its **first turn** (~51s in), having made zero changes.

The orchestrator behaved correctly: `errored` is a session FACT (ADR 0015), and on a dead
session pr-pool applies the role's `OnFailure`. But the **outcome is wasteful**: a brief
server-side blip discards all per-session setup (context load, skills, files read), and for
the worker role `OnFailure` adds the `human` label, which the worker discovery query
excludes (`bd ready --label worker-ready --exclude-label human`) — so a transient 500
**parks the bead for manual intervention even though the worker did no work**.

The retryable-error knowledge already exists in `pa-monitor`
(`internal/core/transcript/disrupt.go`): an `ErrorKind` allowlist and an `IsRetryable()`
predicate, used by its auto-resume daemon. The motivating idea: extract that classifier into
the shared `claude-transcript` module and let **ccpool retry truly-transient errors in place
by default** (disable-able), rather than discarding the session and escalating to the caller
for something a one-second backoff would have fixed.

### Forces

- Each package is its own Go module; cross-module reuse goes through the existing
  `claude-transcript` module (already a `replace => ../claude-transcript` dependency of
  ccpool, pa-monitor, and pr-pool).
- `pa-monitor` is already a daemon that auto-resumes retryable disrupts — but it
  **deliberately skips ccpool-managed pool sessions** via the `PA_MONITOR_NO_NUDGE=1`
  opt-out (set by ccpool, honored by pa-monitor's nudger). So pool sessions have **no**
  actuator today, and adding one to ccpool creates no double-nudge.
- ccpool has **no actuation loop**: `errored` is set purely as a side effect of the
  StopFailure **hook** (`cmd/ccpool/hook.go`), which only transitions state.
- "Retryable" is not an intrinsic property of an error — it is a _policy_. pa-monitor's
  current `IsRetryable()` (retry `server_error` + `unknown`) is pa-monitor's policy and does
  **not** match the policy wanted for ccpool (below).

## Decision

Three coordinated changes.

### 1. Move error classification into `claude-transcript` (shared)

Relocate the transcript error-classification primitives from
`pa-monitor/internal/core/transcript` into the shared `claude-transcript` module:

- `ErrorKind` (the closed allowlist: `rate_limit`, `server_error`, `unknown`,
  `invalid_request`, `authentication_failed`, `model_not_found`).
- `ErrorRecord` (`Kind`, `Text`, `At`, `IsTerminal`, `IsContextLimit`, `FromSubagent`).
- `LastAPIError(path)` and `LastSubagentError(path)`.
- `RateLimitPause(path)` + `parseLimitResetText` (rate-limit reset-time parsing).

**Remove the single `IsRetryable() bool` from the library.** Classification belongs in the
library; the _retry policy_ belongs in each consumer. Replace it with a richer, neutral
classification the library computes from `(kind, text)`:

```go
// RetryClass is the transience category of an api-error, derived from kind + text.
type RetryClass int
const (
    ClassTerminal       RetryClass = iota // not transient; caller decides
    ClassTransientServer                  // 5xx / 529 Overloaded / 522 / 502
    ClassTransientNetwork                 // transport drop: socket closed, ECONNRESET, …
    ClassRateLimited                      // rate_limit; has a reset window (RateLimitPause)
)
func (r ErrorRecord) RetryClass() RetryClass
```

Consumers map `RetryClass` to their own policy. This collapses the allowlist currently
duplicated in `disrupt.go` and `snapshot.go` into one source of truth.

#### Classification taxonomy (grounded in real transcripts)

A scan of all local transcripts produced this distribution and text shapes:

| `error` kind            | Count | Representative text                                                                   | `RetryClass`            |
| ----------------------- | ----- | ------------------------------------------------------------------------------------- | ----------------------- |
| `server_error`          | 18    | `500 Internal server error`, `529 Overloaded`, `522`, `502`                           | `ClassTransientServer`  |
| `unknown`               | 46    | `socket connection was closed unexpectedly` (20)                                      | `ClassTransientNetwork` |
|                         |       | `Unable to connect to API (ConnectionRefused / ECONNRESET / FailedToOpenSocket)` (13) | `ClassTransientNetwork` |
|                         |       | `Stream idle timeout - partial response received` (9)                                 | `ClassTransientNetwork` |
|                         |       | bare `Overloaded` (3), bare `Internal server error` (1)                               | `ClassTransientNetwork` |
| `rate_limit`            | 11    | `…resets 3:30pm (America/New_York)`                                                   | `ClassRateLimited`      |
| `authentication_failed` | 5     | `Please run /login · 401 …`                                                           | `ClassTerminal`         |
| `model_not_found`       | 2     | `…model may not exist…`                                                               | `ClassTerminal`         |

Notable: **every observed `unknown` (46/46) is in fact a transport/connection drop.** The
network matcher is therefore a positive allowlist applied when `kind == unknown`:

- `socket connection was closed`
- `Unable to connect to API` (covers `ConnectionRefused`, `ECONNRESET`, `FailedToOpenSocket`)
- `Stream idle timeout`
- bare `Overloaded`
- bare `Internal server error`
- (defensive) `socket hang up`, `ETIMEDOUT`

An `unknown` that matches none of these stays `ClassTerminal` (a genuine
opaque error → hand back). This bucket is empty in the current corpus but kept for safety.

Matching is case-insensitive and tolerant of an `API Error: ` prefix, matching the existing
`disrupt.go` conventions.

### 2. ccpool: retry transient errors in place (default-on)

ccpool gains a bounded, in-session retry actuated from the **StopFailure hook**. On
StopFailure, ccpool reads the session's transcript via `claude-transcript.LastAPIError`,
computes `RetryClass`, and:

- **`ClassTransientServer` or `ClassTransientNetwork`** and retry budget remaining →
  **retry in place**: wait the backoff, re-nudge the _same_ Claude session (preserving all
  setup), and keep the session out of `errored` (it stays/returns to `working`). Increment
  the persisted attempt counter.
- **Any other class, or budget/timeout exhausted** → transition to `errored` (today's
  behavior); the caller (pr-pool) decides. `ClassRateLimited` (5h/weekly) explicitly hands
  back — ccpool does **not** wait out the window; pr-pool's quota gate owns that policy.
- **Process drop** (pane dead, no StopFailure hook fires at all) is unaffected: it surfaces
  via pr-pool's `active()`-absent path as today → hand back. ccpool cannot resume a dead
  pane in-session.

#### Retry mechanics (defaults)

- **Max 3 attempts**, base delay **1s**, **exponential backoff** (1s → 2s → 4s), bounded by
  an overall **retry timeout** (default 60s) so a persistently-failing session hands back
  promptly rather than burning the full attempt schedule.
- The hook process performs the backoff wait then re-nudges (it already may block, e.g. the
  `ask` path). State persists across hook invocations in the store.
- **Counter resets on a successful turn** (a `stop`/idle transition with real progress) so a
  later, unrelated transient error gets a fresh budget rather than inheriting an old count.

#### Store schema (`005_retry_state.sql`)

```sql
ALTER TABLE sessions ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN retry_window_started_at INTEGER NOT NULL DEFAULT 0;
```

`retry_window_started_at` anchors the overall retry-timeout; `retry_count` drives the
attempt cap and the backoff exponent. Both reset to 0 on a successful turn.

#### Re-nudge mechanism

The retry must resume the _existing_ session, not relaunch. ccpool already owns tmux
send-keys (`internal/tmux`) and session resume; the hook re-delivers a minimal "continue"
nudge to the pane (the same actuation pa-monitor's nudger uses, which ccpool sessions opt
out of). Exact nudge text and whether a bare Enter suffices vs. a short prompt is an
implementation detail to settle against a live 500 repro.

#### Config (`config.toml`, pool-scoped)

```toml
[retry]
enabled      = true       # default-on; set false to restore hand-back-everything
max_attempts = 3
base_delay   = "1s"
timeout      = "60s"
# classes retried; default = the two transient classes
classes      = ["transient_server", "transient_network"]
```

Defaults live in `ccpool/internal/config` alongside the existing pool/tmux/claude blocks.

### 3. pa-monitor: migrate to the shared classifier

`pa-monitor` switches from its `internal/core/transcript` copy to
`claude-transcript`, and re-expresses its auto-resume predicate in terms of `RetryClass`.
Its current behavior (resume `server_error` + `unknown`) becomes resume
`{ClassTransientServer, ClassTransientNetwork}` — a slight, deliberate **tightening**
(opaque non-network `unknown` no longer auto-resumed), covered by tests. No behavior change
intended beyond that tightening; the `RateLimitPause`-based waiting is unchanged.

## Consequences

### Positive

- A transient 500/socket-drop no longer discards an in-flight pool session; the dominant
  failure mode from the 2026-06-16 incident self-heals without caller involvement.
- Workers stop getting parked behind the `human` label for transient blips that did no work.
- One source of truth for Claude api-error classification, shared by pa-monitor, ccpool, and
  available to pr-pool.

### Negative

- New persisted state and a hook that can block for the backoff window (bounded by
  `timeout`). The hook's never-fail policy must still hold: a retry-path error must not crash
  the hook — on any internal failure it falls back to today's `errored` transition.
- A genuinely non-transient error mislabeled `server_error`/network by upstream would be
  retried up to 3× before handing back (bounded waste: ≤ `timeout`).

### Neutral

- `RateLimited` is classified but, by ccpool policy, handed back rather than waited out —
  intentional division of labor with pr-pool's quota gate.
- pr-pool needs no change to consume this; the worker-parking-on-handback improvement (skip
  `human` when the worker made no commits) is a **separate** follow-up, out of scope here.

## Alternatives Considered

### Reuse pa-monitor as the sole actuator for pool sessions

Extract only the classifier; let pa-monitor's existing auto-resume daemon resume pool
sessions too (drop the `PA_MONITOR_NO_NUDGE` opt-out). Rejected: couples ccpool/pr-pool
reliability to a separate daemon running with auto-resume enabled and watching the right
tmux socket; and in the incident pa-monitor's later nudge (errored→needs*input ~1 min after)
landed \_after* pr-pool had already flagged the bead. ccpool owning a synchronous in-hook
retry is self-contained and races nothing.

### A ccpool reconcile pass (in `ccpool list` / reap) instead of the hook

Spot `errored`+retryable sessions on a cadence and resume them. Rejected as the primary
mechanism: needs an external caller on a timer, and reintroduces the same
"detected too late" latency. The hook fires synchronously at the moment of failure.

### Keep `IsRetryable()` in the library

Rejected: a single boolean can't serve two consumers with different policies (pa-monitor
retries `unknown`; ccpool must not retry opaque `unknown`). Classification in the lib +
policy in the consumer is the correct boundary.

## Implementation Phasing

1. `claude-transcript`: move classifier, add `RetryClass` + network matcher, port tests.
2. `pa-monitor`: switch to shared classifier, re-express auto-resume on `RetryClass`, delete
   the internal copy, prove no behavior regression.
3. `ccpool`: migration `005`, config `[retry]`, hook retry actuator + backoff + reset,
   `bypassPermissions`-safe re-nudge; live 500-repro validation.

Each phase keeps `nix flake check` green.

## Out of Scope / Follow-ups

- pr-pool: don't park a worker behind `human` when it made zero commits/worktree changes on
  a handed-back failure (the other half of the 2026-06-16 waste). Separate bead.
- Operational: unpark `zr-245.1` (`bd update zr-245.1 --remove-label human`) so the pool
  retries it — independent of this work.

## Related Decisions

- `ccpool` ADR 0015 — session FACTS, not work judgments (the `errored` semantics this builds on).
- See also: pr-pool budget watchdog design (`docs/superpowers/specs/2026-06-11-pr-pool-budget-watchdog-design.md`).
