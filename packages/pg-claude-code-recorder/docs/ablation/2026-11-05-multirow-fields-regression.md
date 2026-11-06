# Why the multi-row `fields` insert slowed the rebuild, and its reversal

Follow-up to the [re-evaluation](2026-11-05-reevaluation-results.md), which measured the multi-row `fields` insert
22 percent slower than the single-row statement and left the cause open.

## Verdict

- The multi-row insert is **reverted**; `fields` rows are written again one prepared statement per row, in field order,
  with the same upsert (last value wins, whole row replaced). No schema change, no `SchemaVersion` change.
- Cause (from the CPU profile, not from a variant that removes it): a multi-row `INSERT ... ON CONFLICT` makes SQLite
  open a statement savepoint (a sub-transaction so the statement can be undone half way), and SQLite then calls the
  `fts5` virtual table's savepoint method, which flushes the FTS5 in-memory hash to disk and triggers merges. A
  single-row insert needs no statement savepoint. So the FTS rows lost their batching: the fields statements forced an
  FTS5 flush each, for about 150,000 statements.
- Evidence: `_sqlite3VtabSavepoint` (through `_fts5SavepointMethod` to `_sqlite3Fts5FlushToDisk`) is 3.57 s (19.5
  percent of samples) on the head against 1.36 s (8.6 percent) with single-row inserts, in run 3 of each (the 1.36 s is
  the per-file `SAVEPOINT`, present in both). `subjournalPage` also rose (0.9 s on the head), the sub-journal the
  statement savepoint writes.

## Variants tried

Same harness and corpus list as the re-evaluation (1,956 files, 183,602 records), variant V1, built from the branch
head with only `flushFields` changed (git archive per variant), `nice -n 10`, interleaved. The one-minute load average
at run start was 12 to 37 (mostly 14 to 22), much higher than the re-evaluation, so absolute seconds are higher too.

| Variant                                          | Runs | Wall median (s) | CPU median (s) | Against single-row |
| ------------------------------------------------ | ---- | --------------- | -------------- | ------------------ |
| H1 single row per statement (the reverted state) | 8    | 20.68           | 18.51          | baseline           |
| H0 head: multi-row, up to 500 rows               | 8    | 24.45           | 22.10          | +18.2%             |
| H2 chunks of 16, 4 or 1 rows (3 fixed shapes)    | 4    | 22.90           | 21.52          | +10.7%             |
| H3 chunks of 64, 16, 4 or 1 rows                 | 4    | 24.57           | 22.15          | +18.8%             |
| H4 head with `INSERT OR REPLACE` and no upsert   | 4    | 23.91           | 22.26          | +15.6%             |

Against single row: H0 (24.45 - 20.68) / 20.68 = 18.2 percent; H2 (22.90 - 20.68) / 20.68 = 10.7 percent; H3 (24.57 -
20.68) / 20.68 = 18.8 percent; H4 (23.91 - 20.68) / 20.68 = 15.6 percent. Per-run spread inside a variant was large
(H1 from 16.9 to 34.1 s, H0 from 19.4 to 31.8 s: 8 runs sorted, median the mean of the middle two), so only the
ordering is reliable: every multi-row form is at or above 10 percent slower than single row, and none is within the
5 percent noise rule. Medians of 8 runs are the mean of the 4th and 5th values; of 4 runs the mean of the 2nd and 3rd.

What this rules out:

- Re-preparing a statement for each distinct row count per file (the statement text is prepared once per shape per
  file): H2 and H3 bound the shapes to three or four and stay slow.
- The 500-row batch size, per-batch allocation and row-value binding: smaller chunks do not help, and H1 binds the same
  values.
- The `ON CONFLICT DO UPDATE` clause alone: H4 has none (`INSERT OR REPLACE`, the same whole-row replacement) and is
  still slow, consistent with the statement savepoint being the cost, not the upsert.
- The unsorted insert order or page splits on the WITHOUT ROWID table: the rows are in the same order in every variant.

## Why not keep a multi-row form

A multi-row statement is what triggers the statement savepoint, so no multi-row variant tried avoids it, and the
single-row statement it replaces is already prepared once per file. The planning figure (a 3 percent gain) came from the V7 micro-benchmark, a harness-local copy of the insert into the
production `fields` table alone; it does not run the FTS5 table in the same transaction, which is where the cost
arises, so it could not predict this.

## Tests

`TestOutputEquivalenceDigest` passes unchanged against the committed golden. The repeated-name test
(`TestFieldsRepeatedNameKeepsLastWinsAndOrder`, formerly the batch-boundary test) is kept: 1,200 names with repeats far
apart, last value wins with the whole row replaced, and `field_names` ids follow field order. The test of the
engine's variable limit is removed with the batch size it protected.

## Not measured

- The load was high and uneven, so the exact percentages carry an uncertainty of several points; only "multi-row is
  slower than single row by well over 5 percent" is resolved.
- The revert was not re-run through the ladder after the edit (the reverted code is the H1 build's logic: single row,
  same SQL text as before the multi-row commit); the multi-row change was reversed with `git apply -R`.
- The profile attribution is from one run of each of H0 and H1 (run 3).
- The production rebuild, as in the re-evaluation.
