# pg-ccaudit keeps its events table; replacing it waits on a measured snapshot rebuild

**Status**: Proposed (the decider has not ruled)
**Date**: 2026-11-21
**Deciders**: Phillip Green II

## Context

The pg-ccaudit index (`transcripts.db`, 7.4 GB file) is dominated by the `events` table and its
indexes. The question put to the decider: keep the table, slim it, or replace it with reads from the
recorder's query API, which would let a schema bump (which clears every table and re-snapshots from
the recorder) stand in for storing the rows at all. Out of scope: CETA retention, the
`~/.claude/projects` retention, `cleanupPeriodDays`, and recorder retention itself.

## Measurement

Taken on 2026-11-21 on an APFS clone (`cp -c`) of the live database. The live file was only read
(cloned) and never opened for writing. Sizes come from `dbstat` on the clone (4096-byte pages), the
clone holding 487,244 free pages (about 1.9 GB reclaimable by VACUUM, so live data is about 5.5 GB).

| Object                         | MB        | Pages   |
| ------------------------------ | --------- | ------- |
| events (table)                 | 2564.5    | 656,519 |
| idx_events_session             | 664.4     | 170,085 |
| tool_calls                     | 439.3     | 112,470 |
| idx_events_ts                  | 388.6     | 99,483  |
| sqlite_autoindex_events_1 (PK) | 326.9     | 83,694  |
| idx_events_side                | 271.4     | 69,481  |
| idx_events_src                 | 133.3     | 34,128  |
| assistant_text                 | 119.5     | 30,595  |
| user_text                      | 84.8      | 21,721  |
| tool_results                   | 61.8      | 15,816  |
| everything else                | about 150 | -       |

Events plus its five indexes: 2564.5 + 664.4 + 388.6 + 326.9 + 271.4 + 133.3 = 4349.1 MB, about 79%
of the 5.5 GB of live pages. Row counts: 12,632,252 events, 540,120 tool_calls, 539,977
tool_results, 7,180 sessions. The events span 2026-10-01 to 2026-11-21 (51 days); 3,583,608 rows
(28%) are older than 30 days.

**Not measured: the snapshot rebuild (wall time, pages, query-timeout count).** The rebuild was to be
run as `pg-ccaudit ingest` into an empty database (the state a schema bump leaves behind: every table
and every feed cursor cleared) against the live recorder. The drain sandbox's tool approver denied
the run (an ingest is a long write to the persistent index and needs operator authorization), so no
figure for the rebuild exists, and the only recorded re-ingest cost (2,430 files, 1.3 GB, 35 s) still
predates the recorder feed. This ADR does not substitute an estimate for it.

## Options

| Option  | What it does                                                                                   | Pros                                                                                  | Cons                                                                                                                                                           |
| ------- | ---------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Keep    | Leave `events` as is; index drops stay a separate, smaller decision                            | 24 of the 26 lenses join `events`; the inventory shows the recorder cannot serve them | About 4.3 GB stays                                                                                                                                             |
| Slim    | Drop `idx_events_src` and `idx_events_side` (about 405 MB measured) after rewriting two lenses | Cheap, reversible, no re-ingest (`DROP INDEX IF EXISTS` on open)                      | Two lens SQL rewrites and a lens version bump; saves under 10% of the file                                                                                     |
| Replace | Stop storing `events`; lenses read the recorder, and a schema bump rebuilds from its snapshot  | Largest saving (up to about 4.3 GB)                                                   | The recorder lacks `signature`, `lead_cmd`, `prompt_source`, `interrupted`, hook fields, a general join, and has a 25 s read deadline; unmeasured rebuild cost |

## Decision (proposed)

**Keep `events`.** Do not replace it now. The recorder cannot serve the lenses that read it (the
column inventory in `packages/pg-ccaudit/docs/events-column-inventory.md`), and the rebuild cost that
would make a schema bump an acceptable cure is unmeasured. Revisit **replace** only after an operator
authorizes one measured rebuild (an `ingest` into an empty database against the live recorder, with
wall time, final page count by `dbstat`, and the recorder query-timeout count recorded). Slimming the
two indexes remains the separate follow-up decision already open; this ADR neither blocks nor
makes it.

The build-tagged recorder test (real client and daemon against a fixture transcript, row counts after
a schema bump equal to those before) is **deferred**: the decision is not replace, so the test has no
change to guard; it becomes the first work item if replace is later accepted.

## What is lost if recorder `remove.after` is enabled

`remove.after` (30 days, off by default) deletes recorder data older than the window. pg-ccaudit's
database is a derived copy of the recorder feed, with the transcripts as the only other source. With
it enabled:

- A schema bump (or a deleted database, or an expired cursor resync) rebuilds from the recorder
  snapshot and so restores only the last 30 days. Today that would drop 3,583,608 of 12,632,252
  events (28%), and the share grows by about 250,000 rows a day until the window is full.
- Every lens that windows past 30 days, and every error body, tool call and tool result older than
  that, would be unrecoverable after the rebuild; the existing database is then the only copy.
- This is a second reason against **replace**: with no stored `events`, history older than the
  recorder window would not exist anywhere once `remove.after` is on. Keep makes the database the
  long-term record, at the storage cost above.

## Consequences

- No code change. `SchemaVersion` stays 4.
- The measurement above is a floor: the follow-up that unblocks **replace** is the operator-authorized
  rebuild run, which also needs a recorded query-timeout count.
