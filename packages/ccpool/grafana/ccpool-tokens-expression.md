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
