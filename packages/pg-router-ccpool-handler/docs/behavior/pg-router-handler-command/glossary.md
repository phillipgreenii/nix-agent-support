# Glossary — pg-router-handler-command

Terms pg-router's set already defines (**role**, **adapter**, **role object**, **base
environment**, **handler session**, **pre-accept decline**) are **inherited** with their meaning there
(`INV-20`) and are not redefined. This set adds only the terms below.

- **Payload** — the command a dispatch ends up running: the role's argv, rendered for one event.
- **Rendered argv** — the role's argv after every element has been put through the template engine
  with the event's item fields. An element with no template action renders to itself.
- **Item context** — the three variables the adapter sets for every payload (`PG_ROUTER_ROLE`,
  `PG_ROUTER_ITEM_ID`, `PG_ROUTER_ITEM_TYPE`); the item's title is never one of them
  (`INV-CMA-8`).
- **Keep-list** — the base environment of the payload, each variable by explicit name and never by
  prefix (`INV-CMA-8`).
- **Prefix tool** — an external tool the role's argv starts with (`pg-wrap`), which applies its own
  chain of command prefixes around the payload; the adapter neither knows nor checks it
  (`INV-CMA-9`).
- **Busy exit** — the command exiting with the coarse code that means "busy" (`9`,
  `phillipgreenii-nix-agent-support · packages/pg-router/docs/decisions · DEC-WIRE-1`), which the
  adapter turns into a pre-accept busy decline of kind `busy`, whichever layer of the argv exited
  with it (`INV-CMA-4`).
- **Stderr tail** — the bounded, redacted, one-line tail of a failed payload's standard error that
  the adapter puts in a failure's error text.
- **`dispatch_start`** — the event-log record of what an accepted dispatch ran: the rendered argv,
  whether the strict switch is on and the names of the base-environment variables (`INV-CMA-10`).
- **`validate`** — the adapter's one validation entry point, which runs no payload (`INV-CMA-2`).
