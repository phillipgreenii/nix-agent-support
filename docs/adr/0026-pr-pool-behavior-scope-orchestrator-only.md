# pr-pool behavior scope = bare orchestrator; all workflow/domain behavior is a deployment overlay

**Status**: Accepted
**Date**: 2026-07-10
**Deciders**: Phillip Green II

## Context

[ADR 0025](0025-behavior-docs-three-set-split.md) split the behavior docs into three
sets (method / generic pr-pool / per-project overlay), but scoped the **generic
pr-pool** set far too broadly: it kept the review, my-PR, and backlog **workflows**,
the review artifact, tracking-object lifecycle, readiness/triage, escalation, and a
capability map that named tools. Review of that result established a much narrower
model:

- **pr-pool is a bare orchestrator.** It runs **queries** to discover items; for each
  item a query returns it dispatches the matching **role**, run through an
  **agent-runner**, under a **budget**; it **drains** to empty and a **scheduler**
  re-invokes it. It treats an item as **opaque** — whether the item is a PR, an alert,
  a backlog entry, or a policy hit is not pr-pool's concern.
- pr-pool must know **nothing** about PRs/reviews/merging/"backlog"/gates/stacked
  PRs/readiness/triage/tracking-object lifecycle, and must name **no** tool (not pg-pr,
  not beads, not even its agent-runner) and **no** organization. The public generic
  repo must contain **zero** references to a specific deployment.
- pr-pool interacts with the world only through **contracts** (agent-runner,
  query-source). A deployment chooses which tool fills each contract, and why.
- All workflow/domain behavior — and the tool choices — belong to the **deployment's**
  overlay (for this author, the ZR set in the private `your-private-flake`
  repo).

## Decision

**Re-scope the generic pr-pool set to the orchestrator only, and move all
workflow/domain behavior to the deployment overlay.**

- **Generic pr-pool** (`packages/pr-pool/docs/behavior/`) keeps: `README`, `glossary`
  (orchestration terms — orchestrator, query, query-source, item, role, agent-runner,
  session, drain, scheduler, claim, sideline, budget, contract), `invariants`
  (`INV-OP-*`, `INV-BUDGET-1`, `INV-CONT-1/2/3`, `GOAL-CONT-4`, `INV-FAIL-1`,
  `INV-SEC-1/3`, `INV-OBS-1`, `INV-CLAIM-1`, `INV-PREC-1`, `GOAL-SIMPLE-1/2`, and new
  `INV-RUN-1`/`INV-QSRC-1` for the two contracts), `contracts` (the agent-runner and
  query-source contracts, tool-free), and `operating-pr-pool`. It names no tool and no
  deployment.
- **`INV-CONT-2` is repurposed** from "park a tracking object" to a tool-free
  orchestrator rule: _sideline a stuck dispatch off this run's ready set and continue
  the drain_. Durable **parking** (record the situation, keep off future ready sets,
  unclaim) is a deployment concern (`ZR-INV-PARK-1`).
- Kept invariants that named moved concepts were **reworded**, not retained verbatim:
  `INV-CLAIM-1` reasons about "items" (never tracking objects) and leans on the
  query-source contract; `INV-PREC-1` orders orchestrator tiers
  (safety/isolation > never-drop-work > efficiency), dropping "authority"/"right-sizing";
  `INV-CONT-1`/`INV-FAIL-1` "hand the item back" rather than "escalate"; `INV-OBS-1`
  lists only orchestrator telemetry; `INV-SEC-3` drops the authorship example.
- **Everything else moves to the deployment overlay** (`docs/behavior/`
  in `your-private-flake`): the workflows (reviewing others' PRs, shepherding my
  PRs, working the backlog), the review artifact, a `capability-map` (which tool fills
  which contract and why), and the domain invariants — renumbered into a **ZR-distinct
  namespace** `ZR-INV-*` / `ZR-GOAL-*` so no invariant family is split across the
  public/private boundary (`AUTH`, `TRACK`, `WORK`, `FRESH`, `READY`, plus `ATTR` from
  the old `INV-SEC-2` and `PARK` for durable park).
- **Cross-set references** stay textual `<repo-name> · <set-path> · <ID>`; within an
  overlay, a bare `INV-*`/`GOAL-*` is documented to mean the generic set and a `ZR-*`
  ID is local.
- **Downstream public files** (`docs/pr-review-flow.md`, `packages/pr-pool/README.md`,
  `packages/pg-pr/pg-pr.md`) were reframed to cite only generic orchestration and to
  state that the review/work **workflow** behavior is deployment-defined — never
  linking the private overlay (which would be a public→private reference).

## Consequences

### Positive

- pr-pool's behavior docs finally match what pr-pool _is_: a generic orchestrator with
  two contracts. It is reusable by any deployment, names nothing org-specific, and the
  public repo leaks no ZR detail.
- The overlay owns exactly the workflow/domain behavior and the tool choices, which is
  where the author's human attention actually goes.
- No invariant family is split across repos; ownership is unambiguous.

### Negative

- A large one-time migration: most of the old generic set moved repos and its IDs were
  renumbered, so historical references to the old IDs (e.g. `INV-AUTH-2`) no longer
  resolve in the generic repo.
- `pg-pr` (a public tool with review-related capabilities) now has **no generic
  behavior-doc home** for that capability's intended behavior — it lives only in
  deployment overlays. A future `packages/pg-pr/docs/behavior/` may be warranted.
- The public ADR 0023 (bot attribution) now governs a private invariant
  (`ZR-INV-ATTR-1`), cited cross-repo.

### Neutral

- The review **artifact format** (`reviews.md`) moved to the overlay at the owner's
  instruction, though it is tool-neutral; a future non-ZR deployment would re-derive or
  share it.

## Alternatives Considered

### Keep the ADR-0025 boundary (workflows in generic pr-pool)

Rejected: it conflates "the orchestrator" with "the workflows a deployment builds on
it," forces the public repo to describe org-specific behavior, and makes pr-pool know
about PRs/reviews/tools it should be ignorant of.

### Move even more out (minimal core: only orchestration + budget + contracts)

Rejected (the "Option B" boundary): isolation, failure-class handling, continuity, and
observability are genuinely part of what the **orchestrator** guarantees about how it
runs roles — the agent-runner contract would be hollow without them. They stay generic.

### Reuse the same `INV-*` family numbers in both repos

Rejected: splitting a family (e.g. `INV-SEC-1` public, `INV-SEC-2` private) is
ambiguous even with full citations. ZR-owned rules use a `ZR-` prefix.

## Related Decisions

- Supersedes [0025](0025-behavior-docs-three-set-split.md).
- See also: your-private-flake `docs/behavior/` (the overlay).
- `INV-SEC-2` → `ZR-INV-ATTR-1` still records the decision in
  [0023](0023-agent-pr-comments-visible-bot-attribution.md).
