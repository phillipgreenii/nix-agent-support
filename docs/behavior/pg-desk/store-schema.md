# pg-desk — store and schema

`pg-desk` keeps one SQLite store at `$XDG_STATE_HOME/pg-desk/store.db`, migrated with a version
ladder the same way `pg-pr`'s store is, plus one explicit schema cutover (see "Schema versions and
the cutover" below). It MUST be opened in WAL mode with a busy timeout, so
`serve`, the CLI, and `run` can overlap safely. Every pipeline run MUST take a per-`(type, id)`
lock (the same per-entity locking discipline `pg-pr` uses today) so a cascaded re-run and a direct
run for the same entity serialize rather than race — this will matter once `pr-pool` dispatches
handlers in parallel. Rows are keyed by `repo`, so a second repository is additive to the schema
even though Phase 9 supports exactly one (see "Out of scope" below).

## The six tables (schema version 1)

These are the tables the migration ladder builds, and the shape of every store until the cutover
below. "Schema version 2 tables" describes what the cutover changes.

- **`entity`** — the last-gathered facts per `(type, id)`, as of the time `pg-connector` reported
  them, with a staleness flag and a content hash (`head_sha` too, for files/commits). Written only
  by gather.
- **`interpretation`** — ownership, enrichment, urgency, category, feedback dispositions,
  approvals, gate state, match reasons, panel placement, `ready_to_promote`, `degraded`, and
  `sync_error`. Written by interpret; the `sync_error` field is written by sync (Phase 10, see
  [`sync.md`](sync.md)) only on a sync failure for that entity, via the SAME writer, never a
  separate column or table.
- **`xref`** — cross-reference edges (`from`/`to` type and id, the evidence that produced the
  link, first-seen and last-confirmed times). This table's schema exists in Phase 9's ladder;
  starting Phase 13 (docket `pg2-2j5ac.40`) it is written by gather's own ticket-key scan (see
  [`gather.md`](gather.md)) — one row per `(ticket key, evidence field)` pair for the Jira half —
  and read back by `run issue <jira-ticket-key>`'s reverse lookup (see
  [`run-issue.md`](run-issue.md)) and this docket's Slack-half sibling packet.
- **`annotation`** — hidden state and reason, WIP state, and per-`(PR, comment)` disposition
  overrides with who set them. Written only by the CLI — an operator or an agent, through `hide`,
  `unhide`, `wip`, `feedback set`, or `import-pg-pr-annotations` — and MUST NEVER be written by the
  pipeline. Human and agent annotations therefore survive every pipeline run by construction: no
  pipeline stage writes this table.
- **`ledger`** — entity-to-bead-id mapping by kind, the last-synced content hash, the row's last-checked
  time (`last_synced_at`, advanced on every sync of the entity even when nothing changed, so an
  unchanged anchor check is recorded here and never on the bead; see [`sync.md`](sync.md)), and the
  last-reviewed head SHA. Written by sync (Phase 10, [`sync.md`](sync.md)): a `plan`-mode row
  carries an empty `bead_id` (nothing was actually created) and a content hash of the fields the
  write WOULD set; an `apply`-mode row carries the real `bead_id` and a content hash of the fields
  the write DID set — the same hash for the same input regardless of mode, which is what makes the
  plan/apply parity check mechanical. `last_reviewed_head_sha` is meaningful only for the
  `review-request` kind row, where it is the head of the last review REQUEST sync made (not of a
  completed review). The `review-request` row also carries `first_seen_head_sha` and
  `first_seen_head_at`, the head sync is waiting out and when it first saw it (both empty when no
  head is pending; see [`sync.md`](sync.md)'s "Review settle window"). On a version-1 store those
  two columns are added to `ledger` by an idempotent column ensure on open (not a rung of the
  version ladder, so `user_version` stays `1`); the cutover drops the whole table.
- **`meta`** — schema version, last heartbeat, last run, and last sweep times. Written by
  migrations, `heartbeat`, and `sweep` (`last_sweep`, bead `pg2-gznpe`) — `last_run` is reserved in
  this same table but not yet written by any command (a separate, pre-existing gap; not this
  bead's scope). It also holds per-entity keys: `reconcile`'s own stamps and
  deferral counters (see [`operator-commands.md`](operator-commands.md)), and `sync_retry.<entity id>`, the retry
  indicator of a PR's recorded `sync_error` (bead `pg2-xb6fs`, see [`sync.md`](sync.md)'s
  "Automatic retry"): attempts so far, the retry bound, the state (`retrying`, `exhausted` or
  `non-transient`), the last failure time and, while retrying, the next retry time. It is written
  on every failed run of a PR that has a recorded `sync_error` and deleted by that PR's next
  successful run. Keeping it in `meta` needs no schema change; it has meaning only on version 1,
  since the cutover drops `sync_error`.

## Schema versions and the cutover

The store exists in two schema versions. **Version 1** is the six tables above and is what the
migration ladder builds and maintains. **Version 2** is the schema the entity change flow needs:
it reshapes some of those tables and adds two. A store moves from version 1 to version 2 only
through one explicit operator command, never as a side effect of opening the store.

The version is SQLite's `user_version`; `meta.schema_version` mirrors it so that `pg-desk status`
can print it.

### `pg-desk migrate --cutover`

- It MUST apply the whole version-2 change as ONE transaction. Either every part lands and the
  store reports version 2, or the store is left exactly as it was (same schema, same rows, same
  version).
- It MUST set `user_version` and `meta.schema_version` to 2 inside that same transaction.
- It MUST be idempotent: on a store already at version 2 it does nothing and exits 0.
- It MUST refuse a store that is neither at version 1 nor at version 2. A file with no schema at
  all has nothing to cut over.
- It is NOT a rung of the migration ladder, so no other command ever runs it. It is meant to run
  in a maintenance window in which sync is stopped: nothing runs against the old and new schema at
  the same time.

### Opening the store across the two versions

- Opening the store for ordinary use MUST run the migration ladder (which stops at version 1) and
  MUST NOT run the cutover.
- Until the old code paths are retired, that ordinary open MUST accept a store at version 1 or 2.
  It MUST refuse a store newer than version 2, and it MUST NOT rewrite `meta.schema_version` on a
  version-2 store.
- `migrate --cutover`, `status` and `doctor` MUST open the store without running migrations and
  without the version gate, so they can inspect or change a store in any state, including one that
  has not been cut over yet and one that has no schema at all. `status` and `doctor` MUST NOT fail
  merely because the store is on the old schema.
- A binary built before the cutover MUST refuse a version-2 store loudly (its own version gate
  reports the store as newer than it supports). That refusal is what makes rolling back after the
  cutover safe.
- Every command that needs the version-2 schema MUST check for it right after opening the store,
  and on a version-1 store MUST refuse with an error that says to run
  `pg-desk migrate --cutover`.
- Until the old code paths are retired, access to `entity`, `interpretation` and `xref` MUST work
  on both versions, because the cutover adds columns to `entity`, drops `interpretation.sync_error`
  and rebuilds `xref`. The old `annotation` and `ledger` access is available on version 1 only.

## Schema version 2 tables

The cutover makes these changes, and no others:

- **`entity`** gains `version` (integer, not null, default 0), `hydrated_at` (nullable text),
  `active` (integer, not null, default 1) and `list_fp` (nullable text). Existing rows take the
  defaults; `list_fp` is NULL. `list_fp` is the list fingerprint the caller observed at the entity's
  last SUCCESSFUL hydration, the baseline against which a later list fingerprint is compared. A row
  with no `list_fp` (NULL, which reads back as the empty string: a migrated row, or a row created
  by `--reset`) has no baseline and is therefore treated as changed once. The store only stores the
  value; it never computes or recomputes a fingerprint.
- **`interpretation`** loses `sync_error`. On version 2 a write that carries a sync error is an
  error, not a silent drop.
- **`change_log`** is new and append-only: `seq` (integer, autoincrementing primary key), `repo`,
  `entity_type`, `entity_id`, `version` (integer), `kinds` (a JSON array), `origin` and `at`, all
  not null, with an index on `(repo, entity_type, entity_id, seq)`. The cutover creates it empty.
- **`consumer`** is new: `name`, `type`, `cursor` (integer, not null, default 0) and `seen_at`
  (nullable), keyed by `(name, type)`. The cutover creates it empty.
- **`annotation`** becomes key/value: `repo`, `entity_type`, `entity_id`, `key`, `value`, `origin`,
  `set_by`, `set_at`, keyed by `(repo, entity_type, entity_id, key)`. The cutover copies existing
  state onto reserved keys, each with `origin` `pg-desk` and the original `set_by` and `set_at`:
  - a PR-level row with a `hidden` value becomes key `hidden`, whose value is the JSON object
    `{"value": <true|false>, "reason": <text|null>}` (a JSON boolean, not a number);
  - a PR-level row with a `wip` value becomes key `wip`, whose value is the text `true` or `false`;
  - a per-comment row with a `disposition` becomes key `disposition.<comment_id>`, whose value is
    the disposition text;
  - a row carrying none of those produces no key.
- **`xref`** records where a link came from and what it means: it gains `origin` (`derived:<extractor>`
  or `external:<actor>`), `relation`, `actor`, `acted_at` and `reason`, and its first-seen and
  last-confirmed times are kept. Its primary key becomes
  `(repo, from_type, from_id, to_type, to_id, relation, origin)`. Derived rows for an entity are
  replaced on each of that entity's hydrations; external rows persist until removed; no suppression
  state is stored. Every row that exists at the cutover becomes `origin` `derived:legacy`,
  `relation` `references` (the legacy marker), and the old cross-reference accessors read and write
  exactly those rows on a version-2 store.
- **`ledger`** is dropped. Its sync state is not carried over.
- **`meta`** is unchanged in shape; `schema_version` becomes `2`.

The cutover does not append to `change_log`, register any consumer, or write any key other than
the reserved keys above; those belong to the work that uses these tables.

## Entity versions and the change log (schema version 2)

`entity.version` and `change_log` move together, through one write primitive.

- An entity write MUST be conditional on the version the writer read. The write updates the row
  only where `version` still equals that value, sets `version` to that value plus one, and appends
  one `change_log` row naming the new version, all in ONE transaction. Either both persist or
  neither does, so a log record's version always names a snapshot that exists.
- When the stored version differs from the one the writer read (a newer snapshot exists, or the
  row is absent and the writer expected a nonzero version), the write MUST change nothing and MUST
  report a typed version conflict. An older snapshot MUST NOT overwrite a newer one. Retrying (re-read,
  re-classify, write again) is the caller's policy, not the store's.
- A first write (expected version 0) inserts the row with version 1. A row the cutover left at
  version 0 is updated in place. When two first writers race for an absent entity, exactly one
  wins and the other gets the version conflict.
- A hydration write MAY carry the observed list fingerprint. When it does, `list_fp` is set in the
  same statement and transaction as the snapshot, the version bump, `hydrated_at`, `active` and the
  `change_log` row, so a failed write or a lost version race leaves `list_fp` unchanged along with
  everything else. When the write carries no fingerprint, the stored `list_fp` MUST be preserved.
- Every lost race is counted; the count is exposed for the observability metric.
- Reads return the entity with its version and its `list_fp` (the empty string when NULL and on a
  version-1 store). On a version-1 store the version reads as 0.
- `change_log` is append-only. Helpers read it after a given `seq` for one entity type (ascending
  by `seq`, limited) and per entity (newest first, limited). A caller that must append inside its
  own transaction uses the same append primitive.
- These operations need the version-2 schema and refuse a version-1 store.

## Annotations and cross-reference links (schema version 2)

- Every annotation write (set or delete of a key) MUST append one `change_log` record with kind
  `annotation_changed` in the SAME transaction as the write; if the append fails the annotation
  write is rolled back. The record carries the entity's CURRENT version (a version of 0, a migrated
  never-rehydrated entity, is valid) and the annotation's `origin` and time. An annotation write
  MUST NOT bump `entity.version`.
- Annotating (or deleting an annotation of) an entity that has no `entity` row MUST return an error
  and write nothing. Deleting a key that does not exist changes nothing and appends nothing.
- The reserved keys are `hidden` (JSON `{"value": true|false, "reason": text|null}`), `wip`
  (`true`/`false`), `disposition.<comment_id>` (`will-fix`, `wont-fix` or `no-action`),
  `suppress.<kind>`, `force_review`, `ready_to_land` and `decider.<name>.<k>`. Each row records
  `origin`, `set_by` and `set_at`.
- The key/value annotation API is separate from the old per-column annotation API, which keeps
  working on a version-1 store only; the key/value API refuses a version-1 store.
- Cross-reference links are read and written with `origin` and `relation`. Derived links (origin
  `derived:<extractor>`, any entity type) for an entity are replaced as a set each time that entity
  is hydrated: links still present keep their first-seen time and refresh last-confirmed, links no
  longer present are deleted. Replacement never touches external rows or the legacy rows owned by the
  old accessors.
- External links (origin `external:<actor>`, with actor, time and optional reason) persist until
  removed. Each row is one claim on a link, so one link can carry a derived and an external claim
  at once, as two rows. Adding an external link when a derived link with the same source, target
  and relation exists MUST still record the external claim as its own row, so the link survives
  through that claim if a later hydration of the source entity no longer derives it. An external
  link MUST NOT override a derived one: adding, refreshing or removing an external claim MUST NOT
  write, replace or remove a derived row, and while a derived row exists it stays exactly as the
  last hydration left it. Removing an external link removes only the named actor's row; it MUST
  NOT remove a derived row or another actor's link.

## Consumer cursors and change-log pruning (schema version 2)

A consumer is a named reader of `change_log` for one entity type. Its `consumer` row holds the
`cursor` (the highest `seq` it has been handed and confirmed) and `seen_at`.

- Delivery is at-least-once. Reading and advancing are separate steps so the caller flushes its
  output between them: a read returns records after the cursor (ascending by `seq`, optionally
  limited) and MUST NOT move it; the caller advances to the last `seq` it delivered only after the
  flush. A crash between flush and advance re-delivers the same records (a duplicate); it never
  skips one. Consumers MUST tolerate duplicates.
- A read registers the consumer on first use (cursor 0) and stamps `seen_at`. A peek (the read behind
  `--cached`) has no side effect: it does not register, stamp `seen_at`, or move the cursor; an
  unregistered consumer peeks from cursor 0.
- The cursor only moves forward on advance: advancing to a `seq` at or below it changes nothing.
  Advancing or resetting an unregistered consumer is a typed not-registered error. A reset sets the
  cursor to 0 so the next read replays the log from the start (the only backward move).
- Consumers can be listed (ordered by type, then name) and forgotten (forgetting an absent consumer is
  not an error). These are store primitives; the CLI verbs belong to the `changes` phase.
- Two callers for the same `(type, consumer)` serialize through a cross-process advisory lock the
  caller holds across read, flush and advance; the second reads from the cursor the first advanced.
  Different consumers or types do not contend.
- Pruning deletes `change_log` rows that are older than the retention (default 14 days, by the row's
  `at`) AND that every non-stale consumer of the row's entity type has passed. The horizon is per
  type: only consumers registered for that type hold its rows back, and with no non-stale consumer
  every row older than retention is prunable. A consumer is stale when its `seen_at` is NULL or older
  than the stale-after parameter (default 7 days) and is excluded from the horizon. Retention and
  stale-after are parameters; wiring them to configuration belongs to the `changes` phase.

## Exit codes, telemetry, and logs

The store has no CLI surface of its own beyond `pg-desk migrate --cutover` above — it is a shared
data layer every other `pg-desk` command opens. A failure to open or migrate it surfaces as the _calling_ command's own failure: for `run`,
that is exit `1` (a store error, per [`pipeline-run.md`](pipeline-run.md)); every other command's
floor is `0` on success and non-zero when the store cannot be opened.

The store emits nothing over OpenTelemetry or Prometheus on its own (D24) and writes no logs of
its own — a store-open or migration failure is logged by whichever command tried to open it
(structured JSON on stderr for `run`; plain CLI error text otherwise).

## Out of scope

The `xref` table is now populated (Phase 13, the Jira half only — see [`gather.md`](gather.md));
the Slack half (permalink cross-references, `to_type="thread"` rows) is this docket's sibling
packet. The `ledger` table is populated by sync (Phase 10). `repos[]` support for more than one
repository is out of scope; the schema is additive-ready for it, but no packet exercises a second
repository yet.
