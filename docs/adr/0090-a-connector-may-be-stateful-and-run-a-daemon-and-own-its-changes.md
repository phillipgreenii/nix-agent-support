# A connector MAY be stateful, run a daemon per rate-limit domain and own its changes

**Status**: Proposed (draft until the operator approves the design; amends ADR 0062 and ADR 0077)
**Date**: 2026-10-10
**Deciders**: phillipg

This ADR is filed before any implementation starts, so that the behavior docs and the later phases of
program epic `pg2-z5fax` can cite it by number. It lifts a set of earlier rulings, each as a
RESTRICTION: a connector MAY now do what the ruling forbade, and none is required to. The design it
records is `2026-10-09-pg-connector-github-daemon-design.md` (revision 7), the origin of which is bead
`pg2-zhuiu`. That design is provenance only, because per this repo's citation conventions the files
under `docs/superpowers/specs/` are not durable citation targets. The key words MUST, MUST NOT,
REQUIRED, SHALL, SHALL NOT, SHOULD, SHOULD NOT, RECOMMENDED, MAY and OPTIONAL in this ADR are to be
interpreted as described in RFC 2119.

## Context

The connector suite was built on four rulings of the 2026-09-09 pg-desk and connector discovery
design: pg-router is the single scheduler and no connector runs a daemon (D2), connectors are
stateless (D3), the umbrella decides what changed (D5) and the umbrella owns every cursor (D6).
ADR 0062 carried the matching architecture rule, that every Tier-2 backend stays uniformly simple and
stateless, and ADR 0077 (rows S29 to S31) put change detection, the remote sweep and every clock in
pg-desk and pg-router.

Those rulings fit a backend that is a thin call to an origin. They fit badly a backend whose origin
has a points-based rate limit, because only the connector itself knows which fields are worth
refreshing and when, which of them a single batched read can fetch together, and whether a cheap
revalidation shows that nothing changed. The operator's direction on 2026-10-09 (quoted in the design) is that this change "is not a
requirement for all connector implementations, they can remain being no cache or stateless and no
daemon", that the lift is only a lifting of the previous restriction, and that "to be optimal, only
the connector itself understands what to do".

## Decision

1. **A connector MAY be stateful and MAY run a daemon.** A backend MAY persist state it owns (D3), and
   MAY run one daemon per upstream rate-limit domain, with its own clock for its upstream reads (D2
   in full, and D12's rejected alternative "per-connector auto-refresh daemons", accepted for this
   connector only and scoped per rate-limit domain; D12's other rejections stand). Every other
   connector stays as it is.
2. **A backend that declares `owns_changes` decides what changed and owns its consumers' cursors for
   its types** (D5 and D6). The umbrella forwards the consumer's poll and acknowledgement and does
   not diff, sweep or hydrate for those types. pg-router keeps the clocks of its consumer polls
   (ADR 0077 S30 narrowed); an upstream read clock MAY live in a daemon-backed backend.
3. **For a backend that owns its changes, change detection lives in that backend** (ADR 0077 S29 and
   S31 narrowed). The pg-desk baseline fingerprint, the pull-through hydration and the remote age
   sweep do not apply to that backend's types.
4. **ADR 0062's uniformity statement is amended.** "Every Tier-2 backend stays uniformly simple and
   stateless" no longer holds as a rule. Its item 6, which already made a backend's own local store
   that backend's own concern, is now the governing rule and not drift.
5. **The connector's behavior invariants are scoped, not removed.** `INV-STATE-1` (per-call policy
   comes only from the request) no longer binds a daemon-backed backend, which MAY read policy from
   its own config, rendered from the same option. `INV-CACHE-1` and `INV-CACHE-8` apply to backends
   that do not own their cache and changes.
6. **A daemon-backed backend MAY offer `status`.** `ACTOR-BACKEND` (a backend has no human-facing CLI
   identity) is relaxed to that extent.
7. **The sweep period is replaced for this backend.** The 30 minute sweep period of D21, a
   configuration default, is replaced by `detail_max_age` and `conversation_max_age`.

### The lifts

The twelve rulings lifted, with what stands after each, verbatim from the design's "Lifted rulings"
table.

| Ruling                                                                                           | Source                                                                                           | After                                                                                                               |
| ------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------- |
| D2 in full: pg-router (then pr-pool) is the single scheduler; no connector runs a daemon         | `2026-09-09-pg-desk-and-connector-discovery-design.md`, decision table                           | A connector MAY run one daemon per upstream rate-limit domain, with its own clock for its upstream reads            |
| D3: connectors are stateless                                                                     | same                                                                                             | A connector MAY persist state it owns                                                                               |
| D5: the umbrella decides what changed                                                            | same                                                                                             | A backend that declares `owns_changes` decides for its types                                                        |
| D6: the umbrella owns every cursor                                                               | same                                                                                             | A backend that declares `owns_changes` owns its consumers' cursors; the umbrella forwards and acknowledges          |
| ADR 0062, the uniformity statement ("every Tier-2 backend stays uniformly simple and stateless") | `docs/adr/0062-pg-connector-tier1-tier2-connector-architecture.md`                               | Amended; its item on "a backend's own local store" being the backend's concern is now the governing rule, not drift |
| `INV-STATE-1`: per-call policy only from the request                                             | `packages/pg-connector/docs/behavior/invariants.md`                                              | A daemon-backed backend MAY read policy from its own config, rendered from the same option                          |
| `INV-CACHE-1`, `INV-CACHE-8`                                                                     | same                                                                                             | Scoped to backends that do not own their cache and changes                                                          |
| ADR 0077, rows S29 and S31 (change detection in pg-desk, the remote sweep)                       | `docs/adr/0077-entity-change-flow.md`                                                            | For a backend that owns its changes, detection lives in that backend                                                |
| ADR 0077, row S30 (pg-router owns every clock)                                                   | same                                                                                             | pg-router keeps consumer-poll clocks; upstream read clocks MAY live in a daemon-backed backend                      |
| D12 and the rejected alternative "Per-connector auto-refresh daemons"                            | `2026-09-09-pg-desk-and-connector-discovery-design.md`, decision table and rejected alternatives | Accepted for this connector, scoped per rate-limit domain (the other D12 rejections stand)                          |
| D21: the 30 min sweep period as a configuration default                                          | same, decision table                                                                             | For this backend the sweep period is replaced by `detail_max_age` and `conversation_max_age`                        |
| `ACTOR-BACKEND`: a backend has no human-facing CLI identity                                      | `packages/pg-connector/docs/behavior/actors.md`                                                  | A daemon-backed backend MAY offer `status`                                                                          |

### The check of ADR 0081 and ADR 0087

Both ADRs build on pg-desk's change flow, so each was read against the lifts above before this ADR
was filed. The check found no conflict that needs a ruling.

- **ADR 0081 (entity attention is evaluated at read time in pg-desk).** Its rule is that pg-desk
  owns whether an entity needs the operator, evaluated at read time over pg-desk's own store. That
  rule does not depend on who detects a change: the evaluator reads the stored entity and
  interpretation rows, and a backend that owns its changes still has those rows written by pg-desk,
  from the connector's change feed and reads instead of from a pg-desk listing diff. Its decision 6,
  that a connector's attention is limited to what only that connector can see, is NOT lifted: this
  ADR gives no backend a PR or issue attention duty. One wording shifts: where ADR 0081 and the
  pg-desk attention behavior doc say a rule is "exactly as fresh as the last hydration" of the
  entity, then for a type whose backend owns its changes "last hydration" reads as the last time
  pg-desk re-derived the entity from data the connector reported changed. The pg-desk behavior
  packets of this program carry that rewording.
- **ADR 0087 (the daily-focus rank is a read-time pg-desk view).** The rank is a read-only view over
  stored `entity`, `interpretation` and link rows with an injected clock, and minting stays in a
  decider. Nothing in it depends on where detection lives, and nothing in it calls the pieces this
  ADR lifts (S29 to S31). Unchanged.
- **Boundary kept by both.** The decoration-versus-decision rule is untouched: a daemon-backed
  backend decides only WHAT CHANGED for its own types, never what work should exist. Deciders stay
  outside connectors and pg-desk (ADR 0077 G5, ADR 0087 decision 2).
- **Open items.** None needing an operator ruling. One matter is left to the pg-desk behavior docs
  of this program rather than decided here: ADR 0077 S31's local reconcile tier also recovers a
  failed decider run (D6 of that ADR), and the behavior docs MUST state how that recovery is
  provided for a type whose backend owns its changes, since the connector's acknowledged change feed
  (redelivery until acknowledged) covers part of it.

## Consequences

- Behavior docs MUST be edited first, in a later packet of the same phase, and cite this ADR by
  number: the pg-connector invariants, interfaces, actors, glossary and journeys; the pg-desk
  changes, consumer, freshness, gather, refresh and shadow-compare docs; the pg-router, adapter,
  ccpool and pg-pr-retirement docs; and a new behavior-docs set for the new backend.
- ADR 0062 and ADR 0077 each gain an amendment pointer to this ADR, in the convention ADR 0081 and
  ADR 0087 already use when they amend ADR 0077. No decision-log row of ADR 0077 is removed: S29 to
  S31 are read with this ADR for a backend that owns its changes.
- A new stateful backend is a heavier thing to operate than a stateless one: a store, a socket, a
  supervised daemon and a rate-limit budget. Only a backend whose origin makes that worth it SHOULD
  take it on; this ADR does not push any other connector toward it.
- While the design is a draft, this ADR is `Proposed`. It becomes `Accepted` when the operator
  approves the design, and no implementation starts before then.

## Related Decisions

- Amends ADR 0062 (the Tier-1 umbrella and Tier-2 backend architecture): the uniformity statement.
- Amends ADR 0077 (the entity change flow): rows S29, S30 and S31, for a backend that owns its
  changes.
- Checked against, and consistent with, ADR 0081 and ADR 0087.
- Tracked under program epic `pg2-z5fax`, phase docket `pg2-z5fax.1`; origin bead `pg2-zhuiu`.
