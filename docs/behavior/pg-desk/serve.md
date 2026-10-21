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
| `pg_desk_focus_unwatched_selected`              | none                                                                                                              | selected rows whose entity is in no `change_flow.watchset.<type>.<query>` set                                                                                                                                                    |
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

| uid                                       | expression                                                                              | threshold | for   | meaning                                                                                                                       |
| ----------------------------------------- | --------------------------------------------------------------------------------------- | --------- | ----- | ----------------------------------------------------------------------------------------------------------------------------- |
| `pg-desk-focus-pending-beads-age`         | `max(pg_desk_focus_oldest_pending_bead_age_seconds{reason="unminted"})`                 | `> 1800`  | `10m` | a selected, live row has waited over 30 minutes for a bead: the decider role is unbound, still in plan mode, or failing       |
| `pg-desk-focus-divergence-selected-held`  | `max(pg_desk_focus_divergence{kind="selected_bead_held"})`                              | `> 0`     | `30m` | a selected item's bead is held although its source is not terminal                                                            |
| `pg-desk-focus-divergence-rm-claimable`   | `max(pg_desk_focus_divergence{kind="removed_bead_open_unclaimed"})`                     | `> 0`     | `1h`  | a removed item's bead is still open and unclaimed, so a worker can claim work the operator dropped                            |
| `pg-desk-focus-annotation-divergence`     | `sum(pg_desk_focus_divergence{kind=~"row_without_annotation\|annotation_without_row"})` | `> 0`     | `15m` | a plan row and its `focus_selected` annotation disagree, so a select was interrupted between the two writes                   |
| `pg-desk-focus-terminal-source-bead-open` | `max(pg_desk_focus_divergence{kind="terminal_source_bead_open"})`                       | `> 0`     | `1h`  | a finished item's bead is still open: the issue-closed listener is missing or its role is in plan mode                        |
| `pg-desk-watch-listing-degraded`          | `sum(pg_desk_watch_listing_status{status!="ok"})`                                       | `> 0`     | `30m` | a configured watch query's persisted listing is degraded or failed, so the watch sets and the candidate set may be incomplete |
| `pg-desk-focus-rank-degraded`             | `sum(pg_desk_focus_rank_degraded_inputs)`                                               | `> 0`     | `30m` | the rank is silently running on a degraded input (the `input` label names which)                                              |
| `pg-desk-focus-consumer-lag`              | `max(pg_desk_consumer_lag{type="issue"})`                                               | `> 500`   | `30m` | the focus role's single `issue.changed` consumer is over 500 changes behind                                                   |

The lag threshold of `pg-desk-focus-consumer-lag` is provisional: the design does not fix it, and
the operator checkpoint sets the final budget from the decider cost it measures. Decider failures
are not covered by a rule here: they reach `pg_router_failures_total{role="<focus role>"}`, which the
existing `pg-router-failure-rate` rule already watches.

## Out of scope (Phase 9, narrowed by Phase 10)

The primary port (`9818`) and the removal of the temporary soak board are the Phase 11 flip.
`sync_error` is populated once sync runs and fails for a PR (Phase 10, see
[`sync.md`](sync.md)) — it is no longer unconditionally empty. Any payload field this dashboard
could eventually carry from cross-referencing is Phase 13.
