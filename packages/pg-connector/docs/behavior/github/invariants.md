# Invariants — pg-connector-github

The rules the daemon-backed GitHub backend follows. See the [glossary](glossary.md),
[actors](actors.md), [interfaces](interfaces.md) and [journeys](journeys.md). Each definition's
identity is a **stable UUID** minted at its definition (`INV-3`); the typed name is a mutable,
topic-namespaced label (`INV-GH-<TOPIC>-<n>`) so a name this set cites from the parent set never
collides with one of its own.

Numeric defaults (time-to-live values, hard ages, caps, shares, batch sizes, timeouts) are **not**
fixed by these rules: they are set from measurement and recorded in the decision docs. A rule below
that names a window, a cap or a share means whatever value the decision docs set for it.

This set owns the backend's behavior. The umbrella's own obligations toward a backend that owns its
cache and changes are the parent set's (`INV-CACHE-9`, `INV-LEDGER-FRESH-5`) and are cited, never
restated.

## Process and placement

- **`INV-GH-PROC-1`** <!-- uuid: 5a69083b-9573-4cce-9de1-114ecfe3c21d --> — The backend MUST run one **daemon** per upstream
  rate-limit domain (one credential on one host), and that daemon MUST be the **store's only
  writer**. The backend in **client mode** MUST forward each request to the daemon and MUST NOT open
  the store and MUST NOT call the origin. The cache, the refresh policy and the change log live in
  the backend, never in the umbrella: this backend is stateful, and no other backend is required to
  be (`INV-STATE-1`, ADR 0090).
- **`INV-GH-PROC-2`** <!-- uuid: 354a199c-b3f3-4313-942d-9d95e4a17fb3 --> — When the daemon cannot be reached, the client MUST
  answer `unavailable`, and the message MUST name the socket path and the supervisor's restart hint
  (where no supervisor exists, a command that starts the daemon in the background). The client MUST
  NOT read the store and MUST NOT call the origin directly: an outage is made visible, never hidden
  behind old or direct content. The client MUST answer `capabilities`, including its `cache_opt_out`
  and `owns_changes` declarations, without contacting the daemon, so an outage can never be mistaken
  for a backend that opted out of the umbrella's cache. A client MUST retry a refused connection
  with backoff for part of the request's own deadline before it answers, so a supervisor restart is
  survived. It MUST answer `version_mismatch` only when the daemon's protocol version is outside the
  supported range.
- **`INV-GH-PROC-3`** <!-- uuid: 9985e59b-1134-4c08-a0ef-9c6838aa3df8 --> — The application MUST behave identically on every
  supported platform and MUST do everything except restart itself: create its state directory with
  restrictive permissions, take a single-instance lock, remove a stale socket, open and migrate the
  store, rotate its own logs, emit its own telemetry, and drain on a shutdown signal (stop accepting
  connections, finish in-flight writes, append every pending change row, release the lock). The
  supervisor wrapper MUST stay minimal (`INTF-GH-SUPERVISION`). The application MUST take every
  setting it needs, including the origin host, the tool paths, the state and socket paths, its
  queries and its settings, from the one rendered config file and MUST NOT take any of them from the
  environment (`INV-STATE-1`); environment overrides exist only for tests and a comparison harness,
  so a daemon started by hand with an empty environment behaves exactly as it does under its
  supervisor. The daemon MUST reject a peer whose user identity differs from its own and MUST refuse
  to start with a socket path longer than the platform allows, saying so.
- **`INV-GH-PROC-4`** <!-- uuid: ee521e3d-627b-487b-a315-f3e09b47f723 --> — On start the daemon MUST accept connections before
  a long migration and answer `unavailable` with a `migrating:` message meanwhile. A store whose
  schema is **newer** than the binary (a downgrade) MUST make the daemon refuse to start with a clear
  log line and an alert, and the daemon MUST NOT move that store aside. A store that is **corrupt**,
  or whose migration fails, MUST be moved aside with a timestamp, the daemon MUST start empty with
  the governor limiting the refill, and an alert MUST fire; moved-aside stores older than the expiry
  MUST be deleted. A shutdown MUST lose no pending change row (`INV-GH-CHG-3`).

## Freshness

- **`INV-GH-FRESH-1`** <!-- uuid: 97b154fd-fb56-4764-88d8-a5b99a27cc29 --> — The daemon MUST keep two caches, each with its own
  staleness settings. The **query cache** MUST map a query to its ordered list of entity ids and
  nothing more, with a complete flag. The **entity cache** MUST map an entity id to the full object,
  held as **field groups**; every field of a PR and of a CI run MUST belong to exactly one group,
  and each group MUST be fetched, refreshed and expired on its own terms. A group's freshness MUST
  be one of: a window the decision docs set; "until the head or base commit changes" (files and
  commits); or, for a PR's CI runs, the window rule of `INV-GH-FRESH-10`. Merged and closed PRs MUST
  use one longer terminal window for every group.
- **`INV-GH-FRESH-2`** <!-- uuid: 3c15f744-e205-425e-adc0-6299a8165782 --> — An entity read MUST record the access, which keeps
  the entity in the keep-alive set. When every group the op needs is inside its freshness window the
  daemon MUST answer from the store with **no origin call** (a local hit). Otherwise it MUST queue
  the stale groups at the interactive class, fetch them **in parallel**, and wait up to the client's
  deadline; concurrent requests for one (entity, group) MUST share one fetch (single-flight); a
  stale summary MUST be fetched at once, with spare batch slots filled, and an interactive request
  MUST NOT wait for a batch to fill. A fetch still running at the deadline MUST NOT be cancelled: it
  completes in the background and is stored. The answer at the deadline MUST be the stale content
  when it is inside expiry and `unavailable` otherwise, and the same rule applies when the origin
  fails. A failed fetch MUST leave the previous content in place.
- **`INV-GH-FRESH-3`** <!-- uuid: 56e0314b-5577-4dab-a6b6-9790cf2e4d7a --> — An entity MUST be in the **keep-alive set** while it
  is a member of a configured query or was accessed within the keep-alive window, and the daemon
  MUST refresh the summary of every entity in the set on the summary window, in batches. An entity
  outside the set MUST NOT be refreshed. Once every group of an entity is older than expiry, the
  entity and its groups MUST be evicted; change rows MUST be kept for their own retention
  (`INV-GH-CHG-8`). The `pending` group MUST NOT be refreshed in the background; it is read through
  only when asked for.
- **`INV-GH-FRESH-4`** <!-- uuid: b7632d42-0e2a-46c0-9533-c2d8b26653f3 --> — The daemon MUST **revalidate** an entity's summary
  against the stored one. When the fingerprint is unchanged it MUST extend the freshness window of
  the detail and conversation groups, and MUST NOT extend either past its **hard age**: a listing
  fingerprint cannot see merge state or an edited comment body, so the hard age bounds how long such
  a change can go unseen. A changed fingerprint MUST queue the dependent groups: a changed head
  commit queues files, commits and CI runs; a changed base commit or base branch queues files,
  commits and detail; a changed comment, review or thread count, or update time, queues
  conversation; a changed known mergeability value queues detail; a changed CI rollup queues runs.
- **`INV-GH-FRESH-5`** <!-- uuid: a1ff9a3a-87da-43a5-b184-b734c65c55cd --> — Each files, commits and runs row MUST record the head
  and base commit it was fetched for, and MUST be fresh only while those match the current summary.
  Content fetched for an old head MUST NOT be served as content for the new one, and MUST NOT settle
  a change (`INV-GH-CHG-3`) for it.
- **`INV-GH-FRESH-6`** <!-- uuid: eb2ea1ae-bb0a-41f8-bf01-1893a3e8bc3f --> — The store MUST survive a restart, and a group still
  inside its freshness window MUST be served after a restart with **no origin call**, so a restart
  never causes a spike of origin spend. The scheduler MUST rebuild its queue from the stored due
  times, and after a long outage the governor MUST limit the catch-up rate, so a restart never spends
  more than the configured budget (`INV-GH-BUDGET-1`).
- **`INV-GH-FRESH-7`** <!-- uuid: 3a616939-57eb-4ba1-9a84-e3bbbd6c3e75 --> — The origin recomputes mergeability lazily and
  normally passes through `UNKNOWN`, so a move from one known value to another can hide behind
  `UNKNOWN`. The daemon MUST compare mergeability against the **last known value**, carried forward:
  `UNKNOWN` MUST NOT be a change and MUST NOT by itself be a reason to refresh; a move from one known
  value to a different known value MUST be a `mergeability_changed` change regardless of any
  `UNKNOWN` between them. On the wire the daemon MUST return the carried-forward value, and
  `UNKNOWN` only when no known value has been seen for the **current head and base**: a change of
  head or base commit MUST clear the carried value, because the old head's mergeability says nothing
  about the new one. A fingerprint the umbrella computes from the returned value inherits the rule.
- **`INV-GH-FRESH-8`** <!-- uuid: 0236db7d-ba4b-433f-8fec-91f4acbf5930 --> — Every read answer MUST carry `served_from`, `stale`,
  `age_seconds` and `groups[]` (`INTF-GH-WIRE`); a stale answer is `served_from` cache with `stale`
  true, and `age_seconds` is the age of the oldest group served. A read with `fresh: true` MUST force
  an origin fetch of every group it needs; the backend, not the umbrella, decides how current its
  answer is (`INV-CACHE-8`). Polling paths SHOULD NOT use `--fresh`, because the backend keeps the
  data fresh itself. A caller that reads back its own **out-of-band write** MUST use `--fresh` on
  its read or the `refresh` op (PLANNED, not available), and MUST NOT wait for the freshness window.
- **`INV-GH-FRESH-9`** <!-- uuid: 1a3ef125-102e-4ad9-9003-b18f104b17e6 --> — A `list` query name MUST be configured; an unknown
  name MUST be answered `query_not_recognized` (`INV-ERR-3`). A query's key is its name plus its
  arguments. An argument that only filters a configured query's result (a time range) MUST be
  applied locally to the cached ids and MUST cost no origin call; an argument that changes what the
  origin is asked MUST make an **ad-hoc key**, cached for its own window and never re-run by the
  scheduler (its ids still join the keep-alive set by being read). A query MUST be re-run on its
  own window by the scheduler when configured. An answer MUST stay summary-level. A departure from a
  query MUST be recorded only from a **complete** listing and only after one entity read confirms
  it, so a closed or merged PR is told apart from one that merely left the query's filter
  (`INV-GH-CHG-4`).
- **`INV-GH-FRESH-10`** <!-- uuid: a99f71b0-b86b-4549-a594-322fb7681b4b --> — A PR's CI runs MUST be kept fresh with the PR: a
  PR in the keep-alive set has its runs refreshed in the background, and a runs change MUST be a
  change of the PR (`INV-GH-CHG-10`). The **detection bound** MUST hold: a CI change the rollup shows
  is seen within one summary window plus the settle time; a change behind an unchanged rollup is seen
  within the active window while a current-head run is queued or in progress, within the failed
  window while the current head has a non-passing completed run (the case that decides whether a PR
  is reviewable), and otherwise within the CI hard age. Runs on an older head MUST NOT make a PR
  active. **Job results** MUST be cached by (run, attempt), because a completed attempt's jobs never
  change; a failed job fetch MUST keep the last known jobs for that attempt, record the group's last
  error, and MUST NOT be a content change, so a transient error never flips a PR between blocked and
  reviewable.
- **`INV-GH-FRESH-11`** <!-- uuid: 16f59994-0b4e-4da9-a2e9-c9809c309c99 --> — The daemon MUST resolve the acting identity at
  start and periodically. When it changes (a different account is selected), the daemon MUST flush
  every identity-scoped datum (pending reviews, results of queries that name the acting identity,
  and the view of the acting identity's posted reviews) and MUST record the switch in its log. The
  store MUST NOT hold a credential: the credential stays with the tool that holds it.
- **`INV-GH-FRESH-12`** <!-- uuid: b141c9d9-f8ae-4945-95d5-ecc14eb25852 --> — The only writes are `review_submit` and
  `rerun_failed`. A write MAY read the origin for its own preconditions, outside the cache. Writes to
  one PR MUST be serialized and writes to different PRs MAY run in parallel. After a success the
  daemon MUST mark the group the write affects (pending for `review_submit`, runs for `rerun_failed`)
  stale and queue it at the write-invalidated class; a refresh that **started before** the write MUST
  NOT overwrite content stored after it and MUST NOT log a change from it. A client that retries after
  `unavailable` MUST NOT create a duplicate: a posted review comment already present MUST be
  recognized as present, and a rerun of runs already re-running MUST be a no-op answer.

## Change delivery

- **`INV-GH-CHG-1`** <!-- uuid: 8f7f38a5-de4b-4678-9547-8e3567d9c046 --> — The connector MUST own change detection: every refresh
  that changes content MUST bump the **entity version** (one per entity, not per group) and
  MUST append one change row recording the kinds, each changed field with its **before and after**
  value, the **change cause** (`query`, `refresh`, `write` or `read`), the upstream's own update time when
  known, and the time. A consumer MUST NOT need a listing diff, a sweep or a PR hydration of its own
  for the types this backend serves. Failures and timestamps are not changes.
- **`INV-GH-CHG-2`** <!-- uuid: 094d209e-62d8-41e8-9666-b223bfd8fcce --> — An entity's **first fetch** MUST establish its
  baseline and append no row; a query's **first complete run** MUST establish its membership
  baseline and append no `entered_query` row; the **first fetch of a group** for an entity MUST be
  that group's baseline, adding no field to a pending row and appending no row even when the entity's
  other groups already have content. Otherwise a cold store, a moved-aside store or a PR's first CI
  read would push every open PR to its consumers at once.
- **`INV-GH-CHG-3`** <!-- uuid: 59efd12d-10f7-4b1e-a0ac-1573749f1eb4 --> — The **settle rule**: a change to the summary that queues
  dependent groups MUST NOT append its change row at once. The daemon MUST write it as a **pending
  row** in the same transaction that stores the new summary, naming the awaited groups and a
  deadline of detection time plus the settle timeout; a pending row MUST NOT be visible to
  `changes`. A pending row MUST survive a crash, a shutdown and a restart: a shutdown MUST append
  every pending row before exit, and on start the daemon MUST re-queue each row's awaited groups or
  append it at once if its deadline has passed. Only a fetch that **started after** the detection
  (and after any later write invalidation) satisfies the barrier, and its recorded head and base MUST
  match the detected summary (`INV-GH-FRESH-5`). While a row is pending for an entity, every further
  change MUST merge into it: kinds are unioned, per field the earliest before and the latest after
  are kept, a field whose after equals its before again is dropped, and the deadline does not move.
  When every awaited group is refreshed or the deadline passes, the row MUST be appended with the
  entity's version bumped once. A group not refreshed by the deadline MUST stay stale and queued, and
  MUST append its own row when it lands later with changed content. Detection MUST be delayed by at
  most the settle timeout and never lost. With no pending row, a group refreshed by itself appends
  its row at once. The row a consumer receives therefore describes data already in the store, so the
  consumer's follow-up reads are local hits.
- **`INV-GH-CHG-4`** <!-- uuid: 319c8249-f853-4cf9-99f9-f8b4b98c0e66 --> — The daemon MUST be the **only source of upstream
  change kinds** for the entities it serves, drawn from the closed catalog in `INTF-GH-WIRE`, and
  MUST classify per group from each group's before and after content. `left_query` MUST be recorded
  only per `INV-GH-FRESH-9`. `ci_changed` MUST be emitted when the summary's CI rollup changes or
  when the runs group changes in a field the catalog names.
- **`INV-GH-CHG-5`** <!-- uuid: 4eec868c-bb8f-4b7b-9261-fd9e2932c23e --> — `changes --consumer C --query Q` MUST return the rows
  after C's acknowledged position for Q, filtered to Q's members **plus** rows for entities that
  just left Q, so a consumer sees the departure. `--query` MUST stay required, so an entity a skill
  merely read is not delivered to a consumer of another query. Each change's `entity` MUST carry
  the row's **own** `version` and `head_sha`, not the entity's current ones, so a redelivered row
  keeps its identity after a newer change and two rows for one PR in one poll are told apart. Each
  source MUST carry `next_seq`, the position scanned up to, and its existing `version` MUST keep
  carrying the position count so a reader that compares it keeps working.
- **`INV-GH-CHG-6`** <!-- uuid: d5bf79c4-044c-4542-ac08-d6c6e4fff06f --> — Delivery MUST be **at least once** with **explicit
  acknowledgement**: rows are not acknowledged by being returned. A consumer that crashes before its
  caller has the rows MUST receive them again. A poll that returns no rows MUST still be able to
  acknowledge its `next_seq`, so a quiet query's position keeps moving. The cursor key MUST be
  (consumer, kind, query), because two queries share one consumer name, and calls for one key MUST be
  serialized.
- **`INV-GH-CHG-7`** <!-- uuid: 248c0b20-cf19-41b8-a0b6-77bcc82e0942 --> — The **first poll** of a new key MUST start at the
  tail of the log, taken after the query's baseline exists (`INV-GH-CHG-2`), so neither a new key nor
  a cold store pushes every open PR to its consumer at once. `--reset` MUST replay the query's
  current members as `added` and move the key to the tail, deliberately. `--cached` MUST answer from
  the store without running the query, even when it is stale.
- **`INV-GH-CHG-8`** <!-- uuid: d1e96af5-03e8-4990-bb63-d07f7976b00c --> — Change rows MUST be kept for the retention interval,
  and a key unseen for the stale-consumer interval MUST be dropped. A key whose position fell outside
  retention MUST be answered `invalid_argument` whose message starts with `cursor_expired:` and tells
  the caller to re-run with `--reset`; no new envelope field is added. After a store move-aside
  (`INV-GH-PROC-4`) the first poll of **every** key MUST get the same answer, rather than silently
  starting at the tail and skipping what changed in between.
- **`INV-GH-CHG-9`** <!-- uuid: ff6f4eb9-117d-4194-a443-467698235536 --> — For a conversation change a field MUST record the
  changed comment's id with its before and after body, each capped at a size the decision docs set,
  plus a hash of the full text, and MUST NOT record the whole conversation, so the log stays bounded
  for comment-heavy PRs.
- **`INV-GH-CHG-10`** <!-- uuid: 6fd08ee5-7cc9-4fae-af67-162f7dd0e35c --> — A CI change MUST be a change of the PR: a runs change
  MUST be logged as a row of the PR, with kind `ci_changed` and the run and job fields that changed,
  after the settle rule has refreshed the data. It is therefore delivered by `pr changes` to every
  consumer of a PR query, and one CI event MUST cause one row, not one per feed. `ci changes`
  (PLANNED, not available) MUST offer the same rows in the CI view: the PR rows for members of Q
  whose kinds include `ci_changed`, `entered_query`, `left_query` or `removed`, each change's entity
  the PR's current CI state with the row's own `version` and `head_sha`, with its own cursor key,
  its own acknowledgement and the same retention, first-poll, `--cached`, `--reset` and
  `cursor_expired:` rules. The daemon MUST refuse to create the key (C, `ci`, Q) while the key
  (C, `pr`, Q) exists, and the reverse, with `invalid_argument` naming the existing key, so one
  consumer never acts twice on one CI event.
- **`INV-GH-CHG-11`** <!-- uuid: ea2d1e2e-e07d-4936-868d-d65580032c61 --> — The daemon MUST record, per configured query, the
  time of its last **whole-query** origin answer and its last error, and MUST return them on every
  forwarded `changes` answer as `sources_freshness[]`. A cache answer MUST NOT advance the time
  (`INV-LEDGER-FRESH-2`); the umbrella stamps its ledger from the report (`INV-LEDGER-FRESH-5`). The
  same values MUST appear in `status`.

## Budget

- **`INV-GH-BUDGET-1`** <!-- uuid: f5335c40-d98a-4b27-8895-d9926c6349a6 --> — Every origin call MUST pass through the governor.
  The **cap** of each bucket MUST be hard for every class of work: nothing spends past it, and
  nothing spends below the reserve of the point-metered bucket. Where the daemon replaces another
  flow's spend on the same credential, its cap replaces that spend and does not add to it.
- **`INV-GH-BUDGET-2`** <!-- uuid: 8f25cfbd-5f24-43e6-a0ff-8e4375dcae36 --> — Each (entity, group) task MUST carry a due time and
  one of five **priority classes**, highest first: (1) interactive, a caller waiting; (2)
  write-invalidated, `refresh`-requested, and dependent groups a pending row waits on; (3) due
  re-runs of configured queries and overdue members of a configured query; (4) overdue entities in the
  keep-alive set, most recently accessed first; (5) fill, entities due within the fill horizon,
  nearest first, used only to fill spare slots of a batch already going out. Fill MUST NOT displace a
  due task. The scheduler MUST age waiting tasks so class 4 is not starved by class 3.
- **`INV-GH-BUDGET-3`** <!-- uuid: 3d9c2d1d-7fc1-4a92-b786-ecd42ebfb6e8 --> — Background classes (3 to 5) MUST stop at the
  **background share** of each bucket's cap, so the remainder stays available to classes 1 and 2; the
  share applies to the request and search buckets as it does to the point-metered one. When a cap is
  reached, interactive reads MUST be served stale or answered `unavailable` naming the cap.
- **`INV-GH-BUDGET-4`** <!-- uuid: e820a564-bb12-460b-ada1-55a26453b110 --> — An op that is not cached (`search`, `list_activity`,
  `get_logs`) MUST be performed by the daemon, as a **metered pass-through**: the governor sees and
  limits its spend, and nothing is stored. A write is class 2.
- **`INV-GH-BUDGET-5`** <!-- uuid: 08b3ffb4-0b25-4850-809d-96c3b3e5248e --> — A **secondary limit** from the origin MUST pause
  the background classes for the stated time while interactive reads are served stale. A primary
  limit below the reserve MUST stop the background classes. An **authentication failure** MUST make
  fetches answer `unauthenticated` while cached reads are still served with their age, and
  `auth_status` MUST report the failure. A burst that exhausts a background share MUST slow every
  entity's refresh evenly until it drains, never starve the interactive classes.

## Diagnostics

- **`INV-GH-DIAG-1`** <!-- uuid: aa40c5a9-849d-4e52-b478-f68f013c6d4e --> — The backend MUST offer `status` (`INTF-GH-STATUS`).
  When the daemon is down it MUST still report the supervisor probe's answer, the socket path, the
  store file's size and age, and the log paths. Its exit code MUST follow `INV-EXIT-1`: 0 healthy, 2
  degraded, 3 down.
- **`INV-GH-DIAG-2`** <!-- uuid: 74d55235-019d-44ad-babd-91f3de1ac04f --> — `explain` (PLANNED, not available), through
  `pr explain <id>` and `ci explain <pr-id>`, MUST answer why an entity is stale: per group its fetch
  time, freshness end, hard-age end and last error, the head and base it was fetched for, its queue
  position, any pending row (awaited groups and deadline), and the last change rows. It MUST read the
  store and call no origin.
- **`INV-GH-DIAG-3`** <!-- uuid: 3ba638f6-aacc-4afe-9b13-7f3eea58c455 --> — `refresh` (PLANNED, not available), through
  `pr refresh <id>` and `pr refresh --query Q`, MUST queue the named groups at the write-invalidated
  class and return at once, without waiting for the fetch.

## Errors

- **`INV-GH-ERR-1`** <!-- uuid: b0c473b6-ea01-4740-93fc-35cd4f05d38e --> — Every failure MUST be answered from the parent's
  seven-value taxonomy (`INV-ERR-1`) as the failure catalog of `INTF-GH-WIRE` prescribes, and the
  backend MUST NOT introduce a new code. `unavailable` MUST be retryable by every caller, and a
  caller's own documentation MUST say so. A message that carries a machine-matched prefix
  (`migrating:`, `cursor_expired:`) MUST begin with it.
