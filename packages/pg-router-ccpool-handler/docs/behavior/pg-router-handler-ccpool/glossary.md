# Glossary — pg-router-handler-ccpool

Terms pg-router's set already defines (**role**, **adapter**, **role object**, **base
environment**, **handler session**, **pre-accept decline**) are **inherited** with their meaning there
(`INV-20`). The words **session**, **pool** and **lease** are the earlier combined handler's and the
session tool's, inherited the same way. This set adds only the terms below.

- **`sessionEnv`** — a role's generic string-to-string map, which the adapter passes to the session
  tool uninterpreted whenever it starts a session; it carries no secret (`INV-CCA-4`).
- **Item context** — the three variables `PG_ROUTER_ROLE`, `PG_ROUTER_ITEM_ID` and
  `PG_ROUTER_ITEM_TYPE` the adapter passes with every start of a session; the item's title is never
  one of them (`INV-CCA-5`).
- **Pre-ready decline** — the pre-accept busy decline, reason `launch-failed`, kind `fault`, the
  adapter gives when its session never became ready; it writes no bead (`INV-CCA-6`).
- **Decline kind** — the `busy`, `fault` or `neutral` class the adapter maps each decline reason to,
  by which the core counts a role's progress (`INV-CCA-6`).
- **`validate`** — the adapter's one validation entry point, which runs nothing (`INV-CCA-8`).
- **Cutover** — the moment a role stops being run by the earlier combined handler and starts being
  run by this adapter (`INV-CCA-9`).
