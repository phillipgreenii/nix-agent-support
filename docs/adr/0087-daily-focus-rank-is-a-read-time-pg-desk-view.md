# The daily-focus rank is a read-time pg-desk view; minting stays in a decider

**Status**: Accepted (operator ruling, 2026-10-06; revised 2026-10-07 for the defer-and-undefer
mechanism of its strike consequence; lands with the daily-focus design, bead `pg2-2j5ac.27`)
**Date**: 2026-10-06
**Deciders**: phillipg

This ADR amends the decoration-versus-decision rule (the entity change flow design's "Where new logic goes" section, carried by ADR 0077) in place, and extends ADR 0081's
read-time rule from attention to the daily-focus rank. The daily-focus design that needs it is
`2026-09-23-daily-focus-store-first-design.md`, which is provenance only because, per this repo's
citation conventions, the files under `docs/superpowers/specs/` are not durable citation targets.

## Context

The entity change flow design's "Where new logic goes" section, carried by ADR 0077 through S2 and G5, sorts every new behavior into a decoration (a fact derived from what pg-desk already holds,
with no side effect), a decision (anything that creates, changes or closes a work item, label,
annotation or external write) or a role. Its closing sentence reads "Cross-entity judgment that
chooses an outcome (for example daily-focus ranking) is a decision, not a decoration", and G5 puts
decisions in deciders, outside pg-desk.

On 2026-10-05 the operator ruled that the daily-focus rank is a strict-tier ordering computed at
read time inside pg-desk: it depends on the clock (overdue, a 7-day horizon) and on cross-entity
facts (an epic and its children share one slot), so a rank written when an entity changes goes
stale on both. That is the same reason ADR 0081 evaluates attention at read time. The ruling and the
written rule disagree on the one example the rule names.

## Decision

1. **A read-only, side-effect-free cross-entity view over stored facts lives in pg-desk.** The
   daily-focus rank is one: it reads `entity`, `interpretation` and link rows with an injected clock,
   writes nothing, execs nothing and mints no work. It is computed on every read and never cached.
2. **Only the act that creates, changes or closes work remains a decision.** Minting a bead for an
   item the operator selected is done by a pg-decider rule (kind `focus-item`), reacting to the
   `focus_selected` annotation that `focus select` and `focus pull` write. pg-desk MUST NOT contain
   the work kind or its dedup-key logic, and MUST NOT execute a tracker write verb (G5 unchanged).
3. **The decoration-versus-decision rule ("Where new logic goes" in the design, carried by ADR 0077) is read accordingly.** "Cross-entity
   judgment that chooses an outcome" is a decision when it causes a write; the same judgment
   expressed as a read-only view is a view. The closing sentence's example, daily-focus ranking, is
   therefore a view, and its minting is the decision.
4. **Operator-invoked pg-desk verbs record operator input as mechanism.** `focus select` and `focus pull` write the selection row and the `focus_selected` annotation, and `--merge` writes an external link, exactly as `hide`, `wip` and `annotate` do. "Writes nothing" is true of the rank, not of those verbs; they record what the operator said, and the decider decides only what work exists, including holding (deferring) a focus bead when the operator strikes its item and releasing it on a reselect.

## Consequences

- The `internal/focus` package in pg-desk (rank, the selection store) conforms to the change flow's
  ownership split; the `focus-item` rule belongs in pg-decider. A guard test pins that pg-desk
  carries no `focus-item` literal.
- A reviewer reading ADR 0077's section on decorations and decisions finds an amendment note
  pointing here, so the two texts agree at every commit.
- The decider never closes or reopens a focus bead: a strike defers it indefinitely and a reselect undefers it (the operator's direction, 2026-10-07). "Closed" therefore keeps meaning that the work is finished, and nothing in the rule depends on how a work item was closed. The decider's own behavior documents word its reading of a work item more narrowly than this and are amended with the daily-focus change. Tools that look for a focus bead must look at deferred ones too.
- The decider's `focus_selected` selection reaches it through a dedicated `annotations.focus_selected`
  member of the view (D-F15), not through the decider's own namespace.
- ADR 0081 and this ADR share one rule: a rank or an evaluation that depends on time or on sibling
  entities is a read-time view; the event-driven deciders do the writing.
