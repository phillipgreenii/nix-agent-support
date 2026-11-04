# claude-extended-tool-approver decision log retention

`asks.db` (the decision log, `~/.local/share/claude-extended-tool-approver`)
grows without bound: about 3.2 GB when measured (2026-10-30 APFS container
audit). The existing `archive` command bounds it.

The key words MUST, SHOULD and MAY are used as in RFC 2119.

## Window: 90 days

- The consumers (the identify-hook-misses skill, `show`, engine A/B replays, the
  hook-evaluation loop) work on recent behaviour: the hook rules change weekly,
  so decisions older than a quarter describe rules that no longer exist.
- Ground truth is not lost: `archive` samples fixtures BEFORE deleting, and every
  row carrying a human annotation (`correct_hook_decision`), match or miss, is
  among the sampled buckets. The fixtures file is the regression corpus.

## Behaviour

- `claude-extended-tool-approver archive --days 90 --dry-run` counts candidates
  and sample, writing and deleting nothing.
- `archive --days 90 --yes --fixtures-out <file>` samples, deletes, then `VACUUM`s.
  A real run requires `--fixtures-out`.
- `VACUUM` needs free disk space about the size of the database.

## Disk reclaimer

The `ceta-asks-db` registry item (nix-agent-support
`home/programs/pg-disk-reclaimer`) runs this at aggressiveness level 4, writing
fixtures to `~/.local/share/claude-extended-tool-approver/archive-fixtures/`.
Level 4 because the deletion is permanent apart from the sampled fixtures; it is
therefore behind the confirmation gate and never part of the scheduled run
(levels 1 to 3 only).
