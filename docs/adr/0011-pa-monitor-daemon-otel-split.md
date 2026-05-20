# pa-monitor Daemon + OTel Split

**Status**: Accepted
**Date**: 2026-05-20
**Deciders**: phillipg

## Context

Today's `claude-agents-tui` is a single Go binary providing both an
interactive TUI and a headless `--wait-until-idle` mode. Session
enumeration, transcript parsing, 5h block aggregation, caffeinate
management, signal dispatch, and rendering all live inside that one
process. As a consequence, data exists only while the TUI is open or the
headless mode is briefly running at shutdown. There is no continuous
record of agent activity over time.

A separate workspace effort (`phillipgreenii-nix-support-apps` ADR-0037)
landed `phillipgreenii.observability` with `mkEmitterEnv` and
`dashboardProviders` extension points, plus a local OTel collector and
Grafana. Apps can now register dashboards and request OTLP endpoint env
vars from a single nix module.

Three forces motivate restructuring the package now:

1. **Continuous time-series**: we want metrics for window-limit hits,
   caffeinate rounds, post-window nudges, and basic agent usage. A
   process that only runs while the TUI is open cannot supply that.
2. **Multiple clients**: the user wants TUI, CLI controls (caffeinate
   toggle, manual nudge, status queries), and an in-cmux helper that
   updates the sidebar and proxies nudges. These should all observe and
   manipulate the same shared state.
3. **Cross-repo move**: the package belongs in this repo
   (`phillipgreenii-nix-agent-support`), not the consuming
   ZipRecruiter flake. ZR-specific labels (scope, role values) must
   route through an extension point, not live in this codebase.

## Decision

Restructure into a long-running daemon plus thin clients, all shipped as
one Go binary `pa-monitor` with subcommands. Wire OTel emission into
the daemon, gated by the workspace observability module.

Key choices recorded by this ADR:

1. **One binary, multiple subcommands** rather than three separate
   executables. Subcommands: `daemon`, `tui`, `status`, `caffeinate`,
   `nudge`, `info`, `agents-busy-check`, `wait-until-agents-finished`,
   `cmux-bridge`, `config show`.
2. **Daemon owns all state**. Clients are stateless except for
   per-process render caches.
3. **gRPC over Unix socket** for both client↔daemon RPC and OTLP export
   to the local collector. Heartbeat messages embedded in
   server-streaming RPCs detect hung daemons (HTTP/2 PING alone
   cannot).
4. **General label keys, decorator-supplied values.** All consumers use
   the same key set (`workspace.scope`, `workspace.project`,
   `workspace.repo`, `agent.role`, etc.). ZipRecruiter-specific values
   are emitted by a shell-out decorator binary shipped from that repo,
   not by code in this repo.
5. **Correlation labels instead of long-lived spans.** 5h blocks and
   weekly windows are identified by `block.id` (UTC hour of start) and
   `week.id` (ISO week), included on all related metrics and event
   attrs. No span spans hours of wall time.
6. **Traces only for discrete operations**, currently just the `nudge`
   operation. Poll-tick traces would duplicate metric coverage; block,
   week, and caffeinate-round spans would be long-lived
   anti-patterns.
7. **Repo identity via canonical git origin**, not basename. Different
   clones of the same remote share one `workspace.repo` value; the same
   clone's worktrees also share one value (`workspace.project`
   distinguishes them when distinct).
8. **Move code into `phillipgreenii-nix-agent-support`**. No
   ZR-specific behaviour lives here. Decorator shell-outs are the only
   extension mechanism, constrained to nix-store paths.

## Consequences

### Positive

- Continuous metrics regardless of TUI presence.
- Single source of truth for caffeinate state, session enumeration, and
  signal dispatch — eliminates cross-process inconsistency.
- ZipRecruiter labels stay out of this repo; the repo serves all
  workspaces.
- Generic Grafana dashboard works for every consumer; no per-consumer
  dashboard JSON to maintain.
- Disabled-state contract preserved: when
  `phillipgreenii.observability.enable = false`, the daemon still runs
  and serves clients, just emits no telemetry.

### Negative

- A long-running LaunchAgent enters the user's Background Activity
  list. Mitigated by `pa-monitor-daemon` wrapper naming.
- Client startup latency: each CLI invocation pays a Unix-socket
  connect + RPC. Acceptable for one-shots; mitigated by streaming for
  the TUI.
- Larger surface to test: lifecycle (pidfile, socket cleanup) and
  client reconnect behaviour both require explicit failure-path
  coverage.
- gRPC + protobuf codegen adds tooling weight to the build.

### Neutral

- Today's `claude-agents-tui` binary name is removed without a
  back-compat symlink. The package runs on one machine; coordinating
  the rename in lockstep is straightforward.
- `--wait-until-idle` flag is replaced by the
  `wait-until-agents-finished` subcommand. Same semantics, different
  invocation.

## Alternatives Considered

### Add OTel to TUI only

Rejected. Telemetry would have gaps whenever the TUI is closed, which
is most of the time. Window-limit-hit events would only land if the
TUI happened to be open at the moment of the hit — unreliable for
"how often" questions.

### Persist transitions to a JSONL file, tail with the OTel collector

Rejected. Shifts complexity into collector parser configuration and
loses native metric semantics (counters become parsed log fields).
Less ergonomic to query in Grafana.

### Three separate binaries (daemon, client multitool, cmux bridge)

Rejected in favour of one binary with subcommands. Saves one Go
module's worth of duplicated dependency wiring and one nix
derivation. Process-level separation is preserved at runtime via
distinct launchd labels and `writeShellScriptBin` wrappers for BTM
naming.

### Long-lived OTel spans for block and week windows

Rejected. Spans that last hours or days flush only on close, making
them invisible during the window of interest. Correlation IDs on
metrics and event attrs give the same grouping ability without the
anti-pattern.

### `zr.*` and `gascity.*` label namespaces

Rejected. Generic keys (`workspace.scope`, `workspace.project`,
`agent.role`) let one dashboard serve every consumer. Per-namespace
keys would require per-consumer dashboards and per-consumer query
templates.

## Related Decisions

- See also: phillipgreenii-nix-support-apps docs/adr/0037-observability-module-redesign.md
- See also: phillipg-nix-ziprecruiter docs/adr/0041-gascity-otel-decoupling.md
- Design spec: `docs/superpowers/specs/2026-05-20-pa-monitor-daemon-otel-design.md`
