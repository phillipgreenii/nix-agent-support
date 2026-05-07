# Fix Auto-Resume Pause Detection + Manual Fire

**Status**: Draft
**Date**: 2026-05-07

## Context

Two issues observed when a session hit `You've hit your limit · resets 3:20pm (America/New_York)`:

1. The TUI did not mark the session as paused, and no countdown timer appeared.
2. There is no way to manually trigger the auto-resume message ad-hoc.

Root cause for (1): `internal/transcript/snapshot.go` Scan and `internal/transcript/ratelimit.go` only match the old rate-limit shape:

```
type=system, subtype=api_error, error.error.error.type=rate_limit_error, retryInMs>0
```

Recent Claude Code transcripts (≥ 2.1.126) encode rate-limit as a synthetic assistant message:

```jsonc
{
  "type": "assistant",
  "message": { "model": "<synthetic>", "content": [{ "type": "text", "text": "You've hit your limit · resets 3:20pm (America/New_York)" }] },
  "error": "rate_limit",
  "isApiErrorMessage": true,
  "apiErrorStatus": 429,
  "timestamp": "2026-05-05T17:12:37.907Z"
}
```

There is no `retryInMs`. The reset time is encoded only in the text as a clock-time + IANA TZ.

## Decision

### Bug fix: synthetic rate-limit shape

Extend the scanner in `internal/transcript/snapshot.go` to also recognize the synthetic shape and compute `RateLimitResetsAt` by parsing the message text against the event timestamp.

Add parser `parseLimitResetText(text string, eventTime time.Time) (time.Time, bool)` to `internal/transcript/ratelimit.go`:

- Regex extract `H:MM(am|pm)` and the IANA TZ in parentheses, e.g. `(America/New_York)`.
- Load the named location (`time.LoadLocation`).
- Compose candidate `time.Date(eventTime.Y, M, D, hour, minute, 0, 0, loc)`.
- If candidate ≤ eventTime, add 24h (next occurrence).
- Return UTC instant.

The `error` field is polymorphic: an object in the old shape, a string in the new shape. Keep the existing `scanEv` (with object-shaped `Error`) and attempt a *second* unmarshal of each line into a small auxiliary struct that has `Error string`. The two coexist because Go's `json.Unmarshal` errors when type-mismatched; the second try simply leaves the auxiliary fields zero when the old shape is present.

```go
type errStr struct {
  Error             string `json:"error"`
  IsApiErrorMessage bool   `json:"isApiErrorMessage"`
  ApiErrorStatus    int    `json:"apiErrorStatus"`
}
var es errStr
_ = json.Unmarshal(sc.Bytes(), &es) // ignore error; only succeeds when "error" is a string
```

Detection logic in the `assistant` branch:

```go
isSyntheticRateLimit := es.Error == "rate_limit" && es.IsApiErrorMessage
if isSyntheticRateLimit {
    text := plainAssistantText(ev.Message.Content)
    if t, ok := parseLimitResetText(text, ev.Timestamp); ok {
        lastAPIErrTime     = t
        lastAPIErrRetry    = 0   // 0 sentinel: lastAPIErrTime is absolute
        hasAPIErr          = true
        resumedAfterAPIErr = false
    }
    // Skip the existing "if hasAPIErr { resumedAfterAPIErr = true }" branch
    // for synthetic rate-limit events — they are not user/assistant resumes.
    break // out of the assistant case (skip token accounting too — synthetic has zero usage)
}
```

A *real* (non-synthetic) assistant or user event after the rate-limit still clears the pause via the existing `resumedAfterAPIErr` path.

After the loop, when `lastAPIErrRetry == 0` set `snap.RateLimitResetsAt = lastAPIErrTime` directly. Otherwise `lastAPIErrTime.Add(retryDuration)` (legacy path).

### Standalone `RateLimitPause` parity

`internal/transcript/ratelimit.go` `RateLimitPause` is also called from tests. Mirror the same logic so it returns the synthetic shape's reset time. Expose `parseLimitResetText` in the same file so both `RateLimitPause` and `Scan` share it.

### Feature: [M] manual fire

- Add `M` to `internal/tui/update.go` Update switch:
  - Iterate `m.tree.Dirs` → `Sessions`. Skip `Working`. Resolve signaler by PID. Send `m.autoResumeMessage`. Log on missing signaler / send error (same as auto-resume path).
  - Independent of `m.autoResume` and `m.autoResumeFired`. Does not mutate the tree.
  - No-op when `m.tree == nil`.
- Header `[C]affeinate ... | [R] auto-resume: ... | [M] resume now | [q]` controls line gains the `[M] resume now` hint.

### Tests

- `internal/transcript/ratelimit_test.go`:
  - `TestRateLimitPauseDetectsSyntheticAssistant` — synthetic shape with text "3:20pm (America/New_York)" → returns parsed time.
  - `TestRateLimitPauseSyntheticDayRollover` — when parsed clock time precedes event-day time-of-day, returns next-day occurrence.
  - `TestRateLimitPauseSyntheticThenAssistantClears` — synthetic followed by non-synthetic assistant → zero (resumed).
  - `TestParseLimitResetTextFormats` — table for am/pm casing, single/double-digit hour, IANA zones (`America/New_York`, `America/Los_Angeles`, `UTC`).
- `internal/transcript/snapshot_test.go`:
  - `TestScanSyntheticRateLimit` — Scan picks up synthetic shape into `Snapshot.RateLimitResetsAt`.
- `internal/tui/model_test.go`:
  - `TestModelManualFireSendsToAllNonWorking` — pressing `M` sends to two non-working sessions, skips a `Working` one. Independent of `autoResume`.
  - `TestModelManualFireNoTreeNoCrash`.

## Consequences

### Positive

- Pause UI (countdown + ⏸ glyph) works for current Claude Code (≥ 2.1.126).
- Auto-resume continues to fire correctly for the synthetic shape.
- User can force a resume push without waiting (manual override).

### Negative

- Text-parsing the reset time is fragile. If Claude Code changes the wording, detection breaks again. Mitigation: tests on the exact substring; parser is a small regex.
- Adds a key (`M`) that fires real keystrokes into other sessions independent of toggle state — accidentally easy to hit. Mitigation: confirm by user (out of scope for now); the message itself is only `m.autoResumeMessage` so impact is bounded to whatever the user configured.

### Neutral

- `RateLimitPause` and `Scan` continue to support both old and new shapes (last-event-wins).

## Alternatives Considered

### Match `apiErrorStatus==429` only, ignore text

Rejected: gives no reset time. The whole point of pause UI is the countdown.

### Hard-code a 5-hour fallback when text fails to parse

Rejected for now: silent fallback masks parser regressions. Better to surface "no pause detected" and add a test that fails when wording changes.

### Make `M` use the auto-resume fire path (with delay)

Rejected per user: manual must be unconditional and immediate.

## Related Decisions

None.
