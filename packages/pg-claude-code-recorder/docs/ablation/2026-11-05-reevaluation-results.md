# Rebuild re-evaluation after the stage metrics and the four cost reductions

Re-run of the [2026-11-05 insert-cost study](2026-11-05-insert-cost-results.md) with the same harness and the same
variants, after the step metrics and the four cost reductions landed: `temp_store=MEMORY` for the write phase,
one decode per transcript line, no repeated queries per file, and multi-row `fields` inserts. Raw runs are in
`2026-11-05-reevaluation-results.jsonl` (one line per run, field `binary` names the ladder step) and the profile
listing in `2026-11-05-reevaluation-profiles.txt`.

## Verdict

- The baseline rebuild on the harness corpus is 34 percent faster than in the 2026-11-05 study (median 27.7 s to
  18.24 s), but that comparison is indirect: the corpus list was rebuilt, not reused, and the machine was far less
  loaded (see Corpus and Load).
- The reductions are not all gains. Measured one commit at a time on the same corpus and load: the single decode is
  the big win (27 percent of wall), `temp_store` and the per-file query removal are not resolved from noise, and the
  multi-row `fields` insert made the rebuild **22 percent slower**, not 3 percent faster. That regression is the
  main finding and needs a follow-up; the planning figure for it was wrong on this harness.
- Parse share and write share (these settle the 28 versus 57 to 63 percent question) are in "Parse share and write
  share": the answer depends on the code state. Before the reductions the parse step was 54 percent of the parse
  phase; after them it is 29 percent, and the write side is 62 percent.
- The output-equivalence digest is identical before and after on the corpus.

## Corpus

The 2026-11-05 list is not kept, so it was rebuilt by the method of that study's Corpus section. The strides it
used were not recorded, so this is a reconstruction, not the same files.

| Item                      | Value                                                                                                                                                           |
| ------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Copy files in the store   | 55,300 (was 55,148); 31,510 of them transcripts (first 3,000 bytes contain `"sessionId"`)                                                                       |
| Transcripts by size class | 71 of 10 MB or more, 2,718 of 2 to 10 MB, 20,685 of 0.3 to 2 MB, 7,942 of 20 to 300 KB, 94 under 20 KB                                                          |
| Sample                    | the 2026-11-05 per-class counts (2, 23, 154, 1,683, 94), taken by an even stride over each class sorted by file name: 1,956 files                               |
| Bytes                     | 548,901,390 (523.5 MiB); the earlier list had 541,870,936                                                                                                       |
| Largest file              | 11,949,473 bytes                                                                                                                                                |
| Lines, records (re-run)   | 130,781 lines, **183,602 records** (the earlier list: 128,450 lines, 180,459 records)                                                                           |
| Rows written (V1)         | records 183,602; fields 1,170,104; texts 49,786; ext_assistant_usage 28,492; unknown_keys 36,731 (digest rows); sessions 1,365 statements                       |
| Where the list lives      | outside the repository, in a per-user cache directory (`pg-ccrec-e1/corpus.txt`); the files are read through symlinks in a throwaway state directory, read-only |

Because the record count differs (183,602 against 180,459, 1.7 percent more), the comparison with the old study uses
seconds per record where it matters.

## Load and method

- Host: 11 cores. One-minute load average at the start of each run: 3.26 to 6.79 over the 55 runs (the 2026-11-05
  study ran at 25 to 30, and the step-timer pricing at 36 to 47).
- All throwaway data under one `mktemp -d` directory, removed at the end; the corpus is never written to; no
  recorder binary was run; the live state directories were only read to list copy files.
- Each variant was run 4 times (V9 on the older binaries 3 times) with `nice -n 10`, interleaved round by round so
  load drift hits every variant alike. Each run under `/usr/bin/time -l`.
- The "ladder": one test binary built from the commit before each reduction and from the head (git archive of each
  commit, no branch switching): B0 = parent of the `temp_store` change, B1 = with `temp_store`, B2 = with the
  single decode, B3 = with the per-file query removal, B4 = head, with the multi-row inserts. V1 (baseline) was run
  on every rung; V0, V1T0, V2, V6 and V9 on B4.
- Differences under about 5 percent are not resolved, per the README.

## Results

Wall time is `ParseAll` plus `FinishRebuild`. Median of 4 runs unless stated; the best run is in the second column.

| Binary, variant | Change                         | Runs | Wall median (s) | Wall best (s) | Records/s median | Median vs B4 V1 |
| --------------- | ------------------------------ | ---- | --------------- | ------------- | ---------------- | --------------- |
| B4 V0           | parse only, no database        | 4    | 5.49            | 5.47          | 33,442           | 30.1%           |
| B4 V1           | baseline (head)                | 4    | 18.24           | 18.14         | 10,066           | 100%            |
| B4 V1T0         | baseline, step timers off      | 4    | 18.12           | 17.81         | 10,133           | 99.3%           |
| B4 V2           | no FTS inserts                 | 4    | 10.93           | 10.81         | 16,798           | 59.9%           |
| B4 V4           | no savepoint per file          | 3    | 18.13           | 17.77         | 10,127           | 99.4%           |
| B4 V6           | statements not prepared        | 4    | 24.56           | 24.05         | 7,476            | 134.7%          |
| B4 V9           | savepoint kept, temp_store MEM | 4    | 18.31           | 18.00         | 10,028           | 100.4%          |

Records per second is 183,602 divided by the median wall seconds (for example 183,602 / 18.24 = 10,066). Medians of
four runs are the mean of the middle two.

V9 is now the same as V1 because the baseline sets `temp_store=MEMORY` itself (18.31 against 18.24: not resolved).
V2 is 7.31 s faster than V1 (18.24 - 10.93), far more than the FTS step estimate of 1.44 s below; that gap is not
explained here (the variant replaces the FTS table with a plain table behind an ignoring trigger, and may also remove
cost that the multi-row inserts added). V4 equals V1 at head, unlike on the earlier binaries (next section).

### Before and after for the baseline

| Study            | Corpus (records)   | Load           | V1 runs | V1 wall median (s) | V1 best (s) | Microseconds per record (median) |
| ---------------- | ------------------ | -------------- | ------- | ------------------ | ----------- | -------------------------------- |
| 2026-11-05       | 180,459            | about 25 to 30 | 13      | 27.7               | 23.3        | 27.7 / 180,459 = 153.5           |
| This re-run (B4) | 183,602            | 3.3 to 6.8     | 4       | 18.24              | 18.14       | 18.24 / 183,602 = 99.3           |
| This re-run (B0) | 183,602 (pre-work) | 3.3 to 6.8     | 4       | 21.22              | 20.82       | 21.22 / 183,602 = 115.6          |

The head is 34.2 percent below the old median in total time ((27.7 - 18.24) / 27.7) and 35.3 percent per record
((153.5 - 99.3) / 153.5). That mixes the code change with a quieter machine and a different corpus list, so it is
not a measurement of the reductions. The clean measure of the reductions is the ladder on the same corpus and load:
B0 21.22 s to B4 18.24 s, 14.0 percent ((21.22 - 18.24) / 21.22); and B0 to B3, 29.5 percent
((21.22 - 14.95) / 21.22), before the multi-row insert.

### Ladder: one reduction at a time (V1 medians)

| Step                         | Binary | V1 median (s) | Best (s) | Change from the previous step    | Resolved (over 5 percent)? |
| ---------------------------- | ------ | ------------- | -------- | -------------------------------- | -------------------------- |
| parent of all four           | B0     | 21.22         | 20.82    |                                  |                            |
| `temp_store=MEMORY`          | B1     | 21.12         | 20.93    | -0.5 percent (0.10 / 21.22)      | no                         |
| single decode                | B2     | 15.42         | 14.92    | -27.0 percent (5.70 / 21.12)     | yes                        |
| no repeated queries or reads | B3     | 14.95         | 14.85    | -3.0 percent (0.47 / 15.42)      | no                         |
| multi-row `fields` inserts   | B4     | 18.24         | 18.14    | **+22.0 percent** (3.29 / 14.95) | yes, slower                |

Per-reduction comparison with the planning figures:

- `temp_store=MEMORY`: planned 6 to 8 percent (the harness acceptance was at least 4). Measured -0.5 percent wall,
  not resolved, so the 4 percent acceptance is **not reproduced**. The profile does show the intended effect: the
  `subjournalPage` frame falls from 6.30 s (32.1 percent of samples) on B0 to 0.27 s (1.4 percent) on B1 (run 2 of
  each; see the profile listing). Wall time did not follow. V9 on B0 (the same pragma through the connection string)
  gave 21.10 s against 21.22 s, so it is not a defect of how the change sets it. Why the freed time did not show
  up in wall time was not found.
- Single decode: planned 7 to 9 percent of baseline CPU. Measured: the `parse` step fell from 10.64 s to 4.92 s
  (-53.8 percent, 5.72 / 10.64), wall -27.0 percent, median CPU (user plus system) 21.33 s to 15.37 s (-27.9
  percent). The plan's estimate was far too low.
- Per-file queries: planned no figure; measured 3.0 percent, inside the noise.
- Multi-row `fields` inserts: planned about 3 percent gain; measured **22 percent loss**, next.

### The multi-row `fields` insert is slower

| Measure (V1 medians)         | B3 (before) | B4 (after) | Change                                |
| ---------------------------- | ----------- | ---------- | ------------------------------------- |
| wall (s)                     | 14.95       | 18.24      | +3.29                                 |
| `insert` step (s)            | 5.33        | 9.97       | +4.64                                 |
| `fields` table time (s, est) | 1.64        | 6.01       | +4.37                                 |
| `savepoint` step (s)         | 1.65        | 0.13       | -1.52                                 |
| `fields` statements          | 1,170,104   | 150,074    | 7.8 times fewer (1,170,104 / 150,074) |
| `subjournalPage` frame (s)   | 0.22        | 0.69       | +0.47                                 |

Per `fields` row the estimate went from 1.64 / 1,170,104 = 1.40 microseconds to 6.01 / 1,170,104 = 5.13
microseconds, against 0.94 to 1.31 microseconds per row for 500-row statements in the earlier V7 micro-benchmark
(not re-run). The savepoint step fell by 1.52 s, so the net loss is smaller than the insert step's rise. With the
savepoint removed (V4) the loss remains: B3 13.63 s, B4 18.13 s (+33.0 percent, 4.50 / 13.63), so it is not an
interaction with the savepoint. The cumulative `VdbeExec` time in the run-2 profile rose from 5.24 s to 7.66 s. The
cause was not found (statements are prepared once per shape per file, so it is not recompilation per record); the
digest is identical, so this is a speed regression, not a correctness one. It needs its own investigation.

## Step table (from the new step counters)

Seconds per step inside `ParseAll`, medians over the 4 V1 runs of each binary (a median per column, so a row does
not add exactly). `other` is the remainder of the parse phase. The `finish` row is `FinishRebuild` (index build,
FTS optimize, checkpoint), measured by the harness.

| Step (seconds) | B0 pre-work | B1 `temp_store` | B2 single decode | B3 queries | B4 head, multi-row |
| -------------- | ----------- | --------------- | ---------------- | ---------- | ------------------ |
| read           | 0.79        | 0.77            | 0.76             | 0.77       | 0.88               |
| parse          | 10.57       | 10.64           | 4.92             | 4.79       | 4.84               |
| insert         | 5.59        | 5.41            | 5.47             | 5.33       | 9.97               |
| savepoint      | 1.66        | 1.66            | 1.68             | 1.65       | 0.13               |
| commit         | 0.22        | 0.17            | 0.18             | 0.18       | 0.17               |
| other          | 0.73        | 0.80            | 0.71             | 0.60       | 0.65               |
| parse phase    | 19.59       | 19.47           | 13.84            | 13.37      | 16.59              |
| finish         | 1.63        | 1.70            | 1.61             | 1.62       | 1.68               |

`other` is under 4 percent of the parse phase at the head (0.65 / 16.59 = 3.9 percent), so the steps account for
the phase. Seconds inside `insert` by table, a sampled estimate (1 record in 16, scaled), medians:

| Table (seconds, est)  | B0   | B3   | B4   |
| --------------------- | ---- | ---- | ---- |
| fields                | 1.71 | 1.64 | 6.01 |
| fts                   | 1.29 | 1.27 | 1.44 |
| other                 | 1.58 | 1.51 | 1.54 |
| records               | 0.51 | 0.47 | 0.48 |
| texts                 | 0.10 | 0.09 | 0.09 |
| ext                   | 0.07 | 0.07 | 0.07 |
| sessions              | 0.03 | 0.01 | 0.01 |
| sum                   | 5.29 | 5.05 | 9.64 |
| `insert` step (exact) | 5.59 | 5.33 | 9.97 |

The estimates sum to 94 to 97 percent of the exact step (B4: 9.64 / 9.97 = 96.7 percent; B3: 5.05 / 5.33 =
94.7 percent). After the multi-row change `fields` is 62 percent of the insert step at the head
(6.01 / 9.64 = 62.3 percent); before it, FTS (25 percent, 1.27 / 5.05) and `other` (30 percent, 1.51 / 5.05) led.

## Parse share and write share

The question was whether the parse share of a rebuild is 28 percent (a 30 s thread sample of the real rebuild) or 57
to 63 percent (the harness's parse-only variant against the baseline). Measured here, with the same code states as
the step table:

| Code state           | Parse step / parse phase     | Write side (insert + savepoint + commit) / parse phase      | V0 / V1 (parse-only variant against baseline)                                            |
| -------------------- | ---------------------------- | ----------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| B0, before the work  | 10.57 / 19.59 = 54.0 percent | (5.59 + 1.66 + 0.22) / 19.59 = 7.47 / 19.59 = 38.1 percent  | not run on B0                                                                            |
| B3, before multi-row | 4.79 / 13.37 = 35.8 percent  | (5.33 + 1.65 + 0.18) / 13.37 = 7.16 / 13.37 = 53.5 percent  | 5.49 / 14.95 = 36.7 percent (V0 run on B4; parsing is unchanged by the multi-row change) |
| B4, head             | 4.84 / 16.59 = 29.2 percent  | (9.97 + 0.13 + 0.17) / 16.59 = 10.27 / 16.59 = 61.9 percent | 5.49 / 18.24 = 30.1 percent                                                              |

Reading it: the harness's 57 to 63 percent of 2026-11-05 matches the pre-work parse share (54 percent from the step
counters here, with read and `other` making up the remainder). The single decode then halved the parse step, so the
parse share is 29 to 36 percent now, in the neighborhood of the 28 percent thread sample. So the two measurements
were not in disagreement about the same quantity at the same time; the code changed in between. At the head the
write side is the larger stage (about 62 percent of the parse phase, 65.5 percent of the whole wall with `finish`:
(10.27 + 1.68) / 18.24), and within the write side `insert` dominates, with `fields` as its largest table only
because of the multi-row regression above; at B3 the write side was 54 percent and parse 36 percent.

These are shares on the harness corpus. The production rebuild's shares were not measured (see the last section).

## Cost of the step timers

| Quantity                                                                  | Value                                                                                              |
| ------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------- |
| V1 (timers on), 4 runs (s)                                                | 18.29, 18.14, 19.78, 18.19; median 18.24                                                           |
| V1T0 (timers off), 4 runs (s)                                             | 18.25, 17.81, 18.30, 18.00; median 18.12                                                           |
| Difference of medians                                                     | 0.12 s, 0.66 percent (0.12 / 18.12); of the best runs 0.33 s, 1.85 percent (0.33 / 17.81)          |
| One clock read (`go test -bench StepClockRead ./internal/store/`, 5 runs) | 13.41 to 13.72 ns, median 13.55 ns (Apple M3 Pro; the 2026-11-05 note said 25 to 46 ns under load) |
| Arithmetic bound                                                          | 183,602 records x 5 reads x 13.55 ns = 12.4 ms, which is 0.07 percent of 18.24 s                   |

The wall-clock A/B is inside the noise (the V1 spread alone is 18.14 to 19.78 s, 9 percent), so it does not resolve
1 percent, and the criterion "within 1 percent" is met by the median difference (0.66 percent) and the arithmetic
bound but not demonstrated by the A/B. The bound counts about 5 reads per record, as in the plan.

## Peak resident size of the rebuild

`/usr/bin/time -l`, maximum resident set size of the whole harness process (it also holds the corpus bookkeeping
and Go test machinery), in MiB. V1 at the head, 4 runs: 381.6, 370.7, 387.0, 376.4 (maximum 387.0; the peak memory
footprint of run 1 was 369.5 MiB). Other rungs: B0 392, B1 388, B2 383, B3 389 (maximum over their runs). `temp_store`
reset before the index builds makes no resident-size difference at this scale, because the in-memory temp store
holds only the sub-journal of one file's writes at a time; the deferred index sort over 183,602 rows is small. V2 (no
FTS) peaked at 297 MiB and V0 (no database) at 53 MiB.

## Output-equivalence digest

`TestAblationDigest` (build tag `ablation`) ran over the corpus on two binaries and the two output files were
compared with `diff`:

- before: the commit that pins the digest golden, the parent of the single-decode change (this has the first
  reduction, `temp_store`, which changes a pragma and no stored value; the parent of that first reduction predates
  the digest test and the later change to untimed records, so it cannot be compared row for row);
- after: the head, with all four reductions.

The seven tables are identical (row counts and SHA-256 per table):

| Table               | Rows      | Digest (first 16 hex digits) |
| ------------------- | --------- | ---------------------------- |
| records             | 183,602   | 19fbc6f98065da50             |
| fields              | 1,170,104 | a57df59a0d0ed5c3             |
| texts               | 49,786    | f88e7d05276a33bd             |
| ext_assistant_usage | 28,492    | 74acac61053d2431             |
| unknown_keys        | 36,731    | f70ece35e41f2e6f             |
| unknown_keys_total  | 115       | 5d0010e7c4fd476d             |
| bad_lines           | 0         | e3b0c44298fc1c14             |

`diff` of the two files reported no difference, so every reduction, including the multi-row insert, kept the stored
rows. Result: **identical**.

## What could not be measured

- The production rebuild (88,839 files, about 17 million records, a database far above the page cache, WAL
  checkpoints, concurrent readers). Every figure above is the harness corpus; the production parse and write shares,
  production resident size and the production gain of each reduction are not measured. The harness peak resident
  size is not the daemon's.
- Why the multi-row `fields` insert is slower, and why `temp_store=MEMORY` removed the sub-journal cost from the
  profile without shortening the wall time. Both are observations without a located cause.
- Why V2 is 7.31 s faster than V1 when the FTS step estimate is 1.44 s.
- The wall cost of the step timers to the 1 percent resolution (see above).
- The old corpus list was not available, so the comparison with 2026-11-05 is by a reconstructed list, a different
  record count, and a different load; only the ladder (same corpus, same load, same session) is a like-for-like
  comparison.
- The earlier V7 micro-benchmark of multi-row inserts was not re-run, and V0 was not run on the pre-work binary, so
  the pre-work parse share comes from the step counters only.
- The load average was low (3.3 to 6.8 one-minute average), but other agents ran on the host; differences under about 5
  percent are not resolved, which covers `temp_store` (-0.5 percent), the per-file query removal (-3.0 percent), V9
  against V1 and the timer A/B.
