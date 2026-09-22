# ccpool capacity counts non-preserved sessions; the cap is an admission gate

**Status**: Accepted (amends 0037)
**Date**: 2026-09-21
**Deciders**: Phillip Green II

## Context

ADR 0037 spares a `needs_input` session from both reap passes and lets it keep counting
toward `max_sessions`; its rationale assumed the cap "cannot starve new work" because nothing
refuses to start a session. Observed 2026-09-21: with 5 to 6 preserved sessions in a pool
capped at 6, every newly launched session was over cap, was the only non-preserved
candidate, and was closed by the next 300-second reap tick 2 to 5 minutes after its prompt
was delivered — 53 of 56 review dispatches and 44 of 46 feedback dispatches in one week.
The cap did not starve new work; it killed it.

## Decision

1. **Capacity counts only live, non-preserved sessions.** `counted = |{live rows with
state != needs_input}|`; `free = max(0, max_sessions - counted)`. Preserved rows are
   outside the cap entirely.
2. **Cap eviction targets only sessions whose turn has ended.** Pass 2 closes counted rows
   in state `idle` or `errored`, oldest-activity first, until `counted <= max_sessions` or
   no evictable row remains. A `starting`, `ready`, or `working` row is never closed for cap
   pressure. A hung `working` row is still closed by Pass 1 once `last_activity_at` exceeds
   `idle_ttl`; a `ready` row whose prompt was never ingested is closed by its dispatcher
   (reason `handler`) the moment ingestion fails.
3. **The cap is an admission gate for programmatic dispatchers.** `ccpool capacity` reports
   `max_sessions`, `live`, `preserved`, `counted`, `free`. A dispatcher MUST NOT launch when
   `free == 0` or when capacity cannot be read; it reports "not right now" through its
   transport's pre-accept busy decline and lets the caller re-offer with backoff.
4. **ccpool records why it closed a session, before it closes it.** Every close stamps
   `close_reason` and `closed_at` on the row and appends a `close` event BEFORE the tmux
   teardown, so an observer that sees the session gone can already read the reason.
   Reasons: `idle_ttl`, `cap_eviction`, `operator` (default for `ccpool close`), `handler`
   (`ccpool close --reason handler`, used by pg-router-ccpool-handler for every close it
   initiates). This is a session FACT about ccpool's own action, consistent with ADR 0015.
5. **`ccpool list` shows the close reason by default** whenever a visible row has one.

## Consequences

- A pool may hold up to `max_sessions` counted sessions plus any number of preserved ones
  plus any number of working sessions launched by humans outside the gate; `idle_ttl` is
  the only bound on that last group.
- A consumer that observes `close_reason in {idle_ttl, cap_eviction, operator}` knows the
  worker did not fail and MAY release the work item for retry, and SHOULD escalate after
  repeated evictions of the same item rather than retry forever.
- Rejected: an eviction grace period (spare `working` rows for N minutes after last
  activity). It still kills a legitimately long turn once N elapses, adds a config key, and
  depends on `last_activity_at`, which only advances on hook transitions.
- Rejected: preserved rows count and the gate refuses. Correct signal, but the pipeline
  halts until an operator closes preserved rows; ADR 0037's rationale wanted growth, not
  starvation.
- Rejected: a new handler exit code for "pool full". `INV-CCH-3` makes post-accept outcomes
  opaque, DEC-WIRE-1 reserves exit 3 for the core, and the daemon's listener consumes any
  non-busy error as accepted, which would drop the event. The busy decline already exists
  for exactly this case.
