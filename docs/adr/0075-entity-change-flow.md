# Entity change flow: pg-connector, pg-desk, pg-router and deciders

**Status**: Accepted
**Date**: 2026-09-29
**Deciders**: phillipg

This ADR is the durable record of the decisions behind the entity change flow: how pg-connector,
pg-desk, pg-router and per-type deciders divide the work of noticing that an entity (a PR, an
issue, a thread) changed and of deciding what work should exist as a result. It transcribes the
twenty-four rows of the design's decision log, each identified by its S-row label, and groups them into five clusters: ownership split, pull-through, log and cursors, deciders, and
cutover. Later phases of the program (epic `pg2-2j5ac.52`) cite this ADR by number instead of the
design's decision log.

**Approval and provenance.** The operator (Phillip) approved the design and its implementation plan
on 2026-09-29 ("if good, consider it approved and continue", recorded on bead `pg2-2j5ac.51`). The
generic entity pipeline design that this flow governs where the two overlap (bead `pg2-2j5ac.46`)
was signed off the same day: bead `pg2-2j5ac.48` was closed as approved. The decisions are recorded
here rather than left in the design because, per this repo's citation conventions, the design files
under `docs/superpowers/specs/` are not durable citation targets. The design itself, dated
2026-09-29 and named `2026-09-29-entity-change-flow-design.md`, is provenance only. Each row below
carries who decided it and when: **Operator ruling** rows were confirmed by the operator in session;
**Derived** rows follow mechanically from an already-decided goal, invariant or fact; the **Fact
correction** row corrects a claim against verified code and is not a decision at all.

This ADR also records one operator ruling that is not a row of the design's decision log and is
marked as an addition where it appears (the source adapter's form, in the pull-through cluster).

## Ownership split

### Context

pg-desk is a caching and decorating console layer over pg-connector supporting many entity types
(PRs, issues, threads, later others). Before this decision the flow was PR-centric: pg-desk's
`sync` stage (`packages/pg-desk/internal/sync/`, live with `sync.mode = "apply"`) decided and wrote
beads inside the ingest process and kept a private `ledger` table of what it had minted. Decision,
I/O and a private copy of work state were entangled, so a decision could not be previewed per
entity or tested without the pipeline. The implicit contract between those bead writes and
pg-router's role prompts (title prefixes, labels, metadata keys) was the other symptom. The
2026-09-23 generic entity pipeline design (bead `pg2-2j5ac.46`) predates the pull-through and
decider split and overlaps this flow's triggering and store contract.

### Decision

This cluster records S1, S2, S3, S10, S23.

- **S1** — pg-desk is a caching/decorating layer over pg-connector, multi-entity from the start.
  Operator, in session, 2026-09-25, restated 2026-09-29. Why: it generalizes pg-desk beyond being
  pg-pr's replacement, so the flow is generic across entity types from day one, not as a later
  retrofit.
- **S2** — Decisions move out of pg-desk into deciders; deciders read through pg-desk. Operator, in
  session, 2026-09-28. Why: it enforces that pg-desk MUST NOT decide, and it makes a decision
  independently testable and previewable without running the ingest pipeline.
- **S3** — A dry-run `plan` lives on the decider CLI, not on pg-desk. Operator, in session,
  2026-09-28. Why: pg-desk has no rule knowledge to preview (S2); `plan` is a decider-owned view of
  its own rules.
- **S10** — Decider external writes go DIRECTLY to pg-connector, then `pg-desk <type> refresh <id>`.
  pg-desk is not a write-through repository for external systems; pg-desk-owned data (annotations,
  decider state) is still written to pg-desk. Operator ruling, 2026-09-29. Why: a write-through
  repository would make pg-desk re-implement every backend's write path that pg-connector already
  has; a direct write plus a refresh keeps pg-desk's contract (hold snapshots, decide nothing) and
  costs one extra call per decider write.
- **S23** — The entity change flow design governs bead `pg2-2j5ac.46` where they overlap: it owns
  triggering (pull-through `changes`), decisions (deciders) and the composite-view contract. `.46`
  keeps its generic gather/interpret core for issues (its D-G1 through D-G5 decisions, with D-G8
  scoped to targeted `refresh`); its PR adapter (D-G2) becomes this flow's PR hydration strategy;
  its `run issue` fallthrough (D-G6), `sync.KnownLedgerKinds` (D-G7) and implicit keep-`sync` are
  struck. Raw payloads MAY be stored, but the composite view serves typed `schema.*` snapshots.
  Operator ruling ("A: amend .46"), 2026-09-29. Why: `.46` predates the pull-through and decider
  split and assumed the existing `sync`/`run` triggering; most of its content is exactly the issue
  hydration this flow needs, so only its triggering and ledger parts conflict. The `.46`, `.48` and
  `.27` bead bodies were amended with this ruling the same day.

The resulting ownership, from the design's architecture overview: pg-connector owns talking to
external systems (summary-level `changes` with per-consumer cursors, targeted detail reads,
external writes) and MUST NOT hold decorations, cross-entity links or workflow rules. pg-desk owns
the watched set, hydration, snapshots, decorations, annotations, cross-references, per-type
classifiers, the change log with per-consumer cursors, and composite views; it MUST NOT decide what
work should exist and MUST NOT know pg-router's event format or which deciders exist. Deciders own
per-type rules, `plan`, `apply` and the work-item contract, and keep no cache of entity state.
Dependency direction is one-way: deciders depend on pg-desk and pg-connector; pg-desk depends on
pg-connector; pg-router reaches pg-desk only through the source adapter and reaches deciders only
by routing; pg-desk depends on neither pg-router nor deciders. The flow MUST NOT depend on pg-pr.

### Consequences

- A decision can be previewed per entity (`plan`) and tested without the ingest pipeline; pg-desk's
  contract stays narrow (hold and classify state, decide nothing).
- Every decider write to an external system costs one extra call (the refresh). A failed refresh is
  retryable staleness of pg-desk's snapshot, not a failed write, and a write is never rolled back.
- The 2026-09-09 design's D8 ("Beads are for agents. The interpreter writes to beads only to signal
  agent work, only through `pg-connector issue`, and never reads beads directly for the human
  views. A merge-request bead is minted lazily, as an anchor, only when agent work needs one") is
  amended in place, and its rewritten row points at this ADR. Its second clause is superseded: only
  deciders decide and write work items; pg-desk never does. Its other clauses survive: beads are for
  agents; work items are written only through pg-connector's issue verbs (now the deciders'
  writes) while pg-desk never reads beads directly for the human views; and the lazily minted
  `merge-request` anchor is kept.
- The generic entity pipeline design keeps its gather/interpret core for issues, is marked
  approved, and now names the entity change flow design as its governing document.

## Pull-through

### Context

pg-router's sources called `pg-connector <type> changes` and then told pg-desk to ingest
(`pg-desk run pr|issue|thread`), so change detection happened before the layer that holds the
decorations, annotations and cross-entity links. pg-connector's PR summary already carried
`head_sha`, `mergeable`, `merge_state_status` and `checks_rollup`, and `changes` hashes the whole
summary, so pushes, CI and conflict changes were already detected. It lacked `updated_at`, a stable
backend id and any review or comment signal, so new comments and reviews were missed.

### Decision

This cluster records S4, S5, S6, S7, S19, S22.

- **S4** — pg-router's timer polls pg-desk for changes; pg-desk returns one event per changed
  entity with its change kinds; events route to deciders. Operator, in session, 2026-09-29. Why: it
  keeps pg-router the sole scheduler while pg-desk stays the sole source of semantic change
  information.
- **S5** — pg-connector implementations produce most deltas; pg-desk adds changes they cannot see.
  Operator, in session, 2026-09-29. Why: it lets pg-connector detect most changes itself, without
  duplicating detection logic that pg-connector already gets cheaply from summary fields.
- **S6** — `pg-desk changes` pulls fresh data from pg-connector by default; pg-router stays the
  only clock. Operator, in session, 2026-09-29. Why: it avoids a second polling clock inside
  pg-desk while still letting an operator force a fresh read on demand with `--refresh`.
- **S7** — Events carry a reference and change kinds, not the full entity; deciders read the
  snapshot from pg-desk. Operator, in session, 2026-09-29. Why: it keeps the change feed small and
  single-sourced; the entity's current truth lives in exactly one place (the store), never
  duplicated into the event payload.
- **S19** — Add a stable backend id (`node_id`) to pg-connector's `schema.PR`; deciders key
  dedup/adoption matching on it when present. Derived, from a precedent already in the GitHub
  backend, 2026-09-29. Why: the GitHub backend already fetches `NodeID` for comments and reviews
  (`packages/pg-connector/cmd/pg-connector-pr-github/internal/github/github.go`); extending the same
  field to the PR object itself is a small, precedented addition, and a stable id survives a repo
  rename or transfer where `<repo>#<n>` does not.
- **S22** — pg-router binds match by exact string equality; there is no `pr.*` wildcard. Fact
  correction, verified against code, 2026-09-29. Why: `packages/pg-router/internal/orchestrator/listener.go`'s
  `Matches` does `b == evt.Type`, and `internal/config/config.go`'s orphan checks compare
  emitted/bound strings the same way, so a wildcard bind would silently match nothing. Every
  source's `emits` and every role's `binds` MUST list each `<type>.<kind>` explicitly.

**Addition beyond the design's decision log (not one of its rows): the Phase 7 adapter form.** The
source adapter that translates pg-desk's change envelope into pg-router items is a new binary,
`pg-router-source-pg-desk`, not a mode of `pg-router-source-pg-connector`. Ruled by the operator
(Phillip) on 2026-09-29 ("new binary is my decision", recorded on bead `pg2-2j5ac.51`). The
implementation plan had left mode versus binary to decomposition; this ruling closes that open
item. The adapter still MUST NOT add or drop records or decide anything.

### Consequences

- pg-router remains the only scheduler and its core stays generic (ADR 0065); pg-desk starts no
  clock of its own. Because binds match by exact string, the deployment configuration is more
  verbose (each `<type>.<kind>` listed), and pg-router's loader tests own the checks that reject a
  wildcard and an orphan producer or consumer.
- The 2026-09-09 design's D9 ("gather everything available, then interpret, then sync; the three
  stages run inside one process because pr-pool's core does not sequence handlers") is amended in
  place, and its rewritten row points at this ADR. Its gather-then-interpret part stays. Its sync
  stage is superseded by the pull-through change flow, with deciders run as pg-router roles; the
  rationale is rewritten to match, because sync is no longer a stage.
- The 2026-09-09 design's D14 (the query-side adapter between the change feed and pr-pool's item
  contract) is reshaped and not edited by this ADR: the adapter is now the new binary
  `pg-router-source-pg-desk`, fed by pg-desk's change envelope.
- `schema.PR` gains fields additively (including `node_id`), so a pg-connector change here is
  compatible with a consumer that has not yet learned the new fields.

## Log and cursors

### Context

pg-desk MUST hold the current snapshot of every watched entity and MUST produce a semantic change
log that covers connector-detected changes and locally originated ones (annotation writes,
cross-entity link changes, time-based conditions). Delivery from that log to consumers is
at-least-once and may duplicate, reorder or coalesce records, so a consumer that misses records, or
a rule change that needs entities re-evaluated, needs a bounded way to catch up.

### Decision

This cluster records S8, S12, S18.

- **S8** — The sweep MUST NOT replay everything; a duration-based approach replaces `--full`.
  Operator, in session, 2026-09-29. Why: it bounds the cost of catching missed events and rule
  changes without a full-replay storm on every poll.
- **S12** — Advance the cursor after flush plus an age-based sweep; no explicit ack. Derived, from
  the idempotent-decider goal and the design's duplicate-tolerant delivery semantics, 2026-09-29.
  Why: deciders are idempotent and consumers already tolerate duplicates and reordering; the sweep
  already bounds the loss window, no user story needs faster-than-sweep recovery, and the
  operator's `--reset` covers "I need this now". An ack protocol would buy correctness this design
  does not need.
- **S18** — The only day-one time-based change source is thread `resolved` (no reply within
  `watch.thread.active_window`); others are added only when a story needs one. Derived, from
  minimality, 2026-09-29. Why: no user story needs a different time-based signal yet, and
  `active_window` already exists for exactly this one.

### Consequences

- Recovery from a missed or lost record is by the sweep or by an operator `--reset`, never by a
  per-record acknowledgment; the cost is that a consumer sees a record more than once at times, and
  every consumer (deciders in particular) MUST be idempotent.
- The change log is an outbox: it records what changed and why, with an `origin`, and does not
  duplicate the entity's state. History before the cutover is not carried into it.
- Adding a second time-based change source later is an explicit, story-driven change, not
  something the day-one design leaves room to happen by accident.

## Deciders

### Context

Agent work (reviews, feedback triage, fixes) is triggered by changes to entities, and the decision
about what work should exist now lives in a per-type decider that runs as a pg-router command role
and reads pg-desk's composite view. The rules those deciders port from the old `sync` stage needed
settling against the ZR deployment set's journeys and invariants: which work kinds exist for which
PRs, what stops work, and which PRs a feedback cycle may touch.

### Decision

This cluster records S9, S13, S14, S15, S16, S17, S20, S21, S24.

- **S9** — There may be deciders for every entity type. Operator, in session, 2026-09-29. Why: it
  keeps the decider contract generic across entity types; day one still ships only PR rules (S20).
- **S13** — Own PRs (mine/co-owned) get BOTH `fix-ci` and `resolve-conflict` work kinds, labeled
  `worker-ready` so the worker role acts on them; `anchor.priority`'s conflict nudge stays.
  Operator ruling, 2026-09-29. Why: the ZR deployment set's `JOURNEY-ZR-7` requires the worker role
  to iterate until CI is green, but the worker only consumes `worker-ready` work items; with
  neither kind existing, nothing ever produced that work.
- **S14** — Ready-to-land is an annotation, never a work item. Derived, from the landing gate,
  2026-09-29. Why: the ZR deployment set's `INV-GOV-4` and `INV-GOV-5` forbid any agent-reachable
  merge path and require the operator's own, non-self-grantable permission to land. A work item is
  something a role can act on; making "ready to land" one would hand a worker role exactly the
  merge-adjacent job the gate exists to keep out of its reach.
- **S15** — `hide` stops ALL decider actions on that entity until unhidden; `wip` stays view-only.
  Operator ruling, 2026-09-29. Why: it differs from current behavior (the PR decider rules'
  precedence order, first step). An operator hiding something expects it to stop, not merely to stop
  appearing in `open`.
- **S16** — Team PRs get only an operator-pending review; `process-feedback` cycles are gated to
  mine/co-owned PRs. Derived, from `JOURNEY-ZR-8` plus the feedback consumer's own filter,
  2026-09-29. Why: `JOURNEY-ZR-8` says a teammate's PR is never modified beyond the draft review,
  but a feedback cycle leads to worker commits, which is an edit path that forbids. The
  feedback-cycle consumer also only ever reads `mine`-labelled cycles, so today's ungated team-PR
  cycles are already inert. This differs from current behavior (the PR decider rules'
  `feedback.digest-changed` row) and is a documented parity exception that the migration parity
  check MUST list.
- **S17** — Decider packaging is an implementer choice; RECOMMENDED: one binary with a rule
  registry selected by `<type>`. Derived, from avoiding N copies of shared plumbing, 2026-09-29.
  Why: the read, apply and audit plumbing is identical across types, so one binary writes it once,
  and a registry keyed by `<type>` still lets a type's rule set be added without touching another's.
- **S20** — No issue or thread decider ships on day one. Derived, from S9's "may," not "must",
  2026-09-29. Why: today's issue and thread roles only re-interpret linked PRs; S9 permits per-type
  deciders without requiring them, and no story yet motivates independent issue or thread rules.
  pg-desk still watches, hydrates and logs issues and threads regardless.
- **S21** — Keep the `merge-request` anchor. Derived, from the worker prompt's own resolution path
  plus the journeys' tracking-object requirement, 2026-09-29. Why: the worker prompt resolves a PR
  via its parent anchor's metadata, and the ZR deployment set's journeys need a claimable per-PR
  tracking object that pre-exists any specific work kind (`INV-TRACK-1`); dropping the anchor would
  push that identity data onto every child kind redundantly, with no single parent to hang from.
- **S24** — The anchor always mirrors the PR: if a person closes the anchor while the PR is open,
  `all.reopened` reopens it. To stop work on a PR, the operator uses `hide` (S15). Operator ruling
  ("C: always reopen"), 2026-09-29. Why: one suppression mechanism (`hide`) instead of a second,
  anchor-specific dismissal state; the anchor stays a faithful per-PR tracking object (S21).

### Consequences

- Three rulings change behavior relative to the old `sync` stage and MUST be listed by the
  migration parity check as documented exceptions: `hide` stopping all actions (S15), team-PR
  feedback cycles being gated off (S16), and the new `fix-ci` and `resolve-conflict` kinds (S13).
- The 2026-09-09 design's D8 clause that the interpreter writes beads to signal agent work is
  superseded by these decisions together with the ownership split cluster's decision that decisions
  move out of pg-desk: deciders are the only writers of work items. See that cluster for the full D8
  amendment.
- The 2026-09-09 design's D26 (daily-focus's focus sync step) is reshaped and not edited by this
  ADR: the focus step becomes a focus decider, tracked as bead `pg2-2j5ac.27`.
- Two operator design questions remain open in bead `pg2-2j5ac.52.1`: what records PR work-item
  links, and how a person-closed work item is told from an agent-closed one. This ADR records no
  answer to either and adds no rows for them. Follow-up task `pg2-2j5ac.52.2.2` (blocked by
  `pg2-2j5ac.52.1`) records the answers as further decisions, and amends this ADR, once the
  operator gives them.
- Because no issue or thread decider ships on day one, pg-desk's issue and thread coverage is
  watched, hydrated and logged but never acted on until an operator configures a query/role pair
  for a decider of that type.

## Cutover

### Context

pg-connector, pg-desk, pg-router and the deciders all ship through one home-manager apply. The
flow removes pg-desk's `sync` stage, `ledger`, `run` and heartbeat, replaces the `desk-*` ingest
roles and per-query `changes` sources with pull-through sources and decider roles, and migrates the
store schema. Serving two contract versions at once has no independent-rollout path that would
need it.

### Decision

This cluster records S11.

- **S11** — Breaking contract changes ship as one coordinated cutover, no dual-version serving:
  every component is deployed together by the same home-manager apply. Derived, from the deployment
  topology, 2026-09-29. Why: pg-connector, pg-desk, pg-router and the deciders all ship through one
  apply, so no independent-rollout path ever needs two contract versions served at once; the
  additive-change rule already in the design's contracts preamble is the only versioning policy
  this needs.

### Consequences

- The 2026-09-09 design's D17 (sync modes `off`, `plan`, `apply`) is reshaped and not edited by
  this ADR: it is removed at cutover, with `sync.mode` deleted along with pg-desk's `internal/sync`.
- The cutover is a stop-the-world maintenance window with no parallel running; rollback is to
  restore the store backup and reapply the previous release. Whatever pg-desk recorded after the
  cutover is lost on rollback, and pre-cutover `ledger` history is not carried into the new change
  log or audit trail (an accepted loss).
- New pg-desk commands refuse on an old-schema store, and an old binary refuses a newer schema, so
  an early deploy changes nothing the running system uses and a mismatched binary cannot half-run.

## Related Decisions

- Amends the 2026-09-09 pg-desk and connector discovery design in place: D8 (its second clause is
  superseded, the others survive) and D9 (its sync stage is superseded, its gather-then-interpret
  part stays). Each rewritten row points at this ADR. The same design's D7 and D10 still hold.
- Names, without editing them, three further 2026-09-09 rows that the entity change flow reshapes:
  D14 (the query-side adapter, now the new binary `pg-router-source-pg-desk`), D17 (sync modes,
  removed at cutover) and D26 (daily-focus's focus sync step, becoming a focus decider under bead
  `pg2-2j5ac.27`).
- Governs the generic entity pipeline design (bead `pg2-2j5ac.46`) where the two overlap, as
  recorded in the ownership split cluster.
- Builds on ADR 0065, which keeps pg-router's core generic (the core never names a concrete tool or
  entity rule), and on ADR 0062's pg-connector Tier-1 umbrella and Tier-2 backend architecture.
- Consistent with ADR 0064's pg-pr retirement: the flow does not depend on pg-pr, and the review
  role's `pg-pr review submit` call is one of the removals the flow makes.
- Tracked under program epic `pg2-2j5ac.52`, docket `pg2-2j5ac.52.2`; open design questions in bead
  `pg2-2j5ac.52.1`, with follow-up task `pg2-2j5ac.52.2.2`.
