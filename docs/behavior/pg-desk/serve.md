# pg-desk — serve

`pg-desk serve` is a long-lived HTTP server, run as a launchd user agent by a generic
`services.pg-desk-serve` darwin module in this repo (with package, port, and log-path options).

**Soak-port only, this phase (D18, D27).** In Phase 9, `serve` MUST listen only on the soak port
(default `9819`) behind the ZR `soak.enable` option — never on the primary port `9818`, which
`pg-pr sync` still owns for both its dashboard and its own `/metrics` until the Phase 11 flip. The
port move to `9818` as the primary is out of scope for this doc set (see "Out of scope" below).

`serve` reads the store only and reloads its config on `SIGHUP`. It MUST NOT block a request on
the pipeline; it only ever reads committed rows.

## Behavior

`serve` MUST return `503` until the store has at least one interpretation. Once it has one, it
serves `GET /api/v1/dashboard` with today's payload contract — the five named selectors
(`team_awaiting_owner`, `team_awaiting_team`, `team_awaiting_me`, `mine_awaiting_me`,
`mine_awaiting_team`) and the root fields `generated_at`, `age_seconds`, `stale`, `stale_after_seconds`,
`sync_interval_seconds`, `dropped_count` — plus the fields this phase adds: a `hidden` array,
`last_run_at`, `last_sweep_at`, `runs_failed_24h`, `errors[]`, and per-row `degraded`,
`sync_error`, and `ready_to_promote`. It also carries an additive `sources[]` — the per-source data age (see [`freshness.md`](freshness.md)): one row per
connector source with `source`, `label`, `last_success_at`, `age_seconds` (both `null` for a source
with no recorded success) and `stale`. The payload's own `generated_at`, `age_seconds` and `stale`
are PIPELINE LIVENESS, not data age: they derive from `meta.last_heartbeat` with the same
two-heartbeat-period staleness bound `pg-pr` uses today, so a dead scheduler shows as stale within
two heartbeat periods, whatever the sources say. A stale source never flips them, and a stale
heartbeat says nothing about how old the data behind it is (`INV-FRESH-5`).

### The `attention` field

The payload also carries an additive `attention` field: the attention evaluator's groups
([`attention.md`](attention.md), "The dashboard payload"). It is exactly the groups the
`pg-desk-attention` plugin emits for the same store, computed by the same `Evaluate` call (the
first configured repository, the same rules and configuration), so the dashboard and the menu bar
cannot disagree (`INV-ATTNEVAL-2`). Each group is `{key, label, items[]}`, groups and the items
inside them in the evaluator's canonical order; each item is `{type, id, summary, severity, rule,
group}`, `type` and `id` being a valid ref for `pg-desk links`.

- `attention` is `[]` when nothing needs the operator.
- If the evaluation cannot run (no repository configured, an unknown rule kind in the `attention`
  config block, an unreadable store), `attention` is `null` and the root field `attention_error`
  carries the reason. `serve` MUST NOT send `[]` for a failed evaluation (`INV-ATTNEVAL-6`), and
  MUST NOT fail the whole payload for it: the five panels, `hidden` and the freshness fields are
  served as usual. `attention_error` is absent when the evaluation succeeded.
- The five panel arrays and every existing root field are unchanged.

## Exit codes

`serve` is a long-running process: it exits non-zero when it cannot open its config or its store
at startup, and `0` on a clean shutdown. No other exit code is assigned to it in Phase 9.

## Telemetry and logs

`serve` exposes a real Prometheus metrics catalog on `/metrics` (same route, same
503-until-ready gate as `/api/v1/dashboard`): `pg_desk_liveness` (1 while `serve` answers a
scrape), `pg_desk_dashboard_age_seconds` and `pg_desk_dashboard_stale` (promoted from the
dashboard payload's own `age_seconds`/`stale` fields — pipeline LIVENESS, the age of
`meta.last_heartbeat`, not the age of the data; `stale` is `0 = fresh`, `1 = stale`, the
opposite polarity from a presence-style gauge, and a stale source does not flip it), `pg_desk_dropped` (promoted from
`dropped_count`, a point-in-time gauge, matching this payload's own field), and
`pg_desk_sync_errors_total` (a counter, by repo — registered and exposed but with no live call
site wired here, since `serve` never itself runs sync). `pg_desk_source_age_seconds{source}` is the
DATA age: the seconds since each source's last successful origin fetch, one series per source with
a recorded success and none for a source without one (never exported as `0`; see
[`freshness.md`](freshness.md)). Five further gauges are read from the shared
store at scrape time, so they do reflect failures `run` recorded: `pg_desk_sync_error_rows` (rows
with a recorded `sync_error`), `pg_desk_oldest_sync_error_age_seconds`, and — splitting those rows
by their automatic-retry state (bead `pg2-xb6fs`, see [`sync.md`](sync.md)'s "Automatic retry")
— `pg_desk_sync_error_retrying_rows` (still being retried, so they may heal on their own) and
`pg_desk_sync_error_exhausted_rows` (no automatic retry left: the bound was reached, or the failure
is non-transient; these need an operator). The fifth, `pg_desk_oldest_anchor_check_age_seconds`
(bead `pg2-u4c1s`), is the age in seconds of the stalest applied anchor check: the oldest ledger
`last_synced_at` among `anchor` rows that have a bead and are not closed, `0` when there are none
(and on a store that has been cut over, which has no ledger). An unchanged check writes only that
ledger time, never the bead, so this gauge is how a stalled sync stays detectable. The Grafana alert rules follow that split (bead
`pg2-qki4v`): `pg_desk_sync_error_exhausted_rows > 0` pages critical, while
`pg_desk_sync_error_retrying_rows > 0` is only a warning, and only once it outlives 30 minutes — a
row a restart left mid-dispatch heals on its own. The age rule (`pg-desk-unreconciled-anchor-age`,
bead `pg2-h7grf`) follows the same policy: `pg_desk_oldest_sync_error_age_seconds` has no per-state
variant, so a healthily retrying row ages too, and the rule stays critical but only above 21600
seconds (6 hours), which is past every automatic retry (the default 10 retries back off 1m, 2m, 4m,
8m, 16m, then 30m five times, 181 minutes in total, and run only on the 30m `reconcile` cadence, so
at most about 5 hours) — a row that old is exhausted or stuck, not retrying. `serve`'s WARN/ERROR-level operational
log lines additionally export over OTLP as `{service_name="pg-desk-serve"}`
(`internal/telemetry`'s `Init`, wired at `serve` startup only — `run`/`run issue` are untouched);
`serve` still logs to the path its launchd module configures, defaulting to
`~/Library/Logs/pg-desk-serve.log`. This resolves the observability review's (`pg2-7kizi`) `serve`
half.

### Change-flow metrics

`/metrics` also exposes the change-flow families, per entity `type` (and, where noted, `kind`,
`origin` and `consumer`), computed fresh from the store at every scrape:

- `pg_desk_change_log_records{type,kind,origin}` — change-log records currently retained, by kind
  and origin (a gauge: the log is pruned, so it can fall).
- `pg_desk_hydrations_total{type}`, `pg_desk_hydration_failures_total{type}` and
  `pg_desk_occ_retries_total{type}` — hydrations, hydrations that errored or came back degraded, and
  optimistic-concurrency retries. These happen in the `changes` and `refresh` processes, not in
  `serve`, so they are read from the totals those processes persist.
- `pg_desk_repeated_degraded_entities{type}` — entities with repeated degraded hydrations (the one
  definition `status` and `doctor` share).
- `pg_desk_due_backlog{type}` — active entities whose `hydrated_at` is older than the sweep max age
  (or that were never hydrated).
- `pg_desk_consumer_lag{type,consumer}` — the type's highest change sequence minus the consumer's
  cursor.
- `pg_desk_sweep_bound_violated{type,tier}` — `1` for a (type, tier) whose sweep sizing bound
  `active_count / sweep.max_per_poll x poll_interval <= age` is violated, `0` when it holds. The
  bound is checked per capped age tier: `tier="remote"` (re-hydration, age `sweep.max_age`) and
  `tier="local"` (reconcile, age `sweep.reconcile_age`). It is the same evaluation `doctor` prints
  (`changes.EvaluateSweepBound`), so the metric and the doctor line always agree. `pg-desk` does not
  own the poll interval: `serve` reads it from the router config named by `serve --router-config
<path>` (or the `PG_DESK_ROUTER_CONFIG` environment variable), re-reading the file on every scrape
  and using the same lookup as `doctor --router-config` (the smallest period among the router queries
  naming the type). When the interval is not supplied, the file is unreadable, or no router query
  names the type, that type has NO series: the metric reports no verdict and MUST NOT report a
  violation. The deployment MUST pass `serve` the router config: the generic launchd module derives
  the path from the pg-router daemon's own declarative config (the very file the router runs on)
  and lets an operator override it; where no router daemon is configured, the metric stays absent.

The Grafana rule `pg-desk-sweep-bound-violated` (`packages/pg-desk/grafana/alerting/alerts.yaml`,
expression `max by (type, tier) (pg_desk_sweep_bound_violated)`, threshold `>= 1`, `for: 15m`,
warning) fires per (type, tier) while a bound is violated. A cap on the sweep is allowed only while
its bound is checked and alerting. An absent series (no verdict) is Normal, not an alert.

On a store that has not been cut over to the new schema these families MUST be absent from the
scrape; `serve` keeps answering with the dashboard families and MUST NOT refuse or fail the scrape.

## Out of scope (Phase 9, narrowed by Phase 10)

The primary port (`9818`) and the removal of the temporary soak board are the Phase 11 flip.
`sync_error` is populated once sync runs and fails for a PR (Phase 10, see
[`sync.md`](sync.md)) — it is no longer unconditionally empty. Any payload field this dashboard
could eventually carry from cross-referencing is Phase 13.
