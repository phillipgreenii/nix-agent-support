# Investigation: index durability in normal running, group commit and the removal barrier (2026-11-04)

Operator ruling (Phillip, 2026-11-03, verbatim): "i agree that NORMAL would be fine. generally, they we are
pulling data from the files which will be there after a crash. the only exception would be to do a check
before the removing the files from claude."

This page records what changed, the table-by-table decision for the capture-side state, the answer on
`PRAGMA fullfsync` for the removal barrier, and the benchmark. The numbers come from a SYNTHETIC fixture (the
`BenchmarkLiveCommit` benchmark behind the `soak` tag); the live index was not touched. The repo is public: no
real transcript content appears here.

## What changed

| Area                   | Before                                                                                      | After                                                                                                                                                                       |
| ---------------------- | ------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Running `synchronous`  | FULL on every connection                                                                    | NORMAL on every connection (WAL, so a process crash loses nothing and an OS crash or power loss only rolls back the newest commits)                                         |
| Commits a delete needs | `CommitFull`: journal F_FULLFSYNC and `fullfsync=ON`, but `synchronous` was already FULL    | `CommitFull` pins one connection, sets `synchronous=FULL` and `fullfsync=ON` for that commit only, and restores NORMAL                                                      |
| Parse wake-ups         | the worker drained on the first wake-up; one transaction per file and chunk                 | `Coalesce` waits for a quiet window (300 ms, at most 5 s from the first wake-up), then `Drain` parses every dirty file in transactions bounded by 50,000 records or 16 MiB  |
| `parse_seq`            | bumped once per file chunk                                                                  | bumped once per transaction (per batch), and not at all when nothing advanced                                                                                               |
| A failing file         | only itself                                                                                 | a failed multi-file batch is retried file by file, so one bad file still holds back only itself                                                                             |
| Removal                | archive hash-verified, then "removal pending" committed FULL, archive flushed before unlink | before the pending list is committed and before any rename: archive F_FULLFSYNC, then `Store.Barrier`; any failure refuses the removal with the reason `durability_barrier` |
| Bulk rebuild           | finished at FULL                                                                            | finishes at FULL for its last checkpoint, then returns the connection to NORMAL                                                                                             |

The group-commit window does not touch the 60 s parse-lag health threshold: the worst added latency is the 5 s
cap plus the batch itself.

## Capture-side state, table by table

The question: if a power loss rolls the database back to an older commit, is anything lost that cannot be
re-derived? The rule that makes the answer uniform: every capture-side row is written by `apply` from a
JOURNAL record, the journal is fsynced BEFORE the database commit (`Store.commit`), and `Open` replays every
journal record past `meta.last_seq` idempotently (`Store.replay`). A rolled-back capture write is therefore
re-applied from the journal on the next start. WAL mode with `synchronous=NORMAL` keeps the database a
consistent prefix of its commits, so a parse commit that survives always has its earlier capture commits.

| Table or key                                                                                                                          | Written by                                | Rolled-back tail harmless?                               | Evidence and decision                                                                                                                                                                                                                                                                              |
| ------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------- | -------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `files`, `paths`, `tombstones`, `snapshots`, `resets`                                                                                 | journal `apply`                           | yes: replayed                                            | `TestRolledBackIndexTailIsRebuiltFromTheJournalAndTheCopies` restores an older database image beside the full journal and copies; replay advances `last_seq` and the records match a clean run. NORMAL.                                                                                            |
| `generations` capture columns (`copy_offset`, `tail_hash`, `tail_len`, `cur_gen`) and `commits` (the seen-time list)                  | journal `apply` of `commit`/`gen` records | yes: replayed                                            | Same test asserts every `copy_offset` equals the copy file's size after replay. Replay can only raise an offset to a journalled value, and a copy longer than its offset is the harmless direction. NORMAL.                                                                                        |
| `meta.last_seq`, `meta.epoch`                                                                                                         | journal `apply`                           | yes: `last_seq` rolling back is what triggers the replay | Same test. The epoch uses MAX on replay, so it never goes backward. NORMAL.                                                                                                                                                                                                                        |
| `procs`, `stage` and settle rows other than the three below                                                                           | journal `apply`                           | yes: replayed                                            | Same mechanism. NORMAL.                                                                                                                                                                                                                                                                            |
| `stage` rows "settled", "removal pending", "removed" and their path closes                                                            | journal `apply` via `CommitFull`          | NO for the decision that a delete rests on               | Kept at FULL (`CommitFull`: journal F_FULLFSYNC, `synchronous=FULL`, `fullfsync=ON` on a pinned connection) plus the `Barrier`. These are the only capture-side writes that stay FULL.                                                                                                             |
| `generations.parse_offset`, `parse_state`, `parse_seq`, `feed_marks`, `records`, `fields`, `texts`, FTS, `bad_lines`, `unknown_keys*` | the parser, one transaction               | yes: they roll back together and the parser resumes      | `parse_offset` is written in the same transaction as the records, so a rolled-back tail loses offset and records together. The rolled-back test asserts the parse offsets are the old image's, and the re-parse equals a clean run. The kill -9 test shows no duplicate or missing record. NORMAL. |
| `meta.rebuild_unfinished`                                                                                                             | bulk rebuild                              | no: its set and clear are explicit FULL commits          | Unchanged: written and checkpointed at FULL before a rebuild relaxes, cleared by a FULL commit after.                                                                                                                                                                                              |

Decision: keep everything in the one `index.db`. No capture-side table needs its own FULL database file. The
single place a rolled-back write would be a safety problem is removal, and that is the barrier below.

Two honest residuals, both power-loss or OS-crash only (a process crash, including `kill -9`, loses nothing
because the WAL survives):

1. A feed client whose cursor points into the lost tail sees record ids that are reused after the rollback
   (autoincrement ids are not durable past the last commit). The index is consistent and a re-sync fixes it;
   bumping the feed epoch on a start that rolled back was not part of this change.
2. The copier's own appends use a plain fsync, which on macOS does not flush the drive cache. A copy shorter
   than its journalled offset is already detected by `Recover` (`copy_mismatch`); NORMAL does not change that.

## The removal barrier and `PRAGMA fullfsync` on macOS

`Settler.durabilityBarrier` runs after `listFiles` has read every archive segment back and proved it equal to
its original (size and hash), and before the pending list is committed or any file is renamed:

1. `flushArchive`: F_FULLFSYNC of every segment and the manifest, then of the directory.
2. `Store.Barrier`: F_FULLFSYNC of the journal; a `wal_checkpoint(TRUNCATE)` on a pinned connection under
   `synchronous=FULL`, `fullfsync=ON` and `checkpoint_fullfsync=ON`, waiting at most 250 ms for a reader; then
   F_FULLFSYNC of `index.db` itself. A checkpoint that cannot finish (a reader pins the WAL) is
   `ErrBarrierBusy`.
3. Any error refuses the removal (`durability_barrier`), logs `removal_barrier_failed`, and changes nothing.

Does the barrier need `PRAGMA fullfsync` on macOS? Yes. On macOS `fsync` does not flush the drive's write
cache; only `fcntl(F_FULLFSYNC)` does. SQLite issues F_FULLFSYNC only when `PRAGMA fullfsync=ON`, and a
checkpoint's sync is governed by its own `PRAGMA checkpoint_fullfsync=ON`, so the barrier sets both. Because
those pragmas are honoured inside the driver and a unit test cannot observe a drive cache, the barrier does not
rely on them alone: after the TRUNCATE checkpoint the WAL is empty, and the explicit `fsx.FullSync` of the
journal and of `index.db` (the same F_FULLFSYNC helper the archive flush uses) is what makes the claim true
on its own. What no test can prove is the drive's behaviour under a real power cut; that is the same limit the
existing archive flush has.

Tests: `TestCrashBetweenArchiveFlushAndCheckpointRemovesNothing` (crash point between the archive flush and the
checkpoint: the original is untouched, no removal decision exists, and the next pass completes),
`TestBarrierThatCannotCheckpointRefusesRemoval` (a reader pins the WAL; removal is refused and nothing moves;
a hand-applied mutant that skips the barrier fails it), and the store tests for the pragmas and
`ErrBarrierBusy`.

## Crash tests

- `TestRolledBackIndexTailIsRebuiltFromTheJournalAndTheCopies` (unit): an older database image next to the full
  journal and copies models what NORMAL allows after a power loss. Replay restores the capture side; the parse
  resumes from the old offsets; the dump equals a clean run.
- `TestKillNineAfterCommitsReopensConsistentAndResumes` (`integration` tag, like the bulk rebuild kill test):
  SIGKILL after live commits and an unparsed copied tail; the reopened index passes `integrity_check`, the
  parser resumes from the stored offsets, and the dump equals a clean run.
- `TestGroupCommitParsesManyFilesInOneTransactionAndBumpsParseSeqOnce`,
  `TestGroupCommitIsBoundedByRecordsAndStillFinishes` and `TestCoalesceWaitsForTheBurstThenReturns`.

## Benchmark

Run by hand (a Go benchmark behind the `soak` tag, never part of `go test ./...`):

```sh
go test -tags soak -run '^$' -bench BenchmarkLiveCommit -benchtime 3x ./internal/parser/
```

The fixture: 20 files, 40 rounds, each round appends two lines to every file. `per-wakeup` drains after every
file's append (one wake-up per file); `coalesced` drains once per round (what the new worker does for a burst);
`capture-only` appends without parsing. Bytes written are the process's disk-write counter (darwin libproc),
so the parse side is a mode minus `capture-only`. Apple M3 Pro, a busy shared host, three iterations per row;
wall time is noisy, byte counts are stable.

| Build                | Mode         | Parse transactions | Parse time (ms) | Bytes written, total | Bytes written, parse side |
| -------------------- | ------------ | -----------------: | --------------: | -------------------: | ------------------------: |
| before (FULL)        | capture-only |                  0 |               - |           47,616,000 |                         - |
| before (FULL)        | per-wakeup   |                800 |           1,591 |          157,134,848 |               109,518,848 |
| before (FULL)        | coalesced    |                800 |           1,727 |          157,200,384 |               109,584,384 |
| after (NORMAL+group) | capture-only |                  0 |               - |           34,869,248 |                         - |
| after (NORMAL+group) | per-wakeup   |                800 |           1,757 |          130,846,720 |                95,977,472 |
| after (NORMAL+group) | coalesced    |                 40 |             584 |           51,564,544 |                16,695,296 |

Parse-side bytes are the total minus that build's capture-only bytes (157,134,848 - 47,616,000 = 109,518,848;
130,846,720 - 34,869,248 = 95,977,472; 51,564,544 - 34,869,248 = 16,695,296). Commits per second of the parse
phase, parse transactions divided by parse time: before 800 / 1.591 s = 503 and 800 / 1.727 s = 463; after
per-wakeup 800 / 1.757 s = 455; after coalesced 40 / 0.584 s = 68 transactions per second, each carrying 20
files. Reading: NORMAL alone changes little here (the profile that prompted the work found fsync only 6 percent
of non-idle samples; writes dominate), and group commit is what pays: 20x fewer transactions, 6.6x fewer parse
bytes written (109,584,384 / 16,695,296), and 3x less parse time (1,727 / 584).
