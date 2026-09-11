# Interfaces — prpool-ccpool-handler

This module has two kinds of interaction: the two interfaces it **realizes** as pr-pool's
counterparty, and the boundary crossings it makes into its own backing tools. Nothing below is
built yet (see [README](README.md)'s Realization gaps); this file states the contract this
module's implementation is checked against once Task 5.2 onward populates it.

## Realized (this module is the implementer, pr-pool's core is the counterparty)

- **`INTF-SOURCE`** — typed events into the core, over pr-pool's one opaque source contract.
  **Counterparty:** `ACTOR-CCH-CORE`. Fully defined by
  `packages/pr-pool/docs/behavior/interfaces.md`'s `INTF-SOURCE` section
  (uuid `fe42416a-5f10-4db1-b8c3-46b1609213c7`); not restated here — this module implements it,
  it does not own it.
- **`INTF-HANDLER`** — dispatch to a handler session and its accept-or-decline/deferred-ack reply.
  **Counterparty:** `ACTOR-CCH-CORE`. Fully defined by
  `packages/pr-pool/docs/behavior/interfaces.md`'s `INTF-HANDLER` section
  (uuid `10939663-7a48-4d44-8c4a-9a2df8ae4654`); not restated here.

This module's own obligations under both — tolerating a duplicate event, supporting the deferred
ack form, reporting `unavailable`/`busy` pre-accept declines, never leaking a post-accept failure
class back to the core as anything but an opaque outcome string — are exactly the obligations
those two sections already state on the implementer. Restating them here would risk the two
copies drifting; this module is bound by the upstream text as written.

## Boundary crossings (this module is the initiator; named, not fully specified)

Per this set's floor, each backing tool's own internal behavior is out of extent — only what
crosses the line is named here, mirroring `packages/pa-monitor/docs/behavior/interfaces.md`'s
treatment of its own named boundaries (`INTF-BRIDGE`).

- **`INTF-CCH-CCPOOL`** <!-- uuid: d1fc9c42-5d04-4df3-bfb1-a11cc97d668d --> — a ccpool-backed
  handler session's crossing into the `ccpool` CLI to start, observe, and reap an agent session.
  **Counterparty:** `ccpool` (boundary; `packages/ccpool/docs/behavior` owns its internals).
  **Initiator:** this module. **Multiplicity:** one per ccpool-backed handler session.
- **`INTF-CCH-BEADS`** <!-- uuid: 01dd79ce-ffbc-4234-9d6e-e7125561694f --> — this module's
  beads-backed source querying `bd` for events, and a handler session's completion policy writing
  a result back to `bd`. **Counterparty:** `bd` (boundary). **Initiator:** this module.
  **Multiplicity:** one query surface plus one write path per handler session that closes a bead.
- **`INTF-CCH-PGPR`** <!-- uuid: 8676b04d-3f8a-49cb-973d-1607871c1dba --> — a handler session's ACL
  check consulting `pg-pr`. **Counterparty:** `pg-pr` (boundary). **Initiator:** this module.
  **Multiplicity:** zero or more per handler session, depending on configuration.

None of these three crossings is declared to pr-pool's core in any form — per the boundary
principle pr-pool's own README states, a `command`-backed handler session's argv is a
pass-through the core never reads, and these three are exactly the concrete tools that pass-through
may resolve to once configured. `INTF-CCH-CCPOOL`/`INTF-CCH-BEADS`/`INTF-CCH-PGPR` are this
module's own decision, not the core's, and adding a fourth backing tool never touches
`packages/pr-pool`.
