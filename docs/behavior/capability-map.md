# Capability map

**Status:** Living, but the **most implementation-adjacent** doc here — expect it
to change most often, and it is a candidate to move downstream. _Placement is an
open question (see bottom)._ It is part of the **per-project overlay**, not the
shared base.

## Purpose

The workflow and invariant docs stay tool-neutral: they state **capabilities and
constraints**, never which tool provides them. This doc is the **one place** that
records which tool provides each capability **today** — so a requirement like
`INV-SEC-1` (isolate untrusted content) or "custom gates must exist" is never lost
just because the narrative doesn't name a tool. Nothing above this doc depends on
these mappings; a capability can move to a different tool and only its row changes.

## Map

| Capability (as the docs require it)                                                   | Invariant(s)                             | Provided today by              |
| ------------------------------------------------------------------------------------- | ---------------------------------------- | ------------------------------ |
| PR data: open PRs, ownership, draft state, head, labels, requested-reviewers          | —                                        | pg-pr                          |
| Posting reviews/comments; draft reviews; bot attribution                              | `INV-SEC-2`                              | pg-pr (provider write surface) |
| Custom work **gates** (e.g. block a downstream stacked-PR item until upstream merges) | `INV-WORK-3`                             | pg-pr / the tracker's gates    |
| **Tracking objects** + lifecycle (per-PR object, backlog items, park/unclaim)         | `INV-TRACK-*`, `INV-CONT-2`              | beads                          |
| **Escalation surfacing** — the "NEEDS ME" signal on a tracking object                 | `INV-AUTH-3`                             | beads `human` label            |
| **Escalation/nudge delivery** — physically reaching me + auto-resume nudges           | `INV-AUTH-3`, `INV-CONT-3`               | the nudge/notification channel |
| **Role-scoped claim identity** (no shared default claimant)                           | `INV-CLAIM-1`                            | the tracker's claim/assignee   |
| Autonomous **agent sessions** (run a role; isolation; least-privilege tools; budget)  | `INV-SEC-1`, `INV-BUDGET-1`, `INV-OBS-1` | ccpool                         |
| **Orchestration** (drain, roles-from-config, run-to-empty)                            | `INV-OP-*`                               | pr-pool                        |
| **Freshness / as-of** on the surfaces I act on                                        | `INV-FRESH-1`                            | (the data surface's timestamp) |
| **Observability** emission (status to a dashboard)                                    | `INV-OBS-1`                              | otel → Grafana                 |

## Notes

- Tool names here are the **current** realization. The workflow and invariant docs
  never reference them; if a capability moves, only its row changes.
- **Escalation is two capabilities, not one:** _surfacing_ (the `human` label puts
  an item in NEEDS ME) and _delivery_ (a notification/nudge channel actually reaches
  me and drives auto-resume). Delivery is itself failure-prone (`INV-AUTH-3`
  requires it be delivered), so it is a first-class capability with its own failure
  conditions.
- The security posture of autonomous sessions (isolation, least-privilege per role,
  no ambient-credential inheritance) is the realization of `INV-SEC-1`; its
  requirements are invariants, its current realization is downstream.

## Open questions

- **Is "capability map" the right home**, or should tool-mapping live in the
  downstream implementation reference? Given `GOAL-SIMPLE-2` (org/repo config lives
  in org/repo repos), the per-project half of this map may ultimately belong with a
  deployment, not in the generic doc set. Deliberately unsettled.
- Several capabilities span two tools (gates: pg-pr + tracker; claim: tracker) — is
  that a real seam or an artifact of history?
