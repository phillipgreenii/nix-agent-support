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

Both ride pg-router's default CLI transport (`DEC-WIRE-1`). Where this module reaches back into the
core, it does so over the core's socket through the callback commands the core hands it with their
address and token already baked in (`DEC-WIRE-2`), which are part of `INTF-CLI`.

This module's own obligations under both — tolerating a duplicate event, supporting the deferred
ack form, reporting `unavailable`/`busy` pre-accept declines, never leaking a post-accept failure
class back to the core as anything but an opaque outcome string — are exactly the obligations
those two sections already state on the implementer. Restating them here would risk the two
copies drifting; this module is bound by the upstream text as written. That includes the stable
substrings a handler error carries for the core to classify (the "Budget-stop sentinel",
"Upstream-killed sentinel" and "Usage-limit sentinel" of that `INTF-HANDLER` section): this module
emits the usage-limit one, `session ended on a usage limit or API outage`, in the error of an
unclaimed end that died on a usage or spend limit or an API outage (`INV-CCH-28`). It also emits
the "Wrap-failed sentinel" of the same section, `wrap failed before the payload started`, followed
by the failing stage's redacted stderr tail, in the error of any end whose wrap chain failed before
its payload started (`INV-CCH-9`, `INV-CCH-33`); the wording is the core's and is never reworded
here.

One reply field is this module's to fill in, and is stated here only to name where it comes from.
A dispatch that adopts a session an earlier offer launched (`INV-CCH-30`'s one exception) reports
that session's recorded launch time as the optional `startedAt` of its dispatch reply, as
`INTF-HANDLER` allows an adopting handler to (`DEC-EVENT-5`). A dispatch that launches its own
session, and an adopted session that recorded no launch time, reply with no `startedAt`.

## This module's own operator-facing surface

Beside the dispatch and source entry points above, this module exposes the following to an operator
and to the deployment. Each is fully defined by the invariant cited; none is declared to the core.

- **`wrap-gate clear <role>`** and **`wrap-gate list`** — clear the pause of one role whose wrap
  failed, and list the paused roles with the time each pause started and its stderr tail
  (`INV-CCH-34`). Clearing a role that is not paused succeeds and does nothing. The core's own
  gate-clear command cannot clear it.
- **The per-role JSON's `wrap` and `strictBaseEnv`** — the two settings a role's own JSON file
  carries, before the core's role entry carries them: `wrap` is an ordered list of entries, each
  a string or an array of strings (`INV-ENV-1`), and `strictBaseEnv` is a boolean, off by default
  (`INV-CCH-32`). The file is decoded strictly, and a templated entry in a ccpool-backed role's
  `wrap` is rejected.
- **The decline reason `wrap-gated`** — the fixed, bounded reason of the pre-accept busy decline a
  paused role gives (`INV-CCH-34`), like `origin-unavailable` and `session-in-flight`.
- **The `dispatch_start` event-log record** — role, item id and type, wrap entries, the names of the
  base-environment variables and the launcher path, never a value (`INV-CCH-35`).
- **The environment of a payload** — the four item-context variables `PG_WRAP_ROLE`,
  `PG_WRAP_ITEM_ID`, `PG_WRAP_ITEM_TYPE` and `PG_WRAP_READY_MARKER` the handler sets, and the
  keep-list of the base environment (`INV-CCH-32`).

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
  Each dispatch launches a fresh session of its own; it waits on an existing session instead only
  when that session's turn for the same event is still in progress, it declines while any other
  attempt for the bead and role is in flight, it closes a leftover `starting` or `ready` session
  that no handler supervises before launching, and it never adopts a settled session
  (`INV-CCH-30`).
  When a dispatch reaches a terminal outcome, this module also closes (without purging) the one
  session it launched or absorbed, if it is settled and quiet (`INV-CCH-17`).
  Every launch of a ccpool-backed session, whether a new one, an ensure or a resume, passes the
  ready-marker path and the item-context variables again, because the session tool keeps none of
  them (`INV-CCH-33`); the agent it starts is the pool's launcher, an opaque executable that runs the
  role's wrap and then the agent (`INV-CCH-36`), and a resume runs the launcher currently applied.
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
  no record leads to. While such a teardown is unfinished, the record is never absorbed
  (`INV-CCH-30`). This marking is a `ccpool` metadata write, never a `ccpool` label.
  A role configured to pre-fetch also runs `git` in the per-bead worktree before the session
  launches (`INV-CCH-21`): it resolves or fetches one commit from the configured remote, checks it
  out detached, and writes two files under an ignored directory of the worktree.
- **`INTF-CCH-BEADS`** <!-- uuid: 01dd79ce-ffbc-4234-9d6e-e7125561694f --> — this module's
  beads-backed source querying `bd` for events, and a handler session's completion policy writing
  a result back to `bd`. **Counterparty:** `bd` (boundary). **Initiator:** this module.
  **Multiplicity:** one query surface plus one write path per handler session that closes a bead.

None of these two crossings is declared to pg-router's core in any form — per the boundary
principle pg-router's own README states, a `command`-backed handler session's argv is a
pass-through the core never reads, and these two are exactly the concrete tools that pass-through
may resolve to once configured. `INTF-CCH-CCPOOL`/`INTF-CCH-BEADS` are this
module's own decision, not the core's, and adding a third backing tool never touches
`packages/pg-router`.
