# pg-desk — store and schema

`pg-desk` keeps one SQLite store at `$XDG_STATE_HOME/pg-desk/store.db`, migrated with a version
ladder the same way `pg-pr`'s store is. It MUST be opened in WAL mode with a busy timeout, so
`serve`, the CLI, and `run` can overlap safely. Every pipeline run MUST take a per-`(type, id)`
lock (the same per-entity locking discipline `pg-pr` uses today) so a cascaded re-run and a direct
run for the same entity serialize rather than race — this will matter once `pr-pool` dispatches
handlers in parallel. Rows are keyed by `repo`, so a second repository is additive to the schema
even though Phase 9 supports exactly one (see "Out of scope" below).

## The six tables

- **`entity`** — the last-gathered facts per `(type, id)`, as of the time `pg-connector` reported
  them, with a staleness flag and a content hash (`head_sha` too, for files/commits). Written only
  by gather.
- **`interpretation`** — ownership, enrichment, urgency, category, feedback dispositions,
  approvals, gate state, match reasons, panel placement, `ready_to_promote`, `degraded`, and
  `sync_error`. Written by interpret; the `sync_error` field is written by sync (Phase 10, see
  [`sync.md`](sync.md)) only on a sync failure for that entity, via the SAME writer, never a
  separate column or table.
- **`xref`** — cross-reference edges (`from`/`to` type and id, the evidence that produced the
  link, first-seen and last-confirmed times). Written by interpret's cross-reference step. This
  table's schema exists in Phase 9's ladder; it stays unpopulated until that step ships (Phase
  13).
- **`annotation`** — hidden state and reason, WIP state, and per-`(PR, comment)` disposition
  overrides with who set them. Written only by the CLI — an operator or an agent, through `hide`,
  `unhide`, `wip`, `feedback set`, or `import-pg-pr-annotations` — and MUST NEVER be written by the
  pipeline. Human and agent annotations therefore survive every pipeline run by construction: no
  pipeline stage writes this table.
- **`ledger`** — entity-to-bead-id mapping by kind, the last-synced content hash and time, and the
  last-reviewed head SHA. Written by sync (Phase 10, [`sync.md`](sync.md)): a `plan`-mode row
  carries an empty `bead_id` (nothing was actually created) and a content hash of the fields the
  write WOULD set; an `apply`-mode row carries the real `bead_id` and a content hash of the fields
  the write DID set — the same hash for the same input regardless of mode, which is what makes the
  plan/apply parity check mechanical. `last_reviewed_head_sha` is meaningful only for the
  `review-request` kind row.
- **`meta`** — schema version, last heartbeat, last run, and last sweep times. Written by
  migrations, `heartbeat`, and `run`.

## Exit codes, telemetry, and logs

The store has no CLI surface of its own — it is a shared data layer every other `pg-desk` command
opens. A failure to open or migrate it surfaces as the _calling_ command's own failure: for `run`,
that is exit `1` (a store error, per [`pipeline-run.md`](pipeline-run.md)); every other command's
floor is `0` on success and non-zero when the store cannot be opened.

The store emits nothing over OpenTelemetry or Prometheus on its own (D24) and writes no logs of
its own — a store-open or migration failure is logged by whichever command tried to open it
(structured JSON on stderr for `run`; plain CLI error text otherwise).

## Out of scope (Phase 9, narrowed by Phase 10)

The `xref` table exists in the schema ladder but stays unpopulated until the cross-reference step
ships (Phase 13). The `ledger` table is now populated by sync (Phase 10). `repos[]` support for
more than one repository is out of scope this phase; the schema is additive-ready for it, but no
packet in this phase exercises a second repository.
