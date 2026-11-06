# pg-review

Self-review tool. The CLI is grouped by audience:

- `pg-review human` (default output: text): `pending`, `show`, `rule`.
- `pg-review agent` (default output: JSON): `q`, `file`, `context`.
- `pg-review job` (default output: text): `run`, `maintain`, `doctor`, `status`, `show-run`, `resume`.

`--output text|json` overrides the default; there is no terminal detection.

Exit codes (`internal/exitcode`): 0 ok, 2 usage, 3 skipped or already done, 4 validation rejected or
not found, 5 run lock busy, 6 ledger error or state conflict, 7 provider failure or timeout, 8
reserved, 9 never used. A command that is not implemented yet prints `not implemented` and exits 1.

Pinned contracts live in `schemas/` (JSON Schema 2020-12, every payload carries `schema_version` 1)
and are validated through `internal/schema`.

## Implementation notes

- Evidence row `baseline` is an array of `{from, to, metrics}` objects; `window` bounds are RFC 3339.
- Finding sums are `{label, addends: [number], total: number}`; all finding strings are capped at 1200
  characters.
- Schemas are embedded from `schemas/embed.go` and validated with format assertion on.
- Run `./update-deps.sh` after changing dependencies.
