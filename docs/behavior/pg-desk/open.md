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
- A PR that is not open (merged, closed unmerged/rejected, or any other non-`open` state) MUST
  always be excluded.
- The per-side attention default: the team side defaults to needs-attention; `--mine` defaults to
  all. On the team side, "needs attention" resolves to exactly the `team_awaiting_me` panel — a
  PR the operator is a requested reviewer on and hasn't approved yet — not every non-blocked team
  PR. `team_awaiting_team` and `team_awaiting_owner` rows are still selectable with `--all`, just
  excluded from the default view.
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

## Typed `pg-desk <type> open [<id>]`

`pg-desk pr open`, `pg-desk issue open` and `pg-desk thread open` are the typed form of this verb
(entity-change-flow design 6.9). They sit beside the top-level `open`, which keeps serving the old
schema unchanged until the cutover phase removes it. The typed forms read the new schema and refuse
a store not yet cut over: they print the store's error, which says to run `pg-desk migrate
--cutover`, and exit `1`.

- With an `<id>` (`pr`: `OWNER/REPO#N`, a PR URL or a bare number; `issue` and `thread`: verbatim)
  the verb handles exactly that one entity. The selection flags and the hidden state do not apply:
  naming the entity is the selection. An id that has no stored entity fails with an error saying it
  does not resolve.
- Without an `<id>` it lists the entities selected from the local snapshots. It never reads the
  network.
- Every listed entity carries its `as_of` staleness, how long ago its snapshot was taken. `--print`
  shows it as an `AS_OF` column such as `2m ago`; `--json` adds `id`, `as_of` and `age_seconds` to
  each row. Without `--print` or `--json` the selected entities are opened as one browser window,
  one tab per entity that has a URL.
- Hidden is read from the `hidden` annotation. A hidden entity is excluded unless
  `--include-hidden` is given.

### What the typed forms take

| Type     | Flags                                                                                                                                 |
| -------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| `pr`     | Every flag and default of the top-level `open`, unchanged: the selection, attention and ownership rules above apply as they do there. |
| `issue`  | `--max`, `--print`, `--json`, `--include-hidden` only. The list is every active stored issue, ordered by id.                          |
| `thread` | `--max`, `--print`, `--json`, `--include-hidden` only. The list is every active stored thread, ordered by id.                         |

### Design items still open

The design cites the companion notes' "CLI reshape direction (Decided in principle, not yet fully
designed)" for this verb. Two items are not designed, and this section records what was chosen so
far rather than inventing more:

- **Criteria vocabulary for `issue` and `thread`.** No selection criteria beyond the generic flags
  above exist yet. An issue or thread list is not narrowed by ownership, state, label or age.
- **Reconciling the design's sample listing with the browser behavior of `pr open`.** The typed
  `pr open` keeps the old verb's behavior (open a window, or `--print`/`--json` to list) and adds
  the `<id>` argument and the `AS_OF` column to its `--print` rendering. Whether the sample listing
  becomes the default is left to the design.
