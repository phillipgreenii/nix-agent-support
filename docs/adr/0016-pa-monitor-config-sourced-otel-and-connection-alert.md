# pa-monitor OTel sourced from the shared config file + daemon-connection alert

**Status**: Accepted
**Date**: 2026-06-23
**Deciders**: Phillip

## Context

The cmux-bridge dumped low-level RPC/transport churn to its pane. Separately,
nothing alerted when the bridge or TUI could not reach the daemon. OTel was
configured only for the daemon, via its launchd plist `EnvironmentVariables`
(ADR 0011) — the bridge and TUI emitted nothing, and there was no single place
that defined OTel for all three processes.

## Decision

1. **OTel configuration is sourced from the shared XDG config file**
   (`~/.config/pa-monitor/config.toml`, `[otel]` block) for the daemon, the
   cmux-bridge, and the TUI. A `config.ApplyOTelEnv` shim exports the standard
   `OTEL_*` env vars (only if unset) so the SDK-native constructors consume
   them. The daemon's plist `EnvironmentVariables` is removed — the config file
   is the single source of truth. agent-support derives the `[otel]` values
   from `phillipgreenii.observability` and writes them into each
   daemon-enabled user's `settings` (a new generic HM passthrough rendered to
   the config file) via `home-manager.sharedModules`.
2. **The cmux-bridge and TUI publish `pa_monitor.daemon.connected{component}`**
   (1/0) via a minimal `otel.ConnEmitter`, and a Grafana rule alerts on
   `min by (component)(pa_monitor_daemon_connected) < 1 for 1m`
   (`noDataState: OK`). Low-level bridge detail moves off the pane to a local
   log file and OTel logs; the pane shows only timestamped, prefix-less,
   operator-facing lines (state changes, session roster, and Lost/Restored
   connection events).

There is intentionally no `protocol` config knob: the emitters import the
gRPC-only OTLP exporter packages, so transport is fixed and the endpoint URL
scheme (`http://` = insecure gRPC) is what selects behaviour.

## Consequences

### Positive

- One consistent OTel config for all pa-monitor processes; no plist/env drift.
- Daemon-down (or wedged) is now alertable within ~1m.
- The cmux-bridge pane is readable; diagnostics are still captured.

### Negative

- A brief first-activation window where the config file may not yet be written
  leaves the daemon's OTel off until its next restart (self-heals via
  `keepAlive`).
- Adding HTTP/protobuf transport later is separate, explicitly-scoped work.

### Neutral

- The bridge and TUI now construct OTel providers (gated off when no endpoint
  is configured, exactly like the daemon).

## Alternatives Considered

### Wrapper executables that inject OTEL\_\* env when launching bridge/tui

Rejected: more moving parts (the cmux-bridge launch point lives outside this
repo's Nix config), and it would not give the daemon/bridge/TUI a single shared
config source. The config file subsumes it.

### Daemon emits a "bridge stale" metric from its existing bridge registry

Rejected: the daemon knows nothing about the TUI, and — critically — if the
daemon itself is down it cannot emit anything, so the alert would not fire in
the exact case it targets.

## Related Decisions

- Extends / partially supersedes `docs/adr/0011-pa-monitor-daemon-otel-split.md`
  (daemon-only emitter; OTel env was plist-sourced).
- Follows the alert-registration pattern of `grafana/alerting/auth-failure.yaml`.
