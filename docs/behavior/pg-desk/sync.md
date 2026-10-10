# pg-desk — sync

Sync is stage 3 of the pipeline (see [`pipeline-run.md`](pipeline-run.md)), running after
interpret for every `pr`-typed entity. It writes only agent signals, only through `pg-connector
issue` pinned to `agent_tracker_backend`, and it MUST record every write it makes (or would make)
in the `ledger` table (see [`store-schema.md`](store-schema.md)). The dedup key for every bead
kind lives IN THE BEAD, never only in the ledger: `metadata.repo` plus `metadata.pr_number` for
anchors and review requests, and the exact title `process-feedback: <repo>#<n>` for cycles. A
crash between `issue create` and the ledger write MUST NOT mint a duplicate on the next run: sync
consults the ledger first, then the work beads gather already fetched this run, before creating
anything.

## Modes

`sync.mode` is `off`, `plan`, or `apply`:

- **`off`** — sync does not run at all: no `pg-connector issue` call, no ledger write.
- **`plan`** — sync runs the full adoption and rule logic below and records every write it would
  make — kind, dedup key, and fields — as a planned row in the `ledger` table. It calls no
  `pg-connector issue` write. `pg-desk status` and `pg-desk show` print these planned rows (see
  [`operator-commands.md`](operator-commands.md)), so an operator can compare them against what
  the beads-writing sync path actually mints — the Phase 10 sync-parity check.
- **`apply`** — performs the writes for real.

A planned row is distinguished from an applied one by an empty `bead_id` in its ledger row — never
a separate column, since the design pins the ledger table's existing columns (see
[`store-schema.md`](store-schema.md) for how this packet reuses `last_synced_content_hash` as a
deterministic hash of the write's own field content, identical in `plan` and `apply` mode for the
same input, which is what makes the parity check mechanical rather than a manual read-through).

## Adoption

**The `work-beads` query is deployment config and MUST list the children (`pg2-6w396`).** Anchors
are type `merge-request`, but feedback cycles and review requests are created as type `task`
(`--issue-type task`), so a query of only `list --type merge-request --status open` returns no
child at all: adoption by listing can never find a cycle or review request, the closure cascade
below sees no child, and crash-safety rests on the ledger alone. The query MUST be a union that
also lists the `process-feedback:` and `review-pr:` task beads, and MUST include `in_progress` (a
claimed review request or cycle is not `open`), for example the list
`list --type merge-request --status open,in_progress,blocked`,
`list --type task --title-contains process-feedback: --status open,in_progress,blocked`,
`list --type task --title-contains review-pr: --status open,in_progress,blocked`
(`pg-connector-issue-beads` unions a list of expressions by id). `pg-desk doctor`'s
`work-beads reach` line warns when the listing holds anchors and no child while the ledger holds
child rows. Beads a worker improvised under an anchor (for example "Human: unblock ...") match no
title shape; they reach the cascade only if the query is widened to every non-closed bead, and the
`issue-beads-work` change feed that shares the query name would then fire `run issue` for beads
that are not PR work, which it reports as an error, so such a deployment SHOULD give the feed its
own narrower query. The union above is safe for that feed as is: every bead it lists carries an
anchor, `process-feedback:` or `review-pr:` title, each of which `run issue` resolves to its PR.

On every run (not just literally the first), before creating anything, sync classifies the work
beads gather already fetched this run (`issue list --query work-beads`, matched to this PR) by
title/metadata shape (anchor / feedback cycle / review request / none of those). A pre-existing
anchor or review request is adopted by its `repo`/`pr_number` metadata; a pre-existing cycle is
adopted by its exact title; a merge-request bead titled `<repo>#<n>: ...` with no `repo`/
`pr_number` metadata yet is adopted by its title prefix, and gains both metadata keys on its first
`apply` run (never in `plan`, which calls no `pg-connector issue` write at all). This is the SAME
bead-shape classifier [`run-issue.md`](run-issue.md)'s own reverse (bead -> linked PR) lookup
uses — one parser, not two.

## Bead shapes

Reproduced verbatim from the design of record; any later change MUST land with the sources and
prompts that read these shapes, in the same change:

| Kind           | bd type         | Title                          | Labels                                               | Metadata                                                                                                                                                                | Parent |
| -------------- | --------------- | ------------------------------ | ---------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------ |
| anchor         | `merge-request` | `<repo>#<n>: <pr title>`       | `co-owned` when applicable, `pbase:<n>` while nudged | `repo`, `pr_number`, `state`, `branch`, `base`, `author`, `url`, `draft`, `last_synced_at`, `last_checked_at`, `closed_at` (the last two only as described under Rules) | none   |
| feedback cycle | `task`          | `process-feedback: <repo>#<n>` | `mine`, `fbsum:<digest>`                             | `repo`, `pr_number`, `branch`; description is the rendered summary of unaddressed items                                                                                 | anchor |
| review request | `task`          | `review-pr: <repo>#<n>`        | as today                                             | `repo`, `pr_number`, `branch`, `head_sha`, `ownership`                                                                                                                  | anchor |

## Rules

- **Anchor** — exactly one per `(repo, number)`, created lazily only when a cycle or review
  request first needs a parent (D8), never eagerly. The conflict-priority nudge (a port of
  pg-pr's existing `pbase` mechanism: stash the pre-conflict priority on the first conflicting
  tick, nudge mine/co-owned toward higher priority and team toward lower, no-op on a repeated
  conflicting tick, restore the baseline once the conflict clears) is applied via `issue update`.
  `last_synced_at` and `last_checked_at` are written together, and only by a content write (the
  create, or an update because the anchor's fields changed). Closure additionally writes
  `closed_at` and a final `last_checked_at`, before the close transition. An unchanged check MUST
  NOT write the anchor bead: any anchor update changes the issue's `updated_at`, which pg-connector's
  issue changes feed treats as a change, so a per-check stamp would echo an `issue.changed` and a
  needless issue run for every PR on every sync (`pg2-u4c1s`). An unchanged check records its time
  in the anchor's ledger row instead (`last_synced_at` there is the row's last-checked time, in
  every mode), and staleness stays detectable through `serve`'s
  `pg_desk_oldest_anchor_check_age_seconds` gauge and `status`'s
  `oldest_anchor_check_age_seconds` line (see [`serve.md`](serve.md) and
  [`operator-commands.md`](operator-commands.md)). This deliberately reverses the earlier rule that
  a quiet open PR advances `last_checked_at` on the bead each sync (`pg2-kftf9.3`).
  Closed, with its open cycles — BOTH the feedback cycle and the review request, symmetrically —
  only on a CONFIRMED closure — a `--change removed` re-read (or an ordinary/sweep `pr show`)
  reporting `merged` or `closed`, or a removed re-read's own `not_found` (a closure with reason
  `gone`) — never because the PR merely left a query. An already-closed anchor is never reopened.
  The cascade is type-blind, like pg-pr's own cascade-close
  (`beadsbridge.CascadeCloseMergeRequest`): every open direct child of the anchor (a bead
  filed with `--parent <anchor>`) that the work-beads query returned is closed too, whatever its
  title or type (`pg2-kftf9.7`; e.g. an improvised "Human: unblock ..." bead). Children come
  from two reads, merged: the work-beads results, and a live `pg-connector issue children` read
  of each bead being closed, so a child the ledger and the work-beads query never saw (a legacy
  child filed by the retired pg-pr daemon; the closure's own `--change removed` re-read gathers no
  work-beads at all) is closed too (`pg2-ubvmh`). A failed live read fails the run rather than
  closing the anchor on a partial view of its children; a backend without that op falls back to
  the work-beads results alone. Each bead's own open descendants (grandchildren and deeper, to a
  bounded depth) close before the bead itself, children first and the
  anchor last, because `bd` 1.3.1 refuses to close a parent with an open child. A failed close
  leaves the anchor open, and unstamped, for the next run. An earlier
  revision closed only the feedback cycle, leaving review-pr beads open at a far higher stale
  rate (`pg2-ryexi`).
- **Feedback cycle** — for every PR with unaddressed feedback (a disposition that is still
  `open`), ensure one open cycle keyed by title and deduplicated by the `fbsum` digest (a port of
  pg-pr's existing digest computation), carrying the `mine` label only when the PR's ownership
  acts as mine.
- **Review request** — ported verbatim from pg-router's ACL: every PR with ownership `mine` or
  `co-owned` (draft included), and every non-draft team PR, gets one `review-pr` bead. When the
  head advances past the ledger's last-requested SHA AND has then stayed the PR's head for the
  settle window (below), a completed bead is reopened and its metadata refreshed in ONE `issue
update <id> --status open --clear-assignee ...` call (the previous reviewer's claim MUST NOT
  survive the reopen, or no worker can claim the re-review; `pg2-1pt7r`; the same call also clears
  the bead's deferral, below). No gate.
- **Review settle window** (`pg2-a9yhn`) — a head that differs from the last-requested one MUST NOT
  be acted on at once. A review is expensive (the router health review of 2026-10 measured one at
  about 16 minutes and $2.88) and a review whose head moved before it submitted is refused at
  submit with nothing written, so a burst of pushes MUST yield ONE review request, for the head the
  burst ended on.
  - The ledger's review-request row records the pending head and when a sync run first saw it
    (`first_seen_head_sha`, `first_seen_head_at`). The bead is reopened only once that head has
    been the PR's head for `sync.review_settle_window` (default `2m`; a Go duration; `0` disables
    the window and restores acting on a new head at once). A further push inside the window
    replaces the pending head and restarts the timer, so N pushes inside one window produce one
    reopen. A push back to the head already requested drops the pending head.
  - The window applies to the REOPEN only. The first review request of a PR is created at once: a
    PR's first head has no earlier head to supersede, and a pending row with no bead would be
    indistinguishable from a `plan`-mode planned row. `plan` and `apply` run the same settle
    logic, so they agree on when a reopen happens; a run still inside the window writes nothing
    (no bead write and no ledger write).
  - The timer starts when sync first SAW the head, so the delay is the window plus at most one
    polling cadence. Sync runs only on events, so a quiet PR whose last push has settled is
    re-driven by `pg-desk reconcile` once the window elapsed (see
    [`operator-commands.md`](operator-commands.md)); how soon after the window that is depends on
    how often the scheduler runs `reconcile`. A deployment that wants the reopen sooner SHOULD run
    `reconcile` more often than the window.
  - Dedup by head SHA is unchanged and MUST be preserved: sync never re-requests a head it has
    already requested (the same-head check below), which is why no review of an already-reviewed
    head occurs.
- **What the ledger's `last_reviewed_head_sha` means** (`pg2-a9yhn`, decided intended) — it is the
  head of the last review REQUEST sync made, written when the bead is created or reopened, not the
  head of a review that completed (the column name is historical). A request is not "dropped"
  while its bead is open, in progress or deferred: the bead is the durable request, a killed or
  crashed session's claim is released by the session reaping, and re-requesting the same head on
  top of an open bead would change nothing. A worker that closes the bead without reviewing
  (declined) is NOT asked again for the same head; the head is requested again as soon as it
  changes (and settles), or when a person reopens the bead. Sync cannot observe whether a review
  was posted, so it cannot key on a "last reviewed" head, and keying on one would re-review every
  declined head on every run.
- **Review-request lifecycle** (`pg2-kftf9.8`) — the `review-pr` bead is per PR, not per review,
  and its terminal state is reached by the worker, not by sync:
  - Exactly one `review-pr` bead exists per `(repo, number)`. A head advance REOPENS that bead
    (above); sync MUST NOT create a second bead beside it, so there is never an older open bead
    to close as superseded. The older bead IS the newer one, reopened. (Whether a new head
    should get its own bead instead is the open design question `pg2-b3tdu`; until it is ruled,
    this reopen-per-PR rule stands.)
  - The worker MUST close the bead once the review for its head is in place, that is when
    `pg-connector pr review submit` reports `posted`, `append` or `no_change`. A review that is
    still an unsubmitted PENDING review counts as done: the bead MUST NOT stay open waiting for a
    person to submit it, and a `no_change` result MUST NOT be handed back. Any error, including a
    run in which some comments did not land, is handed back, and the retry converges because the
    tool never repeats a comment it already posted.
  - An existing own PENDING review, of any head, is REUSED by the submit itself: the new head's
    comments are appended to it and are anchored to the new head, while the comments already in it
    stay on the head they were made at. Nothing is deleted or replaced, so the worker needs no
    "Human: unblock" bead and nothing is escalated. A stale comment staying on an older head is
    accepted. The operator's own edits to the pending review are preserved, a comment the operator
    deleted is not re-added, and the tool never submits the review.
  - The reopen on head advance MUST clear that deferral in the same `issue update`
    (`--clear-defer`; `pg2-vhs3e`). The deferral was set for the OLD head, and the tracker keeps
    `defer_until` across a reopen (verified on bd 1.2.2), so a reopen that left it in place would
    hide the NEW head from every ready query for up to the deferral (12 hours).
  - When the PR is confirmed `merged` or `closed` (or gone), the closure cascade above closes the
    open `review-pr` bead with its anchor, so no `review-pr` bead stays open for a finished PR.
  - Delivery (operator ruling, Phillip, 2026-09-29, recorded on `pg2-kftf9.8`: no new
    dismiss/finalize command on `pg-pr`): this lifecycle is delivered by the CURRENT pg-desk sync
    (`ensureReviewRequest` reopen, plus the closure cascade) together with `pg-connector pr
review submit` (`pg2-kftf9.13`, whose replace behavior `pg2-8qui6` supersedes with create-or-append)
    and the deployment's review prompt (`pg2-kftf9.17`). The replacement flow (`pg2-2j5ac.52`, rule `review.head-advanced`, decision
    S26) carries the same reopen-per-PR behavior forward unchanged ("as designed"), so no further
    behavior change is required of it for this lifecycle.
- **Operator visibility** (`pg2-kftf9.18`) — `pg-desk pr show <id>` displays the PR's pending agent
  review, how many of its comments are anchored to the current head, whether it is stale (no
  review or comment exists for the current head) and the last append
  ([`show.md`](show.md), "Pending review"). A failed lookup reads `unknown`, never "no pending
  review". Sync neither reads nor writes it.
- **Area labels** (`pg2-lvoye`) — when the deployment configures `area_labels`, each rule is a
  regular expression searched in the PR title (default) or head branch, naming the labels a
  matching PR carries; the union of every matching rule is the PR's area label set. Sync adds the
  set to the anchor at create, and the review-request and feedback-cycle beads carry the same set
  plus any area label (a label named by some rule) their anchor already carries, so a label an
  operator put on the anchor flows to its children. Sync MUST only ever ADD area labels: it MUST
  NOT remove a label from any bead for this reason, so labels added by hand, and an area label
  later removed by hand, are never fought over. With no `area_labels` configured, sync adds
  nothing and the bead write hashes are unchanged. An existing anchor and feedback cycle gain
  missing area labels on their next content write; a review request gains them on creation or on
  its head-advance reopen. Which labels exist is deployment config: this repo names none.
- **Recorded losses (D15)** — draft auto-promotion, `wip on`'s upstream draft conversion, and
  pending reply posting are not performed by sync.

## Failure handling

A sync failure for one PR sets that PR's `interpretation.sync_error` (via the store's existing
interpretation writer) AND fails the `run` invocation with exit `1` (see
[`pipeline-run.md`](pipeline-run.md)), so pg-router retries the event with backoff and counts it as
a failure. A failed run MUST NOT lose the diagnostic: `sync_error` is still recorded and surfaces
on the dashboard and counts into `runs_failed_24h`; a later successful run clears it. A `sweep`
that hits a sync failure for any entity still attempts every entity, then exits `1`.

Sync MUST be safe to retry. Closure is guarded by the ledger: an anchor or cycle already recorded
as closed is not closed again, and a retry after a partial failure (for example the anchor closed
but a child close failed) MUST finish the remaining closes without repeating the finished ones.

### Automatic retry

A recorded `sync_error` MUST be retried automatically when its failure is transient, and MUST NOT
be retried automatically when the failure needs a person (operator ruling, 2026-09-30, bead
`pg2-xb6fs`):

- **Transient** — the environment, not the request, is at fault: a missing path or an unmounted
  volume, a network blip, a tracker that cannot currently be reached. `pg-connector`'s
  `unavailable` code, a targeted `not_found`, a failure to start `pg-connector` at all, a timeout,
  and every failure not named in the next bullet count as transient.
- **Needs a person (non-transient)** — authentication (`unauthenticated`), validation
  (`invalid_argument`), and config or version problems (`unknown_op`, `query_not_recognized`,
  `version_mismatch`, an unknown `sync.mode`), and `bd`'s refusal to close a parent that still has
  open children (`cannot close <id>: N open child issue(s)`; carried as `unavailable`, recognized
  by its message, `pg2-ubvmh`): a child the closure did not close stays open until a person acts.

Every failed run of a PR that has a recorded `sync_error` counts one attempt, whichever stage
failed, and the latest failure decides the class. A transient row is retried with exponential
backoff — by default 1m, 2m, 4m, 8m, 16m, then 30m for every later retry — and MUST stop after a
bounded number of automatic retries (default 10). After that the row is **exhausted**: it stays a
`sync_error` and is not retried again automatically. A successful run clears both the
`sync_error` and its retry state. Automatic retries are performed by `pg-desk reconcile` (see
[`operator-commands.md`](operator-commands.md)), so the backoff is the earliest a retry may run;
how often the scheduler runs `reconcile` also bounds it. The bound governs only these automatic
retries: a new event for the PR still runs the whole pipeline, sync included, whatever the row's
retry state, and its failure counts as one more attempt. `pg-desk reconcile --retry-all` is the
operator's manual repair once the cause is fixed: it re-drives every recorded `sync_error` now,
ignoring backoff, the bound and the class.

The bounds are configuration: `sync.retry.max_retries` (default `10`; `0` disables automatic
retry), `sync.retry.initial_backoff` (default `1m`, doubled for each later retry) and
`sync.retry.max_backoff` (default `30m`, and not below `initial_backoff`). An invalid value fails
config load.

Every `sync_error` row carries a retry indicator — attempts so far, the retry bound, and either
the next retry time or why there will be none (exhausted, or non-transient) — kept in the store
(see [`store-schema.md`](store-schema.md)) and shown by `pg-desk status`, `pg-desk doctor` and
`serve`'s `/metrics` (see [`serve.md`](serve.md)), so an operator can tell a row that is still
being retried from one that no longer will be.

## Telemetry and logs (D24)

Sync emits nothing over OpenTelemetry or Prometheus of its own in this phase — export is a later
observability item, alongside pg-router's own metrics sink (the design's `pr-pool` naming predates
the pg-router rename, bead `pg2-myc6y`). Sync's own outcome (whether it ran, and any `sync_error`)
is folded into `run`'s existing structured JSON log line to stderr — it does not add a separate log
line or stream.

## Out of scope (Phase 10)

- The bead-shape classifier's OTHER consumer — reverse (bead -> linked PR) resolution for `pg-desk
run issue` against the beads backend — is [`run-issue.md`](run-issue.md), not this doc.
- Jira/Slack sync, the cross-reference step, and layered urgency are Phase 13.
- `daemon.enable` is this docket's last packet.
- The store-wide re-verification of ledger rows whose entity has left every gathered query is
  `pg-desk reconcile` (see [`operator-commands.md`](operator-commands.md)), not `Sync` itself:
  `Sync` stays single-entity, and `reconcile` is the store-wide driver that calls the same
  `run` path per entity.
