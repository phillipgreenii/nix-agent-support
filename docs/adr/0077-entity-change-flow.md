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

**Amended 2026-10-03** (bead `pg2-kftf9.13`): section 9.1 makes `supersede_pending` conditional (the
guarded supersede: a stale pending review is replaced only when the bot marker is on the body and
every comment and a content digest carried in the marker verifies; otherwise it is left untouched
and `blocked_human_pending` is reported), adds the output fields `status`, `reason`, `message`,
`pending_review` and `superseded`, adds `url` and `digest_state` to 9.1a's record, and settles two
points the bead left open: the archive is a backend-owned file under the state home
(`PG_CONNECTOR_PR_GITHUB_ARCHIVE_DIR` or `$XDG_STATE_HOME/pg-connector-pr-github/archive`), and
every status, `blocked_human_pending` included, exits 0 under `INV-EXIT-1`. Placement in
pg-connector rather than pg-pr is the operator's ruling of 2026-10-03 ("no further changes are to
be made to pg-pr, it is going away"), and the operator approved the digest-in-marker approach and
folding the post-time hash sidecar bead (`pg2-kftf9.14`) into this one (2026-10-03, "continue"). No
decision-log row is added; it changes none of S1 to S28 beyond the in-place clarification of S27's
example.

**Amended 2026-10-03** (bead `pg2-kftf9.15`): the escalation that section 9.1 left to this bead
(pending-review policy 5) is hosted by a pg-router integration, the leaf command binary
`pg-router-review-escalator`, and not by a pg-decider rule. The reasoning is recorded in the
Deciders cluster's Consequences. No decision-log row is added: the host choice is derived from the
bead's own framing (a decider rule or a router integration), from G5 and from the current state of
the code, and it changes none of S1 to S28. The operator's rulings it builds on are unchanged:
unremovable stale pending reviews MUST escalate quickly (2026-09-29) and "no further changes are to
be made to pg-pr" (2026-10-03).

**Amended 2026-10-06** (bead `pg2-8qui6`): the guarded supersede and the escalator host recorded in
the two amendments of 2026-10-03 above are SUPERSEDED by create-or-append (operator rulings, Phillip,
2026-10-05 and 2026-10-06; design `docs/superpowers/specs/2026-10-06-pending-review-reuse-design.md`).
`review submit` reuses the acting identity's pending review, or creates one, and never deletes
or replaces; its statuses are exactly `posted`, `append` and `no_change`, and `skipped`, `replaced` and
`blocked_human_pending` are retired. With them go the archive-before-delete step, the content-digest
guard (`digest_state`), the escalation, `pg-router-review-escalator` and the escalation filter. The
`pr review pending` record (9.1a) gains per-head comment counts and loses `digest_state`, and pg-desk's
`stale` becomes "nothing is anchored to the current head". It changes none of S1 to S28 beyond the
in-place clarification of S27's example. The superseded text is kept below, marked, because the code
it describes stays deployed until the implementation lands.

**Amendment 2026-10-05** (design bead `pg2-ii38x`, operator rulings recorded on decision bead
`pg2-32wg6`, in force): rows S29 through S35, in the section "Amendment 2026-10-05" before Related
Decisions, change how the pull-through detects a change (a pg-desk-owned baseline written with the
hydrated snapshot), how the age sweep is tiered, which entity types get a fast check and at what
cadence, the event shape the adapter emits, and which fields the cheap list may carry. They amend S5,
S6, S8 and S12 in place; each rewritten row says so and keeps the earlier wording's gist. The design
is `docs/superpowers/specs/2026-10-05-fast-per-type-change-check-design.md`.

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
  (Rewritten in place 2026-10-05, S29, bead `pg2-32wg6`: pg-connector still detects most deltas, but
  through its summary fingerprint on `list`, not through its `changes` ledger; the baseline that
  fingerprint is compared against lives in pg-desk's store.)
- **S6** — `pg-desk changes` pulls fresh data from pg-connector by default; pg-router stays the
  only clock. Operator, in session, 2026-09-29. Why: it avoids a second polling clock inside
  pg-desk while still letting an operator force a fresh read on demand with `--refresh`.
  (Rewritten in place 2026-10-05, S29, bead `pg2-32wg6`: the pulled call is
  `pg-connector <type> list` with no cursor, once per watched query, and `show` runs only for
  entities whose list fingerprint differs from the stored one. Check and pull happen in one command
  per tick; there is no separate pull event or puller role.)
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
  follows pg-connector's own `INV-EXIT-1` Targeted scheme, unchanged: 0 = completed with a
  well-formed result, including a `no_change` outcome, which the JSON output's `status` reports
  (rewritten in place 2026-10-03, bead `pg2-kftf9.13`, and again 2026-10-06, bead `pg2-8qui6`: it
  named `blocked_human_pending`, an outcome create-or-append retired, and before that a review that
  posted when the `supersede_pending` delete failed); a run in which some comments did not land is
  an error (exit 1), not a result;
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
  changes without a full-replay storm on every poll. (Rewritten in place 2026-10-05, S31, bead
  `pg2-32wg6`: the sweep has two rolling, capped tiers, a local reconcile at 30 minutes and a remote
  re-hydration at 6 hours.)
- **S12** — Advance the cursor after flush plus an age-based sweep; no explicit ack. Derived, from
  the idempotent-decider goal and the design's duplicate-tolerant delivery semantics, 2026-09-29.
  Why: deciders are idempotent and consumers already tolerate duplicates and reordering; the sweep
  already bounds the loss window, no user story needs faster-than-sweep recovery, and the
  operator's `--reset` covers "I need this now". An ack protocol would buy correctness this design
  does not need. (Rewritten in place 2026-10-05, S29 and S30, bead `pg2-32wg6`: the cursor rule
  stays for deciders and other consumers; the DETECTION baseline is advanced only on hydration
  success, which is an acknowledgment gated on hydration. There is still no decider-acknowledgement
  protocol: a failed decider run is recovered by the local reconcile tier.)
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
- **SUPERSEDED 2026-10-06 (bead `pg2-8qui6`): there is no blocked pending review to escalate any more,
  so the escalator and this host decision are retired once the implementation lands.** Text kept for the
  deployed state until then. **The escalation of a blocked pending review is hosted outside the
  deciders, for now** (bead `pg2-kftf9.15`, 2026-10-03). The bead left one point open: whether the escalation for a
  `blocked_human_pending` outcome of `pg-connector pr review submit` is a pg-decider rule or a
  pg-router integration. It is a pg-router integration, the stdlib-only command binary
  `pg-router-review-escalator` (`packages/pg-router-review-escalator`), a leaf like
  `pg-router-probe` and `pg-router-disk-watchdog`. Reasons, in order of weight:
  1. **A decider rule has nothing to key on.** A decider is `decide(view)`, a pure function of the
     composite view that does not branch on which change kind routed the item (the Deciders
     contract, STORY-DEC-1). `blocked_human_pending` is the outcome of one write call, not a
     field of any view, and the view only gains pending-review state with bead `pg2-kftf9.18`,
     which depends on this bead. Auto-close has the same shape: it reacts to the `posted`,
     `skipped` or `replaced` outcome of the next call.
  2. **No pg-decider exists yet.** `packages/pg-decider` is a planned package. Building it here
     would pull the decider program's registry, `plan`/`apply` and work-item contract into a bead
     about one escalation.
  3. **G5 is respected either way.** The escalator is none of pg-desk, pg-connector or pg-router's
     core. It writes beads through `pg-connector issue ...` (the S10 path), never through `bd` and
     never through pg-desk, and it changes no pg-router core or handler model (ADR 0065). It keeps
     no state of its own: the open escalation bead carries the per-PR dedupe key, the reason, the
     head and the last-notified time as metadata, so the tracker stays the source of truth, as the
     Deciders contract requires of work items.
  4. **The one tension is accepted.** The component table says pg-router roles MUST NOT decide which
     work items exist. The escalator is not a role of its own: the review role calls it as the
     wrapper of its submit call, and its only writes are the escalation beads for the outcome of
     the call it wraps.

  Obligations that follow. The review role MUST call `pg-router-review-escalator submit <pr>` in
  place of `pg-connector pr review submit <pr>` (bead `pg2-kftf9.17`, in the deployment set's own
  repo); a raw call raises no escalation, which is why the wrapper, not the prompt, is the
  deliberate code path. The escalation policy lives in an I/O-free package
  (`internal/escalate`) behind tracker and notifier ports, so when pg-decider exists and the
  composite view carries pending-review state, the rule MAY be re-homed as a decider rule without
  a rewrite. The defaults are a 12 hour re-notify interval and a roll-up threshold of 3; both,
  the roll-up reasons, the lookup query, extra labels and the push command are configuration,
  and the repo carries no push channel or organization identifier.

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

## Amendment 2026-10-05

> **Status: Accepted, 2026-10-05.** Drafted by bead `pg2-ii38x` from the operator's direction of
> 2026-10-05 (session `c7a2ef42-bfb6-4c46-ad4e-540354f66bb8`): "pg-router handles the scheduling,
> pg-desk is primarily for specific business logic, if it is doing its own retries, that would
> conflict with the scheduling from pg-router"; "age limitations are fine, no total limit though";
> "we want something for any entity type for which pg-desk supports"; "5 minutes for PRs is too long
> ... 1m is better for PRs". The operator (Phillip) ruled on decisions D1 to D12 and the cheap-list
> field set interactively on 2026-10-05, recorded on decision bead `pg2-32wg6`. Each row below is
> now an "Operator ruling" row with its date, and S5, S6, S8 and S12 are rewritten in place to
> match, as the earlier amendments did. Two of the design's recommendations were ruled differently or
> narrowed: D2 (the interim v1 relief) was ruled NONE, and the cheap-list field set is narrower than
> the full set the cost probe priced (S35).

### Context

Measured on 2026-10-05, the live v1 pipeline already runs a cheap per-minute PR list; its problem is
a shared serial lane (a 30 minute sweep re-emits all open PRs as a batch) and a baseline that moves
before the work is done: pg-connector's per-consumer cursor advances after the output is flushed,
before pg-desk hydrates or a decider runs (`changes.go` step 5 of pg-connector's `changes` verb), so
a failed hydration is forgotten until the entity changes again. The new flow's deferral queue
patches this for hydration only, drops after five polls, and keeps the baseline in pg-connector.
The change-flow design routes PRs at 60 seconds but says nothing about issue or thread cadence, and
two of its adapter and removal behaviors do not work as written (findings F-1 and F-2 of the design
`2026-10-05-fast-per-type-change-check-design.md`).

### Decision

This amendment records S29, S30, S31, S32, S33, S34, S35.

- **S29 (Operator ruling, 2026-10-05)** — The detection baseline is owned by pg-desk, per entity, as the list
  fingerprint observed when the entity was last hydrated successfully, stored in the same
  transaction as the snapshot, `hydrated_at`, `active` and the `change_log` rows. A pull-through
  call lists each watched query (`pg-connector <type> list`, no cursor), compares fingerprints
  against the store, and hydrates what differs, all inside one command per tick: the check and the
  pull are NOT two events routed through pg-router. An entity's watched membership is the union of
  the per-query lists, computed in pg-desk, because GitHub search cannot OR across qualifiers (a
  mixed-qualifier OR parses and silently returns nothing). A failed hydration writes nothing, so the
  entity is still different on the next scheduled call. The fingerprint is produced by the connector for the
  type and MUST be compared list against list, never recomputed from a detail read. This amends S5
  (pg-connector still detects most deltas, but through its summary fingerprint, not its ledger), S6
  (the pulled call is `list`, not `changes`) and S12 (the cursor rule stays for consumers; the
  detection baseline is gated on hydration success, which is an acknowledgment). Why: constraint 4
  of the operator's direction (the baseline is pg-desk's store, the only remote call is the cheap
  list) and per-entity success, which one monotonic consumer cursor cannot express.
- **S30 (Operator ruling, 2026-10-05)** — pg-router owns every clock. pg-desk MUST NOT run a retry, backoff or timer
  loop, and MUST NOT keep a retry counter or a deferral queue for the pull-through. Capping work per
  poll is allowed only if the cap carries work to the next poll (no entity is dropped) and the
  sizing bound `active_count / max_per_poll x poll_interval <= max_age` is checked and alerting. The
  v1 `sync_error` automatic retry stays on the v1 pipeline only and is deleted with `internal/sync`
  at cutover. Why: the operator's retry direction, and a deferral queue that gives up after a fixed
  number of polls is a drop.
- **S31 (Operator ruling, 2026-10-05)** — The age sweep (S8) has two tiers. A local reconcile tier
  re-emits a `reconcile` record for an active entity whose latest change-log row is older than
  `reconcile_age` (default 30 minutes, which MAY be lowered because the tier makes no remote call),
  with no remote call and no hydration. A remote tier re-hydrates an entity whose `hydrated_at` is
  older than `sweep.max_age` (default 6 hours). The operator ruled the remote tier allowed (D5). Both
  tiers are rolling (oldest first), capped per poll, and spread by a deterministic per-entity offset
  `hash(entity_id) mod (age / 5)`, because pg-router's triggers have no jitter. A failed decider run
  is recovered by the local tier, not by a decider-acknowledgement protocol (D6). Why: the list fingerprint cannot see review threads, merge-state status or edited
  comment bodies, and a failed decider run needs a cheap local recovery.
- **S32 (Operator ruling, 2026-10-05)** — Fast-check cadence is per entity type, set in
  pg-router's query config: `pr` 60s; `issue` backed by beads 5m now and 60s once local list
  latency is measured under 10 seconds (the anchor echo loop fix `pg2-u4c1s` has landed); `issue`
  backed by Jira 5m with no cursor (the cursor-bounded `updated >= ` list can miss a change and
  advances on read); `thread` backed by Slack is excluded from the fast check because its list is an
  LLM call that is always truncated and cannot be fingerprinted or report removals. A separate
  design for a deterministic, non-LLM Slack list is authorized (bead `pg2-ynxy2`). A beads
  fingerprint MUST exclude `metadata.last_checked_at`. The `pr` cadence holds only while the points
  budget in S35 holds.
- **S33 (Operator ruling, 2026-10-05)** — The adapter emits ONE item per changed entity per poll with
  `emit = <type>.changed`, `id = <entity_id>@<seq>` and the coalesced kinds in `metadata.kinds`. The
  adapter MUST set `emit`, because pg-router types an event from `emit` (default: the query's first
  declared emit), never from the item's `type`. The seq in the id makes a change that lands while a
  run is in flight a distinct event instead of a deduplicated one. This amends the design's 9.4 and
  keeps S4 ("one event per changed entity with its change kinds") and S22 (exact-string binds).
- **S34 (Operator ruling, 2026-10-05)** — When an entity leaves every watched query, pg-desk MUST
  perform one confirmation read before deactivating it, so that a PR that merged or closed is
  classified `merged` or `closed` and not logged only as `removed`; a failed read leaves the entity
  active and is repeated on the next call. Why: every watched PR query is `is:open`, so a merged PR
  is, by construction, absent from the list.
- **S35 (Operator ruling, 2026-10-05)** — The cheap PR list carries only fields that fit the points
  and time budget, and the budget is a guardrail, not a hope. (a) The list ADDS `reviewThreads`
  `totalCount` and the labels `totalCount` (measured cost unchanged at 2 points per 100-PR page).
  (b) `reviewRequests` is DEFERRED: it adds a nested `first:N` connection (3 points per page) and at
  the 11 search strings the corrected watched set needs it exceeds the usable ceiling in the worst
  measured hour. (c) `mergeStateStatus` stays show-only, reaffirming S28 with a measurement: adding
  it made the search return HTTP 502 or 504 at about 11 seconds on strings with 24 or more PRs, and
  GitHub appears to cut a request near 10 seconds. (d) No CI-detail field is added. (e) A
  field-coverage test is REQUIRED: every field a consumer reads is either in the list fingerprint or
  on a declared blind-spot list that names its refresh tier (S31). (f) The list spend MUST fit under
  the usable ceiling of the GraphQL limit minus the connector's reserve (5,000 minus 1,000 = 4,000
  points per hour) in the worst measured hour INCLUDING the shadow run, computed as
  `non-list spend + strings x cost per page x (60 + 12)` and recomputed whenever the string count or
  the field set changes. Why: the cost probe of 2026-10-05 priced the variants (2, 3 and 4 points
  per page) and showed that, at 7 strings, adding every field leaves 271 points of headroom in the
  worst hour; correcting the watched set to 11 strings (bead `pg2-yye5p`) removes that headroom for
  anything above 2 points per page.

### Consequences

- pg-router's core, ADR 0031, `DEC-EVENT-1`, `DEC-EVENT-2`, `INV-FAIL-1` and `INV-CONC-1` are
  unchanged and need no amendment; the design relies on their current text and adds tests only
  (an in-flight dedupe queue test and a multi-emit loader test).
- pg-connector's `list` result gains an additive `fingerprints` map and the GitHub backend logs
  `graphql_cost`; the cached `changes` ledger keeps serving its other consumers. The cache
  tombstone written by `changes` is no longer written for entities pg-desk sees removed, so a
  backend-down `list` may serve a removed entity from the cache for the cache max-age, marked
  stale and degraded; pg-desk treats a degraded source as contributing no removals.
- pg-desk's `entity` table gains `list_fp` in the same cutover migration that already adds
  `version`, `hydrated_at` and `active`; the `change_flow.deferred.<type>` meta key is removed.
- The live v1 store (schema 1) holds no baseline for any type; the new commands refuse it, so the
  check is first exercised on a store copy migrated with `pg-desk migrate --cutover`.
- `pg2-u4c1s` MUST land before, and block, every issue-type implementation bead that follows from
  these rows. It has landed (closed 2026-10-05), so the ordering is already satisfied.
- The cutover is not re-sequenced ahead of the decider phase (phase 8, decider parity); the
  amendment lands in phases 6 and 7 of `pg2-2j5ac.52` and adds no pg-router core change.
- The interim relief for the v1 queueing problem (design decision D2) was ruled NONE: `pr-sweep` and
  `desk-pr` are not changed, and the effort goes into the new flow.
- The R3 shadow check on a COPY of the store is authorized. It makes real GitHub calls and runs the
  list alongside the v1 feeds at a 5 minute cadence, costing `12 x strings x cost per page` points
  per hour; the S35 budget MUST be recomputed immediately before it runs.
- Follow-up beads from the rulings: `pg2-eax6d` (decompose the approved design into implementation
  beads), `pg2-sve9v` (a `type` label on `pg_router_dispatch_latency`, follow-up to `pg2-nimab`),
  `pg2-ynxy2` (the deterministic Slack list design) and `pg2-yye5p` (the watched `team` string that
  returns no results, which gates the S35 budget).

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
- The pending-review escalation host is recorded in the Deciders cluster's Consequences; its
  behavior is documented in `packages/pg-router-review-escalator/docs/behavior/README.md`.
- Tracked under program epic `pg2-2j5ac.52`, docket `pg2-2j5ac.52.2`. The design questions Q-A and
  Q-B were answered in bead `pg2-2j5ac.52.1` (S25, S26), the exit-code ruling was made on
  escalation bead `pg2-2j5ac.52.24` (S27) and the mergeability ruling on the `pg2-2j5ac.52.6`
  finding (S28); task `pg2-2j5ac.52.2.2` recorded all four here.
