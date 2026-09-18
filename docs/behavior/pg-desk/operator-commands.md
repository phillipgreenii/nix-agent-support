# pg-desk — show, status, doctor, heartbeat, heartbeat-item

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

## Telemetry and logs (all five commands)

All five emit nothing over OpenTelemetry or Prometheus through Phase 10 (D24). `status`'s counts
and `doctor`'s checks are read-side reporting only, not an exported metrics surface — that surface
is `serve`'s minimal `/metrics` (see [`serve.md`](serve.md)). None of the five carries `run`'s
structured-JSON logging contract; each logs only ordinary CLI report/error text.

## Out of scope (Phase 9, narrowed by Phase 10)

- `doctor`'s stranded-cycle report is unchanged by Phase 10 — sync minting/reconciling
  agent-signal beads does not, on its own, give `doctor` a report to run; that report's own
  implementation remains this docket's later concern.
- All five commands operate against the single repository this phase supports.
