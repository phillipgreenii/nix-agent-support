# Query broker (`agent q`)

`pg-review agent q <provider> <query-name> [--arg value ...] [--run <id>]` is
the analyst's only route to extra evidence. The run comes from `--run` or the
`PG_REVIEW_RUN` environment variable.

## Rules

- The query name MUST be listed in the provider's `queries` table for the run's
  lens; any other name or provider is refused (exit 4).
- `--scope`, `--from` and `--to` are refused. So are the provider flags the
  broker builds from the run (`--since`, `--until`, `--query`, `--cwd-prefix`,
  `--exclude-cwd-prefix`, `--scope-prefix`, `--layer`), because each would
  widen the run's scope or window. Other flags pass through unchanged.
- An argument holding a shell metacharacter or control character is refused.
- The run MUST be Running or AwaitingAnalyst and have an evidence directory.
- The scope prefixes and window (UTC with a `Z`) are appended from the run,
  with the flag family of the provider's `args_style`.

## Output and bookkeeping

- Output is a JSON array of redacted rows, each with `query_id` (`Q-nnn`, a
  zero-padded run-local counter assigned by the broker). A row's `metrics` are
  also lifted to top-level keys so a `Q-nnn#<row>.<metric>` addend resolves
  against the persisted result.
- Each accepted call is appended to `<evidence dir>/query-log.jsonl` (the
  query-log-entry schema) and its full result is written to
  `<evidence dir>/query-results/Q-nnn.json`.
- The id and the budget slot are reserved in one ledger transaction that
  increments the run's `query_count`; a call beyond the profile's
  `queries_max` is refused. A refused call does not count. A provider failure
  counts, is logged with `rows` 0, and exits 7.
- Errors print as `{"error": "..."}` on stdout.
