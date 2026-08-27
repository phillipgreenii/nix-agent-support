# plan-decompose: a layered medium-binding plugin for design-to-work-packet decomposition

**Status**: Accepted
**Date**: 2026-08-27
**Deciders**: Phillip (operator), in the `pg2-98dt2` brainstorming session (2026-08-27); this ADR
records rulings the operator made live.

## Context

Decomposing an approved design into implementation beads previously left each child a terse
pointer into the epic's DESIGN field, so every implementing agent re-read the full design
(measured on `pg2-svfbb`: ~10k tokens × 8 children ≈ 80k tokens of duplicated reads), with no
staleness guard and sizing rulings hardcoded as prose. The operator asked for general tooling
producing curated, SELF-CONTAINED children, with implementer efficiency (one-read target) and
nothing model- or budget-shaped hardcoded.

## Decision

A new plugin, `plan-decompose`, in this repo's claude-marketplace, structured as:

1. **Layered core + binding** (the wayfinder-beads pattern): a medium-agnostic core skill
   (`plan-decompose`) written against an abstract operation contract, and per-medium binding
   skills (`plan-decompose-beads` ships in v1). The core MUST NOT silently default to an ad-hoc
   medium: it uses the binding named in the brief, auto-selects a SOLE installed binding with an
   announcement, and otherwise refuses with the candidate list.
2. **Native-metadata channel**: model, budget, revision, curation stamps, phase markers, and
   staleness flags ride the medium's structured per-key replace-semantics metadata (bd:
   `--set-metadata`, underscore `pd_*` keys), never packet content and never append-only notes.
   Sizing resolves packet metadata → docket metadata → a single documented fallback default, so
   retargeting a decomposition (e.g. Sonnet/250k → Opus/300k) is one docket metadata edit.
3. **Deferred-held packet lifecycle**: packets are created and stay status-DEFERRED (the only bd
   state the ready queue actually respects — assignee does not hide a bead from `bd ready`)
   through curation and all checks, and are released as a set only after a bounded
   pre-filter → cold-read → semantic post-check pipeline passes. Every early exit leaves packets
   deferred with a `pd_phase=failed:<phase>` marker — an aborted run never releases unverified
   work, and a mid-flight death is distinguishable from an ordinary stranded bead.
4. **Split-and-curate, never author**: every substantive packet clause carries a
   `[design: <section>]` citation; plan gaps halt decomposition with a durable gap report rather
   than being filled in.
5. **Two plugin-owned agents** (pg-pr topology): `plan-decomposer` (executes the pipeline) and
   `packet-implementer` (the reference consumer: stamp check at claim, packet-first, escalate to
   the docket design only when stuck and record it, metric record at closeout). Agents are never
   load-bearing: the packet text alone must suffice for any consumer.

## Consequences

- Curation cost is paid once at decomposition time instead of N times at implementation time;
  metric records (escalation reads, validation retries) make curation quality measurable.
- Zero changes to `pb`/`/drain-beads`: its pointer-briefs already have the subagent read the bead
  itself, so curated packets work in the existing queue unchanged; preferring
  `packet-implementer` in drain is a possible later `pb` change, deliberately out of scope.
- A design amendment obligates a RECONCILE pass (stamp/revision compare makes missed reconciles
  detectable at claim time); claimed packets are flagged, never rewritten underfoot.
- Additional bindings (markdown/frontmatter, Jira) are additive later; none ship until a consumer
  exists.

Full design (provenance; this ADR stands alone):
`docs/superpowers/specs/2026-08-27-plan-decompose-design.md` — repo-committed by operator ruling,
not an ephemeral workspace-root spec.
