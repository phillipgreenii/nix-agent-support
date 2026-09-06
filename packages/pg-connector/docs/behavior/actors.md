# Actors — pg-connector

Who interacts with the umbrella. Everything the umbrella integrates with is an actor — human or
system; an **interface** is _how_ an actor interacts. A behavior docs set MUST define all of its
actors (method `INV-13`).

## Principals (human or agent)

- **`ACTOR-OP` — Operator** <!-- uuid: d92e6141-e897-4806-9b45-88511cd8e25b --> — a **principal —
  a human or an agent/automation** — that authors the `connector.<type>` registry, invokes the
  `pg-connector` CLI to read and write `pr`/`issue`/`ci`/`scm` state, checks auth/config health
  (`auth status`, `config validate`), and chooses the CLI's presentation mode
  (`--output json|human`). Works through `INTF-CLI`, which is drivable by either a human at a
  terminal or a script/subprocess (a pr-pool role, a review-orchestrator tool, …) — pg-connector
  draws no distinction between the two. This actor also covers the **backend-implementer** role:
  building a Tier-2 backend against a capability's Provider interface and the wire protocol is
  `ACTOR-OP` acting in that capacity, not a second actor — the same convention this set's sibling
  `pr-pool` behavior-docs set applies to its own operator/implementer split.

## System actors (participants behind interfaces)

- **`ACTOR-BACKEND` — Tier-2 backend** <!-- uuid: 35459b50-8ead-48b1-85c3-5b70fad85129 --> — a
  registered binary that answers the wire protocol for exactly one (capability, external system)
  pair; opaque to the umbrella beyond that contract. Which concrete system a backend talks to (or
  whether it talks to a remote system at all — `scm`'s backend manages only local git state) is a
  downstream, backend-owned concern, out of this set's scope (`## Scope`). Interface: `INTF-WIRE`.
  Essential: the umbrella is nonsense with zero backends registered for a capability an operator
  is trying to use, though the umbrella itself runs and answers `--help`/`version` with none
  registered at all.
