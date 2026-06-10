# pg-pr daemon OTLP logs via the otelslog bridge

**Status**: Accepted
**Date**: 2026-06-09
**Deciders**: phillipg

## Context

The `pg-pr / Ops` Grafana dashboard could see the sync-error _count_ (Prometheus
`pg_pr_sync_errors_total`) but not the underlying messages. The daemon logs via
`slog` to stderr only — the `pg-pr-sync` launchd agent redirects stderr to
`~/Library/Logs/pg-pr-sync.err`, and nothing ships that file anywhere. The local
observability stack already runs otelcol with a logs pipeline, but its only
receiver is OTLP (`otlp_http/loki`). The daemon's `telemetry.Init` configured a
trace provider only, and the `pg-pr-sync` service set no `OTEL_*` env vars, so
the logs never reached Loki — leaving silent failures (e.g. "invalid issue type:
feedback" at a count of 253) invisible from the dashboard.

## Decision

Stand up an OTLP `LoggerProvider` in `telemetry.Init` alongside the existing
trace provider (no-op when `OTEL_EXPORTER_OTLP_ENDPOINT` is unset), and bridge
the daemon's `slog` logger to it via
`go.opentelemetry.io/contrib/bridges/otelslog`. The log-signal modules are pinned
to the `v0.20.0` line (`otel/log`, `otel/sdk/log`,
`otlplog/otlploghttp`/`otlploggrpc`), matching the otel core `v1.44.0` already in
use. The `otelslog` bridge versions on its own train and is pinned at `v0.19.0` —
the release that requires `otel/log v0.20.0`.

The daemon logger fans out to **both** stderr (unchanged) and the `otelslog`
bridge. The `pg-pr-sync` service sets `OTEL_SERVICE_NAME=pg-pr-sync` via
`mkEmitterEnv`, so logs land in Loki as `{service_name="pg-pr-sync"}`.

## Consequences

### Positive

- Zero changes to existing log call sites — the bridge is wired at the handler
  level, not at individual `log.Warn`/`log.Error` sites.
- `slog` `WARN`/`ERROR` severity is preserved through the bridge into OTel
  severity, so the dashboard panel can filter on error level. (The exact LogQL
  field carrying severity — `severity_text` vs `detected_level` — depends on the
  running Loki version; it is verified against live Loki at rollout, falling back
  to an unfiltered `{service_name="pg-pr-sync"}` selector if needed. See the
  design spec.)
- Stderr file logging (`pg-pr-sync.err`) is completely unchanged — OTLP is
  purely additive.
- Daemon traces also light up as a free side effect: `OTEL_SERVICE_NAME` routes
  the existing trace export through `pg-pr-sync` in Tempo.

### Negative

- Adds the `otelslog` contrib dependency (`go.opentelemetry.io/contrib/bridges/otelslog`),
  new to this workspace (not vendored by any other package here).

### Neutral

- Daemon traces and logs now appear under `service.name=pg-pr-sync`, distinct from
  one-shot `pg-pr` CLI invocations. This is a deliberate and welcome separation.

## Alternatives Considered

### otelcol `filelog` receiver tailing `pg-pr-sync.err`

Rejected. Smaller in scope (no Go change) but introduces a host-path-coupled file
tail and re-parses JSON that the daemon already has structured in memory. The
native OTLP path is the architecturally correct counterpart to the existing trace
pipeline.

### Dashboard-only forward-contract panel

Rejected. The panel would be empty until ingestion is wired separately, which does
not satisfy the goal of seeing the errors now.

### Raw `otellog.Record` API at fixed `SeverityInfo` (pa-monitor pattern, ADR 0011)

Rejected for this package. `pa-monitor` emits OTLP logs via the raw
`otellog.Record` API at a fixed `SeverityInfo`, with no `otelslog` bridge. pg-pr
is already `slog`-native (every log site is `log.Warn`/`log.Error` with structured
attrs): the bridge requires zero log-site rewrites and, critically, preserves the
`slog` level → OTel severity mapping (`WARN`/`ERROR`) that the dashboard panel
relies on to distinguish errors. Adopting the fixed-`SeverityInfo` pattern would
either flatten the severity we need or force rewriting every log site.

## Related Decisions

- See also: ADR 0011 `docs/adr/0011-pa-monitor-daemon-otel-split.md` — the raw-record
  approach this decision deliberately diverges from.
- See also: ADR 0012 `docs/adr/0012-pg-pr-fingerprint-driven-daemon-sync.md` — the
  daemon sync work that prompted adding log visibility.
- Design spec: `docs/superpowers/specs/2026-06-09-pg-pr-sync-error-log-panel-design.md`
