# ccpool reap closes dead needs_input rows after a long TTL, and counts them while they wait

**Status**: Accepted (amends 0037; resolves `pg2-s0pa3`)
**Date**: 2026-10-07
**Deciders**: Phillip Green II

## Context

ADR 0037 makes both reap passes spare a `needs_input` session, so a person can still `ccpool
attach` the question it holds. That reasoning covers a LIVE session. It says nothing about a row
whose tmux session has died: the row keeps its last observed state (ADR 0015), Pass 0 keeps it
because its Claude session is still resumable, and no pass ever closes it. The 2026-10-07 router
health review found exactly one such row on the default pool, held for weeks, with nothing
alerting on it. Two gaps made it invisible:

- `ccpool_sessions_preserved_for_human` counted only live rows, so it read 0 for a pool whose only
  parked session was dead. The correct signal was
  `ccpool_session_states{pool="default",state="needs_input",live="false"} = 1`, which no rule
  watched.
- Nothing closes such a row, ever.

## Decision

1. **Reap closes a dead `needs_input` row once it has been idle past a long TTL.** The TTL is
   `pool.dead_needs_input_ttl` in the pool's `config.toml`, default `720h` (30 days). The 30 days
   is the bead's suggestion and is unmeasured; it is far longer than any plausible human response
   and far longer than the 2-day alert below, so the alert is the human path and the TTL is only a
   backstop. `0` disables the backstop. A non-zero value below `24h`, or a negative one, is a
   config error that names the key (a `needs_input` session is parked for a person; closing a dead
   one within a day would race the operator about to attach it).
2. **The close is a non-purge close with the new reason `dead_needs_input_ttl`.** It stamps
   `close_reason` and `closed_at`, appends a `close` event and ends the run, exactly as
   `idle_ttl` and `cap_eviction` do. It keeps the row and the transcript, so the session stays
   resumable with `ccpool attach`, and it touches no worktree and no tmux session (a dead row has
   none). The reason is added to `store.CloseReasons` and so to `ccpool close --reason`'s
   vocabulary; `ccpool_reap_closures_total` carries it as its `reason` label.
3. **The pass is safe and idempotent.** It runs after Pass 0 and before the metrics and Pass 1.
   It considers only rows Pass 0 classified dead, and re-reads each one under the per-session lock,
   closing it only if it is still not live, still `needs_input`, not already closed since its last
   activity, and still strictly older than the TTL. A live row is never closed by it, whatever its
   age. A closed row has `closed_at >= last_activity_at`, so the next sweep skips it. Because
   `close_reason` is never cleared on resume, "closed" is defined by that timestamp comparison, not
   by the reason being non-empty: a row that is resumed and parks again (`last_activity_at` moves
   past `closed_at`) is awaiting a human again and is closable once more after another full TTL.
4. **A closed dead row no longer counts as awaiting a human.** `ccpool_session_states{live="false",
state="needs_input"}` drops it, even though the stored state is unchanged (ADR 0015: a close does
   not fabricate a state). Without this the alert below would fire forever on a row ccpool had
   already dealt with. This also stops counting a dead `needs_input` row an operator closed.
5. **`ccpool_sessions_preserved_for_human` now includes not-live `needs_input` rows** rather than
   gaining a sibling metric. The gauge's meaning is "sessions awaiting a human decision"; a dead row
   is awaiting one as much as a live row is, and a second gauge would leave the old one reading a
   misleading 0 for the case that motivated this ADR. The cost is that the gauge is no longer
   "live rows only"; a consumer that needs the live subset reads
   `ccpool_session_states{live="true",state="needs_input"}`. `ccpool capacity`'s `preserved` is
   unchanged: it counts live rows only, because capacity is about live slots (ADR 0072).
6. **An alert fires on a dead row waiting more than 2 days.** Rule
   `pg-router-ccpool-dead-needs-input` in `packages/pg-router/grafana/alerting/alerts.yaml`:
   `max by (pool)(last_over_time(ccpool_session_states{live="false",state="needs_input"}[1h])) > 0`
   for 48h, `severity: warning`, with static annotations. It lives beside `pg-router-pool-full-idle`,
   which already alerts on ccpool metrics from this file, so no new alerting home is invented; its
   promtool cases are in `grafana/alerting/rule-tests/pg-router-ccpool-dead-needs-input.test.yaml`
   and its Grafana-only fields are pinned in `internal/alertrules`. The lookback is 1h, not the 10m
   the pool-full-idle rule uses, so a sleeping laptop's gap of a few emitter samples does not reset a
   48h pending timer.

## Consequences

- `ccpool reap` now closes rows the earlier passes never touched, but only past the TTL and only
  without deleting anything. A pool that wants the old behaviour sets `dead_needs_input_ttl = "0s"`.
- `ccpool_reap_closures_total{reason="dead_needs_input_ttl"}` is a new series; dashboards that
  enumerate reasons need no change, ones that hard-code `idle_ttl|cap_eviction` miss it.
- `pg-router-ccpool-handler` treats only `idle_ttl`, `cap_eviction` and `operator` as an external
  close of a dispatch it is supervising. A 30-day-idle dead row is never a supervised dispatch, so
  `dead_needs_input_ttl` is deliberately NOT added there.
- The alert rule does not carry `escalation: human`: that label is pinned to
  `pg-router-origin-unavailable` alone by a test, so a bead filed for this rule goes to the triager.
  Routing it straight to the operator is an operator call. Registering the rule with
  `pg-router-probe` (the rules the probe files beads for) happens in the deployment's own module,
  outside this repo.
- The ccpool behavior docs' glossary says reap MUST spare a human-awaited session, TTL and cap
  eviction alike. That holds for a live one; the glossary now says a dead one is closed only by this
  long backstop.
- Rejected: purging the dead row. It destroys the `external_id` to `claude_session_id` map and the
  operator's way back into the conversation, for a row that costs one registry entry.
- Rejected: doing this in `ccpool-probe`. It is a detector, not a mutator.
