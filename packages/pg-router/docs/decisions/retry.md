# Retry — pg-router decision docs

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

### `DEC-RETRY-2` — a role MAY opt in to a bounded re-run of a transiently failed dispatch <!-- uuid: 3441f46c-18ee-4a18-a5c5-ab5eb07766af -->

**Decided.** `INV-FAIL-1` leaves a post-accept failure to the handler: the core does not re-offer
it. A handler that is a thin command wrapper (for example a sync of one entity) has no retry loop
of its own, and a failure of that kind that is _transient_ — the handler or a child it ran was
killed under host overload, a deadline expired, or a backend reported itself unavailable — was
lost until some unrelated periodic sweep happened to re-drive the entity. So the core offers one
narrow, **opt-in** exception, in the same spirit as `DEC-OBS-3`'s:

- **Opt-in per role, default off.** A role carries `max_dispatch_retries` (0 = off, the default, so
  every existing role and every built-in role is unchanged). It MUST NOT be enabled for a role
  whose handler is not safe to run twice for the same event (event delivery is already
  at-least-once, `INV-EVT-1`, so a handler MUST be idempotent regardless).
- **Transient classes only.** `killed`, `deadline` and `unavailable`. A deterministic failure
  (validation, authentication, an ordinary non-zero exit with a reason) is never re-run, a failure
  while the run is shutting down is never re-run, and a busy decline keeps its own `INV-FAIL-1`
  path.
- **Bounded.** At most `max_dispatch_retries` re-runs per event, hard-capped at 3 by config
  validation, and never past the event's `expiresAt` (`INV-EVT-4`: an attempt on an expired event
  is its last). The count is in memory only (`DEC-EVENT-1`: no attempt history on disk), so a
  restart starts it over, still bounded by `expiresAt`.
- **Mechanism: a pre-accept decline.** The failed attempt is reported to the queue as a decline, so
  the existing `INV-FAIL-2` cadence (exponential backoff with a cap) spaces the re-run and the
  per-handler serial FIFO is preserved. This is what stops a host-overload storm from amplifying:
  re-runs are spaced, bounded per event, and serial per handler.
- **Observable.** Each scheduled re-run increments `pg_router_dispatch_retries{role,class}`
  (`class` is `killed`/`deadline`/`unavailable`; `role` is config-bounded, `DEC-OBS-5`). The
  counter is registered as `pg_router_dispatch_retries`; the Prometheus exporter appends `_total`, so
  queries and alerts spell it `pg_router_dispatch_retries_total`. Every failed attempt is still
  counted as a `handler-error` failure, and the decline is visible as
  `declined` with reason `dispatch-retry`.
- **Only effective with a retry window.** An event with no `expiresAt` is born expired
  (`INV-EVT-1`), so its single attempt is also its last and no re-run is possible: a producer whose
  role opts in MUST stamp a future `expiresAt` on its events. For a pull source that is a command
  query, the producer path is: the adapter's record carries `expiresAt` (and optionally `at`), the
  command query decodes it into the produced event's attributes, and the producer's enqueue copies
  it onto the queued event's `expiresAt` (and `at`), so the window survives into the queue. A record
  with no `expiresAt` is enqueued with neither and stays born expired.
