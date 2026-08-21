# Glossary — pr-pool

Vocabulary for pr-pool's core. Concrete implementations (ccpool, beads, prometheus, …) define
their own terms in a downstream deployment set.

## Core

- **Core** — pr-pool itself: routes events to bound event handlers; runs either long-running or
  drain-and-exit, and stays reachable to its participants while it runs (`INV-LIFE-1`).
- **Registry** — the core's roster of participants that have registered with it. A participant
  registers to receive lifecycle signals and to make its callback reachable, and deregisters on exit.

## Events and matching

- **Event** — a typed, self-contained fact a source emits and the core routes. Carries an `id`, a
  `type`, an optional `at`, an optional `expiresAt`, and an opaque `payload`.
- **Event type** — the **primary matcher**: a binding MUST match an event's `type` before anything
  else applies.
- **Binding** — the rule associating an event handler with the events it handles: a **match** over
  event fields. `type` MUST match; a binding MAY then narrow on a **payload path it names itself** (a
  **narrowing predicate**), applied after the type match, and the core reads only that path. Which
  fields are matchable is the binding's to say, never the source's. A field or path a binding names
  that is absent on an event simply does not match (it is not an error).
- **Query trigger** — what fires a pull event source's query: a **periodic** tick. It is the **core's**
  decision about when to poll, taken from the core's own state — never a source's to dictate.
  (A tick is a query _trigger_, not a dispatched event.)
- **Retry cadence** — how long the core waits before its next attempt after a failure it itself
  schedules the retry for: a pre-accept decline (the **handler retry cadence**, `INV-FAIL-2`) or a
  failed pull-source query (the **pull-source failure backoff**, `INV-FAIL-3`). Both share one
  exponential-backoff-with-a-cap **shape**; each is a config surface **distinct** from the query
  trigger's success-path polling interval — the trigger paces asking when things are fine, the
  cadence paces retrying when they are not.
- **`at`** — an event's **optional source stamp**, the instant the source says the fact happened.
  Absent, the core's own **ingest-now** applies (`INV-EVT-1`).
- **`expiresAt`** — the **optional absolute instant** past which the core stops retrying an event.
  Absent, it defaults to `at`, so an event carrying neither field is **born expired**: it is offered
  once to every matching handler and then dropped (`INV-EVT-1`, `INV-EVT-4`).
- **Retention** — how long the core keeps an event: through `expiresAt` and the one final attempt
  each matching handler is still owed, and no longer. Retention bounds both the queue entry and the
  `id` the core de-duplicates on (`INV-EVT-3`, `INV-EVT-4`).
- **Tracking id** — the id the core assigns to a call so a deferred reply or later callback can be
  matched back to it. Per-call.
- **Queue** — the core's **durable, ordered, de-duped, retention-bounded** store of events
  (`INV-EVT-1`,
  `phillipgreenii-nix-agent-support · packages/pr-pool/docs/decisions · DEC-EVENT-2`). An event
  stays in the queue for its **retention even after
  acceptance** — not so a late-binding handler can pick it up, but so **every matching handler still
  owed an attempt gets one** and so **de-duplication by `id`** (`INV-EVT-3`) still covers
  already-delivered ids. A deployment MAY opt in to evicting an event once all bound handlers have
  accepted it.
- **Acceptance** — a handler's signal that it has taken responsibility for an event: an inline
  **completion** (synchronous) or a deferred **ack** (asynchronous), keyed by the tracking id. The
  core retries delivery **only until acceptance** (`INV-FAIL-1`); after acceptance the handler owns
  persistence, resume, and retry.

## Participants (the system actors)

- **Event source** — emits typed events; **pull** (the core queries it on a query trigger) or **push**
  (it calls the core's ingest callback). A push-only source still registers.
- **Event handler** — responds to an event it is bound to: it **accepts** the event and owns the run
  from there, reporting its progress and outcome on its **own** surface rather than back to the core,
  and may be capacity-limited — a limit it enforces itself, which nothing declares to the core
  (`INV-CONC-1`). Its concrete kinds (a "role") are named in a downstream deployment set.
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
- **Transport contract** — how a message schema is carried over a boundary. One message schema MAY be
  realized over several transport contracts, and which one is the default is a realization decision —
  the schema is what the two sides must agree on (`INV-INTF-1`). ("Binding" is reserved for an
  event-matching rule, never a transport.)

## Outcomes

- **Failure class** — the coarse category a handler failure carries. The vocabulary is the
  handler-side contract; pr-pool itself **classifies and counts only the delivery-side** cases — a
  pre-accept decline and a dispatch failure it could not hand over — while the post-accept classes are
  the accepting handler's own to report (`INV-FAIL-1`, `INV-OBS-1`).
- **retryable** — a transient condition; the same event MAY be re-offered while it is unexpired
  (`INV-EVT-4`) and may then succeed.
- **resource-limit** — a capacity or quota ceiling was reached (e.g. a usage window); not a defect,
  and the handler will be able again once the ceiling lifts.
- **unavailable** — the handler cannot accept work right now (down, starting, or at capacity); an
  availability problem rather than a hit ceiling.
- **critical** — an error that MUST NOT be retried; a human must investigate.

## Wiring and observability

- **Wiring** (a **routing graph**) — the declared flow connecting event sources, event types, and
  event handlers through their bindings. The core validates **only** the wiring, **pre-runtime**, over
  six blocking checks — no orphan event types, no unhandled source output, no disconnected handlers, no
  handler left with no events to listen for, no absent backing command, and no determinably
  non-terminating re-entry cycle — and reports pass or fail on it; a re-entry cycle whose termination
  is **not determinable** is its one warning. It does **not** validate workflow-completeness or
  sequencing — it is a flat edge-router, not a workflow engine (`INV-WORKFLOW-1`). (A deployment's
  user-facing **workflow** is a separate, downstream concept.)
- **Metric catalog** — the set of metrics the core declares (name, kind, unit, labels) and exposes to
  monitoring sinks — the neutral **shape**. `INV-OBS-1` obliges the core to declare it; `INTF-MON`, the
  interface that carries it, states which metrics are in it.

## Principals (human or agent)

- **Operator** — a **principal — a human or an agent** — that configures, runs, and inspects the core
  via the CLI (drivable by either).

## Human actors

- **Observer** — consumes the core's monitoring output.
