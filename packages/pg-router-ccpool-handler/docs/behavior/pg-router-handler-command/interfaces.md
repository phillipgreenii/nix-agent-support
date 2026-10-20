# Interfaces — pg-router-handler-command

This adapter realizes one interface as pg-router's counterparty and has two boundaries of its own.

## Realized (this adapter is the implementer, pg-router's core is the counterparty)

- Realizes **`INTF-HANDLER`**: dispatch to a handler session, with its accept-or-decline reply, plus
  the `postStartup` and `preShutdown` hooks. **Counterparty:** `ACTOR-CMA-CORE`. Fully defined by
  `INTF-HANDLER` in pg-router's set; not restated here. It rides pg-router's default CLI transport
  (`DEC-WIRE-1`), and every request carries the role object (`DEC-WIRE-3`).

This adapter always replies **inline**, with a completion or a decline: a command role has nothing to
do after the command ends, so it never needs the deferred form. Its lifecycle hooks have nothing to
do and answer with an acknowledgement.

## Boundaries this adapter initiates

- **`INTF-CMA-PAYLOAD`** <!-- uuid: b4422e95-a48c-4038-9928-e14f8092125a --> — the crossing into the role's command. **Counterparty:** the role's
  command (boundary). **Initiator:** this adapter. **Multiplicity:** one run per dispatch, never a
  retry of its own. What crosses: the rendered argv, a base environment expanded only by the role's
  wrap (`INV-ROLE-4`), a process group of its own, and back an exit status and standard error.
  The coarse exit status carries the outcome (`INV-CMA-3`, `INV-CMA-4`).
- **`INTF-CMA-CONFIG`** <!-- uuid: 47a2709e-8c4f-4f99-bbf3-d9fa32e72c57 --> — the role's own configuration table, which arrives in the
  role object, plus a build-time check of it. **Counterparty:** `ACTOR-CMA-OP`, through the
  deployment's build. **Initiator:** the operator. The table's one field is `argv`, the list of
  command-line elements the role runs, each of which MAY carry template actions over the event's item
  fields. The adapter rejects a field it does not know on every call, and ships a
  `validate-config` subcommand that applies the same decoding to a rendered table without running
  anything, so a dropped or misspelled field fails the build (`INV-CMA-2`).
