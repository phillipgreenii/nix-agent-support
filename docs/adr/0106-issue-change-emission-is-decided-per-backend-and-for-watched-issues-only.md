# Issue change emission is decided per backend and for watched issues only

**Status**: Proposed (amends [0090](0090-a-connector-may-be-stateful-and-run-a-daemon-and-own-its-changes.md) and [0077](0077-entity-change-flow.md); becomes Accepted when the operator approves the issue change emission design as a whole)
**Date**: 2026-10-22
**Deciders**: phillipg

This ADR is filed before any implementation starts, so that the behavior docs and the later phases of
program epic `pg2-2qyyh` can cite it by number. It records the operator rulings of 2026-10-20 to
2026-10-22 on how a change to an `issue` entity (a bead or a Jira issue) becomes an `issue.changed`
event, and it amends ADR 0090 (a connector MAY be stateful, run a daemon and own its changes) and
ADR 0077 (the entity change flow; rows S29, S31 and S32 of its "Amendment 2026-10-05"). The design it
records is the issue change emission design, whose origin is bead `pg2-oa1h4`. That design is
provenance only, because per this repo's citation conventions the files under
`docs/superpowers/specs/` are not durable citation targets. The key words MUST, MUST NOT, REQUIRED,
SHALL, SHALL NOT, SHOULD, SHOULD NOT, RECOMMENDED, MAY and OPTIONAL in this ADR are to be interpreted
as described in RFC 2119.

**Status of the approval.** Bead `pg2-oa1h4` records ten operator rulings on single questions of the
design (quoted below). It records NO comment in which the operator approves the design as a whole,
and a ruling on one question is not that approval. The ADR is therefore `Proposed`, as ADR 0090 is.
It becomes `Accepted` when the operator's approval of the whole design is recorded on `pg2-oa1h4`.

## Context

The `issue` type has two implementations: a local beads backend, whose reads are one-shot `bd`
processes over local data, and a remote Jira backend, whose reads are slow and rate limited. ADR 0077
(S29, S31 and S32) put change detection for every type in pg-desk: a list-and-diff by fingerprint, a
remote sweep tier that re-hydrates by age, a local sweep tier that re-emits a `reconcile` record for
every active entity older than `reconcile_age`, and a per-type fast-check cadence. ADR 0090 then lifted
the earlier restrictions for one backend, the daemon-backed GitHub backend: that backend MAY run a
daemon, own its store and decide what changed for its own types.

Applied to issues, the S31 tiers flooded the decider. Measured on 2026-10-22 from pg-desk's queue,
5942 pending `issue.changed` events existed for only 478 distinct entities (about 12 per entity), of
which 4100 were local-reconcile events, 1773 were sweep events and only 69 were real changes, and every
sampled issue-decider dispatch returned no actions. That is the first operator ruling below observed
live: events are emitted for things that did not change.

The operator rulings that answer it follow the pattern already built for PRs: the connector side
manages the entities it watches and emits only when something differs, and the roles downstream, which
are expensive, see only that smaller stream.

## Decision

1. **Ownership of change detection is applied per backend, not per type, for the `issue` type.** ADR
   0090 already says that a backend that declares `owns_changes` decides what changed for its own types
   and every other backend stays as it is. The `issue` type has two implementations, so `owns_changes`
   is read per backend instance for a type with more than one implementation: a daemon-backed Jira
   backend that owns its changes, and a local beads backend that does not. The daemon-backed backend is
   no longer assumed to be the GitHub one. Everything else in ADR 0090 stands.

   | Backend       | Process model                                       | Who detects a change and where the watched flag lives                                                                                 | Sweeps                                                                               |
   | ------------- | --------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
   | Beads (local) | One-shot `bd` processes; no store, no daemon        | pg-desk, by fingerprint list-and-diff (ADR 0077 S29) over watched queries and watched entities; the flag lives in pg-desk's own store | The old tiers until the implementation retires them for `issue`, none after          |
   | Jira (remote) | After the daemon lands: a daemon with its own store | The daemon (`owns_changes`), which stores the flag; pg-desk consumes the feed, hydrates, and acknowledges                             | None (the old tiers skip an entity of an owning backend once the daemon is in force) |
   | Jira (today)  | One-shot `pjira` processes                          | pg-desk, as for beads                                                                                                                 | The old tiers until the registry switches to the daemon                              |

   pg-desk therefore handles a MIX of backends inside its one `issue` flow, and the router source, the
   roles and the decider are unchanged: pg-desk stays the single emitter of `issue.changed`.

2. **Beads needs no daemon.** A `bd` read is local, so its data is always current and nothing needs
   refreshing in the background; the fingerprint pg-desk computes covers every field the list
   returns. What a daemon would add (batching, quota protection, revalidation, a store) has nothing to
   act on. A beads daemon is an option, not a requirement: if one is adopted for consistency it is a
   separate binary instance with its own store and config, sharing only the library. Until a later bead
   (`pg2-f7155`) moves detection into the beads connector, which then deletes the watched-query
   configuration and pg-desk's beads list-and-diff as the PR design does for PRs, pg-desk keeps
   detecting beads changes (ruling 8) and keeps the watched flag in its own store.

3. **S29 is narrowed** to the backends that do not own changes, and, for `issue`, to watched queries
   and watched entities only. The fingerprint list-and-diff, the stored `list_fp` baseline and the
   rule that a failed hydration writes nothing are unchanged for those. A watched entity that leaves a
   watched query is not removed: it stays watched and is read by id until it closes.

4. **S31's two sweep tiers are retired for `issue`, with no replacement.** The remote tier (re-hydrate
   by age, always appending a `reconcile` row) and the local tier (re-emit every active entity older
   than `reconcile_age`) no longer apply: nothing re-emits an entity that did not change, and no
   `reconcile` kind or `sweep` origin exists for `issue`. S31's clause that a failed decider run is
   recovered by the local tier is retired for `issue` too. `pr` and `thread` keep S31 as it stands.

   | Today (ADR 0077 S29, S31)                                          | After, for `issue`                                                                                                                                                                           |
   | ------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
   | List-and-diff by fingerprint against the stored `list_fp`          | Kept, over the queries marked watched and over the watched entities only                                                                                                                     |
   | Remote tier: re-hydrate by age and always append a `reconcile` row | **Removed.** No age sweep and no `reconcile` kind or `sweep` origin for `issue`                                                                                                              |
   | Local tier: every active entity older than `reconcile_age`         | **Removed.** Nothing re-emits an entity that did not change                                                                                                                                  |
   | First observation of an entity yields only `reconcile`             | An entity first seen in a watched query after that query's baseline, or becoming watched while known, appends one first-observation row (kind `added`); the baseline run itself appends none |
   | A hydration whose classifier yields no kind appends no row         | Appends a `fields_changed` content row                                                                                                                                                       |
   | Removal when an entity leaves every watched query                  | Leaving a watched query is not removal: a watched entity stays watched and is read by id until it closes                                                                                     |

   For `issue` the change classifier returns `added`, not `reconcile`, for a first observation, a
   return after deactivation, a late watch and a forced hydration; `pr` and `thread` keep `reconcile`.

5. **S32's per-type fast-check cadence is read per backend.** The design fixes only this: the poll
   stays the router's 5-minute tick; a beads-backed `issue` list-and-diffs the watched queries and
   watched entities on it; and a Jira-backed `issue` consumes the daemon's change feed on it once the
   daemon lands, instead of a cursor-less list. This ADR does NOT change any cadence number of S32:
   `pr` 60 seconds; `issue` backed by beads 5 minutes now and 60 seconds once local list latency is
   measured under 10 seconds; `issue` backed by Jira 5 minutes; `thread` excluded.

6. **A failed decider run waits for the next change of that issue, as for PRs.** No
   decider-acknowledgement protocol is built (ruling 7a): there is no blanket sweep and no reconcile
   event, so the recovery question is moot. The router retries the dispatch first (its
   `--retry-window` flag and the per-role opt-in), and after that an issue with no further change is
   not retried. Nothing re-emits an issue on a timer either, including a focus-annotated one: the
   30-minute focus re-emission is removed because the focus annotation it keyed on is dropped by
   `pg2-gudqw`. Revisit only if problems appear; the revisit evidence is an open issue whose decider
   run failed and which has not changed for a day, which the outcome alert makes visible. This resolves
   the open item of ADR 0090 that asked how recovery of a failed decider run is provided for a type
   whose backend owns its changes.

7. **The watched-entity model, delivery gated on `watched`.** The connector side emits change events
   only for watched issues: those bound to a query the deployment marks as watched, plus a specific
   entity watched as a one-off. Which queries are watched is declared by the deployment, not by this
   ADR. pg-desk MUST NOT append or deliver a change row for an unwatched issue entity, whatever the
   origin (a link or work propagation from a PR change, `pg-desk issue refresh`, an annotation write).
   Tracked and watched entities follow the PR design's tracked-versus-watched rule, not the older
   keep-alive rule. The generic feed, change engine and shared `watched` query shape are hoisted into
   the shared connector-daemon library first, and kept generic (ruling 9).

## The operator rulings

The rulings are recorded on bead `pg2-oa1h4`, numbered 1 to 10. Each passage in quotation marks is a
continuous passage of the operator's text, reproduced exactly, except that line wrapping and doubled
spaces after a full stop are normalised. Each ruling supersedes the earlier draft text it names. The
words "higher priority" in ruling 3 are a build order only.

Ruled 2026-10-20.

1. **Do what was done for PRs.** Verbatim:

   > "we should not be overloading the system with messages to expensive roles if nothing is different. it sounds like we need to do what was done for PRs. the connector implementation can manage the entities and only emit when things are different."

2. **The store and the daemon are per implementation.** Verbatim:

   > "the store and daemon should be per implementation. ie, beads doesn't need a store or daemon because it runs locally (i'm open to the idea of store and daemon for consistency, but it would be separte from jira's if it is helpful). only jira would need the local store and daemon. similar to PRs, we will continue to pull any retrieved jira issues (or bead, we do daemon/store for it) until it is closed + 24hours. it can fall off after being closed for 7 days. we need to check if jira has s similar quota metric available so we don't overload it. beads doesn't, but we can decide on one to track internally to keep beads from being overloaded."

   This supersedes the earlier direction of a daemon for every issue backend; it does not supersede
   ruling 1.

Ruled 2026-10-21.

3. **Jira daemon sequencing.** Verbatim:

   > "jira daemon is necessary and higher priority than beads."

   It supersedes the earlier order (the beads path first, the Jira daemon second, with a go or no-go on
   whether it earns its weight): the daemon is committed and goes first.

4. **Bead bodies.** The operator chose the option:

   > "Amend .44.37 only; fix design section 8 for .52.22"

   `pg2-2j5ac.44.37` is to be amended (its text about the retired sweep tiers and its gate); the
   operator's option names the design's own rollout section, which is not a citation target here.
   `pg2-2j5ac.52.22` is already closed and is not amended, and the design's sentence that it "is
   re-scoped" cites that closure.

5. **24-hour interim relief.** Not filed. Verbatim:

   > "the fact that the backlog signals to me a larger problem. there aren't that many jira issues or beads that i think it should be needing to pull in. i don't think the backlog is properly deduping. can you check that? that isn't directly related to this planned work, but that every growing backlog is daunting."

   The check on deduplication is a task of the design's first section; the relief itself is moot under
   ruling 7.

6. **Router selective-drop bead.** Not filed for now. Verbatim:

   > "lets wait on the selective drop for now. see my previous comment about proper dedup"

Ruled 2026-10-22 (rulings 9 and 10 after the independent reviews of the tenth draft).

7. **The watched-entity model.** Issue change emission follows the PR pattern for BOTH beads and Jira
   issues:

   > "the implementation of beads and jira should be similar to how PR will work"

   Verbatim fragments:

   > "the basic 'watched' situation is bound to a query configured in the connector for which were marked as watched"

   > "there were one-off where a specific entity could be watched as well"

   and the connector emits change events only for watched issues, so:

   > "the level of events [will be] much smaller so that the decision role should not be over whelmed"

   (the bracketed words are the bead's own). Which queries are watched is declared by the deployment,
   not by the design. The bead's consequences, each superseding the draft text it names (this list is
   the bead's summary, not a quotation):

   a. No decider-acknowledgement protocol: the decision table, the `pg-desk issue ack` and `redrive`
   commands, the retry tier, the decider view change and the invariant that required them are
   dropped; a failed decider run waits for the next change of that issue, as for PRs; revisit only
   if problems appear.
   b. The 30-minute focus re-emission is removed: the focus annotation it keys on is dropped by
   `pg2-gudqw`, and the watched set is small (its size is unmeasured and is a spike output).
   c. The beads silent 24-hour sweep of every active issue is replaced by watched-only emission; no
   blanket tier remains.
   d. Build order only, the Jira implementation first. Verbatim:

   > "pulling the jira information can be slow and difficult, where as beads are local so having additional support/daemons aren't needed for beads to work well"

   Ruling 3 is unchanged.
   e. The Jira daemon's tracking rule follows the PR design's tracked-versus-watched amendment, not
   the older keep-alive rule.
   f. Ruling 2 still holds: beads needs no daemon or store of its own; where its watched flag lives is
   defined by ruling 8.

8. **Who detects beads changes.** The operator was asked "A: pg-desk detects, or B: the beads
   connector owns detection with a small store". Verbatim:

   > "A is fine until the beads connector handles it"

   pg-desk keeps detecting beads changes (list-and-diff, ADR 0077 S29) over the deployment's watched
   queries and keeps the watched flag in its own store; a LATER bead, `pg2-f7155`, moves detection into
   the beads connector, which deletes the watched-query configuration and pg-desk's beads
   list-and-diff, as the PR design does for PRs.

9. **The shared library is hoisted first and kept generic.** The reviews found that the GitHub daemon
   library holds only the store, governor, scheduler and socket; the change feed, change engine and
   query runner are GitHub-specific code. Verbatim (typos as in the bead):

   > "1) yes, hsared library si good, make sure the shared is generic enough and doesn't contain specifices of either usage."

   So the generic feed, change engine and shared `watched` query shape are hoisted into the shared
   connector-daemon library by bead `pg2-qbu5b`, with no PR, CI, issue, Jira or beads specifics in the
   shared code, and the Jira daemon and the PR design's second amendment wait for it.

10. **Delivery of `issue.changed` is gated on `watched`.** The reviews found that unwatched issue
    entities still reach the decider through pg-desk-local paths (link and work propagation from a PR
    change, `pg-desk issue refresh`, annotation writes). Verbatim:

    > "2) yes"

    (gate delivery of `issue.changed` on `watched`, as recommended). pg-desk MUST NOT append or deliver
    a change row for an unwatched issue entity, whatever the origin.

## Consequences

- Behavior docs MUST be edited first, in later packets of the same phase, and cite this ADR by number:
  the pg-desk changes, consumer, store schema, configuration and operator-command docs, the connector
  interface doc and the router-source adapter doc, and a behavior-docs set for the Jira daemon. No
  code changes until they land.
- ADR 0077 and ADR 0090 each gain an amendment pointer to this ADR, in the convention ADR 0081 and ADR
  0087 already use when they amend ADR 0077. No decision-log row of ADR 0077 is removed or renumbered:
  S29, S31 and S32 are read with this ADR for `issue`.
- ADR 0090's open item about recovery of a failed decider run (S31's local tier) is resolved here: a
  failed run waits for the next change, as for PRs.
- The retirement of the sweep tiers and the classifier change land with the later implementation;
  until then the deployed tiers keep running, and this ADR states the end state.
- pg-desk handles a mix of backends in one `issue` flow, so its tests MUST cover both an owning and a
  non-owning backend of the same type.
- Open for later beads, not decided here: the beads connector owning detection (`pg2-f7155`), the
  shared connector-daemon library hoist (`pg2-qbu5b`), the amendment of the checkpoint bead
  `pg2-2j5ac.44.37`, and any router-side selective drop (ruling 6).
- While the approval of the whole design is not recorded, this ADR is `Proposed` and no implementation
  that depends on it starts before then.

## Related Decisions

- Amends ADR 0090 (a connector MAY be stateful and run a daemon): `owns_changes` is read per backend
  instance for a type with more than one implementation, and the daemon-backed backend is no longer
  assumed to be the GitHub one; everything else stands. Resolves its open item on a failed decider run.
- Amends ADR 0077 (the entity change flow): rows S29 (narrowed to backends that do not own changes and
  to watched queries and watched entities), S31 (both tiers and the failed-run recovery clause retired
  for `issue`) and S32 (cadence read per backend, no number changed).
- Consistent with ADR 0081 and ADR 0087, which build on pg-desk's change flow and do not depend on who
  detects a change.
- Tracked under program epic `pg2-2qyyh`, phase docket `pg2-2qyyh.1`; origin bead `pg2-oa1h4`.
