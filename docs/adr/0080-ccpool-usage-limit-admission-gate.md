# ccpool refuses work while an account usage window is at its limit

**Status**: Accepted (extends 0072; resolves `pg2-wa0o0`)
**Date**: 2026-10-06
**Deciders**: Phillip Green II

## Context

ADR 0072 made the pool cap an admission gate: `ccpool capacity` reports `free`, and a
programmatic dispatcher declines busy when `free == 0`. That gate knows only about the pool's own
occupancy. When the account's 5-hour block or weekly limit is hit, every session ccpool launches or
prompts runs straight into the limit: the work is accepted, burns a slot, and fails or stalls until
the window resets.

The bead's requirement is that this holds "regardless how ccpool is called, directly or via
pg-router or some other way": ccpool itself must say it cannot accept the work, so a pg-router
event is held (re-offered with backoff) until the window resets or the event's TTL expires.

pa-monitor already holds the authoritative reading (ADR 0021): the status-line's server-side
`used_percentage` and reset instant for each window, folded across sessions by ADR 0029.

## Decision

1. **ccpool consults the co-resident monitor for the usage windows.** A window is HIT iff its
   used percentage is at or above `usage_gate.threshold_pct` (default 100) AND its reset instant
   is still in the future. When several windows are hit, the one that clears last binds.
2. **ccpool declines work itself, on every entry point that accepts work.** `ccpool new` and
   `ccpool reply` exit `8` (a code distinct from reply's 5 busy, 6 cancel-unconfirmed, 7
   prompt-not-ingested) with a stderr line naming the window, the percentage and the reset, before
   resuming, launching, trusting or delivering anything. `ccpool capacity` reports `free = 0`
   plus a `usage_limit` object (`window`, `used_pct`, `resets_at`) whatever the occupancy, so every
   existing admission gate that reads `free` declines without a code change.
3. **The pg-router handler keeps its single busy decline.** A usage limit surfaces as the same
   `at-capacity` busy reason (not a new one): it is routine, hours-long backpressure, and the
   failure-rate alert deliberately excludes that reason. The handler logs which window and until
   when. If the window fills between the capacity check and `ccpool new` (exit 8), the handler
   treats it as the same decline: it reclaims the abandoned session and worktree and does NOT stamp
   `pool-launch-fail` or escalate the bead to `human`.
4. **The gate fails open.** A missing monitor binary, an unreachable daemon, malformed output, a
   window with no percentage, or a hit window with no usable reset instant is "unknown", logged at
   warn level and read as not blocked. A hit window with no reset cannot be said to clear, so
   holding events for it could stall them forever; the monitor's own reset-bounding (ADR 0029
   and its horizon check) already discards garbage resets.
5. **Configuration is the `[usage_gate]` block of ccpool's `config.toml`**: `enabled` (default
   true), `command` (default `pa-monitor`), `threshold_pct` (default 100), `timeout` (default 5s).
   The packaged `ccpool` wrapper puts the packaged monitor on PATH, so every pool, including
   dedicated pools with their own `config.toml`, resolves the default command without wiring.
6. **The monitor's contract is `status --json`'s `rate_limits` object** (`five_hour` and
   `seven_day`, each with optional `used_pct` and `resets_at`, plus `captured_at`). Every field is
   independently optional; the key is omitted when no window is known.

## Consequences

- A hit window freezes new work AND new prompts into existing sessions. A session already mid-turn
  is not interrupted; pa-monitor's own auto-resume handles sessions blocked on the window.
- The gate asks the monitor on every `new`, `reply` and `capacity` call (one short subprocess,
  bounded by `timeout`). The monitor's `status --json` also queries per-session detail it does not
  need for this; accepted for now, and a lighter dedicated read is the follow-up if admission
  latency matters.
- A reading is only as fresh as the newest session that rendered a status line. A stale 100% whose
  reset is still in the future stays true (usage only rises within a window), and one whose reset has
  passed stops blocking by itself, so staleness cannot wedge the gate past the reset.
- Rejected: reading the status-line capture files directly from ccpool. It would duplicate the
  monitor's window-peak election (ADR 0029) and reset bounding in a second implementation.
- Rejected: a new handler busy reason. It would page the failure-rate alert for the hours a window
  takes to reset, duplicating the Grafana usage-limit alerts.
- Rejected: failing closed when the monitor is unreachable. ccpool would stop working on any host
  without the monitor daemon, for a protection that is only an optimization over the limit itself.
