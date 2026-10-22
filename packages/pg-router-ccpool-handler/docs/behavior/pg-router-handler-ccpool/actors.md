# Actors — pg-router-handler-ccpool

Who this adapter interacts with. A behavior docs set MUST define all of its actors.

## Principals (human or agent)

- **`ACTOR-CCA-OP` — Operator** <!-- uuid: 87f9cccc-8cba-467b-a919-5402d28f6c83 --> — a **principal, a human or an agent**, that declares a
  role in pg-router's configuration, names this adapter as its `handler`, and writes the role's own
  configuration, its `sessionEnv` included. The operator never talks to the adapter directly:
  everything the operator gives it arrives in the role object (`INV-ROLE-2`).

## System actors (participants behind interfaces)

- **`ACTOR-CCA-CORE` — pg-router core** <!-- uuid: c81a763e-ed0b-44ac-be0a-c97480ea42cb --> — the generic dispatcher that invokes this adapter
  once per call, over `INTF-HANDLER` (`dispatch`, `postStartup`, `preShutdown`). Defined and owned by
  pg-router's own set; named here only as this adapter's counterparty.

## Boundary counterparties (named, internals undescribed)

- **The session tool (`ccpool`)** — the command-line tool this adapter drives to start, observe,
  resume and close an agent session; its own behavior docs (`packages/ccpool/docs/behavior`) own
  that internal behavior. This adapter's one requirement of it is that the executable a pool names
  is opaque to it (`packages/ccpool/docs/behavior`'s `INV-POOL-6`).
- **The agent inside the session** — what the pool's executable ends up running, possibly through
  an external prefix tool the operator chose (`pg-wrap`); this adapter never interprets it and
  verifies nothing about it.
- **The tracker (`bd`)** — the issue store a role's completion policy reads and writes; the earlier
  combined handler's set owns that crossing.
