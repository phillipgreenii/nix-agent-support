# Actors — pr-pool

Who interacts with the core. Everything the core integrates with is an actor — human or system;
an **interface** is _how_ an actor interacts. A behavior docs set MUST define all of its actors.

## Principals (human or agent)

- **`ACTOR-OP` — Operator** <!-- uuid: 41f74815-a594-4443-9dd3-bb83252cf5e9 --> — a **principal — a
  human or an agent** — that configures the core (participants, event sources, handlers and their
  bindings, selectors, wiring), runs it as a daemon or run-until-idle, and inspects it
  (status, resolved config, live deliveries). Works through the CLI (`INTF-CLI`), which is
  drivable by either a human or an agent.

## Human actors

- **`ACTOR-OBS` — Observer** <!-- uuid: 8dfe4a2e-6573-4cfd-ae01-f1d5f7e1b432 --> — consumes the core's monitoring output (the metric catalog surfaced
  through a monitoring sink) to judge throughput, backlog, failures, and liveness.

## System actors (participants behind interfaces)

Each registers with the core, receives lifecycle signals, and interacts only through its interface;
which concrete implementation fills the role is a downstream deployment set's concern.

- **`ACTOR-SRC` — Event source** <!-- uuid: 39e28ce5-ef60-47bb-8aa7-2d93d267447f --> — emits typed events (pull or push). Interface: `INTF-SOURCE`.
- **`ACTOR-HDL` — Event handler** <!-- uuid: a5997046-d20a-445d-bf10-328855b03810 --> — responds to bound events as handler sessions, replying with its
  acceptance or a pre-accept decline and keeping the run's own progress on its own surface;
  capacity-limited. Interface: `INTF-HANDLER`.
- **`ACTOR-MON` — Monitoring sink** <!-- uuid: bf5b0941-b26b-433d-b991-b84fe3b601c2 --> — pulls or pushes a declared subset of the metric catalog.
  Interface: `INTF-MON`.
- **`ACTOR-STO` — Storage** <!-- uuid: 26806cd5-694a-4862-9f86-410d8d1ff498 --> — optional; provides a key/value scratch for core state, never delivery.
  Interface: `INTF-STORE`.
