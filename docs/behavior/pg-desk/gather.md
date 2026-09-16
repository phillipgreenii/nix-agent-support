# pg-desk — gather

Gather is stage 1 of the pipeline (see [`pipeline-run.md`](pipeline-run.md)). It MUST call only
`pg-connector` — never `gh`, `bd`, `pjira`, or any other system client, per the composition rule
(D10) — and MUST read only the ids it is handed plus what the store already links; it MUST NOT
widen its scope to survey. Each fact lands in the `entity` table stamped with the `AsOf` time
`pg-connector` reported.

## Phase 9 inputs

Of the design's full input set, this phase gathers: `pr show`, `pr files`, `pr commits`, `ci
list` for the PR, `issue list --query work-beads` matched to the PR by metadata or title key, and
`issue deps --full` (for waiting-on-me). Every `pg-connector issue` exec — in gather, and later in
sync — MUST carry the beads backend's workspace variable (`PG_CONNECTOR_ISSUE_BEADS_DIR`,
falling back to `BEADS_DIR`) for the PR's own repository, because that backend refuses to run
without one.

**Per-run budget.** `pr files` and `pr commits` are keyed by `head_sha` in the store and are not
re-fetched when the head is unchanged. A `sweep` run skips stage 1 entirely for an entity whose
content hash is unchanged since its last run.

**Degradation.** A gather failure on any input other than the triggering entity itself degrades
that run: interpret proceeds with whatever it has, the interpretation is marked `degraded` naming
the failing input, and the run still exits `0` to pr-pool. A backend answering `unavailable` is
such a degradation. Only a failure to fetch the triggering entity itself is a hard failure (see
[`pipeline-run.md`](pipeline-run.md) for the resulting exit code).

**`--change removed` handling.** Gather re-reads the triggering entity with `pr show`, and the
result decides what "removed" meant: `open` means the PR merely left the named query (only query
membership and match reasons update); `merged` or `closed` is a confirmed closure; `not_found` is
treated as a closure with reason `gone`, and the run exits `0`. An id the store does not know
about is a no-op.

## Exit codes, telemetry, and logs

Gather has no exit code of its own; it contributes to `run`'s exit code (`0` on success or a
degraded fetch, `1` only when the failing fetch is the triggering entity itself — see
[`pipeline-run.md`](pipeline-run.md)).

Gather emits nothing over OpenTelemetry or Prometheus in Phase 9 (D24; OpenTelemetry export is a
later observability item). Its activity is part of `run`'s structured JSON log line to stderr; a
degraded input is named there, and `--verbose` includes it in the three-stage timeline.

## Out of scope (Phase 9)

`issue show` per ticket key found in branch, title, or body, and the linked-thread input, are not
gathered this phase — both arrive with the cross-reference step in Phase 13. Gather targets
exactly one configured repository; multi-repository gather is out of scope.
