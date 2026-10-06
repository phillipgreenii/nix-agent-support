# Entity attention is evaluated at read time in pg-desk

**Status**: Accepted
**Date**: 2026-10-05
**Deciders**: phillipg

This ADR amends ADR 0077's Ownership split in place (its dependency-direction sentence now points
here) and is the durable record of the rule model behind the pg-desk attention evaluator. The
operator (Phillip) approved the design as written on 2026-10-05, recorded on bead `pg2-dnhhh`
("Approve as written": the design's own recommendation on every open question and its small
parameters). The design, dated 2026-10-05 and named
`2026-10-05-pg-desk-attention-evaluator-and-connector-refresh-cache-design.md`, is provenance only,
because per this repo's citation conventions the files under `docs/superpowers/specs/` are not
durable citation targets. The behavior it implies is in `docs/behavior/pg-desk/attention.md`.

## Context

The menu bar reads one feed, `pg-connector attention list`, which fans `list_attention` out to
every source registered in `attention.sources`. Until 2026-10-02 the PR, issue-tracker and
beads-backed connector backends each decided, inside the connector, whether an entity needed the
operator ("review requested", "CI failing on my PR", "due soon"). Doing so cost a per-PR GraphQL
fan-out on the one-minute hot path, so those three backends were taken out of the registry, and
PR, deadline and bead attention disappeared from the menu bar until something replaced them.

Three facts shape the replacement:

- **pg-desk already holds the answer.** It stores the PRs, issues and threads it watches, an
  interpretation of each (the panel it belongs to, its approvals, its urgency) and the
  cross-references between them. Whether a PR needs the operator is, today, the dashboard's
  "awaiting me" panels. A connector re-derives that from the origin, at the origin's price.
- **A write-time decision goes stale.** The change flow's deciders (ADR 0077) are event-driven: they
  act when an entity changes. A "needs me" mark written that way cannot be right for a rule that
  depends on the clock, or on a sibling entity, because no event fires on the entity when the
  clock advances or a sibling merges.
- **Two surfaces must agree.** The menu bar and the My Work dashboard must not disagree about what
  needs the operator.

## Decision

1. **Entity attention is owned by pg-desk and evaluated at read time.** One pure function,
   `Evaluate`, in a new pg-desk package, is the only way attention is computed. It runs four
   stages (project the stored entities, raise candidates from registered rule kinds, suppress,
   group and rank) over a read-only view of the local store, the pg-desk configuration and an
   injected clock. It writes nothing, execs nothing, opens no connection and never reads the wall
   clock itself.
2. **Rule kinds are a registry; parameters are configuration.** A rule kind is a small Go type
   with a stable string id, registered at start-up with a panic on a duplicate (the shape of
   pg-desk's existing classifier registry), tuned by an `attention` block of the pg-desk config
   (the shape of its `verdict_generations` block). Operators override per entity with the
   existing `suppress.<kind>` annotation, which is why a suppressed candidate is retained with its
   reason and can be explained. The pg-decider runtime is NOT reused: only the registry shape is
   shared.
3. **Read time covers the suppression chain, grouping, ordering and any clock-based rule.** The
   first release's raise rules project the STORED interpretation row, so they are exactly as
   fresh as the entity's last hydration. This is "read-time over stored interpretations", and is
   stated plainly so it is not mistaken for re-deriving every fact at every read. Re-deriving
   interpretation at read time is a larger alternative, deliberately not taken.
4. **One evaluator feeds every surface.** The menu bar plugin, the dashboard payload and the
   `pg-desk attention list` and `explain` verbs all call `Evaluate`. A second copy of a rule
   anywhere is a defect.
5. **The menu bar keeps a single feed.** The evaluator is exposed to the connector umbrella as a
   standalone `list_attention` backend, `pg-desk-attention`, built from the pg-desk package and
   registered by bare name in `attention.sources`. The feed gains one additive optional field on
   an item, `group`, so the plugin can render work-context groups without a second source.
6. **A connector's attention is limited to what only that connector can see.** A calendar
   connector's events inside their window, an agent-session monitor's blocked sessions and an alert
   backend's firing alerts stay in their connectors. Whether a PR, an issue or a thread needs the
   operator is pg-desk's, and the connector backends' own `list_attention` for those types is
   retired once the plugin is registered and verified.

### Runtime versus compile-time dependency direction

ADR 0077's Ownership split says the dependency direction is one-way and that pg-desk depends on
pg-connector. At RUNTIME this decision has `pg-connector attention list` exec a pg-desk-owned
binary, which is a call from the umbrella to a pg-desk artifact. That is permitted, and it does
not reverse the dependency, for a stated reason and under stated conditions:

- **Compile-time and code-level dependency stays pg-desk to pg-connector only.** The plugin binary
  lives in pg-desk and imports pg-connector's wire packages, which pg-desk already depends on. The
  umbrella MUST NOT import, link or carry compiled-in knowledge of anything in pg-desk.
- **The umbrella learns of the plugin only through its registry.** The registry entry is a bare
  executable name in `attention.sources`, exactly as for every other source, supplied by
  configuration. A registry entry that happens to name a pg-desk binary is configuration, not a
  dependency.
- **The plugin execs nothing.** It reads pg-desk's local store only, so pg-desk's composition rule
  (it execs only `pg-connector` and the configured browser) and the connector's own composition
  guard are untouched.
- **It is explicitly not a connector backend.** A `pg-connector-pr-desk` backend inside
  pg-connector would invert the compile-time direction and is rejected.

## Consequences

- Entity attention is exactly as fresh as pg-desk's store and costs one process start and
  read-only local reads, with no origin API call, on the one-minute path. Its raise rules are as
  fresh as the last hydration of each entity, and a cross-entity input inside interpretation stays
  stale until that entity is re-hydrated.
- On an unmigrated store (schema version 1) the evaluator still runs, honoring the old `hidden` and
  `wip` columns, and reports that `suppress.*` overrides, work-item grouping and external
  dependency links are unavailable until `pg-desk migrate --cutover`.
- Behavior that the connector backends gave for PRs, issues and beads is re-expressed as pg-desk
  rules or recorded as deferred: the CI-failing rule is re-expressed (`pr.own-ci-failing`); the
  "re-review after my approval" leg and the issue due-date rules are deferred and named in the
  behavior doc, and the former needs an additive review-commit field in the connector.
- The connector's attention invariants for those backends are removed, and the connector docs
  reworded, in the same change that deletes the backends' code (a later bead), after the pg-desk
  behavior is recorded, so no moment exists with the behavior in neither place.
- Data freshness is deliberately NOT an attention item (a tool-health signal would breach the
  connector rule that attention is about interesting things in the data). It is reported through a
  separate freshness contract.
- A new nix package and command exist (`pg-desk-attention`), following the per-binary packaging of
  the `pg-connector-*` backends, with a home-manager option to register it.

## Alternatives Considered

### Keep the decision in the connector backends, cached

Rejected. The cost was the per-entity fan-out to the origin; a cache in front of it still leaves
the decision in a component that holds no decoration, cross-reference or suppression state, so
the cross-entity rules (grouping, suppression by sibling) could never live there.

### Write a "needs me" annotation from a decider

Rejected. It is stale for every clock-based rule and every sibling-dependent rule, because no
event fires on the entity when the clock advances or a sibling merges (see Context). A decider is
the right host for a decision that creates work; attention is a view over current state.

### Reuse the pg-decider runtime for the evaluator

Rejected. The registry shape is shared, the runtime is not: a decider reads through pg-desk and
writes beads and annotations, while the evaluator is read-only and in-process.

### Side calls instead of an additive field on the feed

Rejected for group delivery. A `pg-desk attention list` side call from the menu bar would end the
single-feed arrangement; the additive optional `group` field keeps one feed and one `pg-desk
links` call.

## Related Decisions

- Amends ADR 0077's Ownership split: pg-desk owns entity attention, and the dependency-direction
  sentence there is read with the runtime-versus-compile-time statement above.
- Builds on ADR 0062's pg-connector Tier-1 umbrella and Tier-2 backend architecture, which this
  decision extends with one standalone backend that lives outside pg-connector.
- Tracked under epic `pg2-5l0x4` (Direction 1: pg-desk attention evaluator and menu bar plugin);
  docs-first bead `pg2-5l0x4.1`.
