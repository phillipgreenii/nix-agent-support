# pg-desk — show, status, sweep, doctor, heartbeat, heartbeat-item

## show

`pg-desk show <pr> [--refresh]` prints the store's interpretation for one PR with its `as_of`
time, and — when `sync.mode` is `plan` — that PR's own planned sync writes (kind and content
hash; see [`sync.md`](sync.md)). `--refresh` MUST run the pipeline (see
[`pipeline-run.md`](pipeline-run.md)) for that id first, then print the resulting interpretation.
This replaces `pg-pr pr view`'s enrichment display and `pg-pr sync --pr N` as the manual re-check
(D20) — the bead-writing call sites this docket's design retargets point here.

Exit codes: `0` on success, including when `--refresh` completes a degraded run (degraded is not
a failure — see [`gather.md`](gather.md)); `1` when `<pr>` does not resolve, or the store cannot
be read or written. `show`'s own failure modes never exceed the `run` contract's exit-`1` case
when `--refresh` is given. A config-load failure (needed only to check `sync.mode`) degrades to
omitting the planned-sync-writes section rather than failing `show` outright.

## status

`pg-desk status` prints the store path and schema version, entity and interpretation counts, the
last heartbeat/run/sweep times, degraded rows, sync errors, and — when `sync.mode` is `plan` —
planned sync rows by kind (anchor / feedback-cycle / review-request; see [`sync.md`](sync.md)). It
also prints `pg-connector ledger show` for the configured consumer.

Exit codes: `0` on success; `1` when the store cannot be opened. A config-load failure (needed
only to check `sync.mode`) degrades to omitting the planned-sync-rows section rather than failing
`status` outright — status's own exit-code floor stays "1 only when the store cannot be opened."

## sweep

`pg-desk sweep` (bead `pg2-gznpe`) is the operator's bulk backfill command: it re-runs the full
`gather` -> `interpret` -> `store` -> `sync` pipeline (see [`pipeline-run.md`](pipeline-run.md)),
always with `--change sweep`, for EVERY entity currently in the store — the same pipeline call a
single `pg-desk run pr <id>` uses (an absent `--change` already defaults to `sweep`; this command
just automates that call across every tracked entity rather than requiring an operator to
enumerate and re-run each one by hand). This is the only way to force a full recompute of every
entity after a `gather`/`interpret` schema or enrichment change — otherwise an entity only
refreshes on its own next webhook-triggered event, and a change that adds a field (e.g. a new
`Enrichment` field) silently leaves any untouched-since entity stale.

Every entity is attempted even after an earlier one fails (mirrors `run issue`/`run thread`'s own
"attempt every one, join errors" convention) — a handful of stale or since-deleted entities must
not stop the rest of the backfill. Once the pass over every entity completes, `sweep` stamps
`meta.last_sweep` (surfaced as `last_sweep_at` on the `serve` dashboard — see
[`serve.md`](serve.md) — and as `last_sweep` in `status`'s own output above), even when one or
more individual entities failed: the SWEEP itself completed, mirroring `run`'s own
"degraded-but-completed is still success" contract.

`sweep` is distinct from, and does NOT implement, [`sync.md`](sync.md)'s still-out-of-scope
"store-wide sweep that re-verifies every ledger row whose entity has left every gathered query" —
that needs a driver over the `ledger` table (which entities have vanished from every live query);
`sweep` only re-runs the pipeline for entities the `entity` table already knows about.

Exit codes: `0` when every entity's pipeline run succeeds (including a degraded run — see
[`gather.md`](gather.md)); `1` when the config or store cannot be opened, or when one or more
entities' pipeline run failed (naming which).

## doctor

`pg-desk doctor` checks: the config resolves; `pg-connector` is on `PATH` and its `config
validate` passes; `serve` is reachable; and the stranded-cycle report formerly produced by
`pr-pool reconcile`. `pg-connector config validate`'s own query-name-coverage check (a
config-authoring signal comparing a backend's declared query names against its peers of the
same type, bead `pg2-2j5ac.28.1`) is informational-only as of `pg2-rnnfz` — it does not affect
that check's pass/fail verdict, so a query-coverage gap alone never fails `doctor`.

Exit codes: `0` when every check passes; `1` when any check fails (naming which one).

## heartbeat / heartbeat-item

`pg-desk heartbeat` stamps `meta.last_heartbeat`. `pg-desk heartbeat-item` prints the single
pr-pool item the `desk-heartbeat` query emits, carrying a timestamp id.

Exit codes: `0` on success; `1` when the store cannot be written (`heartbeat`) or read
(`heartbeat-item`).

## Telemetry and logs

All six commands emit nothing over OpenTelemetry or Prometheus through Phase 10 (D24). `status`'s
counts and `doctor`'s checks are read-side reporting only, not an exported metrics surface — that
surface is `serve`'s minimal `/metrics` (see [`serve.md`](serve.md)). `sweep` is the one exception
to "logs only ordinary CLI report/error text" below: because it calls the SAME per-entity pipeline
`run` does (see [`pipeline-run.md`](pipeline-run.md)), it carries `run`'s own structured-JSON
logging contract too — one line per entity, to stderr, plus the three-stage timeline under
`sweep`'s own `--verbose`. `show`, `status`, `doctor`, `heartbeat`, and `heartbeat-item` carry no
such contract; each of those five logs only ordinary CLI report/error text.

## Out of scope (Phase 9, narrowed by Phase 10)

- `doctor`'s stranded-cycle report is unchanged by Phase 10 — sync minting/reconciling
  agent-signal beads does not, on its own, give `doctor` a report to run; that report's own
  implementation remains this docket's later concern.
- All six commands operate against the single repository this phase supports.
