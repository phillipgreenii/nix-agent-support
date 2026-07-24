# pr-pool core holds a durable, ordered, de-duped, TTL-bounded event queue

**Status**: Accepted
**Date**: 2026-07-24
**Deciders**: Phillip Green II

## Context

The pr-pool core's event model was never a persistent queue by design.
[ADR 0026](0026-pr-pool-behavior-scope-orchestrator-only.md) scoped the core to a bare
orchestrator that **drains to empty** and is re-invoked by a scheduler. The event-model
direction that followed — separating role from query via typed events — was captured in bead
`pg2-r6cf` and carried into the behavior-doc sets landed under `pg2-ekqq8`. That rationale settled
on **best-effort, in-memory, TTL-bounded** delivery: a pull source re-derives on its next query
trigger so a dropped event reappears; a push-only source's dropped events are simply **lost**;
storage was deliberately decoupled from delivery.

Critically, that reasoning lived in a **bead**, not a decision doc the behavior-doc set points to
(an instance of the discoverability gap the review labels #17). A review of the session/event seam
(finding **#2**) then found the best-effort model under-serves the core's own lifetime:

- the core uses an event's `ttl` (a **delivery** bound) as the only clock for session waiting, yet
  a session can outlive event deliverability;
- core-exit / restart behavior for `running` / `paused` sessions is undefined, and in-flight work
  is "not guaranteed to survive a restart";
- a callback bearing an unknown or already-expired tracking id has no defined handling;
- a push-only source silently loses events it cannot re-derive.

The review resolution reverses the best-effort stance for **event delivery** (only): the value of
never losing accepted work, letting a new listener catch up within TTL, and de-duplicating across a
real retention window outweighs the disk cost and the per-listener head-of-line blocking a durable
ordered queue introduces.

## Decision

**Introduce a durable, ordered, de-duped, TTL-bounded event queue in the pr-pool core**,
consciously **reversing the best-effort/in-memory delivery stance of `pg2-r6cf`.** The storage
**mechanism** (jsonl / embedded DB / write-ahead log) stays an open realization choice — Kafka and
peers are rejected as too heavy — and is not behavior; the observable behavior below is.

The queue **MUST** satisfy all seven requirements:

1. events are processed **in order**;
2. events are **de-duped by `id`**;
3. events **expire after their TTL**;
4. delivery is **at-least-once** — the durable record is written only **after acceptance is
   confirmed**, so a narrow crash window MAY redeliver; therefore **listeners MUST be idempotent**;
5. an event **stays in the queue until its TTL even after acceptance**, so a new listener within
   TTL can still receive it and the de-dup window covers already-delivered ids;
6. the core **attempts delivery until an event is accepted** (or its TTL expires → dropped
   undelivered);
7. the core **tracks which listeners accepted** an event and holds the event data until TTL.

**Retry boundary.** The core retries **only until acceptance**. After acceptance the **listener**
owns persistence, resume, and retry. A **synchronous** listener's `accept == complete`; an
**asynchronous** listener's `accept == ack`, and it reports the outcome later. "Acceptance" is a
deferred-reply **ack** or an inline **completion**.

**Ordering — per-listener serial FIFO.** The core offers a listener its head matching event until
that listener accepts it or the event's TTL expires; on acceptance it marks the event and offers
the next — **one outstanding offer per listener**. Per-listener cursors are independent and order
is **never global**. Fan-out across listeners is supported; acceptance is tracked per
`(event, listener)`. **Head-of-line blocking is accepted per-listener**: a long-TTL head a listener
cannot accept stalls that listener's stream until the head expires — hence the operational guidance
**"keep TTLs small."**

**Capacity is listener-enforced.** Capacity is a listener's **pre-accept `busy` decline**, not a
core-tracked number. "One event → one session" holds **within** a listener; fan-out is **across**
listeners.

**Retention is independent of consumer state.** An event stays queued until its TTL regardless of
whether any consumer is up, down, or disabled. A disabled or absent consumer simply leaves its
events unconsumed until they expire — **not** an error.

**Opt-in early eviction.** The default is to keep every event until TTL. A deployment **MAY** opt in
to **evicting an event once all bound consumers have accepted it** (disk savings). The trade-off —
and why it is opt-in — is that a consumer added or re-enabled after eviction but before TTL
**misses** the event, and that id's de-dup window shortens; it is safe only when the consumer set is
fixed.

**WAL blind to external changes is intended.** The queue delivers the **event as of its creation**;
a listener that needs current truth looks it up itself.

## Consequences

### Positive

- No accepted event is lost across a normal restart; in-flight work survives (at-least-once).
- A listener that registers within an event's TTL still receives it (new-listener catch-up).
- De-duplication is meaningful across a real retention window, including already-delivered ids.
- A push-only source's events are durable to TTL — the old "push-only events are lost" caveat is
  removed.

### Negative

- **Per-listener head-of-line blocking**: a long-TTL head event a listener cannot accept stalls its
  stream until the head's TTL — mitigated operationally by keeping TTLs small.
- Durable storage costs disk and adds a write-ordering (WAL-after-acceptance) obligation.
- A narrow crash window (accepted but not yet persisted) MAY redeliver — absorbed by idempotent
  listeners.

### Neutral

- The storage mechanism is unspecified here; it is a decision-doc / realization choice, not
  behavior.
- The best-effort **`crashing` lifecycle signal** (`INV-LIFE-1`) is unchanged: it remains
  best-effort. Only event **delivery** is made durable; the lifecycle signal is a separate concern.
- **TTL clock origin** (event `at` vs ingest time) is left as an open question (`OQ-EVT-TTL-ORIGIN`,
  recorded in the pr-pool invariants).
