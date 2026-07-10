# Capability map

**What this doc is for:** the rest of this set names **capabilities** ("post a
draft review", "hold a gate") and never the tool behind them, so the docs stay
tool-neutral and survive a tool swap. The cost of that discipline is that the
tool→capability knowledge has to live _somewhere_ or it is lost. This is that one
place. Read it when you need to know **which tool realizes a given capability
today** and **which invariant that capability exists to satisfy**.

It is the **most implementation-adjacent** doc in the set — expect it to change most
often as tools evolve.

## Purpose

The workflow and invariant docs state **capabilities and constraints**, never which
tool provides them. This doc records which tool in the **generic toolchain** (pg-pr,
pr-pool, ccpool, beads, …) provides each capability **today**, tied to the invariant
it satisfies — so a requirement like `INV-SEC-1` (isolate untrusted content) or
"custom gates must exist" is never lost just because the narrative doesn't name a
tool. Nothing above this doc depends on these mappings; a capability can move to a
different tool and only its row changes.

Org-specific **configuration** of these tools (which labels, which team, which
identities, per-repo integration style) is _not_ here — it lives in the deployment
overlay (`phillipg-nix-ziprecruiter · pr-pool-components/docs/behavior`). This doc is
the generic tool→capability map; the overlay supplies the deployment's values.

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

- **Is "capability map" the right home** for the _generic_ tool→capability mapping,
  or should that mapping live in the downstream implementation reference
  (`docs/pr-review-flow.md`)? The org-specific half is settled — it moved to the
  deployment overlay (per `GOAL-SIMPLE-2`); what remains open is only whether the
  generic mapping belongs in this behavior set at all, or downstream. Deliberately
  unsettled.
- Several capabilities span two tools (gates: pg-pr + tracker; claim: tracker) — is
  that a real seam or an artifact of history?
