# Glossary — pr-pool

Vocabulary for pr-pool's core. Concrete implementations (ccpool, beads, prometheus, …) define
their own terms in `zr pr-pool-components`.

## Core

- **Core** — pr-pool itself: routes events to bound event handlers; runs as a socket service.
- **Registry** — the core's roster of participants that have registered with it. A participant
  registers to receive lifecycle signals and to make its callback reachable, and deregisters on exit.

## Events and matching

- **Event** — a typed, self-contained fact a source emits and the core routes. Carries an `id`, a
  `type`, a `ttl`, an optional `correlationId`, and an opaque `payload`.
- **Event type** — the primary field a binding matches on.
- **Binding** — the rule associating an event handler with the events it handles: a **match** over
  event fields. `type` is the default field; a binding MAY match on other declared fields. A matched
  field that is absent on an event simply does not match (it is not an error).
- **Query trigger** — what fires a pull event source's query: **periodic** (a tick, itself an event)
  or **threshold** ("enough events").
- **TTL** — how long the core may hold or redeliver an event before dropping it.
- **Correlation id** — an optional grouping key on events, used to correlate several events for
  aggregation.
- **Tracking id** — the id the core assigns to a call so a deferred reply or later callback can be
  matched back to it. Per-call, and distinct from a correlation id (which is per-event-group).

## Participants (the system actors)

- **Event source** — emits typed events; **pull** (the core queries it on a query trigger) or **push**
  (it calls the core's ingest callback). A push-only source still registers.
- **Event handler** — responds to an event it is bound to: it runs, reports status, and may be
  capacity-limited. Its concrete kinds (a "role" such as feedback/worker/review) are named in zr.
- **Handler session** — one run of an event handler against one event, tracked by its tracking id.
- **Monitoring sink** — pulls or pushes a declared subset of the metric catalog.
- **Storage** — an optional key/value scratch a participant provides for core state; never backs
  delivery.
- **Callback** — a command the core hands a participant so the participant can push back to the core.

## Outcomes

- **Failure class** — the coarse category a handler failure carries.
- **retryable** — a transient condition; the same event MAY be re-offered within its TTL and may then
  succeed.
- **resource-limit** — a capacity or quota ceiling was reached (e.g. a usage window); not a defect,
  and the handler will be able again once the ceiling lifts.
- **unavailable** — the handler cannot accept work right now (down, starting, or at capacity); an
  availability problem rather than a hit ceiling.
- **critical** — an error that MUST NOT be retried; a human must investigate.

## Workflow and observability

- **Workflow** — a declared flow connecting event sources, event types, and event handlers through
  their bindings, so the wiring can be validated (no orphan event types, no unhandled source output,
  no disconnected handlers, loop detection) and reported on.
- **Metric catalog** — the set of metrics the core declares (name, kind, unit, labels) and exposes to
  monitoring sinks.

## Human actors

- **Operator** — configures, runs, and inspects the core via the CLI.
- **Observer** — consumes the core's monitoring output.
