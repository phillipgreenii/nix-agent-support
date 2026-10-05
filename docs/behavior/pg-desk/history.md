# pg-desk — typed groups and history

`pg-desk pr`, `pg-desk issue` and `pg-desk thread` are the three entity-type command groups every
typed verb hangs off (the existing top-level verbs — `show`, `open`, `hide`, `unhide`, `wip`,
`feedback`, `run` — are unchanged). `pg-desk <type> history <id> [--limit N]` is the first typed
verb: it lists the change records of one entity, newest first (entity-change-flow design 6.9, 9.2).

## Behavior

- `<id>` is the entity's canonical id. For `pr` it accepts `OWNER/REPO#N`, a PR URL or a bare
  number and resolves it exactly as `hide`/`show` do; for `issue` and `thread` it is taken
  verbatim (an issue's tracker key or bead id, a thread's `<channel>/<ts>`).
- `--limit N` returns at most the N newest records. `0` (the default) means all; a negative
  value is an error.
- Text form, one line per record, two spaces between fields: `<seq>  <at>  <kinds joined by ", ">
origin=<origin>`, with `<at>` exactly as stored (RFC3339 UTC). Example:

  ```text
  18422  2026-09-29T14:03:11Z  head_changed, feedback_changed  origin=pg-connector
  18391  2026-09-29T09:47:55Z  reconcile  origin=sweep
  ```

- `--json` (or `PG_DESK_OUTPUT=json`) prints `{"records": [...]}` — NOT the changes envelope —
  with the record shape of contract `pg-desk.changes/v1`: `seq`, `type`, `id`, `title`,
  `version`, `kinds`, `origin`, `at`. `title` is display only and is the empty string when the
  entity row is unavailable. An entity with no records yields `{"records": []}`.
- `history` is read-only: it writes nothing and moves no consumer cursor.

## Old-schema refusal

`history` has no old-schema counterpart, so on a store not yet cut over it refuses with the
store's error, which says to run `pg-desk migrate --cutover`, and exits `1`. The existing
top-level verbs, `serve`, `status` and `doctor` keep working on the old schema until the cutover.

## Exit codes

`0` on success (including no records); `1` on any error. Exit codes `2` (partial) and `3` (total
failure) are reserved for typed verbs that consult watched queries or hydrate (`changes`,
`refresh`); `history` does not use them. pg-desk keeps its own exit-code scheme and does not bind
it to pg-router's.

## Telemetry and logs

`history` emits nothing over OpenTelemetry or Prometheus and has no structured-log contract —
only ordinary CLI error text on failure.
