# pg-desk — serve

`pg-desk serve` is a long-lived HTTP server, run as a launchd user agent by a generic
`services.pg-desk-serve` darwin module in this repo (with package, port, and log-path options).

**Soak-port only, this phase (D18, D27).** In Phase 9, `serve` MUST listen only on the soak port
(default `9819`) behind the ZR `soak.enable` option — never on the primary port `9818`, which
the retired PR tool's `sync` still owns for both its dashboard and its own `/metrics` until the Phase 11 flip. The
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
two-heartbeat-period staleness bound the retired PR tool uses today, so a dead scheduler shows as stale within
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

### Reconcile metrics

`pg-desk reconcile` runs as a separate process, and its `reconcile_open_set` / `reconcile_deferred`
log lines are discarded by the scheduler on a successful run (bead `pg2-q89ng`). It therefore
records a summary of each run in the store (`meta.reconcile.last_run`, see
[`operator-commands.md`](operator-commands.md)), and `/metrics` exposes the LAST run's summary at
scrape time as gauges:

- `pg_desk_reconcile_open_set_ids` and `pg_desk_reconcile_skipped_open` — the open-set size and the
  number of open anchors not re-read because a watched query still listed their PR. A run that did
  not read the open set has NO series for these two (never `0`).
- `pg_desk_reconcile_candidates` — re-drive candidates the run considered.
- `pg_desk_reconcile_deferred` — candidates the run deferred on a transient read failure.
- `pg_desk_reconcile_last_run_age_seconds` — seconds since the run finished, so a stopped schedule
  is visible and the gauges above can be read as current or stale.

Until a run has recorded a summary (and on a store whose value is unreadable) none of these has a
series.

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
  (or that were never hydrated); always 0 for `pr` (no sweep re-hydrates a PR on a clock, and the
  never-hydrated PRs are `pg_desk_pr_unhydrated`). An `issue` entity of a backend that owns its changes
  is never counted: the age sweep skips it, so its `hydrated_at` does not advance and it would otherwise
  be due for ever.
- `pg_desk_consumer_lag{type,consumer}` — the type's highest change sequence minus the consumer's
  cursor.
- `pg_desk_sweep_bound_violated{type,tier}` — `1` for a (type, tier) whose sweep sizing bound
  `active_count / sweep.max_per_poll x poll_interval <= age` is violated, `0` when it holds. The
  bound is checked per capped age tier: `tier="remote"` (re-hydration, age `sweep.max_age`) and
  `tier="local"` (reconcile, age `sweep.reconcile_age`). `active_count` leaves out an `issue` entity of
  a backend that owns its changes, which neither tier touches. It is the same evaluation `doctor` prints
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
expression `max by (type, tier) (pg_desk_sweep_bound_violated{type=~"issue|thread"})`, threshold `>= 1`, `for: 15m`,
warning) fires per (type, tier) while a bound is violated. It is scoped to issue and thread (PR lane
refactor): the PR inputs of the bound vanish with the PR sweep, so a `pr` series is not read. A cap on the sweep is allowed only while
its bound is checked and alerting. An absent series (no verdict) is Normal, not an alert.

- `pg_desk_ingest_failed{backend}` and `pg_desk_ingest_failed_oldest_age_seconds{backend}` — the
  failed-ingest entries (change-feed rows of a backend that owns its changes that could not be
  hydrated; see [`store-schema.md`](store-schema.md), "The failed-ingest table") and the age in seconds
  of the oldest one, derived from the table at every scrape. `backend` is the backend instance of the
  table's rows and of every configured owning instance: an owning instance with an empty table reads
  `0` and `0` (never absent), so an empty table is a value and an absent series means the read failed.
  A deployment with no owning instance and an empty table emits one series with an empty `backend`
  (no label in Prometheus), valued `0`.

The Grafana rule `pg-desk-ingest-failed-old` (`packages/pg-desk/grafana/alerting/alerts.yaml`)
fires per `backend` while the oldest failed-ingest entry is older than `1800` seconds, held for
`for: 10m`, severity `warning`. It reads the outcome of ingest, not that the poll ran: the poll retries the
table at every run (level-triggered, no counter), so an entry this old is one that cannot be hydrated.
This is INGEST recovery (a row that could not be hydrated), not decider recovery.

- The threshold is FIXED in the rule at `1800` seconds, the documented default of
  `hydration.ingest_failed_max_age`. That key governs `pg-desk doctor` only (see [`config.md`](config.md)),
  so a deployment that changes the key changes `doctor` and not this alert.
- The rule has `noDataState: Alerting`, guarded by pg-desk liveness: the series is derived from the
  store at scrape time, so while `pg-desk serve` is up its absence means the store cannot answer, and an
  alert is right. The expression `max by (backend) (pg_desk_ingest_failed_oldest_age_seconds) or
(vector(0) and on() absent(pg_desk_liveness == 1))` answers `0` while `pg_desk_liveness` itself has
  no series (pg-desk down), so the rule stays quiet and `pg-desk-liveness-down` speaks alone. It never
  fires on an empty table, which reads `0`. It carries no `SYSTEM_PAUSE` guard: an empty result would
  alert under `noDataState: Alerting`.
- Its uid is registered with the router probe (`registeredRuleUIDs` in `pg-router-probe`), so a
  firing instance reaches the escalation flow. The rule is modelled in `internal/alertrules` (a model
  test pinning the expression, the firing and clearing, and the NoData behaviour), has a promtool case
  file (`grafana/alerting/rule-tests/pg-desk-ingest-failed-old.test.yaml`) run by
  `grafana/check-alert-rules.sh`, and has its `noDataState` and liveness guard pinned by
  `grafana/check-alert-structure.sh`.

- `pg_desk_watched_source_observed_age_seconds{backend}` — for an owning backend instance (Jira), the age
  in seconds of the OLDEST `change_flow.feed_observed.<instance>.<query>` value across the instance's
  watched queries (the `last_success_at` the feed read took from `sources_freshness`). An instance with
  no value recorded yet reads `0`, so a fresh deployment never pages; the daemon-side alerts cover a
  daemon that never succeeds.
- `pg_desk_issue_unflagged_members{backend}` — the count the last poll stored under
  `change_flow.unflagged.<instance>` (JSON `{"count":n,"at":"<RFC3339>"}`, the count and the read time
  only, never a member set): the distinct members of the instance's watched queries (read with
  `issue list --query Q --backend B`) for which pg-desk holds an entity whose watched flag is not true.
  A member pg-desk does not hold is never counted, and a query whose baseline has been open under one
  day (`baseline_open_age_seconds` of `issue health --backend B`) is skipped. A failed list or baseline
  read, or a truncated list, keeps the previous count; a failed read also makes the poll partial and
  logs `unflagged read degraded: <query>`. No stored count reads `0`.

  A deployment with no owning instance emits one series of each with an empty `backend`, valued `0`
  (no label in Prometheus), so their absence always means the store read failed.

The Grafana rules `pg-desk-watched-source-not-observed` (age over `86400` seconds) and
`pg-desk-issue-unflagged-members` (any non-zero count) fire per `backend`, held for `for: 10m` (two
consecutive 5-minute polls for the second), severity `warning`. Both carry `noDataState: Alerting`
guarded by pg-desk liveness with the same expression tail as `pg-desk-ingest-failed-old`
(`... or (vector(0) and on() absent(pg_desk_liveness == 1))`), so they are quiet while pg-desk is down and
alert while pg-desk is up and the series is missing; neither has a `SYSTEM_PAUSE` guard. Each uid is
registered with the router probe, is modelled in `internal/alertrules`, has a promtool case file under
`grafana/alerting/rule-tests/`, and has its `noDataState` and liveness guard pinned by
`grafana/check-alert-structure.sh`. The `doctor` report "an entity nothing watches" stays a report, not
an alert.

On a store that has not been cut over to the new schema these families MUST be absent from the
scrape; `serve` keeps answering with the dashboard families and MUST NOT refuse or fail the scrape.

### PR lane metrics

`/metrics` also exposes the PR lane's first signals (PR lane refactor, P0b), each emitted with a value
when it is healthy, so that "no series" never reads as "healthy":

- `pg_desk_pr_lane_mode{mode}` — the configured lane mode (`pr_lane.mode`, see [`config.md`](config.md)):
  one series per mode (`off`, `plan`, `apply`), `1` on the configured mode and `0` on the other two. A
  missing or invalid key reads as `off`.
- `pg_desk_pr_unhydrated` — the number of PRs the store lists as open that no hydration has ever
  written: `pr` entities that are active (not marked inactive), at version `0`, not hidden, and not
  known from their stored facts to be closed or merged. It is the signal that would have fired when
  every row of a migrated store was unhydrated and nothing said so.
- `pg_desk_pr_unhydrated_oldest_age_seconds` — the age in seconds of the oldest member of that set
  (from when `pg-desk` first saw it), `0` when the set is empty.

Both unhydrated gauges are computed from the store at scrape time. A scrape whose store read fails
exports NO series for them, never `0`, and a store that has not been cut over has none either; the
mode gauge and every other family are unaffected.

The Grafana rule `pg-desk-pr-never-hydrated` (`packages/pg-desk/grafana/alerting/alerts.yaml`; warning,
`for: 30m`, `noDataState: OK`) fires when `pg_desk_pr_unhydrated` is above `0`, OR when that gauge has
no series while `pg_desk_liveness` is `1` (the store cannot answer). The absence test is in the
expression, not in `noDataState`. It is suppressed while a pg-router `SYSTEM_PAUSE` gate is active,
with the same guard as `pg-desk-sync-stale`. Its uid is NOT registered with the alert probe yet: it is
red for a known reason until the backfill has hydrated the migrated PRs, and registering it earlier
would file a bead for that. The threshold is final (above `0` for `30m`). `internal/alertrules` models
the expression and pins it to the YAML, a `promtool` rule test exercises its four states (the gauge live
and absent, live and `0`, `pg-desk` liveness down, and `SYSTEM_PAUSE` set) and replays 2026-10-19, and a
structure check over the rule file carries plain negative cases (a rule file whose `SYSTEM_PAUSE` guard
was removed, one whose `noDataState` differs, and one whose absence branch was removed each fail).

#### Persisted lane state

The composed command is short-lived and handles one PR, so it cannot hold a cumulative counter or
compute a fleet-wide gauge. It records each run through `pg-desk pr lane-record` (see
[`operator-commands.md`](operator-commands.md), "pr lane-record and pr mark-inactive"), which keeps the
state in the store's `meta` table (see [`store-schema.md`](store-schema.md), "PR lane state in
`meta`"), and `serve` aggregates it at scrape time. Every series below is emitted with a value, `0`
before any run, so the first event is not lost to `increase()`:

- `pg_desk_pr_lane_runs_total{stage,class}` — the lane's non-clean run events: `stage` is `hydrate` or
  `decide`, `class` one of `not_found`, `transient`, `degraded`, `unhydrated`, `unauthenticated`,
  `hydrate_error`, `decide_partial` and `decide_failed` (there is no `retry_exhausted`: pg-router owns
  every retry). Every stage and class pair is emitted.
- `pg_decider_decisions_total{rule,action,outcome,reason}` — the decider's per-rule results. `action`
  is `create`, `update`, `reopen`, `close`, `annotate`, `escalate` or `skip`; `outcome` is `planned`,
  `applied`, `deduped`, `skipped` or `failed`; `reason` is the decider's closed skip reason (carried only
  by a `skip`, empty otherwise); `rule` is one of the decider's rule ids. For every rule, each skip
  reason and each other action with each outcome other than `skipped` is emitted; a recorded series
  outside that set is emitted too. It has a dashboard panel and no alert.
- `pg_desk_pr_lane_residual_prs{mode}` and `pg_desk_pr_lane_residual_oldest_age_seconds{mode}` — the number
  of PRs whose plan after the last recorded run still lists an action, and the age of the oldest such
  residual. `mode` is the persisted LANE-LEVEL mode, which follows the configured `pr_lane.mode`: the value
  sits on that mode's series and the other two read `0`. A residual recorded in another mode (a plan-era
  residual after the flip) is not counted.
- `pg_desk_pr_marked_inactive_total` — PRs the lane marked inactive.
- `pg_desk_pr_lane_lookup_items` and `pg_desk_pr_lane_lookup_ceiling` — the decider's last reported
  work-item lookup item count and configured ceiling (the decider emits no telemetry of its own); `0`
  until a run reports them.

On a store that is not at schema 2, or when the read of the lane state fails, these families have NO
series, never `0`; every other family is unaffected.

#### Open-anchor metrics

An anchor is the parent work item of a PR's feedback, review, fix-ci and resolve-conflict items: an
active issue entity of type `merge-request` whose stored state is not `closed` and that has a `work`
link to a PR. These families are derived from the store's links and PR rows at scrape time, not from
the tracker, not from the daemon and not from the sync ledger (the older anchor gauges read the ledger,
which the legacy deletion removes). Each is emitted with a value when healthy (`0` for an empty view):

- `pg_desk_pr_open_anchors` — the number of PRs, hidden ones excluded, with at least one open anchor.
- `pg_desk_pr_open_anchors_hidden` — the hidden PRs with at least one open anchor, counted apart; a
  hidden PR takes part in none of the other gauges.
- `pg_desk_pr_stale_anchors` — how many of the open-anchor PRs the store shows as merged or closed.
- `pg_desk_pr_stale_anchor_oldest_age_seconds` — the age of the oldest of those, from the PR row's last
  hydration (when the store learned it was closed), `0` when there is none.
- `pg_desk_pr_open_anchor_oldest_hydration_age_seconds` — the longest time since the last hydration
  among the open-anchor PRs the store still shows as open (a PR with no hydration counts from when
  `pg-desk` first saw it; a PR absent from the store has no age). A lost or failed `merged` hydration
  leaves the store saying "open", which the stale count cannot see; this gauge can.

On a store that is not at schema 2, or when the read fails, these families have NO series, never `0`.

### Focus metrics

`/metrics` also exposes the daily-focus families, computed from the store by the one function
(`focus.ComputeHealth`) that the focus alert rules and the `doctor`/`status` focus block read, so the
three cannot disagree. The computation reads only stored rows: it MUST NOT call a tracker, and the
rank behind the candidate and rank-input series is computed at most once per computation. It loads
and ranks the whole store, so a scrape MUST NOT run it synchronously once a result exists: the scrape
serves the last result, and a result older than a short TTL (15 seconds) is refreshed by ONE
background computation shared by every scrape that arrives meanwhile, so the cost of a scrape does
not grow with the store and the focus gauges lag by at most one scrape interval plus one
computation. Only the first scrape after `serve` starts waits for a computation. Every
family is a gauge with a CLOSED, bounded label set, and NO series carries an entity key or a bead
id. "The current plan" and "selected rows" mean the rows of the NEWEST period that holds rows,
never the local day. A focus bead is an issue entity whose own metadata carries `source_id`; an
escalation bead (labelled `human`) is not a focus bead. A bead's age is now minus its tracker
creation time, else the time the store first saw it.

| Family                                          | Labels                                                                                                            | Meaning                                                                                                                                                                                                                          |
| ----------------------------------------------- | ----------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pg_desk_focus_selected`                        | none                                                                                                              | rows of the current plan                                                                                                                                                                                                         |
| `pg_desk_focus_beads`                           | `state`: `open`, `claimed`, `held`, `deferred_other`, `blocked`, `closed`                                         | every focus bead in the store, by state (`blocked` also carries a pinned or hooked bead)                                                                                                                                         |
| `pg_desk_focus_oldest_bead_age_seconds`         | `state`: `open`, `held`                                                                                           | age of the oldest unclaimed open bead (minted and never claimed) and of the oldest held bead; no series for a state with no bead                                                                                                 |
| `pg_desk_focus_pending_beads`                   | `reason`: `unminted`, `hidden`, `suppressed`, `no_bead_by_design`, `unwatched`, `source_terminal`                 | rows of the current plan with no bead to wait for, by reason (below)                                                                                                                                                             |
| `pg_desk_focus_oldest_pending_bead_age_seconds` | `reason`                                                                                                          | seconds since the oldest row of that reason was selected; no series for a reason with no row                                                                                                                                     |
| `pg_desk_focus_divergence`                      | `kind`: the seven kinds below                                                                                     | disagreements between the plan rows, the `focus_selected` annotation and the beads                                                                                                                                               |
| `pg_desk_focus_unwatched_selected`              | none                                                                                                              | selected rows whose entity is in no `change_flow.watchset.<type>.<query>` set; for a backend that owns its changes there is no such set, and a row is unwatched when its entity's watched column is NULL or 0                    |
| `pg_desk_focus_runs`                            | `verb`, `outcome`: `ok`, `partial`, `total`, `period_closed`, `usage`, `empty`                                    | `focus_run` rows by verb and outcome, so partial (exit 2) and total (exit 3) are distinguishable; a run with a NULL exit code (a crash left it unfinalised) counts as `total`; only observed (verb, outcome) pairs have a series |
| `pg_desk_focus_run_age_seconds`                 | none                                                                                                              | seconds since the last non-dry-run `select`; informational; absent when none ran                                                                                                                                                 |
| `pg_desk_focus_rank_degraded_inputs`            | `input`: `unparseable_due`, `unmapped_priority`, `age_fallback`, `unblocks_unavailable`, `status_category_absent` | the silent rank degradations of this scrape's rank                                                                                                                                                                               |
| `pg_desk_watch_listing_status`                  | `type`, `status`: `ok`, `degraded`, `failed`                                                                      | configured watch queries by entity type and persisted listing status; a query whose status was never persisted has no series (it is counted incomplete by `show`, not here)                                                      |
| `pg_desk_focus_candidates`                      | `source`: `pr`, `jira`, `bead`                                                                                    | the candidate set by source, as `show` prints it                                                                                                                                                                                 |
| `pg_desk_focus_linked_candidates`               | none                                                                                                              | candidates in the set only through a link                                                                                                                                                                                        |
| `pg_desk_focus_draft_age_seconds`               | none                                                                                                              | seconds since the stored draft's `made_at`; ABSENT when no draft is stored (never `0`)                                                                                                                                           |
| `pg_desk_focus_plan_rows`                       | `state`: `in_plan`, `finished`, `open_slots`                                                                      | the current plan against its cap (the period's persisted cap, else the default cap); finished rows do not count against the cap                                                                                                  |
| `pg_desk_focus_last_lock_draft_age_seconds`     | none                                                                                                              | the stored draft's age at the last `select` run (its `counts_json.draft_age_seconds`); absent when not recorded                                                                                                                  |
| `pg_desk_focus_last_lock_drift_rows`            | none                                                                                                              | rows a fresh rank would have placed differently at that lock (`counts_json.drift_rows`); absent when not recorded                                                                                                                |
| `pg_desk_focus_jira_status_category_absent`     | none                                                                                                              | active Jira issues whose stored facts carry no status category                                                                                                                                                                   |

Pending reasons are disjoint and are decided in this order for each row of the current plan: the
source is an epic or a bead (its id matches `bead_id_pattern`) is `no_bead_by_design`; the source is
terminal (a merged or closed PR, a done issue) is `source_terminal` whether or not a bead exists,
because a finished item stays in the plan and its bead is held by the decider's terminal-source
hold; a bead exists means the row is not pending at all; then `hidden`, `suppressed`, `unwatched`
(the source is outside every watch query), and finally `unminted` (the `focus_selected` annotation
is set, the source is live, and no bead was minted). Only `unminted` can page: the other reasons are
legitimate permanent states, and counting them would train the operator to mute the alert.

The seven divergence kinds. The row-based kinds are defined over each item's NEWEST `focus_selection`
row only (so an item with rows in two periods, which the verbs never create but a restored backup can,
is not forever mismatched); the two `removed_bead_*` kinds are defined over the annotation and the
link, not over a row (a removed item has no stored row):

- `row_without_annotation` — an item's newest row exists and its `focus_selected` annotation is
  missing, `none` or names a different period than that row.
- `annotation_without_row` — the annotation names a period (it is not `none`) and the item has no
  row in any period.
- `stale_period` — a period that STILL HOLDS rows, has no `closed_at`, and is older than the newest
  period that holds rows or older than yesterday (in `focus.time_zone`). Counted per period.
- `selected_bead_held` — an item's newest row exists and its bead is held (deferred, marker struck,
  unassigned), and the source is not terminal: a finished item's bead is held by design.
- `removed_bead_open_unclaimed` and `removed_bead_claimed` — the item's annotation is `none` (or
  absent) and its focus bead is open and unclaimed, or claimed.
- `terminal_source_bead_open` — an item's newest row exists, its source is terminal, and its bead is
  NOT held (open and unclaimed): the signal that the issue-closed listener is missing or its role is
  still in plan mode. A terminal source whose bead is held counts under none of
  `selected_bead_held`, `terminal_source_bead_open` and `unminted`.

The bounded series are zero-filled: every state, reason, kind, input and source of a closed set has
a series, so a recovery reads as a fall to `0`. The one gap against the design's audit report line
is that the doctor cannot report "the audit comments written today": the stored issue snapshot
carries no comments, so that figure is not available and is never exported.

On a store that is not migrated for the focus tables, or a configuration with no repository, the
focus families MUST be absent from the scrape and the scrape MUST still succeed. A read failure of
the store, by contrast, fails the collection (from the scrape after the computation that hit it)
rather than reading as an all-clear.

Telemetry declaration: the focus verbs emit no OpenTelemetry. This section's families are the
Prometheus view of the store; the verbs' own record is the `focus_run` row and the structured
stderr line ([`focus.md`](focus.md)). The scrape computation logs nothing.

### Focus alert rules

The focus and watch-listing families have eight Grafana alert rules in
`packages/pg-desk/grafana/alerting/alerts.yaml`, in the same folder and group as the other `pg-desk`
rules. Each is a warning with `noDataState: OK` and `execErrState: Error`: query A is the instant
expression below, B reduces it with `last`, and C is a `gt` threshold. An empty state (no draft, no
plan, no series) is no data and is Normal, never an alert. Each rule fires only after its condition
has held for the whole `for` duration. Each expression, selector and `for` is pinned by
`internal/alertrules/focus_rules_test.go`.

| uid                                       | expression                                                                                                                 | threshold | for   | meaning                                                                                                                                |
| ----------------------------------------- | -------------------------------------------------------------------------------------------------------------------------- | --------- | ----- | -------------------------------------------------------------------------------------------------------------------------------------- |
| `pg-desk-focus-pending-beads-age`         | `max(pg_desk_focus_oldest_pending_bead_age_seconds{reason="unminted"}) and on() (pg_desk_pr_lane_mode{mode="apply"} == 1)` | `> 1800`  | `10m` | while the PR lane is in apply mode, a selected, live row has waited over 30 minutes for a bead: the decider role is unbound or failing |
| `pg-desk-focus-divergence-selected-held`  | `max(pg_desk_focus_divergence{kind="selected_bead_held"})`                                                                 | `> 0`     | `30m` | a selected item's bead is held although its source is not terminal                                                                     |
| `pg-desk-focus-divergence-rm-claimable`   | `max(pg_desk_focus_divergence{kind="removed_bead_open_unclaimed"})`                                                        | `> 0`     | `1h`  | a removed item's bead is still open and unclaimed, so a worker can claim work the operator dropped                                     |
| `pg-desk-focus-annotation-divergence`     | `sum(pg_desk_focus_divergence{kind=~"row_without_annotation\|annotation_without_row"})`                                    | `> 0`     | `15m` | a plan row and its `focus_selected` annotation disagree, so a select was interrupted between the two writes                            |
| `pg-desk-focus-terminal-source-bead-open` | `max(pg_desk_focus_divergence{kind="terminal_source_bead_open"})`                                                          | `> 0`     | `1h`  | a finished item's bead is still open: the issue-closed listener is missing or its role is in plan mode                                 |
| `pg-desk-watch-listing-degraded`          | `sum(pg_desk_watch_listing_status{type=~"issue\|thread",status!="ok"})`                                                    | `> 0`     | `30m` | an issue or thread watch query's persisted listing is degraded or failed, so the watch sets and the candidate set may be incomplete    |
| `pg-desk-focus-rank-degraded`             | `sum(pg_desk_focus_rank_degraded_inputs)`                                                                                  | `> 0`     | `30m` | the rank is silently running on a degraded input (the `input` label names which)                                                       |
| `pg-desk-focus-consumer-lag`              | `max(pg_desk_consumer_lag{type="issue"})`                                                                                  | `> 500`   | `30m` | the focus role's single `issue.changed` consumer is over 500 changes behind                                                            |

`pg-desk-focus-pending-beads-age` is suppressed while the lane mode is not `apply`: its expression ends\nin `and on() (pg_desk_pr_lane_mode{mode="apply"} == 1)`, so with the lane in `off` or `plan` mode, or with\nno lane-mode series at all, the whole result is empty and the rule stays Normal. In those modes nothing\nmints a focus bead by design, and `pg-desk doctor` keeps reporting the unminted selections.\n\nThe lag threshold of `pg-desk-focus-consumer-lag` is provisional: the design does not fix it, and
the operator checkpoint sets the final budget from the decider cost it measures. Decider failures
are not covered by a rule here: they reach `pg_router_failures_total{role="<focus role>"}`, which the
existing `pg-router-failure-rate` rule already watches.

### PR lane outcome alerts

Eight Grafana rules in `packages/pg-desk/grafana/alerting/alerts.yaml` read OUTCOMES, never "the
process ran" (the outage of 2026-10-19 read `sync_errors: 0` throughout). Each is a `warning` with
`noDataState: OK` and `execErrState: Error`; query A is the instant expression below, B reduces it
with `last`, C is a `gt` threshold. Every rule ends with the `SYSTEM_PAUSE` guard of `pg-desk-sync-stale`,
so none fires while a pg-router `SYSTEM_PAUSE` gate is active. None of these uids is registered with the
alert probe yet; registration starts at the start of the first apply period (P6), and until then a
deferred-registration allowlist names them.

| uid                               | signal                                                                                                                                       | threshold  | for   | meaning                                                                                                                        |
| --------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ----- | ------------------------------------------------------------------------------------------------------------------------------ |
| `pg-desk-pr-never-hydrated`       | `pg_desk_pr_unhydrated`, or its absence while `pg_desk_liveness` is `1`                                                                      | `> 0`      | `30m` | the lane is not hydrating the PRs it lists, or the store cannot answer                                                         |
| `pg-desk-pr-residual-after-apply` | `pg_desk_pr_lane_residual_oldest_age_seconds{mode="apply"}`                                                                                  | `> 3600`   | `15m` | a PR still has an action in its plan after apply (read in `apply` mode only: in `plan` mode a residual is non-zero by design)  |
| `pg-desk-pr-stale-anchor`         | `pg_desk_pr_stale_anchor_oldest_age_seconds`, only while the lane mode is `apply`                                                            | `> 3600`   | `10m` | an anchor stays open on a PR the store shows merged or closed, longer than the apply pass                                      |
| `pg-desk-pr-anchor-hydration-old` | `pg_desk_pr_open_anchor_oldest_hydration_age_seconds`, only while the lane mode is `apply`                                                   | `> 604800` | `30m` | a PR with an open anchor was last hydrated over 7 days ago (provisional, set from the measured cadence at the plan checkpoint) |
| `pg-desk-pr-lane-hard-failure`    | `increase(pg_desk_pr_lane_runs_total{class=~"unauthenticated\|decide_failed"}[10m])`, or a series that first appears above `0`               | `> 0`      | `0s`  | credentials are gone, or the decider itself failed: any increase fires at once                                                 |
| `pg-desk-pr-lane-failure-rate`    | `increase(pg_desk_pr_lane_runs_total{class!~"unauthenticated\|decide_failed"}[1h])` by stage and class                                       | `> 5`      | `15m` | the other failure classes, including `transient` (the only sign that a delivery failed every router attempt), keep recurring   |
| `pg-desk-pr-hydration-failing`    | `increase(pg_desk_hydration_failures_total{type="pr"}[30m])` over 5, or `pg_desk_repeated_degraded_entities{type="pr"}` above `0`            | `> 0`      | `15m` | PR hydrations fail or degrade repeatedly                                                                                       |
| `pg-desk-pr-hydrate-dead-man`     | `pr-change` dispatches (`pg_router_throughput_total{role="pr-change"}`) in an hour with no increase of `pg_desk_hydrations_total{type="pr"}` | `> 0`      | `15m` | the role is dispatched but its hydrate stage never ran                                                                         |

The two stale-anchor rules read the store's links, not the tracker and not the daemon, and are gated on
`pg_desk_pr_lane_mode{mode="apply"} == 1` (`and on() (...)`): in `plan` mode no anchor is closed, and an
absent mode gauge drops the result. The decisions metric `pg_decider_decisions_total` has a dashboard
panel and NO alert: it exists so an operator can see what the decider is doing over time.

Tests. `grafana/check-alert-rules.sh` wraps query A of each rule as a Prometheus alert and runs the
cases under `grafana/alerting/rule-tests/` with `promtool test rules`: each new rule has a firing, a
healthy and an empty-input case, the primary rule its four states and the replay of 2026-10-19, and the
residual rule a case showing it ignores `mode="plan"` series. `grafana/check-alert-structure.sh` pins
`noDataState` and the `SYSTEM_PAUSE` guard of every outcome rule with plain negative fixtures (a rule
file with the guard removed and one with another `noDataState` each fail). A catalog test
(`internal/httpapi`) asserts that every `pg_desk_` and `pg_decider_` metric a rule names is emitted by a
real scrape of a schema-2 store and, for the lane-run, decisions and residual series, after a run is
recorded the way the composed command records it.

### PR lane remaining alerts

Two more Grafana rules of the same file follow the outcome rules' shape (`noDataState: OK`, query A the
instant expression below, B reducing with `last`, C a `gt` threshold, the `SYSTEM_PAUSE` guard of
`pg-desk-sync-stale` at the end of every expression) and have a firing, a healthy and an empty-input case
under `grafana/alerting/rule-tests/`. Neither uid is registered with the alert probe yet.

| uid                             | severity  | signal                                                                                                                              | threshold | for   | meaning                                                                                                                             |
| ------------------------------- | --------- | ----------------------------------------------------------------------------------------------------------------------------------- | --------- | ----- | ----------------------------------------------------------------------------------------------------------------------------------- |
| `pg-desk-pr-hidden-open-anchor` | `info`    | `pg_desk_pr_open_anchors_hidden`, the hidden PRs with an open anchor, counted from the store at scrape time                         | `> 0`     | `30m` | a hidden PR is skipped by every decider rule including closure, so its anchor stays open even after the PR merges (low severity)    |
| `pg-desk-pr-lookup-ceiling`     | `warning` | `pg_desk_pr_lane_lookup_items / (pg_desk_pr_lane_lookup_ceiling > 0)`, both read from the lane record the composed command persists | `> 0.8`   | `30m` | the decider's work-item lookup holds over 80 percent of its ceiling: the warning before the lane fails closed on a listing above it |

The lookup gauges read 0 until a run reports them, so the ratio is guarded by `ceiling > 0`: with the
ceiling gauge at 0 the denominator is dropped and the rule is quiet instead of dividing by zero (a rule test
pins it). The remedy for the ceiling rule is to raise the configured work-items ceiling and apply, or to
compact closed work items, because closed items only accumulate. The tracked-set rule and the
no-watched-PRs rule read the connector daemon's metrics and live in the connector's alert file
(`packages/pg-connector/grafana/alerting/pg-connector-github-alerts.yaml`); the no-watched-PRs rule reads
`pg_desk_pr_open_anchors` from this service.

## Invariants

- **INV-SERVE-1.** <!-- uuid: 49037238-ded6-4367-a15a-d4e2308dffda --> `pg_desk_pr_lane_mode` MUST be emitted with a value on every scrape (`1` on the configured mode, `0` on
  the other two), and `pg_desk_pr_unhydrated` with its oldest-age companion MUST be emitted with a value
  whenever the store read succeeds on a store at schema 2 (`0` for an empty set), and MUST have no series,
  never `0`, when the store read fails or the store is not at schema 2.
- **INV-SERVE-2.** <!-- uuid: c6c54f9d-a0fb-4544-b613-5c3fffdbd7db --> The persisted lane families (`pg_desk_pr_lane_runs_total`, `pg_decider_decisions_total`, the two
  residual gauges, `pg_desk_pr_marked_inactive_total` and the two lookup gauges) MUST be emitted with a value,
  `0` before any run, on every scrape of a store at schema 2, and MUST have no series when the store is not at
  schema 2 or the read fails. The residual gauges' `mode` label MUST be the lane-level mode, which follows the
  configured `pr_lane.mode`.

- **INV-SERVE-3.** <!-- uuid: 7c3e5a91-2f4b-4d8e-9b06-1a5d7e2c4f38 --> The rule `pg-desk-focus-pending-beads-age` MUST NOT fire while `pg_desk_pr_lane_mode` does not read `1` on
  `apply` (the lane in `off` or `plan` mode, or the gauge absent), and MUST fire as before once it does.
- **INV-SERVE-4.** <!-- uuid: 8ab8ae5c-3ef1-41ef-be6d-fed60758d532 --> `pg_desk_pr_open_anchors`, `pg_desk_pr_open_anchors_hidden`, `pg_desk_pr_stale_anchors`, `pg_desk_pr_stale_anchor_oldest_age_seconds` and `pg_desk_pr_open_anchor_oldest_hydration_age_seconds` MUST be derived from the store's links and PR rows, MUST be emitted with a value (`0` when none) whenever the store read succeeds on a store at schema 2, and MUST have no series, never `0`, when the read fails or the store is not at schema 2. A hidden PR MUST be counted only in `pg_desk_pr_open_anchors_hidden`.
- **INV-SERVE-5.** <!-- uuid: 22ef8d46-9ab4-41fe-add0-fc0a25772a75 --> Every outcome rule of "PR lane outcome alerts" MUST read a metric a rule test can exercise, MUST NOT fire while a pg-router `SYSTEM_PAUSE` gate is active, and MUST have `noDataState: OK`. The residual rule MUST read `mode="apply"` series only, and the two stale-anchor rules MUST NOT fire while `pg_desk_pr_lane_mode` does not read `1` on `apply`.
- **INV-SERVE-6.** <!-- uuid: 09f2a732-be1b-4cc1-aa2d-cda84cf764ba --> `pg-desk-pr-lane-hard-failure` MUST fire on any increase of `pg_desk_pr_lane_runs_total` for class `unauthenticated` or `decide_failed`, with no `for` hold, including the first event of a series that appears already above `0`.
- **INV-SERVE-7.** <!-- uuid: 66d3ee48-e0c9-4da4-a2dc-f515dc67258a --> `pg-desk-sweep-bound-violated` and `pg-desk-watch-listing-degraded` MUST read the issue and thread series only; a `pr` series MUST NOT make either fire.

- **INV-SERVE-8.** <!-- uuid: e631222d-73f7-46fb-a5ca-c4e9283812ee --> `pg_desk_ingest_failed` and `pg_desk_ingest_failed_oldest_age_seconds` MUST be derived from the failed-ingest table at every scrape, MUST be emitted with a value (`0` for an empty table) for every configured owning backend instance and every instance holding entries, and MUST have no series, never `0`, when the read fails or the store is not at schema 2. `pg-desk-ingest-failed-old` MUST fire while the oldest entry of a backend is older than `1800` seconds, MUST clear when the table empties, MUST NOT fire on an empty table, MUST have `noDataState: Alerting` guarded by `pg_desk_liveness` so that it is quiet while pg-desk is down, and its uid MUST be registered with the router probe.

- **INV-SERVE-9.** <!-- uuid: dbe9eda8-edc6-479a-8c83-1e327fe54450 --> `pg_desk_watched_source_observed_age_seconds` and `pg_desk_issue_unflagged_members` MUST be derived from the store at every scrape, MUST be emitted with a value for every configured owning backend instance (`0` when nothing is recorded), and MUST have no series when the read fails or the store is not at schema 2. `pg-desk-watched-source-not-observed` MUST fire while the oldest observation of a backend is older than one day and `pg-desk-issue-unflagged-members` while its count is non-zero across two consecutive polls; each MUST clear afterwards, MUST have `noDataState: Alerting` guarded by `pg_desk_liveness`, and its uid MUST be registered with the router probe. A member pg-desk does not hold MUST NOT be counted, and a query whose baseline has been open under one day MUST be excluded from the count.

## Out of scope (Phase 9, narrowed by Phase 10)

The primary port (`9818`) and the removal of the temporary soak board are the Phase 11 flip.
`sync_error` is populated once sync runs and fails for a PR (Phase 10, see
[`sync.md`](sync.md)) — it is no longer unconditionally empty. Any payload field this dashboard
could eventually carry from cross-referencing is Phase 13.
