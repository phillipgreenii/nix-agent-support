# Every sweep re-ingested about 28,500 unchanged files (2026-11-10)

Evidence is the launchd log (`~/.local/state/pg-claude-code-recorder-launchd/launchd-stderr.log` and its rotation
`.6`: 339 `sweep_done` lines with `verify=false`, 2026-11-06 to 2026-11-09), the code at `internal/serve/sweepcount.go`
and a fixture run. The running daemon was not touched.

## What the log says

- Every non-verifying sweep walks about 60,700 files, skips about 32,200 and ingests about 28,500 (mean ingested over
  the 339 sweeps: 28,168). None of the 339 sweeps ingested fewer than 1,000, so this is not churn: it is the same
  files every time.
- Mean duration is 346 s (range 289 to 600 s), on a 15 minute cycle (`sweep.interval`). The duration matches the
  pacer: 28,500 files at `MaxFilesPerSecond` 100 is 285 s. The sweep is waiting on the pacer, not on reading.

## Cause

`skipExempt` in `internal/serve/sweepcount.go` excluded every snapshot-mode file of 64 KiB or less from the caught-up
cache (`noteCaughtUp`) and from the skip (`caughtMatches`). Snapshot-mode files are tool results, subagent metadata and
the registry and session-mode JSON files; nearly all of them are small, and they are about half of what a sweep walks.
So each sweep ingested all of them again, paced, though the walk reported the same size, mtime and inode. The
exemption was deliberate (the incremental-sweep plan, item 6): a same-size rewrite of a small registry file within one
timestamp tick would otherwise be missed until a forced sweep or the doctor. The cost was not measured then.

## Decision and change

Keep the safety goal, drop the blanket exemption, and use the "racily clean" rule: a snapshot file is cached as caught
up only when its mtime is at least `snapshotRacyWindow` (2 s) older than the moment it is ingested. A same-size rewrite
can share the old mtime only while the clock tick is coarser than the gap between the two writes; once the file's
mtime is older than the window that cannot hide a rewrite that the walk would not see as a changed mtime. A file
written a moment ago is read again next sweep and cached once it has aged. Residual risk, unchanged from transcripts:
a rewrite that sets the same size, the same mtime (to the nanosecond, APFS) and keeps the inode is found only by the
doctor (24 h) or `sweep --force`.

## Measurements

| What                                                  | Before                                     | After                                                                                                                               | How                                                                                                       |
| ----------------------------------------------------- | ------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| Files ingested per steady sweep, live data            | about 28,168 (mean of 339 sweeps)          | not measured live (no apply)                                                                                                        | log `sweep_done.ingested`                                                                                 |
| Sweep duration, live data                             | mean 346 s, 289 to 600 s                   | projected: ingested count falls to the files changed since the last sweep; at 100 files per second even 1,000 changed files is 10 s | log `sweep_done.duration_ms`; projection is ingested / 100                                                |
| Fixture: 3,000 old small snapshot files, steady sweep | 3,000 source opens, 1.3 to 1.8 s (unpaced) | 0 opens, 42 to 46 ms                                                                                                                | throwaway test over the real daemon, `rescan` three times, source opens counted by `copier.CountingSrcFS` |

The fixture is unpaced, so it shows the work, not the pacer wait; on the live host the 28,500 paced ingests were the
285 s.

## Post-deploy check

After the next apply and one full-verification sweep, the second and later `sweep_done` lines should show `ingested`
in the tens to hundreds (not about 28,500), and `duration_ms` well under 60,000. Tests:
`TestUnchangedSmallSnapshotIsNotOpened` and `TestRacySnapshotIsReadUntilItsMtimeAges` in
`internal/serve/snapshotskip_test.go`.
