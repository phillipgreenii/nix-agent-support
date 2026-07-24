# Glossary — pr-pool

Vocabulary for pr-pool's core. Concrete implementations (ccpool, beads, prometheus, …) define
their own terms in a downstream deployment set.

## Core

- **Core** — pr-pool itself: routes events to bound event handlers; runs as a socket service.
- **Registry** — the core's roster of participants that have registered with it. A participant
  registers to receive lifecycle signals and to make its callback reachable, and deregisters on exit.

## Events and matching

- **Event** — a typed, self-contained fact a source emits and the core routes. Carries an `id`, a
  `type`, a `ttl`, and an opaque `payload`.
- **Event type** — the primary field a binding matches on.
- **Binding** — the rule associating an event handler with the events it handles: a **match** over
  event fields. `type` is the default field; a binding MAY match on other declared fields. A matched
  field that is absent on an event simply does not match (it is not an error).
- **Query trigger** — what fires a pull event source's query: a **periodic** tick. (A tick is a query
  _trigger_, not a dispatched event.)
- **TTL** — how long the core **holds, offers, and retains** an event in the queue before dropping
  it if still unaccepted (`INV-EVT-1`).
- **Tracking id** — the id the core assigns to a call so a deferred reply or later callback can be
  matched back to it. Per-call.
- **Queue** — the core's **durable, ordered, de-duped, TTL-bounded** store of events (`INV-EVT-1`,
  `ADR 0031`). An event stays in the queue until its TTL **even after acceptance**, so a handler that
  binds within the TTL can still receive it and de-duplication (`INV-EVT-3`) covers already-delivered
  ids. A deployment MAY opt in to evicting an event once all bound handlers have accepted it.
- **Acceptance** — a handler's signal that it has taken responsibility for an event: an inline
  **completion** (synchronous) or a deferred **ack** (asynchronous), keyed by the tracking id. The
  core retries delivery **only until acceptance** (`INV-FAIL-1`); after acceptance the handler owns
  persistence, resume, and retry.

## Participants (the system actors)

- **Event source** — emits typed events; **pull** (the core queries it on a query trigger) or **push**
  (it calls the core's ingest callback). A push-only source still registers.
- **Event handler** — responds to an event it is bound to: it runs, reports status, and may be
  capacity-limited. Its concrete kinds (a "role") are named in a downstream deployment set.
- **Handler session** — one run of an event handler against one event, tracked by its tracking id.
- **Monitoring sink** — pulls or pushes a declared subset of the metric catalog.
- **Storage** — an optional key/value scratch a participant provides for core state; never backs
  delivery.
- **Callback** — a command the core hands a participant so the participant can push back to the core.
- **Push-inject** (the operator command source) — the operator subcommand (`push-inject`) that
  injects an arbitrary operator-supplied event into the **live** core — the front door to the
  push-ingest path,
  the same enqueue as `ingest-event` but operator-initiated (`INTF-CLI`). Its event is durable via the
  queue like any push event (`INV-EVT-1`); primarily for manual/test injection.

## Message schema & transport

- **Message schema** — the interface's formal **JSON Schema shapes** — the message types that cross a
  boundary (`INV-INTF-2`); the **shape** layer of the contract. The interaction pattern (inline vs
  deferred reply, a callback keyed by the tracking id) rides on top as invariants over these message
  types, not a separate contract.
- **Transport contract** — how a message schema is carried over a boundary: the default **CLI
  transport** (JSON on stdin/stdout, coarse exit codes), or an equivalent gRPC or in-code transport.
  One message schema MAY be realized over several transport contracts. ("Binding" is reserved for an
  event-matching rule, never a transport.)

## Outcomes

- **Failure class** — the coarse category a handler failure carries.
- **retryable** — a transient condition; the same event MAY be re-offered within its TTL and may then
  succeed.
- **resource-limit** — a capacity or quota ceiling was reached (e.g. a usage window); not a defect,
  and the handler will be able again once the ceiling lifts.
- **unavailable** — the handler cannot accept work right now (down, starting, or at capacity); an
  availability problem rather than a hit ceiling.
- **critical** — an error that MUST NOT be retried; a human must investigate.

## Wiring and observability

- **Wiring** (a **routing graph**) — the declared flow connecting event sources, event types, and
  event handlers through their bindings. The core validates **only** the wiring (no orphan event
  types, no unhandled source output, no disconnected handlers, loop detection) and reports on it; it
  does **not** validate workflow-completeness or sequencing — it is a flat edge-router, not a
  workflow engine (`INV-WORKFLOW-1`). (A deployment's user-facing **workflow** is a separate,
  downstream concept.)
- **Metric catalog** — the set of metrics the core declares (name, kind, unit, labels) and exposes to
  monitoring sinks — the neutral **shape**, including queue depth, failure rate, and
  unconsumed-expired (`INV-OBS-1`). **OTel** is the default emission transport for **metrics only**;
  logs stay JSONL.

## Principals (human or agent)

- **Operator** — a **principal — a human or an agent** — that configures, runs, and inspects the core
  via the CLI (drivable by either).

## Human actors

- **Observer** — consumes the core's monitoring output.
