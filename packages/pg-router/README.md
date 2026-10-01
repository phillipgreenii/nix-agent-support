# pg-router

`pg-router` routes typed events from configured sources to configured handler
roles over a durable queue. `run-until-idle` boots the core, discovers once,
dispatches a session per configured role (in config order) up to each role's
cap, and drains the queue to idle before tearing every `pg-router-*` tmux
session down and exiting; `run` boots the same core as a long-running daemon
instead. Bare `pg-router` (no subcommand) requires an explicit subcommand.

> **Behavior:** how pg-router should behave as an **orchestrator** — the drain, roles &
> queries from config, and the agent-runner / query-source contracts — lives in the
> [behavior docs](docs/behavior/README.md). The **workflows** built on pg-router
> (reviewing PRs, shepherding changes, working a backlog) are defined by the
> deployment, not here. For how the code realizes a review flow today, see the
> downstream reference [`docs/pr-review-flow.md`](../../docs/pr-review-flow.md).

## Subcommands

| Command                                 | Description                                                                                                                                                                                                                                                                          |
| --------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `run [--metrics-addr <host:port>]`      | boot the core and run indefinitely, producing + dispatching on a fixed poll interval, until SIGINT/SIGTERM requests shutdown. `--metrics-addr` opts into an OTel Prometheus `/metrics` endpoint (disabled by default) — see [Observability](#observability)                          |
| `run-until-idle`                        | boot the core, discover once, drain the queue to idle, then exit                                                                                                                                                                                                                     |
| `run-query [--json] query:<name>`       | smoke-test one named source's query once, read-only, and print the matches it would emit (text, or one JSON object with `--json`)                                                                                                                                                    |
| `run-role [--json] <role> <json>`       | dispatch one caller-supplied event through a role, then tear down (smoke test; `--json` reports the outcome as one JSON object). `<json>` is a full event blob, the same shape `push-inject <json>` takes — there is no bead-id shorthand any more                                   |
| `config --print-defaults`               | print the built-in default `config.toml` (a copy-paste start)                                                                                                                                                                                                                        |
| `config --show [--json]`                | print the resolved config path, role set, and worker dispatch scalars (permission-mode/allowed-tools/budget); text, or one JSON object with `--json`                                                                                                                                 |
| `push-inject <json>`                    | inject one operator-supplied event into the **running** core (text, or JSON with `--json`)                                                                                                                                                                                           |
| `status`                                | inspect the **running** core: resolved config, live deliveries, per-`type` queue depths, plus gates/mode/listeners/sources/unmatched bindings/recent activity (text, or JSON with `--json`)                                                                                          |
| `tui [--socket <path>] [--token <tok>]` | continuous-interactive view: polls `status`'s activity ring and offers `pause`/`resume` from the same screen — never a third affordance. No `--json` (it is a terminal UI). **Never fails on "no running core"**: it renders a no-core screen and keeps polling instead (`ADR 0036`) |
| `pause [--description T]`               | set the `SYSTEM_PAUSE` gate on the **running** core (`INV-LIFE-2`) — see [below](#pause--resume--gate--operator-gate-control)                                                                                                                                                        |
| `resume [--all]`                        | clear `SYSTEM_PAUSE` (or, with `--all`, every active gate) — see [below](#pause--resume--gate--operator-gate-control)                                                                                                                                                                |
| `gate set\|clear\|list`                 | set, clear or list **generic gates** by TYPE on the running core — see [below](#pause--resume--gate--operator-gate-control)                                                                                                                                                          |
| `version`                               | print the version and exit                                                                                                                                                                                                                                                           |
| `help`                                  | print help and exit                                                                                                                                                                                                                                                                  |

`<role>` is the role's configured `name`; `<name>` in `query:<name>` is a `[[query]]`'s configured
`name`.

### `push-inject` — operator event injection

```
pg-router push-inject [--json] [--socket <path>] [--token <tok>] '<event-json>'
```

`push-inject` is the **operator-facing front door to the push-ingest path**: it validates the
event against the `cli.push-inject` message schema, locates the running core, and performs the
**same core-side enqueue** as the `ingest-event` manager callback — durable via the queue,
delivered at-least-once and deduped (`INV-EVT-*`). It is **distinct from** `ingest-event` (a
manager→core callback) and from `run-role` (a smoke test that tears down). Primarily for
manual/test injection.

It locates the core via `--socket`/`--token`, else `PG_ROUTER_SOCKET`/`PG_ROUTER_TOKEN`, else the
discovery record under the log dir, and **forwards the event over that socket** — the core owns
the durable queue in another process, so nothing is enqueued locally. With **no core running it
fails** with a "no running core" error and **exit 1**; it never starts one
([ADR 0036](../../docs/adr/0036-pg-router-cli-never-auto-starts-a-core.md)).

Exit codes are `0` accepted, `2` a **usage** error, and `1` for everything else — the same
convention every other subcommand follows, because the common contract's pre-accept **busy** sits at
`9` and no longer occupies `2`
([ADR 0042](../../docs/adr/0042-coarse-exit-code-convention-busy-is-not-2.md)). A malformed or
non-schema-valid **event** is not a usage error: it fails on the same path as an unreachable core, so
it exits `1`.

The success report says the core **accepted** the event, never "enqueued": a still-retained
duplicate id is also accepted (`INV-EVT-3`) and the reply has no field that separates a fresh
append from an absorbed re-emit. The auth **token is never printed**, in either output mode.

### `status` — inspect a running core

```
pg-router status [--json] [--socket <path>] [--token <tok>]
```

`status` is the operator-facing INTF-CLI inspection verb: resolved configuration,
live deliveries, and per-`type` queue depths — the three inspection MUSTs
interfaces.md's "Inspecting a running core" declares — plus the current gate
state, run mode, registered listeners/sources, unmatched bindings, and recent
dispatch-outcome activity (`internal/activity.Ring`, Task 3.4). It locates the
core the same way `push-inject` does (`--socket`/`--token`, else
`PG_ROUTER_SOCKET`/`PG_ROUTER_TOKEN`, else discovery under the log dir) and **never
starts one** ([ADR 0036](../../docs/adr/0036-pg-router-cli-never-auto-starts-a-core.md)).

The human-output form orders its sections for incident scanning — header
(core/socket/config/gates/mode), then `QUEUES`, `DELIVERIES (live)`,
`ACTIVITY (last 10)`, `LISTENERS`, `SOURCES`, `UNMATCHED BINDINGS` — and never
omits a section silently: an empty one renders an explicit `(none)` marker
instead. `--json` emits the `cli.status-reply` wire schema verbatim, which now
also carries `activityDropped`: true iff a `since`-cursor request named a
cursor older than what the ring still retains, i.e. some activity entries in
that gap were already evicted (`internal/activity.Ring.Read`). This CLI's own
`status` call never sends `since` (see `runStatus`'s doc), so `activityDropped`
is always `false` through this subcommand today — the field exists for a
future since-cursor caller (Task 4.0's TUI).

Exit codes match every other operator subcommand: `0` ok, `2` usage, `1`
everything else (`9` is reserved for the pre-accept busy decline, which this
read-only verb never returns).

### `pause` / `resume` / `gate` — operator gate control

```
pg-router pause [--description TEXT] [--owner WHO]
pg-router resume [--all] [--by WHO]
pg-router gate set TYPE [--description TEXT] [--owner WHO] [--ttl DURATION]
pg-router gate clear (TYPE | --all) [--by WHO]
pg-router gate list [--json]
```

A **gate** (`INV-LIFE-2`, the Gate Registry) is what prevents pg-router from routing. It has a
**TYPE** (an ALL-CAPS string such as `SYSTEM_PAUSE` or `LOW_DISK_USAGE` — arbitrary), an optional
description, an optional **owner** (the setter's identity — debug only, no behavioral effect,
overwritten on re-set) and an optional **TTL lease** that the owner renews by setting the gate again.
There is one active gate per TYPE, the last writer wins, and **any caller may clear any gate**.
`pause`/`resume` are sugar for `gate set`/`gate clear` of `SYSTEM_PAUSE` (owner: the operator);
a bare `resume` clears **only** `SYSTEM_PAUSE`, so a gate another system owns is never cleared by
accident (`resume --all` clears everything).

Gates are records in the event log (`<LogDir>/queue.jsonl`), so they survive a restart and honor
their TTL across one. A gate acts on **participants, never on events**: while it is active, each
emitter that blocks on it is not polled, each listener that blocks on it is not dispatched to (and
its pull is rejected naming the gate), pushed events are still accepted, acks and confirmations still
processed, and queued events stay queued — an unblocked listener still receives them. A participant
blocks on every gate TYPE by default and declares `non_blocking_gates = [...]` (on a `[[role]]` or
`[[query]]`, or `nonBlockingGates` at `register`) for the TYPEs it does not. The built-in
`type = "timer"` query (the timer emitter) is never blocked by any gate. A listener that is still
blocked when an event's final attempt falls due loses that event, so long gates drop events (counted
in `pg_router_gate_drops`). External systems use the `gate-set`/`gate-clear` socket verbs.

Unlike the file-backed gates they replaced, these commands are **socket clients**: they need a
running core and fail with `no running core` (exit `1`) otherwise — they never start one
([ADR 0036](../../docs/adr/0036-pg-router-cli-never-auto-starts-a-core.md)). See `MIGRATION.md`'s
"Gates: file-backed gates replaced by the Gate Registry" for everything that was removed
(`operator-paused`/`cicd-down`/`disk-space-low`, the `PG_ROUTER_*` gate paths, the disable-override
files).

### Manager → core callback subcommands

The core also carries the **manager→core callback** subcommands. These are **not** operator
commands: the core hands a registered participant one command string with `--socket` and `--token`
already baked in, and the participant appends its arguments and runs it (see the behavior docs'
`INTF-CLI`).

| Command        | Description                                                                                                                                                                                                                                                                                                                            |
| -------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ingest-event` | deliver one or more events to the **running** core: request JSON on stdin, reply JSON on stdout, coarse exit code (0 ok / 1 error / 2 usage / 9 busy)                                                                                                                                                                                  |
| `self-status`  | push the caller's own status (healthy/degraded/unavailable) to the **running** core, naming the `participantId` it registered under: request JSON on stdin, reply JSON on stdout, coarse exit code (0 ok / 1 error / 2 usage / 9 busy). Every registered participant kind gets this callback, unlike `ingest-event` (a source's alone) |

`ingest-event` locates the core via `--socket`/`--token`, else `PG_ROUTER_SOCKET`/`PG_ROUTER_TOKEN`,
else the discovery record under the log dir. With **no core running it fails** with a
"no running core" error and exit 1 — it never starts one
([ADR 0036](../../docs/adr/0036-pg-router-cli-never-auto-starts-a-core.md)).

## Roles, prompts & queries (`config.toml`)

Roles, their prompts, and their discovery queries are configured in a repo-local
`<RepoRoot>/.pg-router/config.toml` (override the path with `PG_ROUTER_CONFIG`). When
no config file is present, pg-router uses the **built-in feedback, worker, and
review roles**. Run `pg-router config --print-defaults` to see the full
schema and the canonical defaults, then copy it and edit.

Roles and queries are typed tagged unions discriminated by a `type` field:

- **role `type`**: `ccpool` (dispatch a Claude session) or `command` (run an
  executable; completion = exit code).
- **query `type`**: `command` (run an executable that emits items as
  JSON/JSONL — an opaque token pg-router just invokes and never interprets, so
  this is how you wire pg-router to `bd`, `gh`, Jira, or anything else) or
  `event` (an in-process correlated-event source for the aggregator/saga
  path). `beads-ready` / `beads-list` / `github-issues` / `jira-issues` were
  typed query sources here through pg2-n75tk; each one typed "how another
  tool is configured" into Core, which the config surface is not supposed to
  know, and `jira-issues` specifically was structurally unsatisfiable (its
  backing command exists only in a downstream flake this one cannot depend
  on). See `MIGRATION.md` for converting an old config using one of the four
  removed types to an equivalent `command` block, worked through for each.
  The built-in feedback/worker/review defaults (`pg-router config
--print-defaults`) are themselves the worked beads-ready -> command example:
  each is now printed as a `command` block shelling to `bd ready | jq ...`.
  `MIGRATION.md` also carries a worked example (not a migration) wiring a
  `command` source to `pg-connector`'s `ci` capability as a per-connector CI
  health probe (bead `pg2-h410q`) — it surfaces through the same Sources pane
  `failing ×N`/`stale <duration>` rendering every other source uses, no core
  or TUI change required.

A `ccpool` role's behavior is set by code-owned enums: `completion`
(`close-only` | `close-or-handback`), `on_failure` (`unclaim` | `add-human`),
`on_dispatch_fail` (`unclaim` | `leave`). When `authorship_guard = true`, pg-router
prepends a **non-editable** safety preamble (assert author is me, branch starts
with `phillipg.`, never force-push) ahead of the role's task prompt, so
externalizing the prompt never weakens the guardrails. A role's prompt is inline
(`prompt`) or an external file (`prompt_file`, resolved relative to the config
dir) — exactly one.

> **Monorepo config hygiene:** add `.pg-router/` to your monorepo's
> `.git/info/exclude` so a repo-local pg-router config (and its prompts) is never
> committed there. `pg-router run-until-idle` warns at pre-flight if
> `.pg-router/config.toml` is git-tracked.

## Configuration (pool-wide env)

Pool-wide settings come from `PG_ROUTER_*` environment variables; roles are NOT
configured via env (use `config.toml`). See `internal/config` for the full set.

- `PG_ROUTER_REPO_ROOT` — monorepo root the drain operates in (default: cwd)
- `PG_ROUTER_BEADS_PREFIX` — expected bead prefix, asserted at precheck (default `zr`, a
  deployment-specific default — set it to your prefix)
- `PG_ROUTER_CONFIG` — explicit `config.toml` path (default `<RepoRoot>/.pg-router/config.toml`)
- `PG_ROUTER_BUDGET_TOKENS` — per-worker token budget; 0 = unlimited (default 0)
- `PG_ROUTER_BUDGET_COST` — per-worker cost budget in cents; 0 = unlimited (default 0)
- `PG_ROUTER_BUDGET_TIME` — per-worker wall-clock budget in seconds (default 1500)
- `PG_ROUTER_MODEL` — claude model override (default: ccpool's default)
- `PG_ROUTER_EFFORT` — claude `--effort` value (default `max`)
- `PG_ROUTER_PERMISSION_MODE` — claude `--permission-mode` for workers (default `dontAsk`: deny-by-default; `bypassPermissions` is the opt-in escape)
- `PG_ROUTER_ALLOWED_TOOLS` — claude `--allowed-tools` allowlist for workers (default: a conservative set; `git push` excluded. Empty clears the flag)
- `PG_ROUTER_AUTONOMOUS` — block AskUserQuestion so human-less workers never stall on the picker (default `true`)
- `PG_ROUTER_HANDLER_COMMAND` — the argv prefix invoked, over the wire (DEC-WIRE-1), for every
  enabled role's registered handler participant (e.g. `pg-router-ccpool-handler`). No default —
  GOAL-MIN-1's Floor keeps this binary's own contract surface from naming a concrete tool; an
  unconfigured deployment gets a clear per-dispatch error instead of a hardcoded participant name.
- `PG_ROUTER_HANDLER_COMMAND_DIR` — a directory of per-role JSON files (`<role.Name>.json`) that
  lets differently-configured roles sharing one `PG_ROUTER_HANDLER_COMMAND` binary (e.g.
  `feedback`/`worker`/`review`, each with its own ccpool actor/prompt) dispatch through their own
  participant config instead of all sharing the same one. When set, the resolved argv threads
  `--role-config <dir>/<role.Name>.json` onto the handler command; unset (the default), every
  enabled role resolves to the plain `PG_ROUTER_HANDLER_COMMAND` argv, unchanged from before this
  variable existed. A deployment with more than one role ENABLED and only
  `PG_ROUTER_HANDLER_COMMAND` set logs a boot-time WARN naming the ambiguity.
- `PG_ROUTER_LOG_DIR` — override the event-log directory (default: the standard path below)
- `PG_ROUTER_ACTIVITY_RING` — dispatch-outcome activity ring buffer capacity (`internal/activity.Ring`, Task 3.4); default 512
- `PG_ROUTER_LOG_DIR` — override the event-log/state directory: `queue.jsonl` (events and gates), `events.jsonl`, the discovery record (default: the standard path below)
- `PG_ROUTER_ACTIVITY_RING` — dispatch-outcome activity ring buffer capacity (`internal/activity.Ring`, Task 3.4); default 512
- `PG_ROUTER_TUI_INTERVAL` — `tui`'s poll interval, floor-clamped to `250ms` (default `1s`). Precedence:
  a CLI flag (none exists yet) wins over this env var, which wins over the built-in default; a value
  that fails to parse as a duration is a usage error naming the bad value.
- `PG_ROUTER_TEST_MODE` — set to `1` by `run-role`/`run-query` for the duration of that one smoke
  test, so a participant it dispatches (or a command-backed source it shells out to) knows a test
  is in flight; advisory only. Not meant to be set by an operator directly.

Precedence for every scalar above that a `[pool]` key can also set: `[pool]` wins over `PG_ROUTER_*` env, which wins over the built-in default — matching
`internal/config`'s package doc and `config --print-defaults`'s header. The XDG-global config
(`$XDG_CONFIG_HOME/pg-router/config.toml`, else `~/.config/pg-router/config.toml`) contributes
`[pool].budget` only, beneath the repo-local file and above env.

**Removed** (now per-role in `config.toml`, not env): `PG_ROUTER_MAX_WORKER`,
`PG_ROUTER_MAX_FEEDBACK`, `PG_ROUTER_FEEDBACK_ENABLED`, `PG_ROUTER_WORKER_ENABLED`,
`PG_ROUTER_SKILL_MD`, `PG_ROUTER_WORKER_SKILL_MD`. Set `role.cap` / `role.enabled` /
the role's prompt in `config.toml` instead; pg-router warns if any are still set.

## Observability

### Metrics

`internal/metrics` builds the full OTel metric catalog (queue depth, failure rate,
unconsumed-expired, unknown-type-rejected, throughput, backlog, liveness, dispatch
latency, source failures, deduped) against an injected `metric.MeterProvider` — the
core stays unaware of any concrete monitoring backend. `run`'s own `--metrics-addr
<host:port>` flag (or `PG_ROUTER_METRICS_ADDR`; the flag wins) opts a **daemon-mode**
run into a real backend: `cmd/pg-router/metrics_http.go`'s `startMetricsServer` builds
an OTel `go.opentelemetry.io/otel/exporters/prometheus` bridge on its own
`prometheus.Registry` and serves it at `/metrics` on that address. Omitted (the
default), no listener opens and the package's own read-back `ManualReader` default
stands unchanged. `run-until-idle` defines no such flag — a drain-and-exit pass has
nothing long-lived to scrape.

### Logs

`internal/telemetry` (`Init`/`NewSlogHandler`/`Fanout`) is a deliberate small
duplication of `packages/pg-pr/internal/telemetry`'s own OTLP-log shape — an
`internal/` package cannot be imported across the pg-pr/pg-router module boundary, so
the proven pattern is copied rather than shared, LOG half only (pg-router emits no
spans). `main()` calls `telemetry.Init(ctx, "pg-router", version)` unconditionally
for every subcommand (a no-op when `OTEL_EXPORTER_OTLP_ENDPOINT` is unset — the common
case for a one-shot operator invocation); `run`/`run-until-idle`'s shared `prepareRun`
then fans the default `slog` logger out to both stderr (unchanged) and the OTLP
bridge, so every `WARN`/`ERROR` operational line reaches Loki as
`{service_name="pg-router"}` once the daemon's OTLP endpoint is configured.

The budget watchdog ALSO writes a structured per-run event stream as JSONL (one JSON
object per line) conforming to the phillipgreenii JSONL logging standard: every
record carries `time` (RFC3339Nano, UTC), `level` (lowercase
`debug`/`info`/`warn`/`error`), and `msg`, plus an event-type `kind` field
(`reminder` → `info`, `cancel` → `warn`, `hard_stop` → `error`). This is
pg-router's own dispatch-outcome ledger, not the Claude transcript, and it is a
DIFFERENT signal from the OTLP operational logs above — see the relabel note below.

The log is written to the standard path
`${XDG_STATE_HOME}/pg-router/events.jsonl` (no `/log` subdirectory), which matches
the default `logSources` glob `${env:XDG_STATE_HOME}/pg-router/*.jsonl`.

Collection into Loki is pull-based: the darwin module
`darwin/modules/pg-router/default.nix` registers
`phillipgreenii.observability.logSources.pg-router-events` (`serviceName =
"pg-router-events"`; guarded on `obs.enable`, so it is a no-op on machines without the
observability stack) — relabeled from the plain `pg-router` name so it no longer
collides with the OTLP log push's own `service_name="pg-router"` (above). The glob and
`events.jsonl` file are unchanged; only the Loki label moved. That same darwin module
also calls `obs.mkEmitterEnv { serviceName = "pg-router"; protocol = "grpc"; }`
(mirroring `pa-monitor`'s own darwin module) and merges the result into the daemon
LaunchAgent's `EnvironmentVariables`, wiring `OTEL_EXPORTER_OTLP_ENDPOINT`/
`OTEL_SERVICE_NAME` for the logs half above.

## Deployment (`home/programs/pg-router`, `darwin/modules/pg-router`)

`phillipgreenii.programs.pg-router` (the home-manager module) exposes two **mutually exclusive**
turnkey deployment modes on top of the `package`/`enable` options — enabling both is a module
assertion failure, since they are independent pg-router cores that would race on the same
`PG_ROUTER_LOG_DIR` (`events.jsonl`, the discovery record, the push-ingest socket):

| Submodule       | systemd unit(s)                           | Runs                                                                        | Shape                                                                                                |
| --------------- | ----------------------------------------- | --------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| `periodicDrain` | `pg-router-drain` service + timer         | `pg-router run-until-idle` on a fixed `interval` (default `5m`), then exits | `enable`, `interval`, `repoRoot`, `beadsPrefix`, `configText`, `handlerCommand`, `handlerCommandDir` |
| `daemon`        | `pg-router-daemon` service (long-running) | `pg-router run`, until SIGINT/SIGTERM                                       | `enable`, `repoRoot`, `beadsPrefix`, `configText`, `handlerCommand`, `handlerCommandDir`             |

Both submodules render `configText` into the Nix store and point `PG_ROUTER_CONFIG` at it — fully
declarative, no machine-local `.pg-router/config.toml` bootstrap step. (The `daemon.gates.*` options
that used to set the gate-file paths were removed with the file-backed gates — gates are runtime
records now; see `MIGRATION.md`.)

`handlerCommand`/`handlerCommandDir` (plain strings, both `null` by default) set
`PG_ROUTER_HANDLER_COMMAND`/`PG_ROUTER_HANDLER_COMMAND_DIR` for that unit — see this file's own
env-var table above for their runtime contract. `handlerCommandDir` is typically pointed at
`phillipgreenii.programs.pg-router-ccpool-handler`'s own `handlerCommandDir` output (that sibling
home-manager module's new `roles` option — keyed by role name, shaped like
`cmd/pg-router-ccpool-handler/roleconfig.go`'s `roleFile` — renders one JSON file per role and
joins them into that directory); interpolate it to a string
(`"${config.phillipgreenii.programs.pg-router-ccpool-handler.handlerCommandDir}"`), since this
option takes a plain string, not a package. See `MIGRATION.md`'s "Multi-role dispatch
differentiation" section for the full worked contract.

`systemd.user.services`/`systemd.user.timers` are a **darwin no-op** (darwin has no systemd), so
`darwin/modules/pg-router/default.nix` mirrors any home-manager user's `daemon.enable` into a
LaunchAgent via `phillipgreenii.system.launchdServices.userAgents.pg-router-daemon` (the same
helper/pattern as `pa-monitor`'s daemon LaunchAgent — see `phillipgreenii-nix-personal` ADR 0049,
amended by ADR 0051). `periodicDrain` has no darwin-side LaunchAgent
equivalent today — a darwin deployment wanting the timer-driven form needs its own launchd timer
wiring, or should use `daemon` instead.
