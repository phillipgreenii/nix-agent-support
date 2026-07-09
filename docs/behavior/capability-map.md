# Truth: Capability map

**Status:** Living, but the most implementation-adjacent of these docs — expect it
to change most often. _Placement is itself an open question (see bottom)._

## Purpose

The workflow docs stay tool-neutral: they state **capabilities and constraints**,
never which tool provides them. This doc is the **one place** that records which
tool provides each capability **today** — so an invariant like "custom work gates
must exist" is never lost just because the narrative doesn't name a tool. Nothing
above this doc depends on the mappings here; they can change without touching the
behavior the workflows describe.

## Map

| Capability (as the workflows/invariants require it)                                             | Provided today by            |
| ----------------------------------------------------------------------------------------------- | ---------------------------- |
| PR data: open PRs, ownership, draft state, head, labels, requested-reviewers                    | pg-pr                        |
| Posting reviews/comments; draft reviews; PENDING reviews; bot attribution                       | pg-pr (GitHub write surface) |
| Custom work **gates** (e.g. block a downstream stacked-PR item until its upstream merges)       | pg-pr / the tracker's gates  |
| **Tracking objects** + lifecycle (per-PR object, backlog items, human escalation, park/unclaim) | beads                        |
| Autonomous **agent sessions** (run a role; budget/watchdog; deny-by-default tools)              | ccpool                       |
| **Orchestration** (drain, roles-from-config, run-to-empty)                                      | pr-pool                      |
| **Observability** emission (status to a dashboard)                                              | otel → Grafana               |

## Notes

- The tool names here are the **current** realization. The workflow and invariant
  docs never reference them; if a capability moves to a different tool, only this
  row changes.
- The security posture of autonomous sessions (isolation, deny-by-default tools,
  credential exposure) is a property of the "agent sessions" capability; its
  requirements are stated as invariants, its current realization is downstream.

## Open questions

- **Is "capability map" the right home**, or should tool-mapping live in the
  downstream implementation-reference doc (the successor to `pr-review-flow.md`)?
  Placement is deliberately unsettled — we'll learn the right home as we iterate.
- Several capabilities are split across two tools (gates: pg-pr + tracker); is that
  a real seam or an artifact of history?
