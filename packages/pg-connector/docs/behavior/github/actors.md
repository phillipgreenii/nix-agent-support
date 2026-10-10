# Actors — pg-connector-github

Who and what interacts with the daemon-backed GitHub backend. Everything the backend integrates
with is an actor, human or system; an **interface** is _how_ an actor interacts. A behavior docs
set MUST define all of its actors (method `INV-13`). The parent set's `ACTOR-BACKEND` is the role
this backend plays toward the umbrella; the actors below are the parties on the other sides of this
backend's own boundaries.

## Principals (human or agent)

- **`ACTOR-GH-CALLER` — Caller** <!-- uuid: e2994ec9-99cc-4ae3-8d6c-c5ae5db1104f --> — a **principal, a human or an agent** that
  reads PRs and their CI, and writes pending reviews or reruns failed runs, through the umbrella.
  An operator at a terminal, a skill, a review tool and a precheck are all callers, and the backend
  draws no distinction between them. A caller MAY also change a PR by a route that does not go
  through the backend (an **out-of-band write**, such as pushing a commit) and then read the result
  back. Works through `INTF-GH-WIRE`.
- **`ACTOR-GH-CONSUMER` — Change consumer** <!-- uuid: 11a43eb6-4c01-48b2-9808-809de8a91e08 --> — an automation that polls what
  changed for a query on its own clock, acts on the changes, and acknowledges what it has received.
  A scheduler role is one; so is a script. It identifies itself by a stable consumer name and works
  through `INTF-GH-WIRE`'s `changes` and `changes_ack`. It keeps no copy of the PR data it is told
  about: after a change it reads the entity back, and that read is a local hit.
- **`ACTOR-GH-OPERATOR` — Operator** <!-- uuid: c1ffbc26-50a1-4864-a7af-88497e75378a --> — a **principal, a human or an agent**
  that asks whether the daemon is healthy, how fresh its data is and how much it is spending, asks
  why one entity is stale, and writes the configuration the daemon and its client read. Works
  through `INTF-GH-STATUS` and `INTF-GH-CONFIG`. The same person or agent MAY also be a caller or a
  consumer; the role is what this set tells apart.

## System actors (participants behind interfaces)

- **`ACTOR-GH-UMBRELLA` — Umbrella** <!-- uuid: a87c3e39-2607-44fb-b0f1-5fc65133737a --> — `pg-connector`, the Facade that
  executes the backend once per call, forwards `changes` and `changes_ack`, and acknowledges only
  after it has flushed its output. Its half of the seam is the parent set's, and this backend
  implements that contract (`INTF-WIRE`). Interface: `INTF-GH-WIRE`. Essential: the backend has no
  caller, and no consumer, without it.
- **`ACTOR-GH-ORIGIN` — Origin** <!-- uuid: ef00c285-5d33-452a-8907-2b7c56166a61 --> — GitHub, the upstream system that holds
  the truth about PRs and CI, meters every read and write against a rate limit, recomputes some
  values (mergeability) lazily, and may answer slowly, wrongly or not at all. Interface:
  `INTF-GH-ORIGIN`. Essential.
- **`ACTOR-GH-SUPERVISOR` — Supervisor** <!-- uuid: f94c4905-2c4b-487f-be20-0bb53b4da7e2 --> — the per-platform process manager
  that starts the daemon at login and starts it again when it exits. It has no logic of its own
  and no knowledge of the backend. Interface: `INTF-GH-SUPERVISION`. Optional: the daemon runs when
  an operator starts it by hand, and a deployment on a platform with no supervisor is valid.
