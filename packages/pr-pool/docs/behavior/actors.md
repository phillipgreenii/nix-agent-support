# Actors — pr-pool

Who interacts with the core. Everything the core integrates with is an actor — human or system;
an **interface** is _how_ an actor interacts. A behavior docs set MUST define all of its actors.

## Principals (human or agent)

- **`ACTOR-OP` — Operator** <!-- uuid: 41f74815-a594-4443-9dd3-bb83252cf5e9 --> — a **principal — a
  human or an agent** — that configures the core (participants, event sources, handlers and their
  bindings, TTLs, selectors, wiring), runs it as a daemon or run-until-idle, and inspects it
  (status, resolved config, live handler sessions). Works through the CLI (`INTF-CLI`), which is
  drivable by either a human or an agent.

## Human actors

- **`ACTOR-OBS` — Observer** — consumes the core's monitoring output (the metric catalog surfaced
  through a monitoring sink) to judge throughput, backlog, failures, and liveness.

## System actors (participants behind interfaces)

Each registers with the core, receives lifecycle signals, and interacts only through its interface;
which concrete implementation fills the role is a downstream deployment set's concern.

- **`ACTOR-SRC` — Event source** — emits typed events (pull or push). Interface: `INTF-SOURCE`.
- **`ACTOR-HDL` — Event handler** — responds to bound events as handler sessions and reports status;
  capacity-limited. Interface: `INTF-HANDLER`.
- **`ACTOR-MON` — Monitoring sink** — pulls or pushes a declared subset of the metric catalog.
  Interface: `INTF-MON`.
- **`ACTOR-STO` — Storage** — optional; provides a key/value scratch for core state, never delivery.
  Interface: `INTF-STORE`.
