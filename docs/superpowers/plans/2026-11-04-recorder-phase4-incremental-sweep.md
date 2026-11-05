# Plan: pg-claude-code-recorder phase 4, an incremental sweep

Repo root: `/Volumes/gitrepos/phillipg/nix-agent-support` (public repo: no employer-specific detail). Package:
`packages/pg-claude-code-recorder`. Earlier plans in this directory: phase 0 (`2026-11-03-...`), phases 1 to 3
(`2026-11-04-...`); phase 1 holds the six-phase re-ranking. Paths are relative to the package unless they start with
`docs/` or `grafana/`.

## The operator rule this phase finishes

Every file the recorder handles MUST be handled as efficiently as possible, and a periodic sweep MUST NOT re-read
all files: it identifies the files that changed since the last read and reads only the bytes since the last read.
Phase 0 did not touch the sweep; this phase does.

## Why (code reading plus phase 0 measurements)

Measured: the sweep runs every `sweep.interval` (15 minutes), so its steady CPU share is small in a 500 s window;
the verification did not capture a sweep. What the code does when a sweep runs (about 58.5k files today):

1. `platformSweep` walks every root (one `getattrlistbulk` pass on macOS) and returns `FileInfo{Path, Dev, Ino,
Birth, Size, ModTime}` for every file, which is already everything needed to decide change.
2. `onSweep` calls `Copier.SweepMissing(found)`: `AllFiles`, then per file `CurrentPaths` (one query per file) and
   one `os.Lstat` per path, all under `Copier.mu`. The walk's result `found` is only used for its identity set; the
   `Lstat` repeats what the walk just read.
3. `onSweep` then calls `notifyDeletedSnapshots` (`AllFiles` again).
4. The watcher then calls `Sink` for EVERY walked file, which is `Daemon.ingest` and `Copier.Ingest`, which for each
   append file does: `Lstat`, `openNoFollow`, `Stat`, `resolveAppend` (identity query, current-path query), one
   `Generation` read, a `stat` of the copy file, a tail comparison (`tailMatches`, which reads the sidecar tail) and
   `appendBytes` that finds zero new bytes. For a snapshot file it reads the whole file (`io.ReadAll`), validates JSON,
   and compares with the last version. Every unchanged file costs a handful of syscalls plus several SQLite reads, one
   at a time, under the copier lock, so live ingest from FSEvents waits behind the sweep.
5. `files.last_size` is stored, but no mtime or identity-of-last-ingest is, and nothing compares the walk's `Size`
   with it.

## Scope

In scope: skip files the walk shows unchanged; derive `SweepMissing` from the walk; stop re-reading unchanged
snapshot files; honour `sweep.interval` and make it testable; release the copier lock between files; sweep
metrics. Out of scope: the doctor (phase 6), the watcher's FSEvents path (unchanged), tracker (phase 5). No schema
change (the index is 30 GB and a schema bump forces a rebuild; the phase 0 deployment showed what that costs).

Behavioural contract: no change to what is copied, when a generation starts, tombstones or path closes. A sweep MUST
still find (a) new files, (b) appended files, (c) same-size rewrites, (d) replaced inodes and renames, (e) deleted
files, (f) changed snapshot files. The first sweep after a daemon start MUST remain a full verification (the
integrity net: a crash, a clock jump or a lost event is repaired there).

## Design

An in-memory `seen` cache in the copier, `map[fileID]seenState{size int64, mtimeNS int64, dev, ino uint64,
birthNS int64, at time.Time}`:

- Written at the end of every successful `Ingest` and `IngestBatch` of a regular file, with the `Stat` values the
  ingest itself used (not a later `Stat`), so a write that lands between the read and the cache write is seen as a
  difference next time.
- Not populated at start. The first sweep after start (and the backfill) therefore ingests everything, exactly as
  today, and fills the cache. This is the full-verification guarantee, and it costs one full pass per daemon start
  (not per sweep).
- A walked file is SKIPPED when the cache has an entry for the file id found by identity (`Dev`, `Ino`, `Birth`)
  AND `Size == seen.size` AND `ModTime == seen.mtime` AND the path set is unchanged (the file's cached current
  path equals the walked path). Anything else, including a missing cache entry, takes the existing ingest path.
- A same-size same-mtime rewrite (a tool that preserves mtime) is NOT caught by the skip. It is caught by the
  doctor (full hash, phase 6) and by the next restart's full first sweep. The README states this explicitly, and
  `sweep.verify_every` (default 24 h, new config key) forces a full non-skipping sweep at that interval, so the bound
  is stated and short.
- Snapshot files: the same rule, so `io.ReadAll` and JSON validation happen only for changed files.

`SweepMissing` from the walk: build `walkedIdent` and `walkedPath` sets from `found` (identity set exists; add the
path set). For each stored file with state not deleted: its current paths are all in `walkedPath` and the identity is
in `found` means present (no `Lstat`); otherwise fall back to the existing `Lstat` check for that file only. The
per-file `CurrentPaths` query becomes one `AllCurrentPaths` read (the status code already uses it). Result: a sweep
with nothing missing performs zero `Lstat` calls and one store read, not 58k queries and 58k `Lstat`s.

Lock discipline: the sweep today holds `Copier.mu` for the whole `SweepMissing` and each ingest separately. After
this phase `SweepMissing` takes the lock per BATCH of 256 files (not the whole pass), so a live ingest waits at most
one batch.

## Task list and order

- T1 (M): the `seen` cache in the copier, written by ingest, with unit tests for every cache-invalidating change.
- T2 (M): the sweep skip (watcher passes `FileInfo` to the sink; `Daemon.ingest` consults the cache).
- T3 (M): `SweepMissing` from the walk, batched lock.
- T4 (S): snapshot skip.
- T5 (S): `sweep.verify_every` and `sweep.interval` plumbing and a test that the interval is honoured.
- T6 (S): sweep metrics and docs.
- T7 (S, gated follow-up): verification after apply.

Dependencies: T1 first. T2 after T1. T4 after T1 (same cache). T3 independent of T1 (but both edit
`internal/copier/copier.go`: land them one at a time, T1 then T3). T5 after T2. T6 after T2, T3, T4. T7 gated on
the apply of T2 to T6 (`pb:pb-gate-lifecycle`). Phase 1 T3 (the `sweep_files_total{outcome}` counters, including
`unchanged_candidate`) SHOULD land first: it measures exactly the skip this phase performs, so T7 can compare.

## T1. The `seen` cache

Files: `internal/copier/copier.go` (`ingestAppend`, `ingestSnapshot`, `IngestBatch`), new
`internal/copier/seen.go`. API: `Seen(fileID) (seenState, bool)` and an internal `note`. Concurrency: guarded by
its own mutex (never `Copier.mu`, so the watcher can read it without queueing behind a copy). Tests (table-driven
against a real store and temp directory): a note after an ingest equals the file's `Stat` at ingest time; an
append, a truncate, a rename, a replace (new inode, same path), a delete and a re-create each invalidate (the
entry no longer matches the next walk); `-race` with concurrent notes and reads. Negative controls: note with a
`Stat` taken after the copy (a write in between would be hidden; assert the test that appends between open and note
fails); omit `ModTime` from the comparison.

## T2. Skip unchanged files in the sweep

Files: `internal/watcher/watcher.go` (`sweep` already has `fi FileInfo`: pass a `Skip func(FileInfo) bool` in
`Config`, called before `Sink`), `internal/serve/daemon.go` (`Skip` implemented over `d.cp.Seen` and the identity
lookup the copier already keeps). The skip applies to the SWEEP path only; an FSEvents hint always ingests.
First sweep after start: `Skip` returns false for every file while `d.backfillDone` is false OR the first
verification sweep has not completed (a `verifiedOnce` flag set when the first full sweep ends).

Tests (real store, real directory, fake clock): (1) 10 files, first sweep ingests 10 (counts `Ingest` calls
through the copier's counters); second sweep with no change ingests 0 and the stored state is identical; (2) append
to one file: only it is ingested and its new bytes are copied; (3) same-size rewrite WITH a changed mtime is
ingested and the tail mismatch starts a new generation as today; (4) same-size same-mtime rewrite is NOT detected by
the incremental sweep (assert this, as documentation) and IS detected by a sweep forced by `sweep.verify_every`
elapsing on the fake clock; (5) replace-inode at the same path is ingested; (6) a rename is ingested (path set
differs); (7) a deleted file is tombstoned by `SweepMissing` (T3). Count test with the counting driver: an
unchanged-everything sweep of 2,000 files issues fewer than 20 store queries in total (today: more than 6,000),
asserting an absolute bound plus a control that the first (verification) sweep issues more than 2,000.

## T3. `SweepMissing` from the walk

As in the design. Tests: the existing `SweepMissing` tests stay and run against the new implementation unchanged
(they are the oracle); new: a sweep with nothing missing performs zero `Lstat` through the `fsx` recorder
(`internal/fsx/recorder.go`) and one `AllCurrentPaths` query; a file missing from the walk but present on disk (a
filter excluded it, or a walk race) is rechecked with one `Lstat` and NOT tombstoned; a file missing from both is
tombstoned; the lock is released between batches (a goroutine calling `Ingest` during a 1,000-file `SweepMissing`
with an injected hook that blocks after batch 1 completes within the test: assert it is not waiting for the whole
pass). Negative control: skip the `Lstat` fallback (the "present on disk" test must fail).

## T4. Snapshot files

`ingestSnapshot`: skip when walk size, mtime and identity match the cache and the path set is unchanged; otherwise
read as today. Tests: an unchanged snapshot file is not opened (fsx recorder shows zero `Open` for it); a changed
one is read and a new version stored; a snapshot with invalid JSON that later becomes valid is re-read (cache is
only written on a successful ingest, so an invalid-JSON skip never happens; assert it).

## T5. Intervals

`sweep.interval` is already wired to `watcher.Config.SweepInterval` (`internal/serve/daemon.go`,
`internal/watcher/watcher.go`), so "honour it" means: test it, plus validate it (minimum 1 minute, maximum 24 h)
in `internal/config`, plus add `sweep.verify_every` (default 24 h, at least `sweep.interval`). Tests: with an
injected ticker the watcher sweeps at the configured interval (not the default); a config with `sweep.interval =
"10s"` is rejected with a message naming the key; the heartbeat alert's literal `2700` (3 x 15 min) is documented
as coupled to the default and the README says to change both (the alert file already says so; add a comment-level
test that the alert text mentions `sweep.interval`, as the structure check allows).

## T6. Metrics and docs

Uses phase 1's `sweep_files_total{outcome}`: add `skipped` and `ingested` outcomes if phase 1 has landed (else add
them here with the same name; one task owns the name, whichever lands first, and the other rebases). Add
`sweep_skipped_ratio` gauge (last sweep). README: sweep section rewritten (what is skipped, the first-sweep full
verification, `verify_every`, the same-size-same-mtime limit); spec: the sweep paragraph gets the incremental rule
and the verification bound. Dashboard panel `sweep walked vs skipped` from phase 1's panel list.

## T7. Verification (gated)

After apply, record from the log and the metrics: the first sweep's duration (full verification), the second
sweep's duration and skipped ratio (target above 99% and a duration an order of magnitude below the first), total
CPU in a 15-minute window containing a sweep versus one without, and a deliberately appended test file picked up by
the next sweep within `sweep.interval`.

## Review findings folded in (corrections to the sections above; where they disagree, this section wins)

1. Cache write condition. The `seen` entry is written only when the ingest is fully caught up: result action
   `unchanged` or `copied` with the committed copy offset equal to the `Stat` size used, and not after hitting the 64
   round limit. A partial copy writes nothing. First-copy paths through `IngestBatch`/`flushStaged` write the entry
   after the flush with the staged `Stat` values; the task lists each writer.
2. The skip bypasses `afterIngest` (parser `Notify`, tracker `Observe`). Today the sweep re-notifies the parser
   for every file, a retry net for parse failures, and re-observes registry files. Decision: a skipped file is still
   passed to a cheap `afterSkip(path, fileID)` that calls `Notify` only when the file's parse offset is behind its
   copy offset (the maintained state of phase 3, else one in-memory check) and `tr.Observe` for registry paths
   (which are snapshot files and so are small and rarely skipped anyway). Tests: a file with a failed parse is
   re-notified by a sweep that skips its copy; a registry file's `Observe` still fires.
3. The cache is keyed by path as well as identity: `seenState` holds `{path, size, mtimeNS, dev, ino, birthNS}` keyed
   by file id, with a second map `path -> fileID`, so a hard link (two paths, one identity) skips only the path that
   was ingested and a new path to a known inode is ingested (as `resolveAppend`'s `set[path]` requires). No store
   read per file.
4. `SweepMissing` from the walk compares a `path -> identity` map built from the walk (not two independent sets): a
   stored path is present only when the walk has that path with the stored identity; anything else gets the
   `Lstat` fallback for that file, which keeps the `!sameInode` close-path behaviour. Per-batch locking re-reads
   each batch's file rows (`FileByID`) under the lock, so a file ingested between batches is not tombstoned from a
   stale snapshot of `AllFiles`. `notifyDeletedSnapshots`'s `AllFiles` per sweep becomes a query for only deleted
   snapshot files (`WHERE mode='snapshot' AND state='deleted'`), added as T3b (S).
5. Locks. The watcher holds `w.mu` for the whole sweep including every `Sink`; FSEvents handling waits for it. T3
   also changes `watcher.sweep` to build the file list under the lock, release it, then call `OnSweep` and the
   sinks unlocked (the checkpoint state and `lastSweep` stay under the lock), and tests that a `Wake` during a
   blocked sink returns promptly. The "waits at most one batch" claim holds only with this change.
6. Snapshot sources are exempt from the skip unless their size is above 64 KiB; small registry and session-mode
   JSON files are re-read on every sweep as today (cheap, and same-size same-second rewrites are plausible for
   them). The README states it.
7. `verifiedOnce` is set only when a full sweep completes without error; a failed first sweep keeps skipping off.
   `sweep.verify_every` runs on the watcher's injected clock (`Now`). `sweep.verify_every = "0"` disables skipping
   entirely and is the documented rollback switch (validated: `0` or at least `sweep.interval`). Config validation
   for `sweep.interval` (1 minute to 24 h) is a behaviour change: a smaller existing value is clamped to 1 minute
   with a startup WARN for one release instead of failing startup.
8. Birth time may be zero on Linux, so identity weakens to (Dev, Ino); the skip also requires a non-zero
   `Birth` on darwin; on Linux it additionally requires `ctime` equality when the walk provides it, else skipping
   is off for that file. Inode reuse with identical size and mtime is bounded by `verify_every`.
9. Test seams. The source-side `Lstat` and `open` bypass `fsx` (which wraps only the copy side), so T3 and T4 add a
   `srcFS` seam in the copier (`Lstat`, `Open`) with a counting implementation for tests. The watcher's ticker is
   real; T5 uses `Rescan` for determinism plus a `ticker` seam in `watcher.Config` for the interval test. The query
   counting driver is the phase 0 task's `internal/store/countdriver` package. The "fewer than 20 queries" bound is
   replaced by a relative bound (the unchanged sweep issues under 1% of the verification sweep's queries) and an
   absolute zero-per-file assertion on `Generation` and `CurrentPaths`.
10. Negative controls added for T2 (drop the path-set check, so the rename test fails), T4 (drop the size gate),
    and the invalid-JSON case. New tests: a hard link, a file under a symlinked directory (not walked, handled by
    `handleLink`), a failed first sweep, a sweep during backfill (`Skip` stays false).
11. Observability: add `sweep_duration` through phase 1's `loop_duration_seconds{loop="watcher_sweep"}` (T6 states a
    dependency on phase 1's T2 and creates the minimal timing locally if phase 1 has not landed; one definition,
    whichever lands first), `sweep_lstat_fallbacks_total`, `sweep_verify_forced_total`, and one log line per sweep
    `sweep_done` with `walked`, `skipped`, `ingested`, `missing`, `duration_ms`, `verify` (bool).

## Risks and rollback

- A stale cache entry hides a change: the skip rule needs size, mtime, identity and path to all match; `verify_every`
  and restart bound the residual same-size same-mtime case; the doctor covers it fully.
- Behavioural drift in `SweepMissing`: guarded by running the old tests unchanged.
- Rollback: `sweep.verify_every = "1m"` (effectively always verify) in config as a stop-gap; revert as the real
  rollback. No schema or data change.

## Proposed bead breakdown

Epic with seven children T1 to T7 and the edges above. Sizes: T1 M, T2 M, T3 M, T4 S, T5 S, T6 S, T7 S (gated
verification). Labels `agent-support`, `pg-claude-code-recorder`.

## Addendum (2026-11-05): paced sweep and spread verification

`sweep.verify_every` is now a real config key (default 24 h, 0 verifies every sweep, otherwise at least
`sweep.interval`). Instead of emptying the whole caught-up cache once per window (a burst of one full pass), each
cached path carries its own deadline: the first one is a stable hash-of-path point inside the window, a re-verified
path gets a full window, so the re-ingest is a steady trickle of files / verify_every per second. A new
`sweep.max_files_per_second` (default 100, 0 unlimited) paces the sweep's own ingests and the first catch-up on the
daemon clock; hint-driven ingest is not paced.
