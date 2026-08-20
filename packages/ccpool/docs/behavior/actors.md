# Actors — ccpool

Who interacts with the pool. Everything ccpool integrates with is an actor — human or system; an
**interface** is _how_ an actor interacts. A behavior docs set MUST define all of its actors.

## Principals (human or agent)

- **`ACTOR-CCP-OP` — Operator** <!-- uuid: 3eb528af-f35d-48a8-af57-60d6823a7d3d --> — a **principal
  — a human or an agent** — that attaches to a session to look at or steer it directly, and
  attends sessions that need a person (`INTF-CALLER`).
- **`ACTOR-CCP-CALLER` — Dispatching caller** <!-- uuid: 49ed8dc9-e6ec-4ebc-9b85-34cec55ebf89 -->
  — a program that drives sessions on the operator's behalf: dispatches prompts, reads status,
  tags metadata, cancels and closes. Works through `INTF-CALLER`, drivable by either a human or an
  agent.

## System actors

- **`ACTOR-CCP-AGENT` — The agent inside the session** <!-- uuid:
  575012ba-439b-4a28-bc56-6d92e27be2b6 --> — the agent process ccpool launches and routes prompts
  to. It **acts back** through the agent binary's own signals (`INTF-AGENTSIGNAL`) and, in
  autonomous mode, **receives the denial decision** when it tries to ask a question
  (`INTF-DENY`) — it is not a passive payload, it is a party this pool talks to in both
  directions.
- **`ACTOR-CCP-MONITOR` — Co-resident monitor** <!-- uuid: c406e1b3-b655-4897-9b2b-eebda7661a06
  --> — another tool sharing this host that also drives input into agent sessions (e.g. a
  passive usage/status watcher). ccpool marks every session it launches excluded from such a
  monitor's own actuation, so the two tools never double-drive one session
  (`packages/pa-monitor/docs/behavior · INTF-NONUDGE` — the monitor owns that contract; ccpool is
  the implementer that sets its marker).
