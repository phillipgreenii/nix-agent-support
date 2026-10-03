# Entity change flow: pg-connector, pg-desk, pg-router and deciders

**Status**: Accepted
**Date**: 2026-09-29
**Deciders**: phillipg

This ADR is the durable record of the decisions behind the entity change flow: how pg-connector,
pg-desk, pg-router and per-type deciders divide the work of noticing that an entity (a PR, an
issue, a thread) changed and of deciding what work should exist as a result. It transcribes the
twenty-eight rows of the design's decision log, each identified by its S-row label, and groups them into five clusters: ownership split, pull-through, log and cursors, deciders, and
cutover. Later phases of the program (epic `pg2-2j5ac.52`) cite this ADR by number instead of the
design's decision log.

**Amended 2026-09-30** (task `pg2-2j5ac.52.2.2`): rows S25 through S28 were added. S25 and S26
answer the two design questions that were still open when this ADR was accepted (bead
`pg2-2j5ac.52.1`); S27 and S28 record two further operator rulings from 2026-09-29. Each is in
the cluster it belongs to, and the text it supersedes was rewritten in place.

**Amended 2026-10-03** (bead `pg2-kftf9.12`): the design gains section 9.1a, the read-only
`pg-connector pr review pending <id>` contract (a structured lookup of the acting identity's
pending review). No decision-log row is added: its placement in pg-connector rather than pg-pr is
the operator's ruling of 2026-10-03 ("no further changes are to be made to pg-pr, it is going
away"), and the contract itself is derived from the pending-review investigation and its
prerequisite results (`docs/superpowers/specs/2026-09-29-pending-review-handling-investigation.md`,
`docs/superpowers/specs/2026-09-30-pending-review-prerequisites-results.md`). It changes none of
S1 to S28; like `review submit`, it follows `INV-EXIT-1`'s Targeted scheme (S27).

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

This cluster records S1, S2, S3, S10, S23, S25.

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
- **S25** — Links (answering the open question Q-A, what records the PR-to-work-item link) are
  BOTH derived and externally managed. Derived: "if we can extract a reference from one entity, we
  should link it", for ANY entity type (a thread can link to PRs, Jira issues, commits, branches or
  other threads and messages); each type's link extraction lives in pg-desk, and not every
  extractor ships now, but the design MUST NOT be limited to PR-to-issue links. External: links
  added and removed from outside pg-desk, by an operator or a decider, with
  `pg-desk <type> link add <id> <type>:<id> [--relation R] [--reason TEXT]` and
  `pg-desk <type> link remove <id> <type>:<id>`. Every link records its origin:
  `derived:<extractor>`, or `external:<actor>` with when and an optional reason. External MUST NOT
  override internal; external MAY add links and MAY remove links previously added externally.
  `remove` on a link that exists only as derived errors ("derived from `<extractor>`; change the
  source entity"); `add` on an already-derived link records the external claim too, so the link
  survives if the source later changes. Derived links are rebuilt on every hydration of the entity
  and external links are untouched by that; every `add` or `remove` appends a `link_changed`
  record for both entities, and `show` lists each link on both entities with its origin.
  Suppressing a derived link is not allowed for now (a separate suppress/unsuppress pair requiring
  an explanation MAY come later). The minimum extractor set ships with the hydration phase:
  work-item links from each work item's own work-item-contract fields (`repo` and `pr_number`; a
  child's parent anchor), plus the existing Jira-key and thread scans rebuilt as extractors. A link
  is derived whenever an entity's own data names the other entity; external is for everything
  else. Operator ruling (Phillip), 2026-09-29, in the DECISION comment on bead `pg2-2j5ac.52.1`;
  the first-extractor set and the derived-versus-external rule were the session's default, not
  objected to. Why: the composite view's links, the `work_changed` change source and every PR rule
  read linked work, and the old bead-to-PR mapping lived in the `ledger` table this flow drops.
  Deriving links from data the entities already carry needs no extra writer, and external links
  cover what no entity's data names. Recognizing work items only from work-item-contract metadata
  fields and parent links keeps work-kind and `dedup_key` literals out of pg-desk, so pg-desk still
  decides nothing. Beads stay as designed; routing work items directly to roles is a separate,
  larger discussion (bead `pg2-v4fot`).

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
- pg-desk's `xref` table gains `origin`, `relation`, `actor`, `acted_at` and `reason` at the
  cutover, keyed by link, relation and origin, so one link can carry a derived and an external
  claim at once; every pre-cutover row becomes origin `derived:legacy`, relation `references`
  (S25). A false-positive derived link cannot be hidden today; the fix is to change the source
  entity, or later a dedicated suppress pair.

## Pull-through

### Context

pg-router's sources called `pg-connector <type> changes` and then told pg-desk to ingest
(`pg-desk run pr|issue|thread`), so change detection happened before the layer that holds the
decorations, annotations and cross-entity links. pg-connector's `schema.PR` had `head_sha`,
`mergeable`, `merge_state_status` and `checks_rollup`, and `changes` hashes the whole summary. The
list query filled `head_sha` and `checks_rollup`, so pushes and CI changes were already detected,
but it never selected `mergeable` or `merge_state_status` (the fact S28 corrects), so a conflict
change was not. The summary lacked `updated_at`, a stable backend id and any review or comment
signal, so new comments and reviews were missed. The exit-code schemes of pg-connector, pg-desk and
pg-router also differ: pg-connector's behavior invariant `INV-EXIT-1` gives a targeted op 0/4/1,
and pg-router's command-query runner discards a source's whole output on any non-zero exit.

### Decision

This cluster records S4, S5, S6, S7, S19, S22, S27, S28.

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
- **S27** — Each tool keeps its own exit-code scheme; the schemes are not bound together, and an
  adapter translates between what it calls and who calls it. `pg-connector pr review submit`
  follows pg-connector's own `INV-EXIT-1` Targeted scheme, unchanged: 0 = completed, including a
  review that posted when the `supersede_pending` delete failed, which the JSON output reports;
  4 = `not_found`; 1 = any other error. The source adapter translates pg-desk's codes into
  pg-router's command-query contract instead of mirroring them: pg-desk 0 or 2 becomes exit 0,
  emitting every record, with `metadata.degraded_sources` carrying the degraded detail; pg-desk 3
  becomes exit 1. Operator ruling (Phillip), 2026-09-29, on escalation bead `pg2-2j5ac.52.24`. Why:
  "the adaptor should do whatever is expected based on who called it and what it calls ... we
  shouldn't expect exit code specs to be exactly the same for pg-router, pg-connector, pg-desk. If
  there is an obvious improvement between them, we can consider it, but we shouldn't bind them
  together." The design's earlier review-submit scheme (0/2/3) conflicted with `INV-EXIT-1`, which
  also forbids `not_found` sharing a code with a real failure; and because pg-router discards a
  source's output on any non-zero exit, an adapter mirroring pg-desk's exit 2 (records written,
  cursor advanced) would lose records.
- **S28** — The GitHub backend's list query fills `mergeable`, carried verbatim (including
  `UNKNOWN`); `merge_state_status` stays show-only. Operator ruling (Phillip), 2026-09-29, option 1
  on the finding recorded on bead `pg2-2j5ac.52.6`, correcting a fact verified against code. Why:
  the design had claimed both fields were already in the summary, but the list query selected
  neither, so a PR turning conflicting surfaced only through another summary change or the age
  sweep, not as a prompt `mergeability_changed`. `merge_state_status` moves with every CI status
  change and base-branch push, so in the summary it would flood `changes`. GitHub computes
  mergeability lazily; an `UNKNOWN` settling costs one `changed` (one re-hydration, no work), which
  the operator accepted.

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
  compatible with a consumer that has not yet learned the new fields. Adding `mergeable` to the
  list results changes every PR's summary hash once, so the first `changes` run after it ships
  reports every already-tracked PR as changed once (S28). `base_sha` (the base commit that
  `resolve-conflict`'s context needs, S26) and `merge_state_status` stay show-only, because each
  moves with events outside the PR itself.
- Every tool's exit codes are read in its own terms: a caller that needs another tool's outcome
  translates it at the seam (S27), so pg-connector's `INV-EXIT-1` and its targeted-op
  classification stay as they are.

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

This cluster records S9, S13, S14, S15, S16, S17, S20, S21, S24, S26.

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
- **S26** — No decider rule may depend on who closed a work item (answering the open question
  Q-B, how a person-closed work item is told from an agent-closed one: it is not). A `review-pr`,
  `fix-ci`, `resolve-conflict` or `process-feedback` work item is not recreated for the same
  context, however it was closed. The context per kind: `review-pr`, the head commit, with
  `force-review` still the explicit override; `process-feedback`, the feedback, where more
  feedback means a new unaddressed comment not covered by an earlier cycle and a changed digest
  alone is not enough; `resolve-conflict`, the tuple (head branch, head commit, base branch, base
  commit), never recreated or reopened for the same tuple, a different tuple being a different
  conflict; `fix-ci`, one item per head commit carrying the list of failing build ids (a build id
  is the CI run id plus its attempt number), where a new failing build id on the same commit
  reopens the item if it was closed and adds the id, CI green closes it, and a new commit gets a
  new item. Operator ruling (Phillip), 2026-09-29, in the DECISION comment on bead
  `pg2-2j5ac.52.1`. Why: "if we can't consistently track who closed a bead, then we can't have any
  rule which requires it." bd records a closer only in its Dolt events table, which no bd CLI verb
  exposes, and even there only as reliably as actor discipline allows (the default actor is the
  human's own name). "a review-pr, fix-ci, resolve-conflict, process-feedback should not be
  recreated for the same context."

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
- S26 removes every closer-dependent mechanism from the design: the person-dismissed precedence
  step of the PR decider rules, the person-dismiss operator control and the composite view's
  per-link `closed_by` are dropped, and the `plan` skip reason `person-dismissed` becomes
  `already handled` (a work item already exists for the current context). Closing a work item,
  by anyone, simply marks that context handled.
- S26's contexts need two connector fields: `schema.PR.BaseSHA` for `resolve-conflict` (bead
  `pg2-2j5ac.52.6.2`, show path only) and `schema.CIRun.Attempt` for `fix-ci` (bead
  `pg2-2j5ac.52.6.3`), because GitHub keeps a run's id when it is re-run. Each work kind's dedup
  key names its context, so `resolve-conflict` never reopens a closed item, while a
  `process-feedback` cycle created for new feedback is a new item rather than a reopened one.
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
- Tracked under program epic `pg2-2j5ac.52`, docket `pg2-2j5ac.52.2`. The design questions Q-A and
  Q-B were answered in bead `pg2-2j5ac.52.1` (S25, S26), the exit-code ruling was made on
  escalation bead `pg2-2j5ac.52.24` (S27) and the mergeability ruling on the `pg2-2j5ac.52.6`
  finding (S28); task `pg2-2j5ac.52.2.2` recorded all four here.
