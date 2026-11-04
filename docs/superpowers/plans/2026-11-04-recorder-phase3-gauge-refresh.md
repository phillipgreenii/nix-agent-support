# Plan: pg-claude-code-recorder phase 3, gauge and status refresh without whole-table reads

Repo root: `/Volumes/gitrepos/phillipg/nix-agent-support` (public repo: no employer-specific detail). Package:
`packages/pg-claude-code-recorder`. Earlier plans in this directory: phase 0 (`2026-11-03-...`), phase 1 and 2
(`2026-11-04-...`). Phase 1 holds the re-ranking: this phase is ranked second because it removes the two remaining
`AllGenerations` callers and the status loop's per-file `Lstat`. Paths are relative to the package unless they
start with `docs/` or `grafana/`.

## Why (measured, post phase 0, build `0.0.0-8ab32e31`, 2026-11-04)

- Allocation profile (`alloc_space`, cumulative since start, includes the 3 minute backfill):
  `Store.AllGenerations` is 21.74 GB of 234.61 GB (9.3%); callers are `parser.UpdateGauges` 87.5%,
  `Settler.refresh` 11.7% and `Daemon.coverage` 0.8%. Before phase 0 it was 85.9% of allocation.
- `parser.UpdateGauges` runs every 5 s from `refreshGauges` (the refresh loop). It reads all files
  (`AllFiles`) and all generations (`AllGenerations`), loops over every generation, and for each one with unparsed
  bytes calls `CopyCommits(file)`. Cost is O(files + generations) per run, 17,280 runs a day, to produce four
  gauges (`parse_lag_bytes`, `parse_lag_seconds`, `parse_progress`, `unknown_kinds`).
- `statusLoop` recomputes the status snapshot every 5 s (`refreshStatus` then `computeCoverage`). It does one
  `os.Lstat` per file (about 58.5k files), so about 11.7k stat calls a second on average, plus `AllFiles`,
  `AllGenerations`, `AllCurrentPaths` and `Stages`. Of the 30 s profile's samples, 76.9% of flat time is in
  syscalls; this loop is a named candidate, and the only way to confirm the share is phase 1's per-loop duration
  metric (`status_refresh`). This loop is NOT in the phase 0 list of later phases, which named only the scrape-
  triggered refresh; that part is already done (the scrape path now runs only process-local collectors, see
  `scrapeRefresh`), so this phase's scope is the remaining loops, not the scrape.
- The `status --watch` command calls the daemon every 2 s; it reads the cached snapshot (cheap) so the cost is the
  loop, not the request.

## Scope

In scope: parse and copy lag gauges maintained from commits; `unknown_kinds` maintained from the parser; the status
snapshot built without a per-file `Lstat` on the steady path; `Daemon.coverage` (the on-demand `coverage` command)
keeps its full read because it is an operator command, but is made cancellable (phase 5). Out of scope: the
settler's gauges (phase 2), the sweep (phase 4).

Behavioural contract: gauge names, units and meanings unchanged; the `status` JSON unchanged except where noted;
`coverage` output byte-identical for the same state (golden test).

## Ground rules

As in the phase 0 and 2 plans: real SQLite and files, counting driver for read counts, injected clock, no slow test
in the unit gate, no bead id in the repo, no new dependency. `nice -n 10 go test -p 4`, `-race` where state is
shared.

## Design

The lag figures are functions of per-file `(copy_offset, parse_offset, time the unparsed bytes were first seen)`.
Both offsets change only inside a copier commit or a parser commit, so keep a `lag` structure in the parser
package:

- `lagState`: `map[fileGen]lagEntry{copy, parse int64, firstUnparsedSeen time.Time}`, a running `sumCopy`,
  `sumParse`, `maxLagBytes`, and `unparsedSince` min-structure (a heap keyed by `firstUnparsedSeen`, or because
  entries leave in roughly FIFO order a sorted slice plus lazy deletion).
- Updated by two hooks: `NoteCopy(file, gen, copyOffset, at)` called where the copier's commit is observed (the same
  place `Parser.Notify` is called from `afterIngest`, with the offset read from the `Result`; `Result` gains
  `CopyOffset` additively) and `NoteParse(file, gen, parseOffset)` called at the end of each parser transaction
  (the parser already has the new offset).
- Gauges are derived in O(1) (`sumParse/sumCopy`) or O(log n) (max, oldest unparsed) on read.
- The first-seen time of an unparsed span is the `commits.seen_ns` row today (`CopyCommits`). The maintained
  version records `at` when `NoteCopy` raises the copy offset above the parse offset and clears it when parse
  catches up. After a restart the structure is rebuilt once by one whole-table read at start (the same read
  `UpdateGauges` does today, run once), so the worst case after restart is the current behaviour.

The status snapshot: the `Lstat` exists to compare the size on disk with the copy offset (`copy_lag_bytes`,
`copy_lag_s` per file). In steady state the watcher already tells the daemon about every change, and the copier
records the size it saw (`files.last_size`). So `status` can report `size = files.last_size` and lag from
`last_size - copy_offset`, with a `Lstat` only for files whose `last_size != copy_offset` or that were touched in
the last `status.fresh` window (default 2 minutes). A file that the watcher has not reported but changed on disk is
visible only after the next sweep (up to `sweep.interval`, default 15 min); that is a stated semantic change and
the status output says so (`size_source: recorded|stat`). The on-demand `coverage` command keeps the `Lstat` for
every file (ground truth; the operator uses it to find exactly this kind of gap).

## Task list and order

- T1 (M): `lagState` and its hooks, with the equivalence test against `UpdateGauges` (the old function stays as the
  oracle and is removed in T3).
- T2 (M): `status` snapshot from recorded sizes, `Lstat` only for the exceptions; `size_source` field.
- T3 (S): switch the gauges to the maintained values; delete the whole-table reads from `refreshGauges`; keep one
  start-up rebuild.
- T4 (S): `unknown_kinds` and the other `UpdateGauges` outputs.
- T5 (S): docs, dashboard, spec.
- T6 (S, gated follow-up): verification after apply.

Dependencies: T1 first. T2 independent of T1 (different files: `internal/serve/statuscache.go`, `ops.go`). T3 after
T1 and T4. T4 after T1. T5 after T2, T3. T6 after T2, T3, T4, T5, gated on their apply (`pb:pb-gate-lifecycle`).
Phase 1's T2 (loop wrapper) SHOULD land before this phase so T6 can read `status_refresh` and `refresh_gauges`
durations; this phase does not depend on it technically.

## T1. `lagState` and hooks

Files: `internal/parser/lag.go` (new), `internal/parser/parser.go` (`NoteParse` call at the end of the commit
transaction), `internal/copier/copier.go` (`Result.CopyOffset`), `internal/serve/daemon.go` (`afterIngest` calls
`NoteCopy`; the backfill batch path `IngestBatch` also, through `afterIngest`).

Equivalence test: a fixture store with 300 append files across 3 sources; a scripted sequence of copies and parses
(random appends with fixed seeds listed in the test) drives both the maintained state and the old
`UpdateGauges` on the same store after each step; the four gauge values are equal (lag bytes exact; lag seconds
within the injected clock's resolution, compared exactly because the clock is injected). Includes: a generation
restart (new `gen` for a file: old entry removed), a deleted file (state `deleted`: excluded as today), a file whose
source has no parser (`HasSource` false: excluded and counted in `unknown_kinds`). Negative controls: forget to
remove the old generation on restart; subtract parse offsets before copy offsets. Race test: concurrent `NoteCopy`
and `NoteParse` under `-race`.

## T2. Status snapshot without per-file `Lstat`

Files: `internal/serve/ops.go` (`computeCoverageFrom` gains a `statMode`: `all` for the `coverage` command, `lazy`
for the status loop), `internal/serve/statuscache.go`, `internal/wire/wire.go` (additive `size_source` on a file's
coverage entry; the aggregate fields are unchanged), README.

`lazy` mode: `Lstat` a file only when `last_size != copy_offset` or its `last_seen` is inside `status.fresh`. The
counting seam is `fsx` (`internal/fsx/recorder.go` records file-system calls; use it to count `Lstat`). Tests:
with 5,000 files unchanged since last copy, one status refresh makes at most `fresh files + lagging files` `Lstat`
calls (assert the exact number for the fixture: 3 fresh, 2 lagging, so 5); the aggregate figures (`Files`,
`CopyLagBytesMax`, `ParseLagBytesMax`, root counts) equal `coverage` mode's on the same state when no file changed on
disk behind the daemon's back; with one file modified behind its back (no watcher event), `lazy` does not see it
and `coverage` mode does (documents the semantic change, so the test fails if someone makes `lazy` silently
stat everything). The wire golden test diff is additive only.

## T3. Switch the gauges, remove the whole-table reads

Files: `internal/serve/daemon.go` (`refreshGauges` stops calling `UpdateGauges`; it reads the maintained values),
`internal/parser/parser.go` (the old `UpdateGauges` becomes `rebuildLag` called once at start). Count test: a
`refreshGauges` call issues no `AllFiles`, `AllGenerations` or `CopyCommits` query (counting driver, by statement
text) and the gauge values equal those of `rebuildLag` on the same state.

## T4. Remaining gauge outputs

`unknown_kinds` (the set of sources with files but no parser) is maintained when a file is created
(`createFile` hook) and when a source gains a parser (never at run time). Test: create files of a known and an
unknown source; the gauge counts distinct unknown sources; a deleted file's source is excluded as today.

## T5. Docs and dashboard

README: replace the sentence that lag is refreshed every 5 s from the whole table with the maintained description;
add the `status` semantics ("sizes are as last recorded; run `coverage` for the on-disk truth"). Spec: signals
section, the `size_source` field. Dashboard: no new panel; the `refresh gauges duration` panel (phase 1) gets its
expected value in its description (milliseconds, not seconds).

## T6. Verification (gated)

After apply and an hour of data: record `refresh_gauges_duration_seconds` p99 (target under 100 ms, from today's
unmeasured but profile-implied seconds; if phase 1 is applied, compare with its baseline comment), `status_refresh`
p99, CPU cores (phase 0 figure 0.63), and a fresh `alloc_space` profile (`AllGenerations` below 2% of allocation).
Misses are reported with the profile; a miss is not a reason to revert unless a gauge is wrong.

## Risks and rollback

- A hook missed in the copier or parser makes a gauge drift. Mitigation: `rebuildLag` runs at start and every
  `settle.reconcile`-style interval (default 1 h) and logs a WARN `lag_drift` with both values when they differ; the
  equivalence test covers every commit site (enumerate them with `rg -n 'Commit\(' internal/copier internal/parser`
  in the task and list each in the test).
- `status` sizes stale between sweeps: stated in the README and in `size_source`.
- Rollback: revert; no schema or data change; the wire change is additive.

## Proposed bead breakdown

Epic with six children T1 to T6 and the edges above. Sizes: T1 M, T2 M, T3 S, T4 S, T5 S, T6 S (gated
verification). Labels `agent-support`, `pg-claude-code-recorder`.
