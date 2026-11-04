# pg-ccaudit retention

The index (`transcripts.db`) grows without bound: about 6 GB on the machine it
was measured on (2026-10-30 APFS container audit). `pg-ccaudit prune` bounds it.

The key words MUST, SHOULD and MAY are used as in RFC 2119.

## Window: 90 days

- The index is an analysis aid, not a record. The transcripts themselves are kept
  for 365 days (`cleanupPeriodDays`), which is the provenance for the agent-rule
  census; this index MUST NOT be treated as that provenance.
- The analysis skills (improvement-retro, tool-error-waste-review) look back over
  weeks, not quarters; the mistake census reports are windowed with `--since`.
  90 days is the widest of those windows plus about a quarter of margin.
- A longer window only keeps data nothing queries.

## Behaviour

- `pg-ccaudit prune [--days N] [--yes]`: dry run unless `--yes`. Default 90.
- A file (recorder file generation) is removed whole or not at all, judged by its
  NEWEST event, so a session straddling the cutoff is never half-deleted. A file
  with no events at all is removed only once it was last touched before the cutoff.
- Feed cursors and the gold set file are never touched, so ingest resumes where it
  was.
- After deleting, `VACUUM` returns the space. It needs free disk space about the
  size of the database and takes the ingest lock for its duration.

## Disk reclaimer

The `pg-ccaudit-index` registry item (nix-agent-support
`home/programs/pg-disk-reclaimer`) runs the prune at aggressiveness level 4:
the rows are gone for good, and the confirmation gate and the exclusion from the
scheduled run (levels 1 to 3 only) are wanted.
