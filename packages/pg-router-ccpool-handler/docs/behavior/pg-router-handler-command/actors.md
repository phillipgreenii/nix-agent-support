# Actors — pg-router-handler-command

Who this adapter interacts with. A behavior docs set MUST define all of its actors.

## Principals (human or agent)

- **`ACTOR-CMA-OP` — Operator** <!-- uuid: 5e548f57-023f-40e7-bc1b-803146f9ae86 --> — a **principal, a human or an agent**, that declares
  a role in pg-router's configuration, names this adapter as its `handler`, and writes the role's
  command. The operator never talks to the adapter directly: everything the operator
  gives it arrives in the role object (`INV-ROLE-2`).

## System actors (participants behind interfaces)

- **`ACTOR-CMA-CORE` — pg-router core** <!-- uuid: 6e7ef2cb-ae16-4a50-9cd2-f6821b197456 --> — the generic dispatcher that invokes this adapter
  once per call, over `INTF-HANDLER` (`dispatch`, `postStartup`, `preShutdown`). Defined and owned by
  pg-router's own set; named here only as this adapter's counterparty.

## Boundary counterparties (named, internals undescribed)

- **The role's command** — the executable the role's argv names. The adapter runs it and observes
  how it ends; it never interprets what it does, which is pg-router's boundary principle applied one
  layer down (a pass-through is fine; enforcing another tool's behavior is not).
