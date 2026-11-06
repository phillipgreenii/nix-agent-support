# Why `temp_store=MEMORY` removed the sub-journal samples but not the wall time

Follow-up to the [re-evaluation](2026-11-05-reevaluation-results.md), which measured `temp_store=MEMORY` on the bulk
writer at -0.5 percent wall (not resolved) although the CPU profile frame `subjournalPage` fell from 6.30 s (32.1
percent of samples) to 0.27 s. Per-run rows are in `2026-11-05-temp-store-runs.tsv`.

## Verdict

- **Cause (supported by measurement, mechanism not proved).** The profile frame was never 6 s of elapsed time. In the
  run without the pragma, the whole process spent 1.38 s in the kernel (system CPU), yet the profile puts 5.42 s under
  the `pwrite` call (`libc.Xpwrite`, 3.9 times the process's total system time) and the whole `insert` step lasts 6.14 s
  by its own clock. So most of those samples are not time the program spent writing. The sub-journal write itself
  costs well under a second of wall time, and the pragma recovers part of that: about 0.2 to 0.5 s.
  The CPU time that the profile had put on `pwrite` shows up on other frames when the writes go away
  (`pthread_cond_signal` and `madvise`), and the total sample count follows CPU time, not the frame
  (see "Profile versus clocks"). That the samples are biased towards the return from a system call (a timer signal
  is delivered when a thread returns to user mode) is the explanation that fits, but it was not tested directly.
- **Decision: keep the pragma and its reset.** It is a pure win on the counters that load does not disturb
  (instructions retired -0.7 to -0.8 percent, system CPU -11 to -20 percent) and is not slower in any pooled
  comparison; wall time is not resolved either way (about 1 percent). Removing it would change tested behavior
  for no measured gain and no measured loss. The code comment that promised "a fraction of a temp-file write" is
  corrected to the measured size of the effect. No test changes, so the output-equivalence digest is untouched.
- The planning figure of 6 to 8 percent (from the 2026-11-05 insert-cost study) is **not reproduced**: that study
  read it from the profile, and the profile overstates it.
- Not resolved: the exact mechanism of the sample bias, and whether the page cache absorbs the temp-file writes
  (the I/O counters are useless here, see below).

## Method

- Host: 11 cores, one-minute load average 4.7 to 9.7 at run start for the main set (a later V1/V2 set rose to 30).
  Same corpus list as the re-evaluation (1,956 files, 183,602 records, 548,901,390 bytes, kept in a per-user cache
  directory outside the repository), read-only; the throwaway state lived under one `mktemp -d` directory, removed at
  the end. No recorder binary was run and no live state directory was touched.
- One test binary (tag `ablation`) built from the branch head by `git archive` into a throwaway directory, with a
  temporary toggle that exists only in that copy: an environment variable makes `beginRebuild` skip `PRAGMA
temp_store=MEMORY` (the reset in `buildIndexesLocked` is harmless when it was never set). The toggle is not in
  the committed tree. A second temporary toggle there turns the CPU profile off, to test whether the profiler itself
  disturbs the run.
- Variants, always V1 unless stated, `nice -n 10`, interleaved round by round, each run under `/usr/bin/time -l`:
  `ON` pragma kept (the head), `OFF` pragma skipped (the same binary), `NOSP` variant V4 (no per-file savepoint,
  pragma skipped). Each with the profile on (5 rounds) and, as `PON`, `POFF`, `PNOSP`, with the profile off
  (5 to 6 rounds; the first `PON` run failed on a stale work directory and is excluded).
- Differences under about 5 percent are not resolved, per the README.

## Results

Wall is the harness's `ParseAll` plus `FinishRebuild` seconds. Medians of 5 runs (6 where noted: the mean of the
middle two); CPU is user plus system.

| Variant (profile)     | Runs | Wall median (s) | Wall best (s) | User (s) | System (s) | Instr. (G) | Cycles (G) | `insert` step (s) |
| --------------------- | ---- | --------------- | ------------- | -------- | ---------- | ---------- | ---------- | ----------------- |
| ON, pragma (on)       | 5    | 15.72           | 15.57         | 14.77    | 1.13       | 227.97     | 58.86      | 5.66              |
| OFF, no pragma (on)   | 5    | 16.66           | 15.99         | 15.28    | 1.41       | 229.84     | 61.40      | 6.16              |
| NOSP, no savepoint    | 5    | 14.65           | 14.41         | 13.75    | 1.11       | 211.66     | 55.41      | 6.31              |
| PON, pragma (off)     | 5    | 16.64           | 15.97         | 15.33    | 1.26       | 230.02     | 60.74      | 5.81              |
| POFF, no pragma (off) | 6    | 16.47           | 15.81         | 15.30    | 1.41       | 231.54     | 61.70      | 6.05              |
| PNOSP, no savepoint   | 6    | 15.71           | 14.13         | 14.54    | 1.23       | 213.98     | 57.92      | 6.67              |

Derived numbers:

- Profile on, medians: OFF minus ON = 16.66 - 15.72 = 0.94 s, 0.94 / 16.66 = 5.6 percent. The paired round
  differences (OFF minus ON) are -0.04, 0.78, 0.42, 1.05, 0.27 s: mean 2.48 / 5 = 0.50 s (3.0 percent), one round
  negative, so this sits at the edge of the noise.
- Profile off: POFF minus PON = 16.47 - 16.64 = -0.17 s on medians; the paired differences over rounds 2 to 6 are
  -0.16, -0.18, 0.57, -0.18, -0.87 s, mean -0.82 / 5 = -0.16 s (-1.0 percent). No gain.
- Pooled over all 10 paired rounds: (2.48 - 0.82) / 10 = 0.17 s, 1.0 percent of 16.6 s. Not resolved.
- Load-independent counters: instructions 229.84 - 227.97 = 1.87 G less with the pragma (0.81 percent), and
  231.54 - 230.02 = 1.52 G (0.66 percent) with the profile off, stable run to run (the 5 runs of each lie within
  0.7 G). System CPU 1.41 - 1.13 = 0.28 s (19.9 percent) and 1.41 - 1.26 = 0.15 s (10.6 percent). Cycles, which
  follow contention, -4.1 percent ((61.40 - 58.86) / 61.40) and -1.6 percent ((61.70 - 60.74) / 61.70).
- The whole sub-journal and savepoint mechanism, by removing it (V4): wall 14.65 against 16.66 is 12.1 percent
  ((16.66 - 14.65) / 16.66) with the profile on and 4.6 percent ((16.47 - 15.71) / 16.47) with it off. That is
  also unresolved at the low end, and it includes the per-file `RELEASE`, which `temp_store` does not touch (the
  `savepoint` step is 1.7 to 1.8 s in both ON and OFF).
- The `insert` step, which contains the sub-journal writes, differs by 6.16 - 5.66 = 0.50 s (profile on) and
  6.05 - 5.81 = 0.24 s (profile off). That is the wall ceiling for the effect of the pragma, 1.5 to 3 percent of
  the run, against 5.1 s of profile samples.

## Profile versus clocks

CPU profile frames of run 2 with the profile on, cumulative seconds (`go tool pprof -top -cum`):

| Frame                         | OFF (no pragma) | ON (pragma) |
| ----------------------------- | --------------- | ----------- |
| total samples                 | 15.79           | 14.28       |
| `subjournalPage`              | 5.11            | 0.28        |
| `libc.Xpwrite`                | 5.42            | 0.27        |
| `syscall.rawsyscalln`         | 6.10            | 0.98        |
| `runtime.pthread_cond_signal` | 0.48            | 2.45        |
| `runtime.madvise`             | 1.54            | 2.22        |

The same shape holds in run 3 and in the 2026-11-05 re-evaluation profiles (B0: `rawsyscalln` 7.26 s flat, system
CPU of that process 1.41 s; B1: 1.04 s). Checks against clocks that do not sample:

1. The process's system CPU (`getrusage`, in the results) is 1.38 s in OFF run 2 and 1.41 s median, while
   `rawsyscalln` holds 6.10 s of samples: 6.10 / 1.38 = 4.4 times the kernel time of the whole process. A leaf
   system call cannot consume more kernel CPU than the process did.
2. The `insert` step timer (wall clock, includes every sub-journal write) is 6.14 s in the same run, so the
   5.11 s `subjournalPage` frame would be 83 percent of the insert step; with the pragma the step is 5.53 s, only
   0.61 s shorter.
3. Sample totals follow CPU, not the frame: 15.79 - 14.28 = 1.51 s fewer samples with the pragma (run 2), of
   which the `subjournalPage` loss of 4.83 s is partly refilled by `pthread_cond_signal` (+1.97 s) and
   `madvise` (+0.68 s). Runtime wake-up and memory-return frames are where the profile puts CPU time when no
   system call return is pending. The no-savepoint variant shows the same two frames at the same size
   (2.0 and 2.3 s), so they are not an effect of the pragma.
4. Turning the profile off does not change the conclusion: wall is the same with it off (16.47 and 16.64 s).

So the profile frame measured where timer signals were delivered, not where the main thread's time went. That is
the likely reason the 2026-11-05 insert-cost study ranked the sub-journal first and predicted 6 to 8 percent.

## Candidates

| Candidate                                                              | Result                                                                                                                                                                                                                                                              |
| ---------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| The profile attributes time to a frame that overlaps other work        | Supported in the sense that matters: the frame's seconds exceed the process's system CPU (4.4 times) and the step it sits in. Mechanism not proved.                                                                                                                 |
| The sub-journal was already absorbed by the page cache (cheap in wall) | Plausible and consistent (the write is a few tenths of a second), but not testable here: the `block input/output operations` counters read 0 in every run, including the 130 MB database write, so they carry no information on this host.                          |
| Wall time is bound elsewhere (FTS5 flush work now dominating)          | The remaining wall is `parse` 5.0 s, `insert` 5.7 s, the per-file `RELEASE` 1.7 s and the finish 1.6 s; `Fts5FlushToDisk` is 1.04 to 1.30 s of samples in both ON and OFF, so it did not grow when the pragma took effect. Not the explanation of the missing gain. |
| Masked by host load                                                    | Contributes: run-to-run spread of one variant is 1.6 to 2.0 s (10 to 12 percent), more than the 0.5 s effect. The load-independent counters (instructions, system CPU) do show a small gain.                                                                        |

## The V2 gap (7.31 s against an FTS estimate of 1.44 s)

Not resolved, but one fact narrows it. Under a load that rose to 30 while it ran, V1 and V2 at the current head
were run 4 times each (profile off). Wall is useless at that load (V1 25.6 to 35.9 s, V2 16.5 to 22.2 s), but
instructions retired are not: V1 232.3 and 232.7 G (runs 1 and 4), V2 185.2 and 186.1 G, so V2 removes
(232.5 - 185.7) / 232.5 = 20.1 percent of the instructions, which is about 3 s of a 15 s run, not 7.3 s and
more than the 1.3 s the `fts` statement timers show: the FTS5 cost is spread beyond the statements of the `fts`
table (tokenizing, flushing the hash at each per-file `RELEASE`, and the FTS rows in `other`). The 7.31 s of the
re-evaluation was measured when the multi-row `fields` insert was in, which the [follow-up](2026-11-05-multirow-fields-regression.md)
found to make FTS5 flush its pending hash to disk once per statement (3.57 s of samples against 1.36 s); that
extra flush went away with V2 and is gone from the head. A repeat of V1 against V2 on a quiet machine is what
would close it.

## Reproduce

Build the binary from `git archive HEAD`, add the two toggles to the throwaway copy only (skip the pragma when
`X_TS=off`; skip the profile when `X_NOPROF=1`), then interleave runs as in the README (`go test -c -tags ablation`,
`PG_CCREC_ABLATION_*`, `/usr/bin/time -l nice -n 10`). Run time of one run is about 30 s of harness setup plus 15 to 17
s of measured wall.

## What could not be resolved

- Direct proof of the sample bias (it needs a system-call trace, which needs elevated rights on this host).
- Whether the temp-file writes are absorbed by the page cache: no usable I/O counter.
- A wall gain of 1 to 3 percent from the pragma, which is below the 5 percent rule on a loaded host; the load
  average ranged 4.7 to 9.7 in the main set, with other agents running.
- The V2 gap (above), and the production rebuild: this is the harness corpus, as in every study here.
