# pg-ccaudit retention

The index (`transcripts.db`) grows without bound: about 6 GB on the machine it
was measured on (2026-10-30 APFS container audit). `pg-ccaudit prune` bounds it.

The key words MUST, SHOULD and MAY are used as in RFC 2119.

## Window: 45 days

- The index is an analysis aid, not a record. The transcripts themselves are kept
  for 365 days (`cleanupPeriodDays`), which is the provenance for the agent-rule
  census; this index MUST NOT be treated as that provenance.
- The analysis skills (improvement-retro, tool-error-waste-review) look back over
  weeks, not quarters: the skills pass an explicit `--since`/`--until` window
  (the tool-error-waste-review examples span 8 days) and improvement-retro runs
  from the last retro to now. 45 days is several times the widest of those
  windows, so a retro that was skipped for a few weeks still finds its data.
- Operator ruling (Phillip, 2026-11-19): 45 days, "probably still too much".
  Shrinking it further is a one-line change to `defaultRetentionDays` and the
  reclaimer item.
- A longer window only keeps data nothing queries.

## Behaviour

- `pg-ccaudit prune [--days N] [--yes]`: dry run unless `--yes`. Default 45.
- A file (recorder file generation) is removed whole or not at all, judged by its
  NEWEST event, so a session straddling the cutoff is never half-deleted. A file
  with no events at all is removed only once it was last touched before the cutoff.
- Feed cursors and the gold set file are never touched, so ingest resumes where it
  was.
- After deleting, the WAL is checkpointed (`wal_checkpoint(TRUNCATE)`; if a reader
  keeps it busy the run says `WAL not truncated (reader active)` and continues) and
  `VACUUM` returns the space. SQLite documents that VACUUM may need up to about 2 x
  the database size in free space, so the guard budgets
  `needed = 2 x db + WAL + margin`, `margin = max(10 percent of db, 1 GiB)`, read
  with `statfs` on the database directory (and on TMPDIR when it is on another
  volume). With `--yes`, when less is available the command exits 1 (never 75, which
  the reclaimer reads as skip) before deleting anything; `--force-low-space`
  overrides. The dry run prints needed and available bytes and an `OK` or
  `WOULD-REFUSE` verdict, and `--json` carries `needed_bytes`, `available_bytes` and
  `refused`. VACUUM holds the ingest lock and the SQLite write lock for the whole
  run, so recorder writes wait. A lower `--days` does not reduce the VACUUM cost much.

## Disk reclaimer

The `pg-ccaudit-index` registry item (nix-agent-support
`home/programs/pg-disk-reclaimer`) runs the prune at aggressiveness level 4:
the rows are gone for good, and the confirmation gate and the exclusion from the
scheduled run (levels 1 to 3 only) are wanted.
