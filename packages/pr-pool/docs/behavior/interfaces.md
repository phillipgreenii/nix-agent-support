# Interfaces — pr-pool

This file follows the interface convention of the behavior-docs method
(`phillipgreenii-nix-agent-support · behavior-docs/docs/behavior`): an **interface** is a
boundary described by **what crosses it** and **what must hold**, so the parties on each side
can be confirmed to agree — never _how_ it is implemented. See the [glossary](glossary.md) for
terms, [actors](actors.md) for who sits on each side, [invariants](invariants.md) for the rules,
and [journeys](journeys.md) for the flows that exercise these interfaces.

pr-pool's core is a **dispatcher**: everything outside it is a pluggable **participant** reached
through one of the interfaces below. Which concrete implementation fills a participant — a bead
store, ccpool, prometheus — is uninteresting to the core; only these contracts are. (The concrete
implementations and any organization-specific workflow live in a downstream deployment set that
implements these interfaces.)

The five interfaces, each named for its boundary rather than a number, are described on **two axes**
— the **kind** of party on the far side, and whether that party is an **essential or optional
participant** in the system's own operation (the split this set's [README](README.md) draws). Kind
alone would flatten the second axis away, because four of the five counterparties are the same kind:

| Interface      | Boundary                                 | Counterparty (kind)                       | Participation | Initiator                    |
| -------------- | ---------------------------------------- | ----------------------------------------- | ------------- | ---------------------------- |
| `INTF-SOURCE`  | typed events into the core               | `ACTOR-SRC` event source (implementer)    | essential     | core (pull) or source (push) |
| `INTF-HANDLER` | events out to a handler; acceptance back | `ACTOR-HDL` event handler (implementer)   | essential     | core                         |
| `INTF-MON`     | the metric catalog out                   | `ACTOR-MON` monitoring sink (implementer) | optional      | either (sink declares)       |
| `INTF-STORE`   | key/value scratch for core state         | `ACTOR-STO` storage (implementer)         | optional      | core                         |
| `INTF-CLI`     | operator commands; manager callbacks     | `ACTOR-OP` operator (**actor**)           | driving port  | operator / manager           |

The sections below follow that ordering — **essential** participants first, then **optional** ones,
then the **driving port** last. `INTF-CLI` is last because it is the odd one out on both axes: its
counterparty is an **actor** rather than an implementer, so there is nobody to run a conformance
suite on the far side, and it drives the system rather than participating in the event path.

```mermaid
flowchart LR
    subgraph core["pr-pool core — dispatcher + registry"]
      C["route event → a bound handler"]
    end
    SRC["event source"] -- "INTF-SOURCE: typed events" --> C
    C -- "INTF-HANDLER: dispatch event → handler session" --> HDL["event handler"]
    HDL -. "INTF-HANDLER: accept or decline (reply)" .-> C
    C -- "INTF-MON: metric catalog" --- MON["monitoring sink"]
    C -- "INTF-STORE: get/put/delete" --- STO["storage (optional)"]
    OP["operator"] -- "INTF-CLI: run / inspect" --> C
```

## The common manager contract

Every participant interface shares one shape. `INV-INTF-1` (in [invariants](invariants.md))
requires it; the details are here.

- **A message schema, carried over a transport contract.** Every request and reply is defined by the
  interface's formal **message schema** — the shape layer, and what the two sides must agree on — and
  that schema is carried over a **transport contract**. One message schema MAY be realized over
  several transport contracts, so a participant speaking a different transport still conforms so long
  as it carries the same schema. Which transport is the default, and what its payloads look like
  concretely, is a realization decision
  (`phillipgreenii-nix-agent-support · packages/pr-pool/docs/decisions · DEC-WIRE-1`).
- **Schema versioning.** Every request and reply is **versioned**, and a party that receives a
  version it **cannot handle MUST report it rather than guess**.
- **Tracking id.** Every core→participant request carries a **tracking id** unique to that call, and
  a deferred result or a later callback **MUST be correlatable back to the originating call** — by
  echoing the core's id, or by returning the participant's own id in the deferred acknowledgement,
  which the core then stores and maps back. Either way the core can match a later delivery to the
  original call. The tracking id is per-call.
- **Deferred replies.** A reply is **either** an inline result **or** a **deferral**. Where a
  deferred reply still **owes the core a result**, the participant later reaches the core over its
  **callback** channel, keyed by the tracking id — `INTF-SOURCE`'s deferred `query`, whose events
  arrive later via `ingest-event`. Where the deferred reply **is** the acceptance and the core is owed
  nothing further — `INTF-HANDLER`'s `dispatch` — **no later callback follows**. The core handles
  either form on every call; a participant does not declare a sync/async mode up front. Which calls a
  given interface may defer is shown in that interface's sequence diagram below.
- **Callback.** When the core needs to be reached back, it hands the participant a **single
  ready-to-run callback** already carrying everything needed to reach and authenticate against the
  core. The participant appends its own arguments and runs it, and **MUST NOT have to assemble the
  core's address or credential itself** — so no participant carries addressing or credential logic of
  its own to get wrong. The one concrete callback target for a deferred or pushed result is the
  `INTF-CLI` `ingest-event` subcommand — a **source**'s. A handler needs no callback target of its
  own: its acceptance already arrives in the **dispatch reply**, so nothing is left for it to call
  back about.
- **Coarse outcome, rich reply.** A call's outcome is signalled **coarsely by the transport** — it
  worked, it failed unexpectedly, it was invoked wrongly, or the participant is busy — with the **rich
  outcome carried in the reply body**. A participant in a degraded state MAY answer with the coarse
  signal alone and no body, so declining never depends on being able to compose a reply. On the
  default CLI transport those four signals are the exit codes `0` ok, `1` unexpected error, `2`
  **usage** error, and `9` **`busy`** — a **pre-accept decline** (`INV-CONC-1`). The **low** codes
  carry meanings general to any app, which is why `2` is **reserved** for usage and `busy` sits
  outside that band rather than on `2`; a caller reading one as the other would treat a decline as a
  typo, and a typo as "re-offer later" (`ADR 0042`). Codes at or above `3` other than `9` are a
  participant's own (`phillipgreenii-nix-agent-support · packages/pr-pool/docs/decisions · DEC-WIRE-1`).
- **Self-status.** Any participant MAY push its **own** status — `healthy` / `degraded` /
  `unavailable` — independent of any per-item outcome. This is the participant reporting on
  **itself**, and it is the **only** status channel into the core: an `unavailable` self-report is a
  **pre-accept decline** the core acts on by re-offering the event while it is unexpired
  (`INV-FAIL-1`, `INV-CONC-1`). It is distinct from the accept-or-decline reply a participant gives to
  one dispatched item, and pr-pool takes no per-item progress stream at all. **How this push reaches
  the core is not yet realized**: no participant kind is handed a callback for it today — the one
  callback target that exists at all (`INTF-SOURCE`'s `ingest-event`) carries events, not self-status
  — see the realization-gap register (`README.md`'s "Realization gaps", against `INV-INTF-1`).
- **Registry & lifecycle.** A participant **registers** with the core (joins the **registry**) to
  receive lifecycle signals and to make its callback reachable, and **deregisters** on exit. The
  lifecycle and its state diagram are the next section.
- **Conformance suite.** Each interface ships a suite of **positive and negative checks** that
  invoke the subcommands and assert the replies match the JSON Schema, so an implementation can
  verify it adheres _before_ the core is trusted to route through it (`INV-INTF-2`).

## Lifecycle

The core signals a participant through a fixed lifecycle. For an executable participant these are
realized as spawn/stop; for a daemon participant that may outlive the core, as register/deregister.
The core tolerates participants whose lifetime is shorter or longer than its own; **multiple**
participants of any kind may exist, and one process MAY implement several interfaces at once (the
core neither knows nor cares).

```mermaid
stateDiagram-v2
    [*] --> starting
    starting --> started: ready to serve
    started --> stopping: orderly shutdown requested
    stopping --> stopped: drained
    stopped --> [*]
    starting --> crashing: sudden failure
    started --> crashing: sudden failure
    stopping --> crashing: sudden failure
    crashing --> [*]
    note right of started
      Messages are accepted ONLY here:
      after started, before stopping.
    end note
    note right of crashing
      Best-effort signal on sudden
      shutdown; MAY not be delivered.
    end note
```

- **`starting`** — the participant is initializing (opening its store, registering). The core does
  **not** yet route messages to it, and does not yet rely on its callback.
- **`started`** — the participant is serving. **This is the only state in which messages are
  accepted** — core→participant requests and participant→core callbacks. `INV-INTF-1` states this
  boundary as a rule.
- **`stopping`** — an orderly shutdown is underway. The core stops issuing _new_ requests; in-flight
  deferred work MAY complete on the callback channel or be abandoned. New messages are not accepted.
- **`stopped`** — the participant has drained and deregistered; no messages cross.
- **`crashing`** — a **best-effort** signal emitted on a _sudden_ shutdown (crash or forced kill)
  from any active state. It is a courtesy, not a guarantee: it MAY be lost entirely. A participant
  or the core that receives it SHOULD treat the peer as immediately gone. Because it is best-effort,
  no correctness rule may depend on it — it only lets the healthy side react faster than a timeout.

## Event delivery (shared by `INTF-SOURCE` and `INTF-HANDLER`)

Delivery of an **event** from a source to a handler goes through the core's **durable, ordered,
de-duped, retention-bounded queue** and is **at-least-once** (`INV-EVT-1`, `ADR 0031`). An event is
enqueued durably and the core **offers it until a handler accepts** (`INV-CONC-1`), and **every
matching handler gets at least one opportunity** — nothing is dropped un-offered. An event carries an
optional **`at`** (the source stamp; absent, the core's own ingest-now applies) and an optional
**`expiresAt`** (an absolute instant; absent, `at` applies). If the event is **already expired at the
moment an attempt is made, that attempt is the last one for that handler**, after which the event is
dropped unconditionally (unconsumed-expired, `INV-EVT-4`). An accepted event is **retained** while its
`id` is still needed for de-duplication and while any matching handler is still owed an attempt, and
delivery **survives a restart**.

**Acceptance** is the reply that hands responsibility to the handler: an inline **completion**
(synchronous) or a deferred **ack** (asynchronous), keyed by the tracking id. The core's delivery
responsibility ends at acceptance; the durable record is written **after acceptance is confirmed**,
so a **narrow crash window** (accepted but not yet persisted) MAY redeliver. Therefore:

- an **event handler MUST tolerate duplicates** (be idempotent) — the crash window, a pre-accept
  re-offer while the event is unexpired, or a source re-emitting can redeliver the same event
  (`INV-EVT-2`);
- the core **de-duplicates by event `id` across the retained id set** — including ids already
  delivered/accepted (`INV-EVT-3`) — so a source never has to track "did I already emit this". With
  the born-expired default that retention window is roughly one dispatch cycle, so a pull source's
  next-trigger re-emit is **not** deduped (`INV-EVT-3`);
- a **pull source** re-derives current truth on its next **query trigger**; a **push-only source**'s
  events are now **durable through their retention** (no longer lost on a core restart), lost only if
  nothing accepts them before they expire.

A callback bearing a **tracking id the core does not recognize** (never issued, or already expired and
evicted) is **acknowledged and ignored** — a logged no-op, not an error, and it does not resurrect an
expired event (`INV-INTF-1`).

## `INTF-SOURCE` — event source <!-- uuid: fe42416a-5f10-4db1-b8c3-46b1609213c7 -->

- **Counterparty:** `ACTOR-SRC`, a pluggable event source. **Initiator:** core (**pull**) or source
  (**push**). **Multiplicity:** zero or more.
- **Purpose:** supply typed **events** for the core to route.

**One opaque invocation, not a menu of source kinds.** There is exactly **one** source contract. To
pull, the core makes **one invocation** of the source and reads the reply against the **query-reply
contract** below; it does not know — and **MUST NOT** be told — **what kind** of source it is
invoking or how that source arrives at its events. Whatever a particular source needs in order to
answer (a tool it drives, a query language, credentials, paging, a cache) sits **inside** the
invocation the operator configures, and is **never** a case the core distinguishes: the core would
otherwise hold one tool's configuration inside its own, which is what `GOAL-MIN-1` forbids and what
the boundary principle in this set's [README](README.md) rules out. So **adding a source is
configuration**: name the invocation, the event types it emits, and — for a pull source — its query
trigger. The core gains nothing.

**Pull mode.** The core invokes `query` on a **query trigger** — a **periodic** tick. The reply
carries events inline, or defers and delivers them later on the callback (the `ingest-event`
target).

**Push mode.** The core never calls `query`; the source invokes the `ingest-event` callback as
external facts arrive. A push-only source still **registers** so it appears in the registry and its
lifecycle is known.

**The query trigger belongs to the core, and stays.** Deciding **when to poll** is pr-pool's own
scheduling decision, taken from pr-pool's own state; it is not part of the source's configuration and
a source is never asked when it would like to be queried. That is why the trigger stays in this
contract while the query's internals do not — the two look alike from outside and are opposite sides
of the boundary.

**What a source declares — and what it does not.** A source declares the **event types it emits**;
that declaration, its invocation, and its mode are the whole of its configuration. The declared
emitted types are a **contract boundary and MUST stay one**: the wiring validation runs on them in
**both** directions (`INV-WORKFLOW-1`, `USECASE-VALIDATE-CONFIG`) — a bound `type` no source emits is an
**orphan event type** and an emitted `type` no binding declares is an **unhandled source output**, and
both are **blocking errors** — and neither check has anything to compare without them. What a source does **not** declare is which of its fields may be
matched, or any shape for `payload`: **matchability is the handler's alone** (`INV-DISP-1`).

**Event shape.** An event carries the following fields, and nothing here constrains how a source
arrives at their values:

- `id` — unique; the core de-duplicates on it across the retained id set (`INV-EVT-3`).
- `type` — the **primary matcher**: a **binding** MUST match it before anything else applies.
- `at` — **optional** source stamp. Absent, the core's own "now" at ingest is used (`INV-EVT-1`).
- `expiresAt` — **optional**, an **absolute instant**. Absent, `at` is used, so an event carrying
  neither field is **born expired**: offered once to every matching handler, then dropped
  (`INV-EVT-4`). Setting it in the future is how a retry window is requested; nothing computes a
  duration.
- `payload` — MUST be a JSON **object** (a keyed structure, never a bare scalar/array), so a handler
  always receives a struct. The core neither reads nor validates it, save for a single path a
  **binding** names for narrowing (below). Its **shape is not declared anywhere** — see
  `OQ-EVT-CATALOG`.

**Matching (what a binding may match).** A **binding** matches an event over its **fields**, and
**matchability is the handler's alone** — a source declares nothing about which of its fields may be
matched:

- `type` is the **primary matcher** and **MUST** match; a binding does not match an event whose `type`
  it does not name, whatever else that event carries;
- a binding **MAY** then carry one **narrowing predicate** over a **payload path it names itself**,
  applied **only after** the type match succeeds. The core reads **only that path** and nothing else
  in the payload, so `payload` stays opaque everywhere the binding does not point — it is the
  **binding**, not the source, that makes one path readable _for matching_;
- a field or payload path a binding names that is **absent** on a given event **simply does not
  match** — that is a non-match, not an error.

This is the field-matching model `INV-DISP-1` states as a rule.

A named path **cannot be validated when the config is authored**, because no per-`type` payload shape
is declared anywhere (`OQ-EVT-CATALOG`); **while that open question stands**, a mistyped path narrows
to nothing and nothing reports it. That is the deferred catalog's consequence, not a property of path
matching: settling `OQ-EVT-CATALOG` gives config-time validation a shape to check the path against.

**Unknown type.** An event whose `type` **no configured binding declares at all** is **rejected** to
the source — it is **not** enqueued, it is named in the reply's `rejected` list (`INTF-CLI`), and the
core records it to logs and metrics. It is a genuine error rather than a silent drop, and the same
condition already fails **pre-runtime validation**, which blocks startup (`USECASE-VALIDATE-CONFIG`,
`INV-WORKFLOW-1`). A `type` that **is** declared by a binding but whose binding is merely **disabled
for this run** is the other case: that event **is** accepted and enqueued, is offered to nobody, and
is dropped **unconsumed-expired** — counted by the unconsumed-expired metric, because run-scoping is
not a config defect (`INV-DISP-3`).

**The query-reply contract carries events or a deferral, and nothing else — so it stays as it is.**
A reply says either "here are events" or "later, on the callback"; it carries no view into how the
source produced them and no per-run stream about the source itself. The source side therefore has
**no twin** of the handler-side internals the boundary principle puts outside this contract: there is
nothing here to trim, and a later reader MUST NOT trim it by analogy with the handler side. What the
core acts on — the events, and whether it must wait — is exactly what crosses.

```mermaid
sequenceDiagram
    participant Core as core
    participant Src as event source (INTF-SOURCE)
    Note over Core,Src: pull — inline reply
    Core->>Src: query { schemaVersion, id, callback }
    Src-->>Core: { id, events: [Event] }
    Note over Core,Src: pull — deferred reply
    Core->>Src: query { schemaVersion, id, callback }
    Src-->>Core: { id, deferred: true }
    Src->>Core: ingest-event { id, events: [Event] }
    Core-->>Src: { id, accepted }
    Note over Core,Src: push — source-initiated
    Src->>Core: ingest-event { id, events: [Event] }
    Core-->>Src: { id, accepted } — a type no binding declares is rejected, never queued (INV-DISP-3)
```

## `INTF-HANDLER` — event handler <!-- uuid: 10939663-7a48-4d44-8c4a-9a2df8ae4654 -->

- **Counterparty:** `ACTOR-HDL`, a pluggable event handler. **Initiator:** core. **Multiplicity:**
  one or more. (A handler's concrete kind — a **role** — is named in a downstream deployment set;
  the core knows only the interface.)
- **Purpose:** run a handler against one event as a **handler session**, tracked by the request's
  tracking id, and report its outcome.

**Dispatch (core → handler).** The core hands over **one event** under **one tracking id**, and the
handler answers in one of exactly two ways:

- **Reply (sync)** — an inline **completion** carrying an outcome: the handler took the event and
  finished it inside the call.
- **Reply (deferred)** — an **ack**: the handler took the event and will run it on its own. The core
  never holds an open call across long or paused work, and **the ack is the last thing the core is
  owed** for that dispatch.

**A handler's run status is not part of this contract.** The core's delivery responsibility ends at
acceptance (`INV-EVT-1`, `INV-FAIL-1`), so how far a handler has got, what it is doing, and how it
finished are the **handler's** to expose on the **handler's own** surface (for a ccpool-backed
handler, ccpool's own CLI — which pr-pool's implementation already reads directly rather than being
pushed to, so nothing that works today depended on a status callback). The core keeps only what it
needs to route: acceptance per `(event, handler)`, queue depth, and the unconsumed-expired count
(`INV-OBS-1`).

A handler reporting **its own** health — `healthy` / `degraded` / `unavailable`, over the common
**self-status** channel above — is a **different channel**, and pr-pool **does** act on it: an
`unavailable` self-report is a **pre-accept decline** that drives the core's re-offer (`INV-FAIL-1`,
`INV-CONC-1`). Self-status describes the **participant**; it never describes one dispatched event, so
the two never collide.

**Failure class** (coarse; response follows the **acceptance boundary**, `INV-FAIL-1`). This
vocabulary is the **handler-side** contract — a handler reports these and `INV-FAIL-1` routes them —
and it is deliberately **not** the list of things pr-pool measures. pr-pool **classifies and counts**
the **delivery-side** cases only: a **pre-accept decline** (`unavailable`, or a **`busy`** decline) and
a **dispatch failure** where the core could not hand the event over at all (`INV-OBS-1`).
The three **post-accept** classes — `retryable`, `resource-limit`, `critical` — pr-pool merely **hands
over**; they are the accepting **handler's own observability concern** and live on the handler's own
surface (for a ccpool-backed handler, ccpool's own metrics and logs), so their absence from pr-pool's
metric catalog is the boundary working, not an oversight.

| class            | meaning                                                       | response                                            |
| ---------------- | ------------------------------------------------------------- | --------------------------------------------------- |
| `retryable`      | transient (network blip, flake)                               | post-accept: handler retries; MAY surface / re-emit |
| `resource-limit` | a capacity/quota ceiling (e.g. a usage window) — not a defect | post-accept: handler pauses, resumes once it lifts  |
| `unavailable`    | cannot take work now (down, starting, or at capacity)         | pre-accept decline: core re-offers while unexpired  |
| `critical`       | MUST NOT be retried; a human is needed                        | post-accept: surfaced to a human; never re-offered  |

The core itself re-offers **only on a pre-accept decline** — an `unavailable` report, or a **`busy`**
decline when the handler is at capacity (`INV-CONC-1`). Once a handler **accepts** an event,
retry/resume/persistence is the **handler's** responsibility (`INV-FAIL-1`), not the core's.

**Obligations.** A handler **MUST tolerate a duplicate event** (be idempotent) and **MUST support
the deferred form**, so a paused or long-running handler session never pins an open call from the
core.

```mermaid
sequenceDiagram
    participant Core as core
    participant H as event handler (INTF-HANDLER)
    Note over Core,H: sync dispatch
    Core->>H: dispatch { id, event }
    H-->>Core: { id, outcome }
    Note over Core,H: deferred dispatch - the ack IS the acceptance
    Core->>H: dispatch { id, event }
    H-->>Core: { id, deferred: true }
    Note over Core,H: nothing further is owed - the run, its progress and its outcome are the handler's own
    Note over Core,H: a pre-accept decline is a busy decline or an unavailable self-status, and the core re-offers
```

## `INTF-MON` — monitoring sink <!-- uuid: 11c08936-42df-4fd6-a912-70fe88244012 -->

- **Counterparty:** `ACTOR-MON`, a pluggable monitoring sink. **Initiator:** either — the sink
  **declares** push or pull. **Multiplicity:** optional; may be absent or several.
- **Purpose:** expose the core's metrics. The **core owns the metric catalog** — a declared set of
  metrics, each with `name`, `kind` (`counter | gauge | histogram`), `unit`, and label shape
  (`INV-OBS-1`). An **observer** (`ACTOR-OBS`) reads the sink's surface, never the core directly.
- **The catalog.** Because an enumerated catalog belongs to the interface that carries it, the
  members are named here and `INV-OBS-1` states only the obligation to declare them. The catalog
  **MUST** declare at least:
  - **queue depth** — gauge, per `type`;
  - **failure rate** — counter, per **delivery-side** failure class, of which there are exactly two: a
    **pre-accept decline** (an `unavailable` self-report, or a **busy** decline from a handler at
    capacity, `INV-CONC-1`) and a **dispatch failure** where the core could not hand the event over at
    all. The post-accept classes are **not** counted here — after acceptance the handler owns the work
    (`INV-FAIL-1`), so classifying its outcomes is the handler's own concern on the handler's own
    surface;
  - **unconsumed-expired** — counter, per `type`: events that expired with no handler accepting them,
    which under `INV-EVT-4` is a **genuine miss** and is the concrete "no event misses" signal
    (`INV-DISP-3`, its **declared but inactive this run** case together with the ordinary miss);
  - **unknown-type-rejected** — counter, per `type`: events **rejected** at ingest because **no
    configured binding declares** their `type`, which is the metrics half of "recorded to logs and
    metrics" (`INV-DISP-3`, its **unknown to the configuration** case). It is a **separate** member
    from **unconsumed-expired** above and never a relabelling of it: this one counts an event the core
    **refused** and named as rejected to the caller, that one counts an event the core **accepted** and
    later dropped, so neither member ever stands for both of `INV-DISP-3`'s cases;

  and, alongside those four, the **throughput**, **backlog**, **liveness** and **dispatch-latency**
  metrics an observer watches to tell a busy system from a stalled one (`STORY-OBS-1`).

- **Emission.** Observability covers **metrics and logs** (traces are a later concern). Both the
  **emission transport** and the concrete backend behind it (a scrape target, a log store) are
  deployment bindings the core is unaware of; which transport is the default is a realization decision
  (`phillipgreenii-nix-agent-support · packages/pr-pool/docs/decisions · DEC-OBS-1`).
- A sink **declares which mode it uses and which subset of the metric catalog it handles**:
  - **pull** — the sink reads current values from the core on its own schedule;
  - **push** — the core sends the named metric updates to the sink as they change.
- The sink's own external surface (e.g. serving a scrape endpoint, writing a dashboard feed) is
  entirely the sink's concern and invisible to the core.

```mermaid
sequenceDiagram
    participant Core as core
    participant M as monitoring sink (INTF-MON)
    Note over Core,M: pull (sink-initiated, on its own schedule)
    M->>Core: read { schemaVersion, id, metrics: [names] }
    Core-->>M: { id, values: [ { name, value, labels } ] }
    Note over Core,M: push (core-initiated, on change)
    Core->>M: update { schemaVersion, id, name, value, labels }
    M-->>Core: { id, accepted }
```

## `INTF-STORE` — storage <!-- uuid: aa658dfb-7631-4f16-87aa-6c17d9abc097 -->

- **Counterparty:** `ACTOR-STO`, an optional pluggable storage. **Initiator:** core.
  **Multiplicity:** optional; a **default in-memory** store applies when none is configured.
- **Purpose:** a general **key/value scratch for core state** — **explicitly not event delivery**.
- **Operations:** `get(key) → value?`, `put(key, value)`, `delete(key)`; `key` is a string, `value`
  is a JSON string; each request/reply carries `schemaVersion`. The operation set is identical
  whether the backing is in-memory, local, or remote — the core codes to the interface, never the
  backing.
- **Guarantees.** Best-effort like any participant: the core MUST function if storage is absent or
  down, and MUST NOT rely on it to back any delivery guarantee. The default store's contents do not
  survive a restart.

```mermaid
sequenceDiagram
    participant Core as core
    participant S as storage (INTF-STORE)
    Core->>S: put { schemaVersion, id, key, value }
    S-->>Core: { id, ok: true }
    Core->>S: get { schemaVersion, id, key }
    S-->>Core: { id, value }   (or { id, value: null } when absent)
```

## `INTF-CLI` — operator commands (and the manager callbacks) <!-- uuid: 746dd5a6-34c4-4294-b727-e442c2afa723 -->

- **Counterparty:** `ACTOR-OP`, the operator — an **actor**, not an implementer, which is what makes
  this the set's one **driving port**: nobody on the far side implements a contract, so there is no
  conformance suite here and every obligation below is the **core's own**. **Initiator:** operator.
  The same binary **also** carries the **manager→core callback** `ingest-event`; it belongs to
  `INTF-SOURCE`'s manager-initiated direction and is invoked through the callback the core hands out,
  not by the operator.
- **What the operator can do.** The boundary offers exactly these affordances: run the core in its
  **long-running** mode or its **drain-and-exit** mode (`INV-LIFE-1`); **smoke-test one handler**
  against one explicitly named event, and **smoke-test one pull source's query** once and
  **read-only**, both under a **test-mode signal** so the participant knows a test is in flight and
  neither running any discovery of its own; **inject** an operator-supplied event into the live core;
  **inspect** a running core for its resolved configuration, its live **deliveries** and its per-`type`
  queue depths — and, as a **MAY** rather than a fourth obligation, for **which configured bindings
  have matched no event this run**, the one signal available against a mistyped narrowing payload path
  while `OQ-EVT-CATALOG` stands (`INV-DISP-1`, `USECASE-DEBUG-RUN`); and **resolve** the
  configuration, both as authored and as built-in defaults. The
  concrete command surface that spells these is a realization decision
  (`phillipgreenii-nix-agent-support · packages/pr-pool/docs/decisions · DEC-CLI-2`). _"role"_ is the
  operator-facing name for a configured **event handler** (its concrete kind); the core dispatches it
  as a **handler session**. _"query"_ names one pull **event source**'s query.
- **Operator push-inject.** The **push-inject** affordance is the **operator-facing front door to the
  push-ingest path**: it performs the **same core-side enqueue** as the `ingest-event` manager
  callback, but is **operator-initiated** (not invoked through a core-issued callback). The injected
  event is durable via the queue and delivered at-least-once and deduped like any push event
  (`INV-EVT-*`); no new delivery semantics. It is **distinct from** `ingest-event` (a manager→core
  callback) and from the one-shot handler smoke test, which tears down.
- **Run-scoped selectors.** The operator MAY restrict the **active** set of sources and handlers for a
  single run — as an allow-list, a deny-list, or both — **without editing the configuration**
  (`STORY-OP-3`). The restriction scopes which participants that run activates and which a smoke test
  may reach, and it **MUST NOT** outlive the run it was given for; the configuration itself is left
  untouched, which is what makes "isolate or pause part of the system" a reversible act.
- **Locating the core.** When the CLI cannot reach a **running** core it **MUST fail with a "no
  running core" error** and a **non-zero exit code**; it **MUST NOT start one**. Locating means being
  able to **reach** a core, not merely finding a trace that one once existed — a trace left behind by a
  core that has died is the same outcome as no trace at all. This holds on every locate path: the
  manager callbacks and the operator commands alike (`ADR 0036`).
- **Output.** Every operator command emits **human-readable text by default** and a
  **machine-readable form on request**, so an operator and a script read the same state without a
  second surface. A **usage** error is distinct from a **runtime** error: on the default CLI transport
  a usage error exits `2` and an unexpected one exits `1`, under the **one** convention every
  subcommand follows — the callback subcommands included, because `busy` no longer occupies `2`
  (the common contract above, `ADR 0042`).

### The manager→core callback

There is exactly **one** callback target: the core takes **event ingest** back over the callback
channel and nothing else. In particular a handler pushes back **no run status at all** — the core's
delivery responsibility ends at acceptance (`INV-EVT-1`, `INV-FAIL-1`), so there is nothing for it to
report. The callback the core hands out arrives ready to run, already addressed and authenticated; a
manager appends its own arguments (the common contract above).

**`ingest-event`** — a **push source**, or a **deferred pull** reply, delivers **one or more** events
under one tracking id. The reply reports how many the core **accepted** and lists the ones it
**rejected**, each with a reason.

`accepted` counts the events the core **took**, which is broader than "freshly appended": a
still-retained **duplicate** `id` is accepted too, because de-duplication is the core doing its job
(`INV-EVT-3`). The reply carries **no field** separating a fresh append from an absorbed re-emit, so
`accepted` is not a fresh-append count and a caller cannot distinguish the two over this interface.

An event whose `type` **no configured binding declares** is **rejected**, not accepted: it never
enters the queue and it is named in `rejected` with a reason, because a `type` unknown to the
configuration is an error rather than a silent drop (`INV-DISP-3`) — and the same condition already
blocks startup at **pre-runtime validation** (`USECASE-VALIDATE-CONFIG`). An event whose binding **is**
declared but merely **disabled for this run** by a **run-scoped selector** is the other case: it **is**
counted as accepted and enqueued, offered to nobody, then left to **expire unconsumed**, and
visibility there comes from the drop being **counted in the metric catalog** `INTF-MON` carries
(`INV-OBS-1`). The `rejected` list therefore carries **malformed** events — bad schema, or a missing
required field — and events whose `type` is **unknown to the configuration**, each with a reason.

**Inspecting a running core** yields three things it **MUST** offer, and nothing else it must offer.
**Deliveries** are **delivery
provenance** — which event the core handed to which handler, keyed by that dispatch's tracking id —
and the core legitimately knows it, because it already marks acceptance per `(event, handler)`
(`INV-EVT-1`). It is **not** a window into a handler session, so it carries no per-run progress:
that is the accepting handler's own, on the handler's own surface. The **queue depths** are the
per-`type` depth `INV-OBS-1` obliges, and the **resolved configuration** is pr-pool's own
source/handler count. A **fourth** reading is a **MAY** and is deliberately not one of the three: a
core **MAY** also report **which configured bindings have matched no event this run**, which is a
debugging convenience against a path no config-time check can validate today — and, like the three
above, it is core-side routing knowledge rather than a window into any handler (`INV-DISP-1`,
`OQ-EVT-CATALOG`, `USECASE-DEBUG-RUN`).

```mermaid
sequenceDiagram
    actor Op as operator
    participant Core as core
    participant Src as event source
    participant H as event handler
    Op->>Core: run in drain-and-exit mode
    activate Core
    Core->>Src: query { id, callback }
    Src-->>Core: { id, events: [Event] }
    Core->>H: offer { id, event }
    H-->>Core: { id, deferred: true }  (accept = ack, nothing further owed)
    Note over Core: queue drained and no offer outstanding → exit (INV-LIFE-1)
    Core-->>Op: success
    deactivate Core
```

## Configuration (an interface the operator authors)

The **configuration structure** is itself a first-class interface — the operator authors it, and
the CLI resolves it on request. It declares: the participants (each with its command, mode,
and — for a monitoring sink — its selected metrics); the sources, each as **one invocation** plus the
event types it emits and, for a pull source, its query trigger — never a per-tool source kind
(`GOAL-MIN-1`); the handlers and their event-type **bindings** (including any **payload path a
binding names** for narrowing); and the workflow wiring the core validates (`INV-WORKFLOW-1`). Expiry is **not** declared here — it
rides on each event as `at` / `expiresAt` (`INV-EVT-1`, `INV-EVT-4`). The **run-scoped selectors**
above restrict the _active_ subset of this config per run without editing it.

The **full configuration schema** is not yet pinned; it is tracked as an open question
(`OQ-CONFIG`), with pr-pool's existing TOML as prior art.

## Notes / forward references

- **Inter-consistency (method `INV-18`) binds here in its _implementer_ form.** Every counterparty
  above is either a pluggable implementation with no behavior-docs set of its own or a downstream
  deployment set that **implements** these interfaces. In both cases
  agreement is reconciled by the **conformance suite** (`INV-INTF-2`) each implementation runs, not
  by a verbatim peer cross-check: an implementer **cites** this contract and states only its own
  side, and a counterparty with no set leans on the same suite as its sole reconciliation. Each
  interface here **is** the authoritative contract its implementations adhere to.
- **Open questions** (tracked in [journeys](journeys.md)): `OQ-CONFIG` (the full configuration
  schema) and `OQ-EVT-CATALOG` (a declared per-`type` payload shape — what a binding's narrowing path
  would be validated against at config time, and what would make two sources' events on one `type`
  comparable at all). Pre-runtime validation of the wiring is **not** among them — it is settled and
  stated as a rule (`INV-WORKFLOW-1`, `USECASE-VALIDATE-CONFIG`).
