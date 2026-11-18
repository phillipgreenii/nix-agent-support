# Metrics, catalog and alert rules

Notes for the metrics work: the choices the specification left open, the alert uids, and the
follow-up that lives in the deployment repository.

## What is emitted

Every metric is a gauge computed from the ledger (`internal/ledger/metrics.go` reads the figures,
`internal/metrics` names them). The names, types and label keys are in `metrics.catalog`, one
`<name> <type> <labels>` line each; a test fails when the code and the file disagree. No name ends
in `_total`, `_bucket`, `_sum` or `_count`.

Gauges are emitted:

- at the end of every run that wrote a run row, whatever its outcome (`internal/runner/pipeline.go`);
- when `agent file` completes an interactive run (the run ends there, not in the pipeline);
- after every `human rule`;
- by `job maintain` (its metrics step), which runs every 5 minutes.

## Best effort

Metrics are optional. With no `OTEL_EXPORTER_OTLP_ENDPOINT` (or
`OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`) the push does nothing and says nothing. A push that fails
or times out (2 seconds for the whole push) is logged at debug level (`PG_REVIEW_DEBUG=1` shows
it on standard error) and never changes a command's outcome or exit code. A test runs every
ledger-touching command with no endpoint and with an unreachable one (HTTP and gRPC) and asserts
identical exit codes and output.

Transport choice: the tool posts OTLP/HTTP JSON itself (standard library only, so the module gains
no dependency). The observability module's emitter environment may select the `grpc` protocol;
the tool does not speak gRPC, so with that protocol, or when the HTTP push fails, it falls back to
`ccpool emit-gauge` (one call per gauge), tried only when `ccpool` is on `PATH`.

## Definitions the specification left to the implementer

- `pending`, `oldest pending age`, topics, decisions and outbox-failed carry `lens` and `scope`;
  a topic's scope is the scope of its latest occurrence (empty, so the label is omitted, when it
  has none). The age of the oldest pending item is measured from the topic's last update, the same
  clock the `pending` banner uses.
- `pg_review_subjects` counts a subject once per (lens, scope, type) of a topic that tracks it; a
  subject no topic tracks carries no lens or scope.
- With nothing pending, an unlabelled zero is emitted for `pg_review_pending_items` and
  `pg_review_oldest_pending_age_seconds`, so the series does not go absent.
- The run duration is `ended_at - created_at` of the latest terminal run of the (lens, scope),
  falling back to the stored elapsed minutes.
- The three schedule gauges (`pg_review_last_success_timestamp_seconds`,
  `pg_review_last_window_seconds`, `pg_review_consecutive_failed_runs`) count only runs that carry
  a schedule id. A manual run produces no series and neither resets nor adds to the failure count;
  a Skipped run neither adds to it nor resets it; a Filed run resets it. A schedule that never
  filed has no last-success or last-window series, only a failure count and the
  `pg_review_unfiled_window_fraction` series below.
- `pg_review_unfiled_window_fraction{lens,scope,schedule}` is computed from the schedule's latest
  terminal scheduled run (Filed, Skipped or Failed). It is 0 when any run sharing that run's run
  key Filed, and otherwise `(now - window end) / window length` (0 while the window is still open).
  So it exists for a schedule that only ever ended Skipped or Failed (a never-Filed lens), it
  excludes manual runs (no schedule id), it returns to 0 when the window Files (including by
  `job catch-up --force`), and a daily and a weekly schedule of one lens and scope are separate
  series.

- `pg_review_infrastructure_skipped_runs{lens,scope,reason}` is the count of Skipped runs whose
  reason is an infrastructure one, by class: `capacity` (the ccpool capacity check failed after its
  retries), `usage_limit` and `busy`. It is derived from the stored skip reason (the runner and the
  ledger share the reason prefixes), so it needs no schema change and survives restarts. Skips for
  the pending cap or a paused lens are not counted, and a permanent capacity fault is a Failed run,
  not a skip. Each such skip also writes one stderr line
  `pg-review: infrastructure skip run=<id> lens=<lens> scope=<scope> reason_class=<class>`.

## Catch-up of Skipped scheduled windows

A scheduled run that ends Skipped (ccpool capacity check failure, usage limit, busy pool) exits
with the Skipped code, which the router adapter reports to the router as success, so the router
never retries it. The router role `review-catchup` (an hourly period query, rendered by the Nix
module while schedules exist) runs `pg-review job catch-up` through the adapter. It re-runs, with
the same window, run key and schedule id, the latest scheduled run of a (lens, scope, schedule)
when that run is Skipped, no run with its run key Filed, fewer than 24 runs share the key, the
newest attempt ended at least 55 minutes ago, and now is before the window end plus one window
length (the next slot supersedes it). At most one window is re-run per invocation, under the same
run lock as `job run`. A Failed run (an analyst failure) is not retried. `job catch-up <lens>
[--scope S] --force` ignores the cooldown, the cap and the expiry, also re-runs a Failed window,
and carries the schedule id and run key, so a Filed result clears the unfiled-window alert (a
plain `job run` does not).

## Alerts

`grafana/alerting/alerts.yaml` holds four warnings, checked by `check-alert-rules.sh` (structure
pins plus `promtool test rules` over `grafana/alerting/rule-tests/`; run through the flake check
`test-pg-review-alert-rules`).

| Uid                           | Fires when                                                                       | For |
| ----------------------------- | -------------------------------------------------------------------------------- | --- |
| `pg-review-pending-too-large` | the sum of `pg_review_pending_items` is above 12                                 | 1h  |
| `pg-review-lens-not-running`  | per schedule, `time()` minus the last Filed window end exceeds twice that window | 30m |
| `pg-review-runs-failing`      | `pg_review_consecutive_failed_runs` is 3 or more for one schedule                | 30m |
| `pg-review-window-unfiled`    | `pg_review_unfiled_window_fraction` is above 0.125 (strictly) for one schedule   | 30m |

Expression choices. All four read `max_over_time` and end in `or vector(0)`, because OTLP gauges
go stale. The default is safe in each: a missing series becomes 0, and 0 is not above any
threshold, so a schedule that never filed cannot fire the not-running rule; the unfiled-window
rule covers it (and a first Skipped or Failed window), because the unfiled-window series exists
for every schedule with a scheduled run. The failing rule still needs 3 Failed runs in a row. The
unfiled-window threshold is one eighth of the window: 3 hours for a 24 hour lens (about 3.5 hours
after the slot with two hourly catch-ups already tried) and 21 hours for a 7 day lens. Lookbacks: 10 minutes for the pending and failing rules
(so a recovery clears quickly), one day for the not-running rule (its two values only move forward,
so the last values stay valid across a pusher outage). The not-running rule aggregates both sides
to `lens, scope, schedule` and matches on exactly those labels, so a lens and scope with a daily
and a weekly schedule is judged per schedule. The cap 12 is the default `items_max_pending`.

The darwin module `darwin/modules/pg-review/default.nix` registers the alert-rule file with the
observability module, gated exactly as the keep-awake darwin module is: observability enabled and
some home-manager user enabling `phillipgreenii.programs.pg-review`.

## Follow-up in the deployment repository (not done here)

A new alert rule uid must also be registered with the router's probe in the deployment
repository, or it never escalates to a bead, and the deployment's guard that lists rule files may
need this file added. Register these uids there:

- `pg-review-pending-too-large`
- `pg-review-lens-not-running`
- `pg-review-runs-failing`
- `pg-review-window-unfiled`

Also register a `requires` clause for the `review-catchup` role (the same resource levels as
`review-run`), through the module's `requires."review-catchup"` option.

This repository does not check either, because both live in another repository.
