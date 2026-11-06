# Parse-ahead pipeline for the rebuild: decision (gate G1)

Date: 2026-11-06. Decision: **NO, do not build the pipeline now.** This closes gate G1 of
[the rebuild speed plan](2026-11-05-recorder-rebuild-speed-and-stage-metrics.md). No code changes.

## Inputs

Measured shares come from the
[re-evaluation study](../../../packages/pg-claude-code-recorder/docs/ablation/2026-11-05-reevaluation-results.md)
(harness corpus: 1,956 files, 183,602 records, host one-minute load average 3.3 to 6.8, 4 runs per variant, medians).

Which code state: the study's head (B4) carries the multi-row `fields` insert. That change was
[reverted](../../../packages/pg-claude-code-recorder/docs/ablation/2026-11-05-multirow-fields-regression.md) after the
study, because it made the write side 22 percent slower. The current head therefore has the write side of **B3**
(all other reductions, single-row `fields` inserts), and this decision uses the B3 column. The B4 column would
overstate the write share. The reverted code was not re-run through the ladder, so B3 is the closest measured state, not
a measurement of the current head.

B3 step seconds (V1 medians): read 0.77, parse 4.79, insert 5.33, savepoint 1.65, commit 0.18, other 0.60; parse phase
13.37; `FinishRebuild` 1.62; wall 14.95.

## Arithmetic

- Parse share of the parse phase: p = 4.79 / 13.37 = 0.358 (35.8 percent). With the chunk read on the parse side:
  (0.77 + 4.79) / 13.37 = 0.416.
- Write share (insert, savepoint, commit): w = (5.33 + 1.65 + 0.18) / 13.37 = 7.16 / 13.37 = 0.535 (53.5 percent).
- A single parse goroutine feeding the single writer finishes no sooner than the slower side, so the pipelined phase is
  at least max(p, w) of the serial phase, and the speedup of the phase is at most 1 / max(p, w) = 1 / 0.535 = 1.87.
- Whole wall, `finish` stays serial: pipelined wall at least 7.16 + 1.62 = 8.78 s, so 14.95 / 8.78 = 1.70 at the very
  most. Putting `other` (0.60, session flush and bad-line writes, which touch the writer) on the writer side gives
  7.16 + 0.60 + 1.62 = 9.38 s and 14.95 / 9.38 = 1.59, a saving of 14.95 - 9.38 = 5.57 s (37 percent).
- Sensitivity: the harness corpus never spills the page cache and has no concurrent readers. The production database is
  far above the page cache, which can only raise the write share. At w = 0.70 the phase bound is 1 / 0.70 = 1.43.
  Production shares are not measured.

For comparison, the B4 column (w = 10.27 / 16.59 = 0.619) would give 1 / 0.619 = 1.62 for the phase; the regression
inflated w and so lowered the bound. The pre-work state B0 (p = 0.540, w = 0.381) is not the current code.

## Noise

Load was low for this host (3.3 to 6.8 against 25 to 30 in the earlier study), but other agents ran on it; per-variant
spread reached 9 percent (V1 at head: 18.14 to 19.78 s). Differences under about 5 percent are not resolved. The shares
are medians of 4 runs, and the step counters (not the wall) give them, so p and w are good to a few points, which moves
the bound by about 0.1. The 1.59 to 1.87 range is above the noise; it is the production transfer and the unmeasured
reverted state that are uncertain.

## Why NO

1. The gain is at most 1.59 to 1.70 on the harness wall and falls toward 1.4 if production is more write-bound, and it is
   paid once per rebuild. A rebuild is a rare, operator-accepted event (the plan rules out schema bumps for this
   reason), not a steady-state cost.
2. The write side is the larger stage and has unexplained slack: the no-FTS variant is 7.31 s faster than the 1.44 s FTS
   estimate explains, and `temp_store=MEMORY` removed its profile frame without shortening the wall. Work on the writer
   shortens the wall one for one and adds no concurrency. The pipeline's own ceiling is the writer, so it does not
   substitute for that work, and every writer reduction raises the pipeline's later payoff anyway (the bound approaches 2
   as w falls toward p).
3. The risk is high and its failures are silent. A duplicate record caused by a stale offset is reported only at
   `FinishRebuild`; barriers and lock scope must be exactly right (see the requirements below).
4. The production shares and write cost are unmeasured, so the decision cannot rely on a production gain.

## Revisit when

Re-open the question if the step metrics (M0) on a real production rebuild show that parse plus read is at least 40
percent of the phase and the writer at most 60 percent, and the rebuild duration is a cost the operator wants cut. Then
the plan MUST be written as a separate file and critiqued by an independent reviewer before landing.

## Requirements any future pipeline plan MUST meet

Established by review before this decision; recorded so they are not lost.

- It MUST hold the parse lock (`p.run`) for the whole `ParseAll`, or revalidate each file's offsets under that lock at
  insert time: `Drain` and `ParseTail` also advance offsets, and a parse-ahead worker that read an old offset would
  produce duplicate records (`reparseStale` already documents this failure).
- It MUST add barriers for `reparseStale` and for snapshot files: `parseGen` calls `EnsureIndexes`, which commits the open
  batch, and snapshot parsing reads back what it stored (`ParseAll` calls `BulkFlush` first for that reason).
- Memory MUST be bounded by decoded records in flight, not by input bytes read ahead.
- `Source.Parse` is already called concurrently with the parser by the query path: `Parser.Value` re-reads a line and
  calls `src.Parse`, so concurrent use of a source is an existing property to cite and test, not a new assumption.
- A differential test MUST compare serial and pipelined output (the existing digest, row for row) with `Drain` injected
  in the middle of the rebuild.
