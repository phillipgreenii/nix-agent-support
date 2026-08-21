# Retry — pr-pool decision docs

Realization decisions about the retry-cadence SHAPE and its concrete default values — the tuning
constants the behavior docs deliberately exclude (the floor names no tuning constant).

### `DEC-RETRY-1` — exponential-backoff-with-a-cap shape, and its default values, for both retry cadences <!-- uuid: 45879b15-28f4-473b-b090-74fde015dbb7 -->

**Decided.** Both cadences `INV-FAIL-2` (handler retry) and `INV-FAIL-3` (pull-source failure
backoff) share one SHAPE — a short initial wait, growing by a fixed factor on each consecutive
failure, capped at a maximum interval — realized once as `internal/backoff.Policy` and reused by
both surfaces. Only the surface-specific defaults differ, and both are seconds-to-low-minutes,
never hours: this is an interactive dev-workflow tool, not a background batch system, so a human
or a downstream automation is often waiting on the outcome.

**The shared shape's default values** (`internal/backoff.Default()`):

- **`Initial` = 5s** — fast enough that a handler freed within a few seconds (the common case: it
  simply finished its current session) sees the retry almost immediately, and that a transient
  pull-source blip (a rate limit, a momentary network hiccup) is smoothed over almost invisibly.
- **`Factor` = 2.0** — standard exponential-backoff doubling: it climbs to the cap within a handful
  of consecutive failures rather than lingering at a barely-useful interval for many attempts.
- **`Max` = 2m** — a handler still busy, or a source still down, after a couple of minutes of
  retries is probably going to stay that way for a while, so the cadence settles at a coarse,
  low-cost interval rather than hammering it or waiting hours to find out it recovered.

**The pull-source failure backoff's own additional default: `Retries` = 0.** Unlike the handler
retry cadence — whose "how many more attempts" question is answered externally by the event's own
`expiresAt` (`INV-EVT-4`) — a pull source's failure has no such external bound, so
`query.FailureBackoff` carries its own attempt cap. Defaulting it to zero means a query that has
not opted in fails exactly as fast as it always has (`pg2-qq9v`: "a query failure must NOT
masquerade as no ready work") — this addition changes nothing for an existing deployment unless it
explicitly configures `retries` (per query) or a pool-wide default above zero.

**Both cadences MAY be overridden**: the handler cadence per handler (a `Listener` implementing
`BackoffListener`, or the realization's own per-role config surface), the pull-source backoff per
query (`[query.failure_backoff]`) or pool-wide (`[pool].retry` / `[pool].pull_failure_backoff`) —
realization detail, not restated here since the behavior docs describe only that an override is
possible, never the concrete keys.

**Not decided here.** Whether a future surface needs a DIFFERENT shape (e.g. jittered backoff, to
avoid a thundering-herd effect across many handlers freeing up at once) is left for if and when
that need is observed; nothing here forecloses it.
