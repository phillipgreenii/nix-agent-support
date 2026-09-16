# pg-desk — open

`pg-desk open` is a port of `pg-pr open`, reading the store directly with no daemon required —
`--addr` is therefore dropped, unlike `pg-pr open`. It composes only the existing darwin opener
and the configured browser (D10's composition rule; `open` itself execs no `pg-connector`
command).

## Flags and defaults

Pinned by the ported `open_test.go`/`open_json_test.go` goldens: `--mine`, `--all`,
`--needs-attention`, `--reason R`, `--owner O`, `--not-owner O`, `--unapproved`,
`--include-hidden`, `--promotable` (new this phase), `--max N`, `--print`, `--json`, and
`--no-hyperlinks`.

- `--all` and `--needs-attention` are mutually exclusive, as are `--json` and `--print`.
- `--reason`, `--owner`, and `--not-owner` MUST be rejected together with `--mine`.
- A merged PR MUST always be excluded.
- The per-side attention default: the team side defaults to needs-attention; `--mine` defaults to
  all.
- The `PG_DESK_OUTPUT=json` environment variable overrides the default text rendering.

`open` opens one browser window with one tab per selected PR.

## Exit codes, telemetry, and logs

`0` on success, including an empty selection; a non-zero code on an invalid flag combination
(e.g. `--reason` together with `--mine`) or when the store cannot be read. The exact non-zero
values are pinned by the ported goldens, not restated here.

`open` emits nothing over OpenTelemetry or Prometheus (D24) — it is a short-lived, synchronous
CLI read. It carries no structured-JSON logging contract of its own (that contract belongs to
`run`; see [`pipeline-run.md`](pipeline-run.md)) — only ordinary CLI error text on failure.

## Out of scope (Phase 9)

`open` selects only within the single repository this phase supports; selecting across multiple
repositories is out of scope.
