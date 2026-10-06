# Interfaces — pg-router-ccpool-handler

This module has two kinds of interaction: the two interfaces it **realizes** as pg-router's
counterparty, and the boundary crossings it makes into its own backing tools. Task 5.2/5.3 (folded
together, commit `c6b016fb`) built this module's implementation against the contract below; see
[README](README.md)'s Realization gaps for what has not yet caught up.

## Realized (this module is the implementer, pg-router's core is the counterparty)

- Realizes **`INTF-SOURCE`** — typed events into the core, over pg-router's one opaque source
  contract. **Counterparty:** `ACTOR-CCH-CORE`. Fully defined by
  `packages/pg-router/docs/behavior/interfaces.md`'s `INTF-SOURCE` section
  (uuid `fe42416a-5f10-4db1-b8c3-46b1609213c7`); not restated here — this module implements it,
  it does not own it.
- Realizes **`INTF-HANDLER`** — dispatch to a handler session and its accept-or-decline/deferred-ack
  reply. **Counterparty:** `ACTOR-CCH-CORE`. Fully defined by
  `packages/pg-router/docs/behavior/interfaces.md`'s `INTF-HANDLER` section
  (uuid `10939663-7a48-4d44-8c4a-9a2df8ae4654`); not restated here.

This module's own obligations under both — tolerating a duplicate event, supporting the deferred
ack form, reporting `unavailable`/`busy` pre-accept declines, never leaking a post-accept failure
class back to the core as anything but an opaque outcome string — are exactly the obligations
those two sections already state on the implementer. Restating them here would risk the two
copies drifting; this module is bound by the upstream text as written.

## Boundary crossings (this module is the initiator; named, not fully specified)

Per this set's floor, each backing tool's own internal behavior is out of extent — only what
crosses the line is named here, mirroring `packages/pa-monitor/docs/behavior/interfaces.md`'s
treatment of its own named boundaries (its `INTF-BRIDGE` — example only, not a citation).

- **`INTF-CCH-CCPOOL`** <!-- uuid: d1fc9c42-5d04-4df3-bfb1-a11cc97d668d --> — a ccpool-backed
  handler session's crossing into the `ccpool` CLI to start, observe, and reap an agent session,
  PLUS (pg2-oju6w.15) this module's own `preShutdown` hook sweeping the prefix-matching sessions
  of the daemon's default pool — not scoped to one dispatch — the once-per-process-lifetime
  relocation of a sweep pg-router's own core used to run directly against `ccpool` before this
  module existed. **Counterparty:** `ccpool` (boundary; `packages/ccpool/docs/behavior` owns its
  internals). **Initiator:** this module. **Multiplicity:** one per ccpool-backed handler session
  for the per-dispatch crossing, plus one lease write per poll interval for as long as that
  dispatch runs and one role-scoped orphan reconcile per dispatch; one sweep per daemon shutdown
  (the core delivers `preShutdown` to the first enabled role's handler only, so the sweep is not
  repeated per role).
  The `preShutdown` sweep MUST spare every actively working session (`INV-CCH-14`): it purges only
  sessions closable for another reason (turn ended, or `needs_input` with its bead already closed)
  and leaves a working session and its worktree untouched. It lists only the default pool; a
  session in a role's own pool is outside it.
  When a dispatch reaches a terminal outcome, this module also closes (without purging) the one
  session it launched or absorbed, if it is settled and quiet (`INV-CCH-17`).
  While a dispatch runs, this module writes a supervision lease into the session's metadata
  (`INV-CCH-18`), never as a `ccpool` label. Before checking capacity, a dispatch also reconciles
  its own role's orphaned sessions in that role's pool: a session whose lease has expired is
  reclaimed or, if still working and over its time budget, hard-stopped (`INV-CCH-18`).
  A dispatch of a role with worktree isolation also holds a shared per-bead worktree lock for its
  whole run and, before checking capacity, sweeps the worktree directory for leaked per-bead
  worktrees no session row leads to, removing a worktree and its anchor branch only under the
  guards of `INV-CCH-19`.
  A session in a per-bead linked worktree is torn down in two phases (`INV-CCH-20`): this module
  closes it without purging, removes the worktree and its anchor branch, and only then purges the
  record, so a removal interrupted midway leaves a record the next reconcile of the same pool
  retries (one per dispatch; for the default pool, the shutdown sweep too) rather than a worktree
  no record leads to. While such a teardown is unfinished, the record is never treated as a
  duplicate to absorb. This marking is a `ccpool` metadata write, never a `ccpool` label.
- **`INTF-CCH-BEADS`** <!-- uuid: 01dd79ce-ffbc-4234-9d6e-e7125561694f --> — this module's
  beads-backed source querying `bd` for events, and a handler session's completion policy writing
  a result back to `bd`. **Counterparty:** `bd` (boundary). **Initiator:** this module.
  **Multiplicity:** one query surface plus one write path per handler session that closes a bead.
- **`INTF-CCH-PGPR`** <!-- uuid: 8676b04d-3f8a-49cb-973d-1607871c1dba --> — a handler session's ACL
  check consulting `pg-pr`. **Counterparty:** `pg-pr` (boundary). **Initiator:** this module.
  **Multiplicity:** zero or more per handler session, depending on configuration.

None of these three crossings is declared to pg-router's core in any form — per the boundary
principle pg-router's own README states, a `command`-backed handler session's argv is a
pass-through the core never reads, and these three are exactly the concrete tools that pass-through
may resolve to once configured. `INTF-CCH-CCPOOL`/`INTF-CCH-BEADS`/`INTF-CCH-PGPR` are this
module's own decision, not the core's, and adding a fourth backing tool never touches
`packages/pg-router`.
