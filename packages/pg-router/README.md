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

| Command                                 | Description                                                                                                                                                                                                                                                                                                                                                                                         |
| --------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `run [--metrics-addr <host:port>]`      | boot the core and run indefinitely, producing + dispatching on a fixed poll interval, until SIGINT/SIGTERM requests shutdown (which stops new offers, lets in-flight dispatches finish for up to 30s, then cancels any still running before the `preShutdown` sweep). `--metrics-addr` opts into an OTel Prometheus `/metrics` endpoint (disabled by default) — see [Observability](#observability) |
| `run-until-idle`                        | boot the core, discover once, drain the queue to idle, then exit                                                                                                                                                                                                                                                                                                                                    |
| `run-query [--json] query:<name>`       | smoke-test one named source's query once, read-only, and print the matches it would emit (text, or one JSON object with `--json`)                                                                                                                                                                                                                                                                   |
| `run-role [--json] <role> <json>`       | dispatch one caller-supplied event through a role, then tear down (smoke test; `--json` reports the outcome as one JSON object). `<json>` is a full event blob, the same shape `push-inject <json>` takes — there is no bead-id shorthand any more                                                                                                                                                  |
| `config --print-defaults`               | print the built-in default `config.toml` (a copy-paste start)                                                                                                                                                                                                                                                                                                                                       |
| `config --show [--json]`                | print the resolved config path, role set, and worker dispatch scalars (permission-mode/allowed-tools/budget); text, or one JSON object with `--json`                                                                                                                                                                                                                                                |
| `push-inject <json>`                    | inject one operator-supplied event into the **running** core (text, or JSON with `--json`)                                                                                                                                                                                                                                                                                                          |
| `status`                                | inspect the **running** core: resolved config, live deliveries, per-`type` queue depths, plus gates/mode/listeners/sources/unmatched bindings/recent activity (text, or JSON with `--json`)                                                                                                                                                                                                         |
| `tui [--socket <path>] [--token <tok>]` | continuous-interactive view: polls `status`'s activity ring and offers `pause`/`resume` from the same screen — never a third affordance. No `--json` (it is a terminal UI). **Never fails on "no running core"**: it renders a no-core screen and keeps polling instead (`ADR 0036`)                                                                                                                |
| `pause [--description T]`               | set the `SYSTEM_PAUSE` gate on the **running** core (`INV-LIFE-2`) — see [below](#pause--resume--gate--operator-gate-control)                                                                                                                                                                                                                                                                       |
| `resume [--all]`                        | clear `SYSTEM_PAUSE` (or, with `--all`, every active gate) — see [below](#pause--resume--gate--operator-gate-control)                                                                                                                                                                                                                                                                               |
| `gate set\|clear\|list`                 | set, clear or list **generic gates** by TYPE on the running core — see [below](#pause--resume--gate--operator-gate-control)                                                                                                                                                                                                                                                                         |
| `version`                               | print the version and exit                                                                                                                                                                                                                                                                                                                                                                          |
| `help`                                  | print help and exit                                                                                                                                                                                                                                                                                                                                                                                 |

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
(core/socket/config/`queue log` size/gates/mode), then `QUEUES`, `DELIVERIES (live)`,
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

Gates are records in the event log (`<LogDir>/queue.jsonl`, see "Queue log compaction" below), so they survive a restart and honor
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
  executable; completion = exit code). A non-zero exit fails the
  dispatch; the error (and so the `dispatch result` WARN line) ends with
  `: stderr tail: <text>` — the handler's last ~2 KiB of stderr, redacted,
  collapsed to one line (lines joined by `|`), and prefixed `...` when cut.
  A successful run's stderr is never logged.
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

### Re-running a transiently failed dispatch

A failed dispatch is normally accepted and never re-run (`INV-FAIL-1`: post-accept failures are the
handler's). A `[[role]]` MAY opt in with `max_dispatch_retries = N` (0 = off, the default; hard cap 3) for a handler that has no retry of its own, such as a command wrapper whose child was killed
under host overload. Only **transient** failures are re-run (`killed`, `deadline`, `unavailable`;
never a deterministic failure or a failure during shutdown), at most N times per event, at the
role's retry cadence (`[role.retry]` / `[pool].retry`), and never past the event's `expiresAt` — so
the event MUST carry a future `expiresAt` or its single attempt is also its last. Each re-run
increments `pg_router_dispatch_retries{role,class}`; the failed attempts still count as
`handler-error` and the hand-back as `declined` with reason `dispatch-retry` (`DEC-RETRY-2`).
The handler MUST be idempotent per event (delivery is already at-least-once).

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
- `PG_ROUTER_COMPACT_THRESHOLD_BYTES` — `queue.jsonl` size above which the write-ahead log is compacted
  to live state in the background; default `8388608` (8 MiB); `0` disables runtime compaction (the
  startup compaction still runs). `[pool].compact_threshold_bytes` in `config.toml` overrides it. See
  "Queue log compaction" below.
- `PG_ROUTER_MAX_LOG_BYTES` — the HARD size limit of `queue.jsonl`: at or above it new events are
  rejected with a `log_full` reason; default `67108864` (64 MiB). Accepts a plain byte count or a
  unit (`64MiB`, `200MB`, `1 GiB`); an unparseable value, zero or a negative is an error (never
  silently the default). `[pool].max_log_bytes` in `config.toml` overrides it and takes the same
  grammar: an integer or a string (`max_log_bytes = "64MiB"`); so does `compact_threshold_bytes`. The derived soft
  threshold is 90% of it, and `compact_threshold_bytes < soft < max_log_bytes` is enforced at load.
  See "Queue log size limit" below.
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

### Queue log compaction

`<LogDir>/queue.jsonl` is the append-only write-ahead log behind both the event queue and the
Gate Registry; an eviction is itself an appended record, so without compaction it grows for ever
and every start re-reads all of it. The queue therefore compacts it down to **live state**:
the retained events (FIFO order, resolved instants, accepts), the active gate projection, and the
set of event types ever enqueued. `Replay(compact(L))` rebuilds exactly the state `Replay(L)` does
(property-tested); only history a replay already discards is dropped. Delivery semantics (at-least-once,
INV-EVT-2) and gate persistence are unchanged.

- **When.** Once at startup, before the queue replays the log or accepts an event; and at runtime,
  from the `Expire` sweep, once the file exceeds `compact_threshold_bytes`
  (`[pool].compact_threshold_bytes` / `PG_ROUTER_COMPACT_THRESHOLD_BYTES`, default 8 MiB, `0` = no runtime
  compaction). After a compaction the trigger rises to twice the compacted size when that is larger, so
  a live set bigger than the threshold cannot make the queue compact on every sweep.
- **How it coexists with appends.** The runtime compaction does not hold the queue lock. It notes the
  log length `n`, folds the first `n` bytes into a temp file (`queue.jsonl.compact.tmp`) while appends keep
  landing after `n`, then — holding only the store's own mutex, briefly — copies the bytes appended in
  the meantime onto the temp file, fsyncs it, renames it over the log and fsyncs the directory. Nothing
  appended is lost or reordered.
- **One opener at a time.** `pg-router` takes an exclusive, non-blocking `flock` on
  `<LogDir>/queue.jsonl.lock` before it touches the log, and holds it until it exits (the kernel drops it
  if the process dies, so a crash never leaves a stale lock). A second opener — a double start, a
  `run-until-idle` beside the daemon — fails fast with a message naming the lock instead of compacting and
  renaming the log under a live writer (which would leave that writer appending to the unlinked file and
  silently losing every later record). The lock is a sibling file because compaction renames a new file over
  `queue.jsonl`; the empty `queue.jsonl.lock` is left in place.
- **Crash safety.** Until the rename the log path holds the complete old log; from it on, the complete
  new one. A leftover temp file is removed on the next start. A torn trailing line is tolerated as before
  and startup compaction drops it.
- **`pg-router log compact [--dry-run] [--json]`** (bead `pg2-maxn1`) compacts the log now instead of
  waiting for the startup or threshold trigger, and `--dry-run` reports what it WOULD do.
  - _Where it runs._ With a core running (found via `--socket`/`--token`, else `PG_ROUTER_SOCKET`/
    `PG_ROUTER_TOKEN`, else discovery under the log dir) the request goes over its socket and the
    compaction runs inside the daemon, which holds the log's lock; the reply says `"via": "daemon"`. With
    none running it compacts the file itself (`"via": "offline"`), taking the lock, and is **refused with
    exit 1** if another process holds it. A core named by `--socket` never falls back to offline.
  - _`--dry-run` changes nothing_ — no rename, no temp file, no lock kept or created, no counter. It prints
    bytes and records before/after, events kept vs dropped (evicted), gates kept, the percent of
    `max_log_bytes` afterwards, a warning for a torn tail, and whether a real run would be refused (the
    log is locked by another process) or would do nothing.
  - _A real run on a log that already holds live state only_ rewrites nothing and says "nothing to do"
    (exit 0); it does not count as a compaction.
  - _Output and exit codes._ A human summary, or with `--json` one object (the `cli.log-compact-reply`
    schema, identical online and offline apart from `via`). Exit `0` ok (including "nothing to do"), `1`
    refusal or failure (log locked, core unreachable or refusing, compaction failed), `2` usage.
  - It reclaims **dead history only** — a queued event is never discarded.
  - _In the TUI_ `c` opens a "Compact queue log" modal and runs the dry run at once, showing before/after,
    events kept vs dropped and the last compaction; nothing has changed yet. `y` there is the
    confirmation that runs the real compaction (it does nothing before the dry run is shown, or when a
    compaction would not shrink the log); `esc` cancels. The header's `log:` summary does not carry the
    last compaction (its line is already full); the modal and `pg-router status` do.
- **Observability.** The `pg_router_queue_log_bytes` gauge, one `eventqueue: queue log compacted` log
  line per compaction (trigger, bytes and records before/after, duration), a `queue log:` line in
  `pg-router status` followed by a `last compaction:` line (`queueLog` and `queueLog.lastCompaction` in
  `--json`: `at`, `trigger` = `startup`/`threshold`/`limit`/`manual`, bytes and records before/after,
  `durationMs`) and `log: <size>` in the TUI header. The size limit built on top of it is described in
  "Queue log size limit" below.

### Queue log size limit

`queue.jsonl` is also bounded (bead `pg2-5d3ui`). Compaction keeps it to live state (a few KB on the
real workload), so the limit is a backstop — against a backlog that is genuinely large, or a disk that
cannot be written — not something a healthy daemon approaches. One user-visible knob,
`max_log_bytes` (default 64 MiB, see `PG_ROUTER_MAX_LOG_BYTES` above), and two thresholds derived from
it:

| Log size                                    | State             | What happens                                                                                                                      |
| ------------------------------------------- | ----------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| up to 90% (the **soft** threshold)          | `ok`              | Nothing.                                                                                                                          |
| above 90%, still after a compaction attempt | `emitters_halted` | **Polled command-source emitters are not polled.** Everything else keeps running (below).                                         |
| at `max_log_bytes` (the **hard** limit)     | `log_full`        | The above, **and** every new event is rejected with a `log_full:` reason — timer emitters and pushed events included.             |
| the log cannot be written (disk full, I/O)  | `log_unwritable`  | The same as `log_full`, with a `log_unwritable:` reason; the daemon re-probes every tick and resumes by itself once it can write. |

- **The soft step is not a gate.** It is an in-memory `emittersHalted` flag on the queue, consulted
  by the producer next to the gate check and re-derived every tick from the log size. It adds nothing
  to the Gate Registry, does not appear in `pg-router gate list` / `pg_router_gates_active`, and is
  **not** cleared by `gate clear` or `resume --all`. **It does NOT stop timer emitters or pushed
  events** — only the hard limit does. Listener dispatch and the drain (including `run-until-idle`)
  keep running while halted, so space can be reclaimed and no listener loses an event.
- **The hard limit applies to enqueue admission only.** Accept, evict and gate records still append
  above it. A still-retained duplicate id (a re-emit) is still accepted: it writes nothing.
- **A refusal is non-fatal.** A pull or timer source whose event is refused is recorded as a source
  error with a per-source reject count and the tick carries on (it still runs the limit controller,
  `Kick`, `Expire` and publishes the tick); `run-until-idle` drains before exiting `1`. A push caller
  sees the reason in `rejected[].reason` and exit `1`; `push-inject` prints it. The fixed prefix is
  the contract: `log_full: pg-router event log is at N of M bytes; the event was NOT queued; safe to
retry later ...` and `log_unwritable: ...`. Retrying is safe because delivery is idempotent.
- **Order at startup:** compaction first, then the limits are evaluated.
- **Seeing it.** `pg-router status` prints `queue log: bytes=... limit=... used=P% state=...` and,
  when not `ok`, a notice stating what is HALTED, what STILL RUNS, why, and the remedies
  (`queueLog` in `--json`: `limitBytes`, `softBytes`, `percent`, `state`, `emittersHalted`,
  `rejected.{logFull,logUnwritable}`, `detail`). The `tui` header shows `log: 58.0 MiB / 64.0 MiB
(91%)` colored by band — plain below 70%, yellow from 70%, red at 90% or whenever the state is not
  `ok` — and, when not `ok`, a notice zone with the same wording (also in the Problems modal, `!`).
  Metrics: `pg_router_queue_log_limit_bytes`, `pg_router_queue_log_percent` (0-100),
  `pg_router_emitters_halted` (0/1), `pg_router_log_rejecting{reason}` (0/1) and the counter
  `pg_router_enqueue_rejected{type,reason}` (push and pull together; the pull side is also broken out
  per source in the produce report). The Grafana rule `pg-router-log-limit` fires after 10 minutes of
  either `emitters_halted` or a rejection.
- **Remedies** (there is no CLI to purge queued events): wait for queued events to expire (the log is
  compacted automatically); run `pg-router log compact` (or restart pg-router, which compacts at startup)
  to reclaim dead history now — `pg-router log compact --dry-run` shows what it would reclaim first, and
  it never discards a queued event, so it only helps when the log is large because of churn, not backlog;
  raise `PG_ROUTER_MAX_LOG_BYTES` (or `[pool].max_log_bytes`) and restart; or stop the daemon and move
  `queue.jsonl` aside (this LOSES the queued events). `log_unwritable`: free disk space or fix
  permissions on the log directory. `events.jsonl` and `launchd-stderr.log` in the same state
  directory are unbounded and out of scope, but share the disk and can cause `log_unwritable`.
- **Not in scope:** per-type fairness — one noisy type can fill the file for all (accepted; event TTL
  still bounds how long any event waits).

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
