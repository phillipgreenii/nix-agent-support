# Actors — pg-wrap

Who interacts with pg-wrap. Everything pg-wrap integrates with is an actor, human or system; an
**interface** is _how_ an actor interacts. A behavior docs set MUST define all of its actors.
pg-wrap knows its actors only by the role they play at a boundary, never by name.

## Principals (human or agent)

- **`ACTOR-WRAP-CALLER` — Caller** <!-- uuid: a1f3c9d2-7b48-4e1a-9c6d-3f20b8e57a14 --> — a
  **principal, a human or a program**, that runs a payload inside the layers of a spec, validates a
  spec, or asks to see its composed layers. Works through `INTF-RUN`, `INTF-CHECK` and `INTF-SHOW`.
- **`ACTOR-WRAP-AUTHOR` — Spec author** <!-- uuid: 0d7e5b61-c2a9-4f38-8b14-e69a1d3c4f02 --> — a
  **principal, a human, an agent or a build step**, that writes or renders a wrap spec. Any author
  may write a spec by hand; a rendering helper is a convenience, not a requirement (`INV-SPEC-6`).
  Works through `INTF-SPEC`.

## System actors

- **`ACTOR-WRAP-SLOT` — Launcher slot** <!-- uuid: 5c88e2f7-19d4-4a6b-b3e0-7a4d92c61b58 --> — a
  part of some other program that lets its user replace only a program path and cannot take a whole
  command line. It reaches pg-wrap through the second binary (`INTF-EXEC`) and hands it the spec
  path through the environment.
- **`ACTOR-WRAP-LAYER` — Wrapper layer** <!-- uuid: e3b0a4c8-6f15-4d72-a9e1-02c8f5d7b396 --> — a
  program a spec names as one entry of its chain. It obeys the wrapper contract (`INV-WC-1` to
  `INV-WC-9`) and is reached through `INTF-LAYER`. pg-wrap does not know what any layer does beyond
  that contract.
- **`ACTOR-WRAP-PAYLOAD` — Payload** <!-- uuid: 92d6f1a3-8e07-4b5c-8d2a-c41e7f90a6b3 --> — the
  program the innermost layer finally runs. pg-wrap hands it its argv, stdin, controlling terminal
  and signals unmodified and returns its exit status unchanged (`INTF-PAYLOAD`).
