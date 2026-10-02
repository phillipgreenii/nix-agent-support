# ccpool per-pool token usage expression

Consumed verbatim by the ccpool-pools dashboard (packet E).

## Expression

```promql
pa_monitor_session_tokens
  * on (session_id) group_left (pool, pgrouter_role)
    label_replace(
      max by (claude_session_id, pool, pgrouter_role) (ccpool_session_info),
      "session_id", "$1", "claude_session_id", "(.*)"
    )
```

Dashboard panels aggregate the result with `sum by (pool)` (optionally also
`by (pgrouter_role)`). Sessions with no `ccpool_session_info` match (for
example non-ccpool sessions such as `pristine-a1`) drop out of the `*` join and
are never mislabelled. The right side is deduplicated with `max by` so a
session seen by more than one reap cannot cause a many-to-many error.

## Panel description

Output tokens used so far by each LIVE Claude session, grouped by pool. Source:
pa-monitor `session_tokens`, which is CUMULATIVE over the session (the sum of
output_tokens across all assistant events in the session transcript; it is not
the current context size, which pa-monitor reports separately as
`context_tokens`). Only live sessions appear: a session's series vanishes when
it ends, so this is not a per-pool lifetime total. Sessions not managed by
ccpool are excluded.

## Verification status

- Gauge semantics verified by reading pa-monitor source:
  `internal/core/transcript/context.go` (`TotalTokens` = cumulative
  output_tokens), `internal/core/poller/poller.go` (`SessionTokens:
snap.TotalTokens`), `internal/otel/emitter.go` (gauge
  `pa_monitor.session.tokens`, Prometheus `pa_monitor_session_tokens`).
- Go emission verified by unit tests (`ccpool_session_info`, one per live
  session, none for a pruned phantom).
- NOT yet verified live (2026-09-30): the expression against a running
  Prometheus, and claude_session_id == session_id on a review and a feedback
  session (worker equality was verified 2026-09-29). The metrics cannot be
  observed before `pn workspace apply`; the live check, including review and
  feedback sessions and the fallback
  (`label_replace` of `session_name` into the role, valid only while a pool
  holds one role) if the join fails, is left to packet F.

## Finished-session tokens (pg2-om899.9)

The live-session expression above loses a session's tokens when it ends. The
dashboard therefore also carries two panels backed by a counter, not by a
recording rule.

### Expression

```promql
label_replace(
  sum by (pool, pgrouter_role) (
    increase(ccpool_session_output_tokens_total{pool=~"$pool",pgrouter_role=~"$role"}[$__range])
  ),
  "pgrouter_role", "unlabelled", "pgrouter_role", "^$"
)
```

The time-series panel uses the same expression with `[$__rate_interval]`.

### Mechanism and why

`ccpool_session_output_tokens_total{pool, pgrouter_role}` is an `Int64Counter`
incremented when an ended run's lifecycle metrics are emitted (the same
exactly-once `ClaimRunEmission` path as `ccpool_sessions_closed_total`, by the
reaper sweep, a close, or a purge). The increment is the session transcript's
output tokens (`claude-transcript.OutputTokens`, counted once per distinct
assistant message) minus the largest snapshot of the session's earlier runs,
kept in `session_runs.output_tokens_total` (migration 011); a resume appends to
the SAME transcript, so without the snapshot a resumed session would be
re-counted.

Chosen over the recording-rule option (`max_over_time` per
`(claude_session_id, pool)`, summed) because:

- Bounded cardinality: the counter has only the pool and allowlisted role
  attributes. A recording rule keyed on `claude_session_id` stores one series
  per session indefinitely, which the cardinality rule forbids on long-lived
  series.
- Correct windows: `increase()` over a counter gives tokens produced in the
  window. `max_over_time` per session over a window counts a session that began
  before the window in full, and `max - min` undercounts the first interval.
- Coverage: the live join depends on `ccpool_session_info`, emitted only once
  per 300 s reap, so a session shorter than one sweep is never joined and its
  tokens would be lost; the counter reads the transcript at run end.
- Respects the temporality caveat: it is a counter, so it relies on the delta
  temporality plus `deltatocumulative` pipeline (design P5) exactly like the
  other counter panels.

### Semantics and known limits

- Live and finished are shown on separate panels and are not summed: a resumed
  live session's cumulative gauge already includes tokens counted when its
  earlier run ended.
- A session whose transcript is already gone when its run is emitted (a pruned
  phantom) cannot be attributed. Sessions that exist at deploy time contribute
  their whole transcript to date at their first emitted run after migration 011
  (a one-time catch-up).
- Subagent (sidechain) transcripts are not summed, same as pa-monitor.
- Finding: `pa_monitor_session_tokens` does not deduplicate assistant lines by
  message id, although a multi-block assistant turn is written as one line per
  block with the same usage. On one real transcript the line sum was 687297
  against 294537 over distinct message ids (about 2.3x). The finished-session
  counter uses distinct ids, so the live and finished panels differ for the
  same work until pa-monitor is fixed; both panel descriptions say so.
