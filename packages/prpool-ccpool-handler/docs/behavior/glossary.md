# Glossary — prpool-ccpool-handler

Vocabulary for this module's realization of `INTF-HANDLER`/`INTF-SOURCE`
(`packages/pr-pool/docs/behavior/interfaces.md`). Terms the core itself defines — event, binding,
handler session, role, wiring — are not restated here; see
`packages/pr-pool/docs/behavior/glossary.md`. Nothing below exists in the module's code yet (see
[README](README.md)'s Realization gaps) — this vocabulary names what Task 5.2 onward moves in.

## Handler-side vocabulary

- **Handler executor** — the concrete code path this module runs when the core dispatches an
  event to one of this module's handler sessions. Exactly one of **ccpool-backed** (drives the
  `ccpool` CLI) or **command-backed** (execs an operator-configured argv); which one backs a given
  role is this module's own configuration, never the core's concern (`INTF-HANDLER`).
- **Watchdog** — this module's own policy for noticing a handler session that has stalled, distinct
  from the core's delivery bookkeeping (`INTF-HANDLER`'s "a handler's run status is not part of
  this contract").
- **Budget** — this module's own policy for a usage ceiling on a handler session (a `resource-limit`
  post-accept outcome in `INTF-HANDLER`'s vocabulary); pr-pool's core never declares this ceiling.
- **Prompt template** — how this module renders the text a handler session is asked to act on, from
  the opaque event payload the core handed over.
- **Completion policy** — how this module turns a finished handler session into the opaque outcome
  string it reports back over `INTF-HANDLER`; the core stores that string without interpreting it.

## Source-side vocabulary

- **Beads-backed source** — this module's realization of `INTF-SOURCE`'s one opaque source
  contract, backed by a `bd` (beads) query. What query it runs and how it shapes the reply is this
  module's own configuration; the core distinguishes no source kinds.

## Backing tools (named here only — the Floor forbids naming them in the core)

- **`ccpool`** — the CLI a ccpool-backed handler session drives.
- **`bd` (beads)** — the CLI/store a beads-backed source reads, and a handler session's completion
  policy may write back to.
- **`pg-pr`** — the CLI a handler session's ACL check consults.
