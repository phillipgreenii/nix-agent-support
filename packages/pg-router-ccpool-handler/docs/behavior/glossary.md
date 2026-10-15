# Glossary — pg-router-ccpool-handler

Vocabulary for this module's realization of `INTF-HANDLER`/`INTF-SOURCE`
(`packages/pg-router/docs/behavior/interfaces.md`). Terms the core itself defines — event, binding,
handler session, role, wiring — are not restated here; see
`packages/pg-router/docs/behavior/glossary.md`. Task 5.2/5.3 (folded together, commit `c6b016fb`)
built every term below into real code (`internal/budget`, `internal/watchdog`, `internal/prompt`,
`internal/complete`, the `internal/executor` ccpool/command split); see [README](README.md)'s
Realization gaps for the build's one remaining residual.

## Handler-side vocabulary

- **Handler executor** — what a handler session of this module does for one dispatched event,
  according to its role's **backing kind**; exactly one kind backs a given role. A
  **ccpool-backed** handler session drives an agent session through `ccpool` (`INTF-CCH-CCPOOL`):
  it starts the session, supervises it, and settles it when it ends. A **command-backed** handler
  session runs the role's operator-configured command and reports the outcome of that run. Which kind
  backs a given role is this module's own configuration, never the core's concern
  (`INTF-HANDLER`).
- **Watchdog** — this module's own policy for noticing a handler session that has stalled, distinct
  from the core's delivery bookkeeping (`INTF-HANDLER`'s "a handler's run status is not part of
  this contract").
- **Budget** — this module's own policy for a usage ceiling on a handler session (a `resource-limit`
  post-accept outcome in `INTF-HANDLER`'s vocabulary); pg-router's core never declares this ceiling.
- **Prompt template** — how this module renders the text a handler session is asked to act on, from
  the opaque event payload the core handed over.
- **Completion policy** — how this module turns a finished handler session into the opaque outcome
  string it reports back over `INTF-HANDLER`; the core stores that string without interpreting it.

- **Claim latch** — the handler's own record that a dispatched session claimed its bead: set the
  first time the bead is read in progress. For a `close-or-release` role it is set only when the
  bead is held by the role's own actor, so a peer's claim never sets it (`INV-CCH-28`).
- **Session ended** — for a `close-or-release` role, a session that ccpool no longer reports
  active AND whose transcript and subagents have been quiet for the quiet window; never a bare
  `idle` (`INV-CCH-28`).
- **Unclaimed end** — a session that ended, for a `close-or-release` role, without ever claiming
  its bead and without leaving a mark that it gave the bead back; two in a row escalate the bead
  to a human (`INV-CCH-28`).
- **Fresh session** — a session a dispatch launches for itself, with its own launch time, budget
  clock and supervision lease; every dispatch runs in one unless it absorbs (`INV-CCH-30`).
- **Turn in progress** — a live session that is `working` or `needs_input`: a turn under way, or
  one paused for a person. The only state in which an earlier session can be absorbed
  (`INV-CCH-30`).
- **In flight** — an earlier session of the same bead and role that is not over: its turn is in
  progress, or a handler still holds its supervision lease (launching it, waiting on it, settling
  it or handling its death), whatever its state. While one is, no dispatch launches beside it
  (`INV-CCH-30`).
- **Settled session** — a session whose turn has ended, or that is no longer live. It is never
  absorbed, whoever closed it: while its handler still holds its lease it is still in flight and a
  later dispatch declines, and after that a later dispatch runs in a fresh session (`INV-CCH-17`,
  `INV-CCH-30`).
- **Absorb** — a dispatch waiting on an earlier session's turn in progress as its own instead of
  launching a fresh session; allowed only for the same event, a session nobody has closed and a
  bead not yet closed, and the one exception to a fresh session (`INV-CCH-30`).

## Source-side vocabulary

- **Beads-backed source** — this module's realization of `INTF-SOURCE`'s one opaque source
  contract, backed by a `bd` (beads) query. What query it runs and how it shapes the reply is this
  module's own configuration; the core distinguishes no source kinds.

## Backing tools (named here only — the Floor forbids naming them in the core)

- **`ccpool`** — the CLI a ccpool-backed handler session drives.
- **`bd` (beads)** — the CLI/store a beads-backed source reads, and a handler session's completion
  policy may write back to.
- **`pg-pr`** — the CLI a handler session's ACL check consults.
