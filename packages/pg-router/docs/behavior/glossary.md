# Glossary — pg-router

Vocabulary for pg-router's core. Concrete implementations (a session runner, an issue store, a
metrics backend, …) define their own terms in their own behavior sets.

## Core

- **Core** — pg-router itself: routes events to bound event handlers; runs either long-running or
  drain-and-exit, and stays reachable to its participants while it runs (`INV-LIFE-1`).
- **Registry** — the core's roster of participants that have registered with it. A participant
  registers to receive lifecycle signals and to make its callback reachable, and deregisters on exit.
- **Resource** — a measured or declared condition with an ordered scale (`router`, `disk`, a tracker's
  reachability, a credential), named by the deployment; a registration's `requires` clauses and a
  sensor's answers both speak of resources (`INV-LIFE-2`, `INV-REQ-1`).
- **Level** — a resource's current position on its scale (graded: `PLENTY > OK > TIGHT > EXHAUSTED`;
  two-level: `UP > DOWN`, `AVAILABLE > UNAVAILABLE`, `RUNNING > PAUSED`; "greater" means better). A
  level is **global and out-of-band** and **persists across runs**: it is a durable record in the
  core's event log, reported by a sensor or set by the operator through the `INTF-CLI` (`level set`,
  `level clear`, `level refresh`, and `pause`/`resume` as sugar for the `router` resource), and it acts
  on **registrations, never on events** (`INV-LIFE-2`, `INV-LVL-1`). Contrast a **run-scoped
  selector** (`STORY-OP-3`), which selects the **active** subset of sources and handlers for a
  **single run** and never outlives it: a selector changes no persisted state; a level outlives every
  run until it is cleared, lapses or is replaced.
- **Sensor** — a **level source** that answers with a level and owns its thresholds, hysteresis and
  dwell; the core holds no thresholds and does not constrain how a sensor is built
  (`INV-SEN-1`, `INV-SEN-3`). Sensors keep working while the core is paused.
- **Level source** — a query whose output is **diverted to level ingest** instead of the queue
  (`INV-LVL-4`); a pull query or a push through the level-ingest call.
- **Requires** — the AND-only list of clauses (`<resource> <op> <LEVEL>`) a registration declares; the
  registration may start only while every clause holds (`INV-REQ-1`).
- **`ignored`** — what silence on a resource means to a registration: a resource with no clause is one
  the registration does not care about, whatever its level.
- **`unknown`** — the level an absent or grace-expired resource reads: best for every resource except
  credentials, which read the worst (`INV-REQ-1`, `INV-STALE-1`).
- **Grace** — the period after `expires_at` during which a stale level is still held, before the
  resource reads `unknown` (`INV-STALE-1`).
- **Operator override** — an `operator` level that wins while fresh; the sensor's value returns on
  expiry or `level clear` (`INV-SEN-6`).
- **Level owner** — the identity that reported a level (`operator`, or a sensor's own name); a typo
  guard, not a trust boundary (`INV-LVL-2`).
- **Probe versus sensor** — a **probe** files issues (it acts on an alert); a **sensor** only reports
  a level and never files or changes anything.
  **Debug-only**: it has no behavioral effect, is overwritten on re-set, and is shown on inspection
  surfaces but is never a metric label (its cardinality is unbounded).
- **Quiescing** — the reading a client gives an inspected core whose own lifecycle state (the
  `starting → started → stopping → stopped` diagram, `INV-LIFE-1`) has left `started` for `stopping`
  while it is not failing — the orderly-shutdown leg of that lifecycle, most visibly a
  **drain-and-exit** run winding toward its own exit predicate. It is a **distinct** fact from
  **quiescent** (`INV-LIFE-2`: nothing unsettled remains in flight): a core MAY be `stopping` with
  work still draining, and a long-running daemon MAY be quiescent while its state stays `started`
  throughout, so the two readings are never substituted for one another — both are informational,
  never failures.
- **Decline kind** — the optional tag a participant puts on a pre-accept decline to say how it
  counts toward its role's progress gauges: `busy` (a capacity decline; absent means `busy`), `fault`
  (a defect to count against the role) or `neutral` (counts toward neither). The core reads the kind,
  never the decline's reason (`INV-FAIL-1`, `INV-OBS-3`).
- **Progress gauges** — the three per-role gauges the core exports: the age since the role last made
  progress, and the counts of busy and of unsuccessful outcomes since it did (`INV-OBS-3`).

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
- **Calendar trigger** — a timer query's trigger that fires at a **local time of day** on chosen
  weekdays (every day by default), in the host zone or a named IANA zone, once per **slot**
  (`INV-EVT-6`, `INV-EVT-7`). It is a fourth kind beside the three of **Query trigger**, valid only
  for a timer query.
- **Slot** — the scheduled instant a calendar query fires at; the latest slot at or before a time `t`
  is the previous one, and the next is the first slot strictly after it. A slot yields at most one tick
  and names the tick's event id (`INV-EVT-6`, `INV-EVT-7`).
- **Query trigger** — what fires a pull event source's query, one of three kinds (a timer query may
  also use the **calendar trigger**, above): **periodic** (a
  tick, on the source's own configured period), **threshold** (queue-depth back-pressure — the
  depth of that source's own queued events crossing a bound), or **manual** (the smoke affordance
  only — `run-query` firing the query once, read-only, under a test-mode signal,
  `USECASE-VERIFY-PARTICIPANT`). Which kind fires is the **core's** decision about when to poll,
  taken from the core's own state — never a source's to dictate. (A tick is a query _trigger_, not
  a dispatched event.)
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
  `phillipgreenii-nix-agent-support · packages/pg-router/docs/decisions · DEC-EVENT-2`). An event
  stays in the queue for its **retention even after
  acceptance** — not so a late-binding handler can pick it up, but so **every matching handler still
  owed an attempt gets one** and so **de-duplication by `id`** (`INV-EVT-3`) still covers
  already-delivered ids. A deployment MAY opt in, **per event type**, to evicting an event once all
  bound handlers have accepted it (`[pool].early_eviction_types`); an event of a type that has not
  opted in keeps its full retention, and an event of an opted-in type MUST leave the queue and the
  queue-depth gauge when its last bound handler accepts it, not at its `expiresAt`. The
  `escalated.pg2` type uses this opt-in, because one bead is escalated at a time and de-duplication
  by `id` is not expected to matter for it.
- **Acceptance** — a handler's signal that it has taken responsibility for an event: an inline
  **completion** (synchronous) or a deferred **ack** (asynchronous), keyed by the tracking id. The
  core retries delivery **only until acceptance** (`INV-FAIL-1`); after acceptance the handler owns
  persistence, resume, and retry.

## Participants (the system actors)

- **Event source** — emits typed events; **pull** (the core queries it on a query trigger) or **push**
  (it calls the core's ingest callback). No source, push or pull, registers into the self-report
  registry — a source's liveness is reported entirely through its own `lastTick`/staleness fields
  (`INTF-CLI`), never registry membership. Self-report is handler-only.
- **Event handler** — responds to an event it is bound to: it **accepts** the event and owns the run
  from there, reporting its progress and outcome on its **own** surface rather than back to the core,
  and may be capacity-limited — a limit it enforces itself, which nothing declares to the core
  (`INV-CONC-1`). A configured handler is a **role**; the participant that runs it is the role's
  **adapter**.
- **Handler session** — one run of an event handler against one event, tracked by its tracking id.
- **Role** — a configured event handler, named by the operator: its **bindings**, the command of its
  **adapter**, an opaque **configuration table** and an optional **survives-shutdown** property, all
  declared once in the core's configuration (`INV-ROLE-1`). A role whose configuration fails
  validation stays registered and is **unavailable** (`INV-ROLE-5`).
- **Adapter** — the participant a role names to run it: a command the core invokes over the common
  manager contract, which bridges the core's handler contract to one kind of runner. The core never
  distinguishes adapters by kind; a new kind of runner is a new adapter, with no change to the core
  or to any other adapter (`INV-ROLE-1`, `GOAL-MIN-1`).
- **Role object** — the role's name and its opaque configuration table, which the core sends to the
  role's adapter in the payload of every request and never reads (`INV-ROLE-2`).
- **Base environment** — the minimal, documented set of environment variables (system variables,
  terminal variables and the markers the participant injects) that the processes pg-router starts
  itself, a query's producer and a command role's payload, start from while their strict-env switch
  is on; the concrete names live in each adapter's own docs (`INV-ENV-2`).
- **Item context** — the data of the item a role is dispatched, which the participant gives the
  payload through the templated elements of its argv and the markers it injects (`INV-ENV-2`).
- **Monitoring sink** — pulls or pushes a declared subset of the metric catalog.
- **Storage** — an optional key/value scratch a participant provides for core state; never backs
  delivery.
- **Callback** — a command the core hands a participant so the participant can push back to the core.
- **Excluded** — a **run-scoped selector**'s exclusion of a configured participant from the
  **active** set for a single run (`STORY-OP-3`); it ends with the run and edits no persisted state.
  This is the same fact `INV-DISP-3` describes in prose as a binding merely "disabled for this run"
  — the two texts name one condition. Inspecting a running core (`interfaces.md`) reports it as its
  own **independent boolean**, per listener/source, computed over the full configured set before any
  selector narrows it.
- **Disabled** — a **configuration**-level fact about a participant, independent of any run-scoped
  selector and outlasting the run — contrast **excluded** above, which is scoped to a single
  invocation and edits nothing persisted. Inspection reports it as the negated boolean `enabled`,
  independent of `excluded`, because a participant may carry either fact, both, or neither. Today's
  configuration schema has no such toggle yet (`OQ-CONFIG`), so `enabled` reads `true` for every
  configured participant until one exists — the boolean is carried now so the reply shape is correct
  once it does.
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
  handler-side contract; pg-router itself **classifies and counts only the delivery-side** cases — a
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
  six blocking checks — no orphan event types (a wildcard such as `pr.*` is never a match: routing is by
  exact string equality, so it is reported here), no unhandled source output, no disconnected handlers, no
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
