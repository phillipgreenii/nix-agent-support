# Invariants — pg-connector-issue-jira

The rules the daemon-backed Jira backend follows. See the [glossary](glossary.md),
[actors](actors.md), [interfaces](interfaces.md) and [journeys](journeys.md). Each definition's
identity is a **stable UUID** minted at its definition (`INV-3`); the typed name is a mutable,
topic-namespaced label (`INV-JIRA-<TOPIC>-<n>`) so a name this set cites from the parent set or from
the GitHub set never collides with one of its own.

Numeric defaults (freshness windows, rate and concurrency caps, delivery timeouts, leases, pause
defaults, retention intervals of change rows, alert thresholds, sizes) are **not** fixed by these
rules: they are set from measurement and recorded in the decision docs. A rule below that names a
window, a cap or a timeout means whatever value the decision docs set for it. The two intervals
that bound a closed issue's life in the store (24 hours after closing, 7 days after closing,
`INV-JIRA-TRACK-1`) are part of the rule itself.

This set owns the daemon's behavior. The umbrella's own obligations toward a backend that owns its
cache and changes are the parent set's (`INV-CACHE-9`, `INV-LEDGER-FRESH-5`) and are cited, never
restated. Where this daemon behaves as the GitHub backend's daemon does, the rule is **inherited by
citation** from the GitHub set and this set states only the difference; where this set needs a
different meaning for a shared term it takes a different name (`INV-20`).

## Process and placement

- **`INV-JIRA-PROC-1`** <!-- uuid: ea10c7a7-e581-4ec5-9384-c2736e537606 --> — The backend MUST run one **daemon** per configured
  Jira **instance**, each with its own store, and that daemon MUST be the **store's only writer**.
  The backend in **client mode** MUST forward each read to its daemon and MUST NOT open the store
  and MUST NOT call the origin to answer a read. The cache, the refresh policy and the change log
  live in the backend, never in the umbrella (`INV-STATE-1`, `INV-CACHE-9`). Two instances stay
  fully separate: each one's tracking, watched flags, baselines, consumer positions and budget are
  its own, so no component of an instance appears in any key.
- **`INV-JIRA-PROC-2`** <!-- uuid: 96b7e903-2a06-4480-bdad-4336f916509f --> — When the daemon cannot be reached, a read MUST be
  answered `unavailable`, and the message MUST name the socket path, the supervisor and its restart
  hint (where no supervisor exists, a command that starts the daemon in the background). The client
  MUST NOT read the store and MUST NOT call the origin to answer a read: an outage is made visible,
  never hidden behind old or direct content. The client MUST answer `capabilities`, including its
  `owns_changes` and `cache_opt_out` declarations, without contacting the daemon, so an outage can
  never be mistaken for a backend that opted out of the umbrella's cache. Retry of a refused
  connection with backoff, and `version_mismatch` only outside the supported protocol range, are as
  in the GitHub set (`INV-GH-PROC-2`). A write never needs the daemon (`INV-JIRA-WRITE-1`).
- **`INV-JIRA-PROC-3`** <!-- uuid: e1b17350-b625-4394-b843-f9aff204a89e --> — The application duties of the GitHub daemon hold for
  this daemon unchanged: identical behavior on every supported platform, every setting from the one
  rendered config file and none from the environment, a single-instance lock, a restrictive state
  directory, a drain on a shutdown signal that loses no pending change row, acceptance of
  connections before a long migration, refusal to start on a store newer than the binary, and
  move-aside of a corrupt store (`INV-GH-PROC-3`, `INV-GH-PROC-4`). A store moved aside loses its
  watched flags and baseline markers, and `INV-JIRA-WATCH-7` states what follows.

## Operations and writes

- **`INV-JIRA-WRITE-1`** <!-- uuid: 9b3c7d5c-7683-49dc-bf6f-0345f516c5f4 --> — A write MUST NOT depend on the daemon. The writes
  (`create`, `comment`, `transition`, `update`, `close`) MUST stay in client mode: the calling
  process executes them directly against the origin tool, because the acting identity comes from the
  caller's own environment, which the daemon never sees, and because the callers that write MUST NOT
  stop working when the daemon is down. A write MUST NOT be answered `unavailable` for want of the
  daemon, and a write whose notices (`INV-JIRA-WRITE-2`) were not delivered MUST still be performed.
- **`INV-JIRA-WRITE-2`** <!-- uuid: 4bb6d235-afa2-4a11-abc1-87daf2e46d07 --> — Around a write the client MUST send the daemon two
  **write notices**, each with a short delivery timeout the decision docs set. Before the write it
  sends `write_begin` naming the issue (a `create` has no id yet, so it names the instance); after
  the write it sends `invalidate` naming the issue. On `write_begin` the daemon MUST raise the
  entity's generation, hold every new refresh of that entity and discard one that is in flight; on
  `invalidate` it MUST release the hold and queue exactly one re-pull of the entity. If `invalidate`
  is never delivered, the hold MUST expire after a lease the decision docs set. If `write_begin` is
  not delivered within its timeout, the write MUST proceed and the client MUST skip `invalidate`. A
  notice MUST NOT make the write fail and MUST NOT delay it by more than its delivery timeout. A
  refresh that began before the write MUST NOT overwrite content stored after it, and MUST NOT
  append a change row from it.

## Groups and freshness

- **`INV-JIRA-FRESH-1`** <!-- uuid: 1cf91dbb-a9a5-4dc4-8502-c651abc7800f --> — An issue MUST be held as **field groups**, each
  with its own freshness window, and every field of an issue MUST belong to exactly one group. The
  **summary** group is what a listing returns; the daemon fetches it with one full query run per
  configured query. The **detail** group is what a single-issue read adds (description, comments and
  the like); the daemon fetches it one issue at a time, and it is fresh until its window ends or
  until the issue's update time changes, whichever is first. The **children group** is never held:
  an issue's children are a live read. The daemon MUST use the issue's update time as the
  **revalidation token**: a changed update time re-pulls the detail group, and an unchanged one
  lets the detail group stay current to the end of its window. The update time itself is not part
  of the issue's content (`INV-JIRA-CHG-3`).
- **`INV-JIRA-FRESH-2`** <!-- uuid: 6fa914d5-f5da-46a6-8787-6f4017431f33 --> — `show` and `list` MUST be answered from the store
  (the read-through rules of `INV-GH-FRESH-2`): when every group a read needs is inside its window
  the answer is a **local hit** with no origin call; otherwise the stale groups are fetched at the
  interactive class, the answer waits up to the client's deadline, and a failed fetch leaves the
  previous content in place and answers stale content inside expiry, `unavailable` otherwise.
  `children`, `search` and `list_activity` MUST be **live pass-through reads** performed by the
  daemon under the governor, and nothing they return is stored. A read with `fresh: true` MUST
  force a fetch of every group it needs; the backend decides how current its answer is
  (`INV-CACHE-8`). A read carrying the watch option is forwarded to the daemon (`INV-JIRA-WATCH-1`).
  Every read answer MUST carry `served_from`, `stale`, `age_seconds` and `groups[]`, as the GitHub
  backend's does (`INV-GH-FRESH-8`).
- **`INV-JIRA-FRESH-3`** <!-- uuid: 782a82e1-933c-4cc8-9336-3abce722692f --> — The issue entity MUST carry a **comment summary**,
  the count of comments and the time of the latest comment, derived from the detail group, so that
  a comment is a visible field difference. The normalised content (`INV-JIRA-CHG-1`) covers exactly
  the fields the origin tool emits plus this summary: a field the backend does not map is not
  compared, and a change to it is not a change this daemon can deliver.

## Tracking and retention

- **`INV-JIRA-TRACK-1`** <!-- uuid: 484945da-f17b-4080-a915-d589075412fa --> — Every issue the daemon retrieves MUST be
  **tracked**, whether it came as a member of a configured query, because a caller read it, or
  because it was watched, and a tracked issue MUST be refreshed in the background until it has been
  **closed for 24 hours**, closed meaning a status in the origin's done category. From 24 hours to 7
  days after closing (the **grace period**) it MUST be served from the store and MUST NOT be refreshed. After 7 days closed
  it MUST be **evicted**; change rows keep their own retention (`INV-JIRA-CHG-8`). A direct read
  MUST NOT have a different tracking rule from a query result, and there MUST be no manual untrack.
  The daemon MUST record the time an issue was seen to close, because the two intervals run from it.
- **`INV-JIRA-TRACK-2`** <!-- uuid: 361a5c6c-83a6-4125-9657-bebf2784c7aa --> — Tracking has no decay before closure, so an issue
  that someone merely read keeps being refreshed until it closes, and an abandoned open issue is
  refreshed indefinitely; that cost is accepted and MUST be visible before it grows. The daemon
  SHOULD refresh a tracked issue that is not a member of any configured query at a lower priority
  class than a member (`INV-GH-BUDGET-2`), MUST export the count of tracked issues, by class
  (members, other open, closed within their grace), and of watched issues (`INV-JIRA-OBS-1`), and an
  alert MUST fire when the tracked count exceeds a configured size (`INV-JIRA-OBS-2`). There MUST
  NOT be a hard cap on tracking that a query did not ask for.

## Watching

- **`INV-JIRA-WATCH-1`** <!-- uuid: 0a198145-5c4c-4bfb-b8f4-2028d4239d32 --> — The daemon MUST store each issue's **watched
  flag**, and nothing MUST be watched by default. An issue MUST become watched when it is a member
  of a configured query whose configuration carries `watched = true` (a property of the query's
  definition, default false), or when a `show` or lookup of that one issue carries the watch option
  (a one-off watch of one entity). The daemon MUST NOT accept a watch option on a query run, a
  listing or a feed read: a **set** of issues is watched by declaring its query watched, never by
  running a query with an option. Every other read, member or lookup is tracked and delivers
  nothing (`INV-JIRA-WATCH-3`). A watch option on an issue that is already watched changes nothing
  and appends nothing.
- **`INV-JIRA-WATCH-2`** <!-- uuid: d4cbf1bd-f6a2-4c6f-9819-06703922b745 --> — **Stickiness.** A watched issue MUST stay watched
  after it leaves every query, until it is closed and past retention, which for this daemon is
  **eviction**, 7 days after closing (`INV-JIRA-TRACK-1`). While the flag is set the daemon MUST
  deliver the issue's rows, including `closed` and `reopened`, so a `closed` row is delivered
  although the issue is no longer a query member, and a reopen inside the retention window needs no
  new watch. After the 24-hour refresh period of `INV-JIRA-TRACK-1` a reopen is observed when a
  query run or a read next retrieves the issue. After the flag is cleared by eviction the issue is
  a first observation again (`INV-JIRA-WATCH-6`). Nothing but eviction clears the flag: no read and
  no watch option does.
- **`INV-JIRA-WATCH-3`** <!-- uuid: 5dd1f489-c7ea-476d-9d4c-01d414ac6495 --> — The daemon MUST append and deliver change rows
  **only for watched issues**. An issue that was only read, or is only tracked, MUST deliver
  nothing, however much of its content changes. The feed selects by the stored flag, never by
  query membership (`INV-JIRA-CHG-6`).
- **`INV-JIRA-WATCH-4`** <!-- uuid: 072301a1-a144-4fae-bd6f-5d81239d91f2 --> — Which queries are watched MUST be declared by the
  deployment in the backend's configuration, not by this set, which defines only the flag and its
  default. A caller can read the declared value of every query through the caller-facing
  `capabilities` verb (`INTF-WIRE-CAPS-1`), and one-off watches are not part of that answer: they
  are per entity.
- **`INV-JIRA-WATCH-5`** <!-- uuid: 217e27c0-c45c-4d3c-8916-c9657b450bf1 --> — **A baseline marker per query.** The daemon MUST
  keep, per configured query, a **baseline marker** (a time) set at the query's first **complete**
  run. A run of a query with no marker is a **baseline run**: every member MUST be tracked, and for
  a query that is watched every member MUST be marked watched, and the run MUST append **no
  row**. A run that does not complete (an origin failure, a truncated listing) MUST NOT set the
  marker and MUST append no row, so a baseline that takes several runs stays silent. A query added
  to the configuration later has no marker, so its first run is also a baseline run, and a member
  present at its query's baseline run is never "appearing later".
- **`INV-JIRA-WATCH-6`** <!-- uuid: b61ca878-baeb-4877-81c4-b2a33841db7d --> — **First observation and late watch.** An issue first
  seen in a watched query that already has its marker MUST append exactly one **`added`** row, the
  one first-observation kind (`INV-JIRA-CHG-2`). A **known** issue that becomes watched after its
  query's baseline, by being listed in a baselined watched query it was not in or by a one-off
  watch, MUST append exactly one `added` row (the **late watch**). A query flipped to watched after
  its baseline is the late watch of each known member: one `added` row per known member, a burst
  bounded in total by the number of known open members of that query, and the operator reads that
  count before the change. An issue whose flag was cleared by eviction is a first observation again.
  Nothing else produces an `added` row.
- **`INV-JIRA-WATCH-7`** <!-- uuid: 8449102b-bb45-45ef-8fc4-3dd9e5359ab0 --> — **Move-aside.** A daemon store moved aside
  (`INV-JIRA-PROC-3`) loses every watched flag and every baseline marker. The members of a watched
  query MUST become watched again at the next run of that query, as a baseline run, so it is
  silent and no flood follows; an issue watched only by a one-off watch MUST NOT become watched
  again, because nothing re-asserts it, and the loss is visible in the store identity the next feed
  answer carries (`INV-JIRA-CHG-9`). The daemon MUST NOT sweep to restore flags.

## Change delivery

- **`INV-JIRA-CHG-1`** <!-- uuid: 631ec164-ad37-4421-b49b-9e765054094e --> — The connector MUST own change detection. A **real
  difference** in an issue's normalised content (`INV-JIRA-CHG-3`) MUST bump the entity's version
  once and append exactly one change row recording the kinds, each changed field with its before and
  after value, the **change cause** (`query`, `refresh`, `write` or `read`), the origin's own update
  time when known, and the time. When no specific kind matches the difference, the row MUST carry
  the catch-all `fields_changed` naming the fields: a difference that yields no kind is a lost
  change, so every row MUST carry at least one kind (`INV-JIRA-OBS-2`). Each row's entity carries
  the row's own version. Failures and timestamps are not changes.
- **`INV-JIRA-CHG-2`** <!-- uuid: ede76d31-42ab-4774-aa89-2992d2501700 --> — The daemon MUST be the only source of upstream
  change kinds for the issues it serves, drawn from this closed catalog and classified per group
  from before and after content: `closed`, `reopened`, `status_changed`, `assignee_changed`,
  `priority_changed`, `labels_changed`, `parent_changed`, `title_changed`, `description_changed`,
  `comments_changed`, `renamed`, `removed`, the catch-all `fields_changed`, and the **one
  first-observation kind** `added`. There MUST be no `opened` kind: that word already names a state
  moving to open, so a first observation uses `added`, and nothing but a first observation or a late
  watch (`INV-JIRA-WATCH-6`) produces it. `entered_query` and `left_query` are membership
  information, not kinds of a stored row (`INV-JIRA-CHG-5`).
- **`INV-JIRA-CHG-3`** <!-- uuid: 1ee76dca-38c6-4abc-ab52-c19d2f147f24 --> — The compared content MUST NOT include `as_of`,
  `stale` or the update time. A change to the update time **alone** re-pulls the detail group
  (`INV-JIRA-FRESH-1`) and MUST NOT by itself append a row; any real field change it reveals MUST
  be delivered once, as a content change. A comment MUST be a visible difference through the
  comment summary (`INV-JIRA-FRESH-3`), so the update time is never used as a proxy for one.
- **`INV-JIRA-CHG-4`** <!-- uuid: aa4fc35f-1d40-4ca6-bd30-3f42a57f6fc2 --> — The daemon MUST NOT re-emit an issue that has not
  changed. A row for an issue MUST be appended only for: (1) a real difference in the normalised
  content, including `closed`, `reopened` and `removed`; (2) the first observation of a watched
  issue after its query's baseline; (3) the late watch of a known issue (causes 2 and 3 are
  `INV-JIRA-WATCH-6`). Every cause is subject to the emission gate (`INV-JIRA-WATCH-3`). There MUST
  be no age-driven sweep, no timer, no `reconcile` kind, and no acknowledgement or retry tier that
  appends a row.
- **`INV-JIRA-CHG-5`** <!-- uuid: 71ad1c78-ef6f-4915-a39f-504ab1d2e83d --> — `entered_query` and `left_query` are **membership
  information**: the daemon MUST NOT store them as change rows, because a flip that changed nothing
  about the issue would otherwise become a row and an event. The feed MAY report a query's
  membership beside its rows, and that report MUST NOT bump an entity's version. An `entered_query`
  for an issue never seen before is delivered as that issue's first-observation `added` row
  (`INV-JIRA-WATCH-6`), when the issue is watched.
- **`INV-JIRA-CHG-6`** <!-- uuid: de3bf43d-30ad-4f13-8e40-f0645c36be50 --> — The **change feed** MUST keep **one consumer
  cursor** per consumer within a store: the consumer's acknowledged position in the change log,
  with no component for the kind of entity or for a query. `changes --consumer C` MUST return the
  rows after C's position **for watched issues only**, selected by the stored flag and not by query
  membership, so a closed row or a row of an issue that has left every query is still delivered and
  a row of an issue that is not watched never is. `--query` is no longer required and MAY remain as
  a filter on membership that narrows what is returned and never gives the consumer a second
  position. Each source MUST carry `next_seq`, the position scanned up to, which can be past the
  last returned row because rows of other issues are skipped.
- **`INV-JIRA-CHG-7`** <!-- uuid: ef6505a3-2389-45b9-9d5c-50d6987d7e09 --> — **Settle, acknowledgement and first poll.** The
  settle rule of the GitHub set applies with the summary and detail groups as the awaited groups: a
  change to the summary that queues the detail group is held as a pending row until the group is
  refreshed or the settle timeout passes, so a row describes data already in the store, and a
  pending row survives a crash, a shutdown and a restart (`INV-GH-CHG-3`). Delivery MUST be **at
  least once** with **explicit acknowledgement**: a row is not acknowledged by being returned, a
  poll that returns no row can still acknowledge its `next_seq`, and calls for one consumer are
  serialized (`INV-GH-CHG-6`). A consumer's first poll MUST start at the tail of the log, taken
  after the baselines exist, so a new consumer is not handed every watched issue (`INV-GH-CHG-7`).
- **`INV-JIRA-CHG-8`** <!-- uuid: d764f2e7-fee8-4b11-b66d-114496e3e92a --> — Change rows MUST be kept for the retention interval
  and a consumer cursor unseen for the stale-consumer interval MUST be dropped. A cursor whose
  position fell outside retention MUST be answered `invalid_argument` whose message starts with
  `cursor_expired:` and tells the caller to re-run with `--reset`, and after a store move-aside the
  first poll of **every** consumer MUST get the same answer rather than silently starting at the
  tail (`INV-GH-CHG-8`). `--reset` and `--cached` keep the meaning the parent set gives them for a
  forwarded `changes`, restricted to watched issues by `INV-JIRA-WATCH-3`: a reset replays only
  watched issues, as `added`, and never as a `reconcile`.
- **`INV-JIRA-CHG-9`** <!-- uuid: 8c6f4b58-ed89-41a9-9352-74193a20b788 --> — Every feed answer MUST carry the daemon store's
  **store identity**: a value created with the store, persisted across restarts, and different for
  a store that replaced another. A consumer that compares the identity with the one it last saw can
  tell a replaced store, with its lost flags and positions, from a quiet feed.
- **`INV-JIRA-CHG-10`** <!-- uuid: 59a69e33-94bd-4a1e-8b7a-69b830d6837a --> — The daemon MUST record, per configured query, the
  time of its last **whole-query** origin answer and its last error, and MUST return them on every
  forwarded `changes` answer as `sources_freshness[]`; a cache answer MUST NOT advance the time
  (`INV-GH-CHG-11`, `INV-LEDGER-FRESH-2`). The same values MUST appear in `status`
  (`INV-JIRA-DIAG-1`).

## Budget

- **`INV-JIRA-BUDGET-1`** <!-- uuid: 3a87427f-ccd1-4f36-821c-801597f3a447 --> — Every origin call MUST pass through the
  **governor**, the live pass-through reads included (`INV-GH-BUDGET-1`). The budget MUST be
  **internal and conservative**: the daemon MUST enforce a configured **request-rate cap**
  (requests per second) and a configured **concurrency cap**, and MUST **count** its requests. The
  origin's tenant is shared with every other user's traffic, so the budget is a small share of it.
  The set assumes no quota figure for the origin: the external signal the daemon can rely on is a
  rate, not a quota.
- **`INV-JIRA-BUDGET-2`** <!-- uuid: fec1b2ee-5810-43d9-9934-ff7dfc082cac --> — When the origin refuses a request for rate
  (a `429` answer), the daemon MUST **pause background work** for the retry delay the answer states,
  or for a configured default pause when it states none, and MUST **double the pause with jitter**
  on each consecutive refusal. **Interactive reads MUST be served stale** during a pause, and an
  interactive read with nothing stored is answered `unavailable` naming the pause. The priority
  classes, the background share and the rule that interactive work keeps room (`INV-GH-BUDGET-2`,
  `INV-GH-BUDGET-3`) apply to this one request-rate budget.
- **`INV-JIRA-BUDGET-3`** <!-- uuid: 56c02035-49a3-42dc-a9c0-2f3cdc4430fb --> — Until the origin tool surfaces the origin's own
  rate-limit report (the remaining allowance, a near-limit warning, a retry delay and a reason), the
  daemon MUST run on the refusal signal and its own counter. When the tool does surface it, the
  daemon SHOULD use it to slow down before a refusal; how the tool conveys it across its own
  boundary is that tool's decision and is not part of this set. If the origin's credential kind
  changes to one that is quota-governed, the quota's signals become the budget's input and this set
  is revisited.

## Diagnostics

- **`INV-JIRA-DIAG-1`** <!-- uuid: 33da41fe-4347-40bd-a941-bd80a3d42c0e --> — The backend MUST offer `status` (`INTF-JIRA-STATUS`).
  When the daemon is down it MUST still report the supervisor probe's answer, the socket path, the
  store file's size and age and the log paths. Its exit code MUST follow `INV-EXIT-1`: 0 healthy, 2
  degraded, 3 down. It reports **counts only**: the tracked and watched counts, the per-query last
  whole-query success and last error, the per-consumer positions and the pause state, and no issue
  content (`INV-GH-DIAG-1`).
- **`INV-JIRA-DIAG-2`** <!-- uuid: 3d2299e3-b09c-493a-93f3-cb59f42c82e3 --> — `explain` MUST answer why an issue is stale or
  silent: per group its fetch time, freshness end and last error, whether the issue is tracked and
  whether it is watched and why, its queue position, any pending row, and the last change rows. It
  MUST read the store and call no origin (`INV-GH-DIAG-2`).

## Observability

- **`INV-JIRA-OBS-1`** <!-- uuid: b3638e53-5ce3-42c2-a5c3-0f2c5cc84d20 --> — The daemon MUST export the following metrics, under
  the prefix `pg_connector_issue_jira.`: `daemon.up`; `governor.paused{reason}`;
  `jira.requests{result}`; `budget.remaining`, the headroom under the configured request rate;
  `queue.depth`; `queue.oldest_overdue_seconds`; `group.staleness_seconds`; `request.duration`;
  `change.detection_delay_seconds{kind,origin}`; `change.settle{result}`;
  `query.last_success_age_seconds`; `consumer.lag_rows`; `consumer.oldest_unacked_age_seconds`;
  `consumer.redeliveries`; `tracked.entities{class}` with the classes members, other open and
  closed within grace; `watched.entities`; `store.bytes`; `store.evictions`; and
  `change.rows_without_kind`, which MUST be zero (`INV-JIRA-CHG-1`).
- **`INV-JIRA-OBS-2`** <!-- uuid: 6f53d2c7-0eae-4504-92e7-7d7921c5a607 --> — These alerts MUST exist: the daemon down; queries
  stale; a consumer stuck (its oldest unacknowledged row older than a bound, 15 minutes at first);
  the settle rule holding; the store moved aside; the tracked set above its configured size
  (`INV-JIRA-TRACK-2`); any non-zero `change.rows_without_kind`; and Jira authentication failing,
  Jira unavailable and Jira rate limited, the three alerts of the stateless backend carried onto
  the daemon's own log with the same failure classes. Each alert MUST have an automated rule test
  with a positive and a negative case, MUST be registered with the probe that checks that the alert
  set is live, and MUST NOT be active while the daemon is not deployed, because an alert that fires
  on missing data would otherwise fire forever.
- **`INV-JIRA-OBS-3`** <!-- uuid: 2c5ce80b-4d99-4b6d-a111-84b8a12f076d --> — Silence MUST be told apart from failure. The
  daemon-side inputs by which a consumer of the feed tells a watched issue that silently stopped
  being observed from a quiet feed are `query.last_success_age_seconds` and
  `consumer.oldest_unacked_age_seconds` (`INV-JIRA-OBS-1`); the daemon MUST keep both current while
  it runs, and the alerts built from them belong to the consumer's own set.

## Errors

- **`INV-JIRA-ERR-1`** <!-- uuid: 34336488-2891-4ed9-acd0-cf2412a183d0 --> — Every failure MUST be answered from the parent's
  seven-value taxonomy (`INV-ERR-1`) and the backend MUST NOT introduce a new code. A query name the
  configuration does not define MUST be answered `query_not_recognized` (`INV-ERR-3`).
  `unavailable` MUST be retryable by every caller, and every caller whose own documentation or
  prompt treats `unavailable` as terminal MUST be updated to retry, because a read can now be
  `unavailable` for want of the daemon. An authentication failure MUST make
  fetches answer `unauthenticated` while cached reads are still served with their age. A message
  that carries a machine-matched prefix (`migrating:`, `cursor_expired:`) MUST begin with it.
