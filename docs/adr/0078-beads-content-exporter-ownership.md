# Bead content metrics are owned by a dedicated exporter

**Status**: Accepted
**Date**: 2026-10-05
**Deciders**: phillipg

## Context

Operators want to see the shape of their bead trackers over time: how many beads are in each
state, how long the oldest claimable bead has waited, how many beads each work queue offers, and
how much work is created and closed per day. Several existing components already touch beads, so
the question is which one owns publishing that content as metrics.

The data has demanding requirements:

- **State needs the dependency graph.** Whether a bead is ready, blocked or merely held by a
  deferred parent is decided by bd, not by the bead's own fields. The categorisation also needs
  the deferral time (`defer_until`), the assignee, the start time and the template marker.
- **Counts must be complete.** bd's list and ready commands silently truncate at 50 and 100 rows
  unless told otherwise, and the default list view hides closed beads, gates, ephemeral wisps and
  templates. A metric that is quietly capped is worse than none.
- **Collection must be safe next to live sessions.** Any bd session that finds an
  `issues.jsonl` beside the store can import it and overwrite newer rows, so a monitor must never
  provoke that, and must never write.
- **A scrape must never wait on a database.** The embedded store takes a lock; a scrape that
  blocked on collection would make the monitor itself a source of stalls.

## Decision

A new package, `beads-exporter`, owns bead content metrics. It is a periodic snapshot publisher:

1. A background poller reads each configured database through a pinned, read-only bd (every call
   passes `--readonly --sandbox`, runs by absolute path, and gets a fully re-asserted child
   environment), builds an immutable per-database snapshot and swaps it in atomically. A scrape
   serves the last snapshot and never blocks on collection.
2. Exposition is hand-rolled Prometheus text with a zero-dependency module.
3. Each collection pass keeps its own last-success timestamp, error counter (closed-enum reason,
   never error text) and series; a failing pass drops only its own series for only its database.
4. Queue membership is computed from one shared `bd ready` result and the queue definitions
   described in ADR 0079, so the number of bd spawns per cycle does not grow with the number of
   queues.
5. A contract suite (build tag `contract`) pins the bd behaviour the exporter relies on against a
   real bd, and a version pin plus a recorded flag list fail a build when a different bd package
   lacks a flag or reports a different version.

The exporter does not decide what a queue contains, does not write to any tracker, and does not
detect stranded claims (a separate pass added later).

## Consequences

### Positive

- One component is accountable for the accuracy of bead counts, and its limits (truncation,
  default views, state skew between four non-atomic bd calls) are documented in one place.
- The stale-`issues.jsonl` guard and the read-only flags make the monitor incapable of the
  clobber hazard.
- A bd upgrade cannot silently change the numbers: the pin and the contract suite fail first.

### Negative

- A new daemon and package to maintain, and a recorded bd version that must be re-pinned (by
  re-running the contract suite with `-update`) on every bd bump.
- The four bd calls of a cycle are not one snapshot, so a bead claimed mid-cycle can sit in the
  wrong state for one cycle. This is accepted and documented.

## Alternatives Considered

### One combined exporter for beads content and repository metrics

Rejected. The two data sources have different failure modes, cadences and permissions (a tracker
store with a lock versus read-only repository walks). Sharing one process would let a hang in one
withhold the other's series and would make a version bump of either a risk to both.

### A Grafana datasource reading the tracker's database directly

Rejected. It would bypass bd and reimplement its ready and blocked semantics in SQL, would couple
dashboards to the store's schema, and would need a live database server where the supported
configuration is an embedded store with no server at all.

### Collecting through `pg-connector`

Rejected. Its issue schema drops `defer_until`, dependency blocking, assignee, start time and the
template marker, and it has no aggregate operation, so the states and queues could not be derived
from it without widening it into a different tool.

### Computing the counts in `pg-router`

Rejected. A core `bd count` would add tracker-specific code to a generic event router, cannot
express the derived states or the queues, and ties metric availability to the router's own health
(`pg-router` is one of the things the numbers are meant to watch).

### `pa-monitor`

Rejected. It models agent sessions, not the tracker's content, and has no bd dependency.

### One bd spawn per queue

Rejected. Every queue the operator adds would add a spawn per cycle per database, so cost would
scale with configuration. Queues made only of label and type filters are instead computed
in-process from the single shared ready result, and only a queue that needs a different bd filter
pays for its own spawn.

## Related Decisions

- [0079](0079-queue-definitions-mirror-contract.md): how queue definitions reach the exporter.
- [0017](0017-static-nix-built-local-plugin-marketplace.md): why the queue mirror is checked for
  drift rather than generated.
