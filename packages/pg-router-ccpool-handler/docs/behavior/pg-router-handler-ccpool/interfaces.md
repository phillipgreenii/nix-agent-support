# Interfaces — pg-router-handler-ccpool

This adapter realizes one interface as pg-router's counterparty and has two boundaries of its own.

## Realized (this adapter is the implementer, pg-router's core is the counterparty)

- Realizes **`INTF-HANDLER`**: dispatch to a handler session, its accept-or-decline reply (inline or
  deferred), and the `postStartup` and `preShutdown` hooks. **Counterparty:** `ACTOR-CCA-CORE`. Fully
  defined by `INTF-HANDLER` in pg-router's set; not restated here. It rides pg-router's default CLI
  transport (`DEC-WIRE-1`), and every request carries the role object (`DEC-WIRE-3`). Its
  pre-accept declines carry the optional `kind` and `detail` of `DEC-WIRE-1` (`INV-CCA-6`), and it
  answers the optional `validate` subcommand of the same decision (`INV-CCA-8`).

This adapter is a **full** participant, not only a dispatch endpoint: it also answers the capacity,
shutdown, startup-reconcile and orphan-sweep obligations the earlier combined handler's set states for
a session-backed role, because a session outlives the daemon that started it (`INV-CCA-1`).

## Boundaries this adapter initiates

- **`INTF-CCA-SESSION`** <!-- uuid: 26aa0962-705b-4e9c-819b-3e8402558789 --> — the crossing into the session tool, to start, observe,
  resume, reply to and close a session. **Counterparty:** the session tool (boundary).
  **Initiator:** this adapter. **Multiplicity:** as the earlier combined handler's set states it for a
  session-backed role (`INV-CCA-1`). What is new: **every start** passes the whole environment
  explicitly, the role's `sessionEnv` and the item context included, because the session tool does
  not persist it (`INV-CCA-4`, `INV-CCA-5`), and every send first checks that the session is live
  (`INV-CCA-7`).
- **`INTF-CCA-CONFIG`** <!-- uuid: a7df2e40-891a-4d97-af3a-db83b128dfa6 --> — the role's own configuration table, which arrives in the role
  object, plus a build-time check of it. **Counterparty:** `ACTOR-CCA-OP`, through the deployment's
  build. **Initiator:** the operator. The table carries the fields the earlier per-role file carries
  today for a session-backed role (the actor, the prompt or its file, the completion policy, the
  failure actions, the budget, the isolation, the tool grants, the effort level, the pre-launch check
  and the other per-role settings the earlier set's rules name); this set neither lists nor respells
  them. The adapter rejects a field it does not know on every call, and ships a `validate`
  subcommand that applies the same decoding to a rendered table without running anything
  (`INV-CCA-2`, `INV-CCA-8`).
