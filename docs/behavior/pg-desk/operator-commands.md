# pg-desk — show, status, doctor, heartbeat, heartbeat-item

## show

`pg-desk show <pr> [--refresh]` prints the store's interpretation for one PR with its `as_of`
time. `--refresh` MUST run the pipeline (see [`pipeline-run.md`](pipeline-run.md)) for that id
first, then print the resulting interpretation. This replaces `pg-pr pr view`'s enrichment
display and `pg-pr sync --pr N` as the manual re-check (D20) — the bead-writing call sites this
docket's design retargets point here.

Exit codes: `0` on success, including when `--refresh` completes a degraded run (degraded is not
a failure — see [`gather.md`](gather.md)); `1` when `<pr>` does not resolve, or the store cannot
be read or written. `show`'s own failure modes never exceed the `run` contract's exit-`1` case
when `--refresh` is given.

## status

`pg-desk status` prints the store path and schema version, entity and interpretation counts, the
last heartbeat/run/sweep times, degraded rows, sync errors, and — once `sync.mode` exists (Phase 10) — planned sync rows by kind. It also prints `pg-connector ledger show` for the configured
consumer.

Exit codes: `0` on success; `1` when the store cannot be opened.

## doctor

`pg-desk doctor` checks: the config resolves; `pg-connector` is on `PATH` and its `config
validate` passes; every configured query name is recognized by some backend; `serve` is
reachable; and the stranded-cycle report formerly produced by `pr-pool reconcile`.

Exit codes: `0` when every check passes; `1` when any check fails (naming which one).

## heartbeat / heartbeat-item

`pg-desk heartbeat` stamps `meta.last_heartbeat`. `pg-desk heartbeat-item` prints the single
pr-pool item the `desk-heartbeat` query emits, carrying a timestamp id.

Exit codes: `0` on success; `1` when the store cannot be written (`heartbeat`) or read
(`heartbeat-item`).

## Telemetry and logs (all five commands)

All five emit nothing over OpenTelemetry or Prometheus in Phase 9 (D24). `status`'s counts and
`doctor`'s checks are read-side reporting only, not an exported metrics surface — that surface is
`serve`'s minimal `/metrics` (see [`serve.md`](serve.md)). None of the five carries `run`'s
structured-JSON logging contract; each logs only ordinary CLI report/error text.

## Out of scope (Phase 9)

- `status`'s planned-sync-rows section has nothing to show this phase: `sync.mode` is not yet a
  configuration key at all (it arrives in Phase 10 with sync), so `status` never renders that
  section in Phase 9.
- `doctor`'s stranded-cycle report has nothing to find this phase: sync — the stage that would
  create the beads a stranded-cycle check looks for — does not run yet, so this check reports no
  stranded cycles by construction, not because it has verified an empty backlog.
- All five commands operate against the single repository this phase supports.
