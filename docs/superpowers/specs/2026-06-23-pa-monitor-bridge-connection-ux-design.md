# pa-monitor: human-friendly cmux-bridge output + daemon-connection alerting

**Status**: Draft
**Date**: 2026-06-23
**Deciders**: Phillip

## Context

The `cmux-bridge` subcommand runs in a cmux pane and streams `DaemonState`
from the daemon to drive the cmux sidebar. Today it dumps low-level
diagnostics straight to the pane's stderr, each prefixed with `cmux-bridge:`
and with no timestamp:

```
cmux-bridge: version=0.0.0-f706966c daemon=0.0.0-f706966c
cmux-bridge: initial state: Caffeinated Enabled, Auto Nudge Disabled
cmux-bridge: Auto Nudge Enabled
cmux-bridge: RegisterBridge: rpc error: code = DeadlineExceeded desc = context deadline exceeded
cmux-bridge: cmux set-status claude-agents: signal: killed
cmux-bridge: RegisterBridge: rpc error: code = Unavailable desc = connection error: ... daemon.sock: connect: no such file or directory
cmux-bridge: stream lost: rpc error: code = Unavailable desc = closing transport ... received prior goaway: ... "graceful_stop"
cmux-bridge: initial state: Caffeinated Disabled, Auto Nudge Disabled
```

Most of this is noise to a human watching the pane. The retry/RPC churn, the
transport dial errors, and the `cmux set-status` subprocess failures are
debugging detail, not operator-facing events. What an operator actually wants
to see is: state changes they care about, which sessions are present, and —
crucially — **whether the bridge is talking to the daemon**.

Separately, there is no alert when a bridge (or the TUI) cannot reach the
daemon. A daemon that has crashed or is wedged is invisible until someone
notices the sidebar has gone stale.

### Current behaviour (code references)

- `cmd/pa-monitor/cmux_bridge.go` — `runCmuxBridge` / `streamOnce`. All output
  is `fmt.Fprintln(os.Stderr, "cmux-bridge:", …)`. The reconnect loop logs
  `stream lost: %v` on every failure and sleeps 2s. `registerBridge` logs
  every `RegisterBridge` error. `logBridgeVersions` prints the startup banner.
- The `cmuxstatus.Reporter` is constructed with
  `Logf: func(s) { fmt.Fprintln(os.Stderr, "cmux-bridge:", s) }` — this is the
  source of the `cmux set-status …: signal: killed` line.
- `internal/otel/emitter.go` — the OTel `Emitter`. `otel.New` returns a no-op
  `(nil, nil)` when `OTEL_EXPORTER_OTLP_ENDPOINT` is unset. **Only the daemon
  constructs an emitter** (`cmd/pa-monitor/daemon.go:120`); the bridge and TUI
  emit nothing.
- `internal/config/config.go` — pa-monitor already reads an XDG TOML config at
  `$XDG_CONFIG_HOME/pa-monitor/config.toml` (`DefaultPath`). The daemon
  (`daemon.go:54`), the TUI (`tui_remote.go:27`), and `config_helper.go` all
  load it. The bridge does **not** yet.
- `internal/rpcclient/remote_poller.go` — the TUI's `RemotePoller` already
  tracks reconnect state: `IsOffline()`, `LastFreshAt()`, backoff.
- `darwin/modules/pa-monitor/default.nix` — derives `emitterEnv =
obs.mkEmitterEnv { serviceName = "pa-monitor"; protocol = "grpc"; }` and
  injects it into the daemon LaunchAgent plist via
  `serviceConfig.EnvironmentVariables`. Also registers Grafana alert rule
  files via `phillipgreenii.observability.alertRuleFiles` (currently just
  `grafana/alerting/auth-failure.yaml`).
- `grafana/alerting/auth-failure.yaml` — the template for a provisioned
  Grafana alert (instant Prometheus query → reduce → threshold).

## Goals

1. The `cmux-bridge` pane shows **human-friendly** output: timestamped, no
   `cmux-bridge:` prefix, no low-level retry/RPC/transport noise.
2. Connection state is explicit: a single `Lost connection to daemon` on
   disconnect and a single `Connection to daemon restored` on recovery;
   intervening retries are silent on the terminal.
3. Low-level detail is preserved in **logging and OTel**, not discarded.
4. An OTel-backed **alert fires when the cmux-bridge OR the TUI cannot reach
   the daemon for more than a minute**.
5. OTel configuration is sourced from a **single XDG config file** shared by
   the daemon, bridge, and TUI — no wrappers, no per-process env divergence.

## Non-goals

- Changing the TUI's on-screen rendering of offline state (it already shows an
  offline indicator). The TUI change here is limited to emitting the
  connection signal for alerting.
- Alerting on a _daemon that exited_ via the bridge/TUI signal. If the bridge
  process itself is killed, its series simply goes stale; this alert targets
  "process alive but cannot reach the daemon." (Daemon liveness is a separate
  concern, already covered by the launchd `keepAlive` and existing daemon
  metrics.)
- Reworking the daemon's own metrics/gauges.

## Decisions

### D1. Terminal output: format and scope

A new line formatter prepends a local date+time stamp and drops the prefix:

```
2006-01-02 15:04:05 <message>
```

(Go layout `2006-01-02 15:04:05`, local time.)

Lines that **remain on the terminal** (operator chose "connection + state +
sessions"):

| Event               | Old                                | New                                     |
| ------------------- | ---------------------------------- | --------------------------------------- |
| Startup banner      | `cmux-bridge: version=X daemon=Y`  | `<ts> pa-monitor bridge vX (daemon vY)` |
| Caffeinate toggle   | `cmux-bridge: Caffeinated Enabled` | `<ts> Caffeinated enabled`              |
| Auto-nudge toggle   | `cmux-bridge: Auto Nudge Enabled`  | `<ts> Auto Nudge enabled`               |
| Session added       | `cmux-bridge: +<pid> <sid>/<name>` | `<ts> +<pid> <sid>/<name>`              |
| Session removed     | `cmux-bridge: -<pid> <sid>/<name>` | `<ts> -<pid> <sid>/<name>`              |
| Connection lost     | (was: `stream lost: …`)            | `<ts> Lost connection to daemon`        |
| Connection restored | (none)                             | `<ts> Connection to daemon restored`    |

Note the verb case lowercases to `enabled`/`disabled` (was title-case);
existing assertions in `cmux_bridge_test.go` and `render/controls_test.go`
that check the bridge's emitted phrases must be updated. (The
`render/controls_test.go` strings belong to the sidebar renderer, which is a
different surface — verify whether they share the phrase constant before
changing.)

Lines **removed from the terminal** and routed to logging/OTel only:

- `RegisterBridge: <err>` (every heartbeat failure)
- `stream lost: <err>` and the underlying transport/dial errors
- `push missed: no message in <budget>`
- the `cmuxstatus.Reporter` `Logf` output (`cmux set-status …: signal: killed`)
- the startup "daemon unreachable" diagnostic variants of the banner

### D2. Connection state machine

The bridge tracks one boolean of operator-visible connection state with an
`announcedLost` flag to make transitions idempotent:

- **Disconnect** (any `streamOnce` error, or an initial dial failure): if
  `!announcedLost`, print `Lost connection to daemon`, set `announcedLost =
true`, record gauge `connected = 0`, and emit a log/OTel event with the
  underlying error. While `announcedLost` is true, subsequent retry failures
  print nothing to the terminal (the error detail still goes to log/OTel).
- **(Re)connect** (a stream is established and the first message is received):
  if `announcedLost`, print `Connection to daemon restored`, clear
  `announcedLost`, record gauge `connected = 1`. At clean startup
  (`announcedLost` was never set), no "restored" line is printed.

This lives in `runCmuxBridge`'s reconnect loop, replacing the current
`stream lost`/`time.Sleep(2s)` logging. The 2s backoff between retries is
retained; only the _logging_ changes.

### D3. Low-level detail sink

Low-level detail must survive even when OTel is disabled. Two sinks:

1. **Local log** — the bridge writes structured detail to its **own**
   bridge-named log file under `~/.cache/pa-monitor` (e.g.
   `cmux-bridge.log`), built on the same mechanism as `tui.ErrorLogger`
   (which writes `<CacheDir>/signal-errors.log`,
   `internal/tui/errorlog.go:11,35`). A separate file avoids co-mingling
   bridge detail with the TUI's signal-error log. Detail is always captured
   on disk, even when OTel is off.
2. **OTel logs** — when an emitter is configured, the same detail is emitted
   via `Emitter.LogEvent(name, attrs)` (Loki). Event names:
   `daemon.disconnect`, `daemon.reconnect`, `bridge.register_failed`,
   `bridge.stream_lost`, `bridge.push_missed`, plus the reporter's own errors.

The TUI already has `tui.ErrorLogger` for its low-level detail; it gains the
OTel `LogEvent` calls on the same transitions.

### D4. Connection signal (the alertable metric)

A new **minimal** OTel emitter for the bridge and TUI, distinct from the
daemon's full emitter (which registers ~12 daemon-specific gauges that would
sit unused in a bridge/TUI process).

- New constructor `otel.NewConnectionEmitter(ctx, opts)` in a new file
  `internal/otel/connection.go`. It builds the metric + log providers (reusing
  `buildResource`) and registers exactly one observable gauge:
  `pa_monitor.daemon.connected` — `Int64ObservableGauge`, value `1`
  (connected) / `0` (disconnected), attribute `component` ∈ {`cmux-bridge`,
  `tui`}. It exposes `RecordDaemonConnected(bool)`, `LogEvent`, and
  `Shutdown`, and is nil-safe like the existing `Emitter`.
- Export cadence: the connection emitter uses a **15s** `PeriodicReader`
  interval (vs the daemon's default 60s) so a `connected = 0` reading reaches
  Prometheus quickly and the `for: 1m` alert is responsive.
- A buffered-value + `known` gate (same idiom as the daemon emitter's
  `caffeinateActiveKnown`, guarded by a mutex because the SDK's
  `PeriodicReader` fires the observable-gauge callback from its own goroutine
  while the bridge/TUI call `RecordDaemonConnected` from their loops) avoids a
  ghost label-less series before the first reading.
- `buildResource` gains `resource.WithFromEnv()` so `OTEL_RESOURCE_ATTRIBUTES`
  (e.g. `host.name`, `deployment.environment`) flows into the resource for all
  emitters. (Today `resource.New` is called with only `WithAttributes`, so the
  env var is ignored — confirmed in `internal/otel/resource.go`.) **Ordering
  matters**: later detectors win on key conflict, so order the options so the
  explicit `service.name`/`service.version` win over any env-supplied value
  (put `WithFromEnv()` first, or keep `service.name` out of the env). Pin this
  in a test.

**Shutdown/flush (required, not polish).** `runCmuxBridge` currently neither
imports otel nor flushes anything; the daemon flushes via
`defer opts.Emitter.Shutdown(...)` (`internal/daemon/lifecycle.go:248`). The
bridge and TUI MUST `defer connEmitter.Shutdown(ctx)` on exit so the batch log
processor flushes buffered `LogEvent`s. (The `connected=0` metric is held by
the live process and re-exported every 15s, so it survives until the process
dies; on death the series goes stale → `noDataState: OK`. The buffered logs are
what a missing Shutdown loses.)

Wiring:

- **Bridge**: `runCmuxBridge` constructs the connection emitter (with
  `defer Shutdown`); the state machine in D2 calls
  `RecordDaemonConnected(true/false)`.
- **TUI**: `runTUIRemote` constructs the connection emitter (with
  `defer Shutdown`) and starts a small sample ticker that records
  `connected := !rp.IsOffline()` — which is sound, because every
  `scheduleBackoff()` in `remote_poller.go` is preceded by `r.client = nil`,
  so `IsOffline()` (`client == nil`) is reliably true throughout a
  disconnect/backoff window (verified at `remote_poller.go:77,88-90,179`). Do
  **not** key a freshness window off `cfg.RefreshInterval` (default **1s**,
  `config.go:78`) — with a ~10s sample ticker that yields constant
  false-disconnects. If a staleness guard is wanted (to catch a wedged daemon
  that keeps answering `GetState` with stale data), use a window comfortably
  larger than the sample interval (e.g. `≥ 3 ×` the ticker period), specified
  as a concrete constant. Transitions emit a `LogEvent`.

### D5. OTel config via the existing XDG config file (single source of truth)

OTel settings move into the existing `internal/config` TOML so the daemon,
bridge, and TUI all read the same file.

New config schema (`internal/config/config.go`):

```toml
[otel]
endpoint = "http://127.0.0.1:4317"

[otel.resource_attributes]
"deployment.environment" = "local"
"host.name" = "phillipg-mbp-02"
```

**Transport is gRPC; the endpoint scheme is what's load-bearing — there is no
`protocol` knob.** The emitters import the protocol-specific
`otlpmetricgrpc`/`otlploggrpc` packages (`internal/otel/emitter.go:14-15`),
which never read `OTEL_EXPORTER_OTLP_PROTOCOL` — transport is fixed at compile
time. What the gRPC exporter _does_ honour is the endpoint URL: an `http://`
scheme selects insecure gRPC (so `http://127.0.0.1:4317` works against the
local stack, whose default gRPC port is 4317). Therefore the config schema
omits a `protocol` field entirely; adding HTTP/protobuf support later would be
separate, explicitly-scoped work (import the `*http` exporter families and
branch). `endpoint` and `resource_attributes` are the only knobs.

- `Config` gains `OTel OTelConfig` where `OTelConfig{ Endpoint string;
ResourceAttrs map[string]string }`. `tomlConfig` gains a matching `*tomlOTel`,
  with `apply` and `defaults` updated. Defaults: all empty (OTel off) —
  preserving today's behaviour when no `[otel]` block is present.
- New `config.ApplyOTelEnv(o OTelConfig)`: sets `OTEL_EXPORTER_OTLP_ENDPOINT`
  and `OTEL_RESOURCE_ATTRIBUTES` **only if the env var is currently unset and
  the config value is non-empty**. (No
  `OTEL_EXPORTER_OTLP_PROTOCOL` — see above; setting it would be a silent
  no-op.) This lets the existing SDK-native `otel.New` / `NewConnectionEmitter`
  consume the values without bespoke exporter wiring, and keeps "explicit env
  wins" precedence for any edge case. The on/off gate in `otel.New` keys off
  `OTEL_EXPORTER_OTLP_ENDPOINT`, so a config with an endpoint turns OTel on.
- Each entrypoint calls `ApplyOTelEnv(cfg.OTel)` immediately before
  constructing its emitter:
  - daemon: `buildRunOptions` (has `cfg`) before `otel.New`.
  - TUI: `runTUIRemote` (has `cfg`) before `NewConnectionEmitter`.
  - bridge: `runCmuxBridge` adds `config.Load(config.DefaultPath())` (or the
    `config_helper` loader), then `ApplyOTelEnv`, then `NewConnectionEmitter`.

Because the daemon already loads `config.Load(config.DefaultPath())` and
launchd runs it as the user (HOME set, so `DefaultPath` resolves
`~/.config/pa-monitor/config.toml`), the daemon reads the same file as the
tools.

### D6. nix: config file install + plist env removal

- **`darwin/modules/pa-monitor/default.nix`**:
  - **Remove** `serviceConfig.EnvironmentVariables = emitterEnv;` from the
    daemon LaunchAgent. The config file is now the single source of OTel
    settings; a divergent plist env is exactly the inconsistency to avoid.
  - **Derive** the OTel config from the existing `emitterEnv` and write it into
    each daemon-enabled user's pa-monitor settings (darwin→HM cross-scope
    write; the module already reads `config.home-manager.users`):

    `mkEmitterEnv` returns the keys `OTEL_EXPORTER_OTLP_ENDPOINT`,
    `OTEL_EXPORTER_OTLP_PROTOCOL`, `OTEL_SERVICE_NAME`, and (only when
    `resourceAttrs` is passed) `OTEL_RESOURCE_ATTRIBUTES`. The
    `ENDPOINT` key is present iff `obs.enable`, so it is the correct gate.
    Drop the others: `protocol` has no config field (see D5), `OTEL_SERVICE_NAME`
    is set in Go via `otel.Options{ServiceName}`, and the darwin module passes
    **no** `resourceAttrs` today so `OTEL_RESOURCE_ATTRIBUTES` is never produced
    — don't write parsing for a key that isn't set.

    ```nix
    otelSettings =
      lib.optionalAttrs (emitterEnv ? OTEL_EXPORTER_OTLP_ENDPOINT) {
        otel.endpoint = emitterEnv.OTEL_EXPORTER_OTLP_ENDPOINT;
      };
    # for each user u with pa-monitor.daemon.enable:
    #   home-manager.users.<u>.phillipgreenii.programs.pa-monitor.settings = otelSettings;
    ```

    Sequencing/merge caveats (S4): (1) the HM `settings` option below must land
    **before** this darwin write, or eval fails on an undefined option; (2)
    merge with any user-supplied `settings` via the module system / `mkMerge`
    semantics — do **not** read `config.home-manager.users.<u>.…settings` and
    write it back (that is a read-then-write of the same attr and risks
    recursion); set the option as a contribution and let the module system
    merge. Gate on `daemon.enable` (the same predicate as the LaunchAgent),
    matching `daemonEnabledByAnyUser`.

- **`home/programs/pa-monitor/default.nix`**: add a generic settings
  passthrough option, typed by the TOML format so renderability is validated at
  eval time (the `tuicr`/`ccpool` modules use this pattern):

  ```nix
  let tomlFormat = pkgs.formats.toml { }; in
  # …options…
  phillipgreenii.programs.pa-monitor.settings = lib.mkOption {
    inherit (tomlFormat) type;       # keys must match config.toml field names
    default = { };
    description = "Rendered to ~/.config/pa-monitor/config.toml";
  };
  ```

  When `settings != {}`, render via
  `tomlFormat.generate "pa-monitor-config.toml" cfg.settings` and install as
  `xdg.configFile."pa-monitor/config.toml"`. Empty → no file written → today's
  default behaviour. This module is the single owner of that path. `(pkgs.formats.toml {}).generate`
  renders the nested `[otel.resource_attributes]` table and quotes dotted keys
  correctly (confirmed against the in-repo `tuicr`/`ccpool` usages).

- **`phillipg-nix-ziprecruiter`**: no new work required — it composes
  agent-support + the observability stack, so the derived `settings.otel`
  flows through automatically once `obs.enable`. (It may still override or
  extend `settings` if desired.)

### D7. Grafana alert

New `grafana/alerting/daemon-connection.yaml`, modelled on
`auth-failure.yaml`:

- Group `pa-monitor`, folder `Claude Agents`, `interval: 1m`.
- `for: 1m` (the "more than a minute" requirement).
- `noDataState: OK` — a closed pane / exited process makes the series stale →
  no alarm. We only alarm on a live process reporting `0`.
- Query chain:
  - `A` (Prometheus, instant): `min by (component) (pa_monitor_daemon_connected)`
  - `B` (reduce, last of `A`)
  - `C` (threshold): `B < 1` → fire.
- Unlike `auth-failure.yaml`, this rule must **not** append `or vector(0)` to
  the query — that would manufacture a 0 series when the metric is absent and
  fire spuriously. Here absence must fall through to `noDataState: OK`.
- Metric name: OTel `pa_monitor.daemon.connected` is exposed by Prometheus as
  `pa_monitor_daemon_connected` (dots→underscores; an observable gauge gets no
  `_total`/unit suffix), matching the naming of every existing gauge in
  `grafana/pa-monitor-overview.json`.
- Annotations name the component(s) so the alert says which side
  (cmux-bridge / tui) lost the daemon.

Register the file in `darwin/modules/pa-monitor/default.nix`'s
`phillipgreenii.observability.alertRuleFiles` list, next to `auth-failure.yaml`.

## Architecture / data flow

```
                ~/.config/pa-monitor/config.toml   (single source; [otel] section)
                          │  read by config.Load(DefaultPath())
        ┌─────────────────┼──────────────────────┐
        ▼                 ▼                        ▼
   pa-monitor daemon   pa-monitor cmux-bridge   pa-monitor tui
   (otel.New, full)    (NewConnectionEmitter)   (NewConnectionEmitter)
        │                 │  connected 1/0           │  connected 1/0
        │                 │  + LogEvent detail       │  + LogEvent detail
        ▼                 ▼                          ▼
                   OTLP endpoint (local otel-stack: Prometheus + Loki)
                                    │
                                    ▼
                  Grafana alert: min by(component)(pa_monitor_daemon_connected) < 1 for 1m
```

Bridge terminal (operator-facing) is now distinct from the OTel/log stream:

```
streamOnce ok  ──▶ state/session diff ──▶ terminal (timestamped, friendly)
streamOnce err ──▶ state machine ──▶ terminal: "Lost connection to daemon" (once)
               └─▶ log/OTel: bridge.stream_lost{err=...}   (every retry)
```

## Components (units, each independently testable)

1. **`linefmt` (bridge)** — pure function `format(ts, msg) string`. Trivial,
   table-tested. Decouples timestamp/format from the I/O.
2. **connection state machine (bridge)** — pure-ish: given a sequence of
   ok/err events, produces the terminal lines + gauge transitions. Testable by
   driving events and asserting emitted lines (mirrors existing `diffAndLog`
   table tests).
3. **`otel.connEmitter`** — minimal emitter; nil-safe; unit-tested for
   on/off and that `connected` reflects the last `RecordDaemonConnected`.
4. **`config` `[otel]`** — extend existing round-trip tests; add a
   `TestApplyOTelEnv` (sets when unset, leaves explicit env untouched).
5. **nix settings render** — `(pkgs.formats.toml).generate` round-trips the
   attrset; covered by `nix flake check` eval + a fixture assertion if cheap.
6. **Grafana yaml** — validated by the same provisioning path as
   `auth-failure.yaml`.

## Error handling

- Emitter construction failure (bad endpoint) must **not** crash the bridge or
  TUI — log to the local sink and continue with a nil emitter (best-effort,
  matching the existing `otel.New` contract).
- `ApplyOTelEnv` is a no-op when the config has no `[otel]` block → OTel stays
  off, the connection metric is absent, and the alert is `noData → OK`.
- The bridge's primary job (driving the sidebar) is unchanged and must remain
  resilient to a missing daemon, as today.

## Testing

- Go unit tests for components 1–4 above.
- Update `cmd/pa-monitor/cmux_bridge_test.go` (assertions at `:40,55,74,77`
  check `"Caffeinated Enabled"` / `"Auto Nudge Disabled"` etc.) for the new
  lowercase phrases/format, and add coverage for the lost/restored transitions
  (no duplicate "lost", no spurious "restored" at clean startup).
- `nix flake check` must pass (formatting, eval, package build, Go tests).
- Manual: with the local otel-stack up, kill the daemon, confirm the pane
  shows a single `Lost connection to daemon`, `otel-stack`/Loki shows the
  low-level detail, Prometheus shows `pa_monitor_daemon_connected{component=...}
= 0`, and the Grafana alert fires after ~1m; restart the daemon and confirm
  `Connection to daemon restored` + alert clears.

## Documentation deliverables

- **ADR 0016** (`docs/adr/0016-pa-monitor-config-sourced-otel-and-connection-alert.md`):
  records (a) OTel config sourced from the shared XDG config file for _all_
  pa-monitor processes (extends/partially supersedes ADR 0011's daemon-only
  emitter split), and (b) the bridge/TUI connection signal + alert. Update
  `docs/adr/index.md`.
- Update `packages/pa-monitor/README.md`: the new `[otel]` config section, the
  friendly bridge output, and the connection metric/alert.

## Rollout / migration

- No manual user steps. After deploy, the daemon stops reading OTel from its
  plist and reads the config file; agent-support writes the config file from
  `obs`, so the change is transparent on hosts with observability enabled. On
  hosts without observability, OTel was already off and stays off.
- **First-activation ordering**: if the daemon LaunchAgent is (re)bootstrapped
  before home-manager writes `~/.config/pa-monitor/config.toml`, the daemon
  comes up with OTel off (`config.Load` treats a missing file as
  defaults-with-OTel-off, `config.go:96-99`). With `keepAlive = true` it
  self-heals on the next restart; no host _regresses_ (OTel was plist-driven
  before, config-driven after — the only cost is a brief OTel-off window on the
  very first activation).

## Risks / edge cases

- **Stale 0-series on process death**: if the bridge dies _while
  disconnected_, Prometheus may hold its last `0` for the staleness window
  (~5m), so the alert could fire briefly after the pane is already gone.
  Acceptable; `noDataState: OK` bounds it and the alert auto-resolves once the
  series goes stale.
- **Two emitters per pane host**: a host running both a bridge and the TUI
  emits two `component` series — intended; the alert is `by (component)`.
- **Clock**: terminal timestamps use local wall-clock; if the machine clock
  jumps, lines reflect it. Acceptable for an operator pane.
- **Phrase coupling (resolved)**: the bridge defines its own
  `caffeinatePhrase`/`autoNudgePhrase` (`cmux_bridge.go:66-78`); the sidebar
  renderer does not share a constant, and `render/controls_test.go:22`'s
  `"Caffeinated Enabled"` strings are a **negative** (`wantNone`) assertion
  that the sidebar does _not_ render those phrases. Lowercasing the bridge
  verbs is therefore safe and does not touch the renderer — the only tests to
  update are in `cmux_bridge_test.go`.
