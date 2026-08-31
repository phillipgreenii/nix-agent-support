# pr-pool

`pr-pool` runs one drain pass over a monorepo: it discovers ready beads,
dispatches a session per configured role (in config order) up to each role's
cap, waits for completion, then tears down every `pr-pool-*` tmux session. Bare
`pr-pool` (no subcommand) now requires an explicit subcommand; see
`run-until-idle` below for the single-pass drain behavior this used to default to.

> **Behavior:** how pr-pool should behave as an **orchestrator** — the drain, roles &
> queries from config, and the agent-runner / query-source contracts — lives in the
> [behavior docs](docs/behavior/README.md). The **workflows** built on pr-pool
> (reviewing PRs, shepherding changes, working a backlog) are defined by the
> deployment, not here. For how the code realizes a review flow today, see the
> downstream reference [`docs/pr-review-flow.md`](../../docs/pr-review-flow.md).

## Subcommands

| Command                            | Description                                                                                                                                                                                 |
| ---------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `run`                               | boot the core and run indefinitely, producing + dispatching on a fixed poll interval, until SIGINT/SIGTERM requests shutdown                                                                |
| `run-until-idle`                   | boot the core, discover once, drain the queue to idle, then exit (also reachable as `drain`, kept as a deprecated alias)                                                                    |
| `drain`                            | deprecated alias for `run-until-idle` (see above); no longer the default — bare `pr-pool` (no subcommand) now requires an explicit subcommand                                               |
| `run-query [--json] query:<name>` | smoke-test one named source's query once, read-only, and print the matches it would emit (text, or one JSON object with `--json`); the old `run-query <role>` form no longer runs anything — see `MIGRATION.md` |
| `run-role [--json] <role> <bead>`  | dispatch one bead through a role, then tear down (smoke test; `--json` reports the outcome as one JSON object)                                                                              |
| `config --print-defaults`          | print the built-in default `config.toml` (a copy-paste start)                                                                                                                                |
| `config --show [--json]`           | print the resolved config path, role set, and worker dispatch scalars (permission-mode/allowed-tools/budget); text, or one JSON object with `--json`                                        |
| `sessions`                         | list this pool's sessions (bead/role) from session metadata                                                                                                                                 |
| `reconcile`                        | report stranded self-owned feedback cycles, then run the pg-pr ACL: ensure a review-pr bead per open PR (reads `pg-pr pr list`; mutates beads; exit-0-on-partial)                           |
| `push-inject <json>`               | inject one operator-supplied event into the **running** core (text, or JSON with `--json`)                                                                                                  |
| `status`                           | inspect the **running** core: resolved config, live deliveries, per-`type` queue depths, plus gates/mode/listeners/sources/unmatched bindings/recent activity (text, or JSON with `--json`) |
| `pause [<gate>]`                   | set gate `<gate>` (default `quota-paused`) directly on its file-backed state (`INV-LIFE-2`) — see [below](#pause--resume--operator-gate-control)                  |
| `resume [<gate>] \| --all`         | clear gate `<gate>` (default `quota-paused`), or every outstanding gate with `--all` — see [below](#pause--resume--operator-gate-control)                         |
| `version`                          | print the version and exit                                                                                                                                                                  |
| `help`                             | print help and exit                                                                                                                                                                         |

`<role>` is the role's configured `name`; `<name>` in `query:<name>` is a `[[query]]`'s configured
`name`.

### `push-inject` — operator event injection

```
pr-pool push-inject [--json] [--socket <path>] [--token <tok>] '<event-json>'
```

`push-inject` is the **operator-facing front door to the push-ingest path**: it validates the
event against the `cli.push-inject` message schema, locates the running core, and performs the
**same core-side enqueue** as the `ingest-event` manager callback — durable via the queue,
delivered at-least-once and deduped (`INV-EVT-*`). It is **distinct from** `ingest-event` (a
manager→core callback) and from `run-role` (a smoke test that tears down). Primarily for
manual/test injection.

It locates the core via `--socket`/`--token`, else `PR_POOL_SOCKET`/`PR_POOL_TOKEN`, else the
discovery record under the log dir, and **forwards the event over that socket** — the core owns
the durable queue in another process, so nothing is enqueued locally. With **no core running it
fails** with a "no running core" error and **exit 1**; it never starts one
([ADR 0036](../../docs/adr/0036-pr-pool-cli-never-auto-starts-a-core.md)).

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
pr-pool status [--json] [--socket <path>] [--token <tok>]
```

`status` is the operator-facing INTF-CLI inspection verb: resolved configuration,
live deliveries, and per-`type` queue depths — the three inspection MUSTs
interfaces.md's "Inspecting a running core" declares — plus the current gate
state, run mode, registered listeners/sources, unmatched bindings, and recent
dispatch-outcome activity (`internal/activity.Ring`, Task 3.4). It locates the
core the same way `push-inject` does (`--socket`/`--token`, else
`PR_POOL_SOCKET`/`PR_POOL_TOKEN`, else discovery under the log dir) and **never
starts one** ([ADR 0036](../../docs/adr/0036-pr-pool-cli-never-auto-starts-a-core.md)).

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

### `pause` / `resume` — operator gate control

```
pr-pool pause [<gate>]
pr-pool resume [<gate> | --all]
```

`pause`/`resume` set or clear a global **gate** (`INV-LIFE-2`) directly on its **file-backed
state**: while a gate is set, the core suspends event production and new dispatch (accepted
work still runs to completion, and expiry still advances). There are exactly two named gates,
`quota-paused` (the operator's own) and `cicd-down` (an automation actor's); omitting `<gate>`
defaults to `quota-paused`, and clearing **every** outstanding gate requires an explicit
`resume --all` — a bare `resume` clears only the default gate, so an automation-owned gate is
never cleared by accident. `resume --all <gate>` (both at once) is a usage error (exit `2`).

**FILE-DIRECT**: unlike every other operator subcommand, `pause`/`resume` **never Discover or
Dial** a core — they act on the gate file's existence directly and **succeed even with no core
running** (exit `0`), reporting that the change takes effect at the next start (a currently
running `run` picks it up on its next tick). This deliberately breaks the
verb-named-subcommand-is-a-socket-client symmetry that `push-inject`/`ingest-event`/
`self-status` follow. A **socket**-level `pause`/`resume` verb also exists (Phase 3) for a
client already holding a connection to a running core; both paths act on the same file-backed
state, so they can never disagree about what outlives the call.

Re-pausing an already-set gate is idempotent-visible (`already paused (quota-paused since
14:03)`) and never resets the original mtime. `pr-pool config --show` prints each gate's path,
whether it is set, and its "paused since" mtime when it is.

### Manager → core callback subcommands

The core also carries the **manager→core callback** subcommands. These are **not** operator
commands: the core hands a registered participant one command string with `--socket` and `--token`
already baked in, and the participant appends its arguments and runs it (see the behavior docs'
`INTF-CLI`).

| Command        | Description                                                                                                                                                                                                                                                                                                                            |
| -------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ingest-event` | deliver one or more events to the **running** core: request JSON on stdin, reply JSON on stdout, coarse exit code (0 ok / 1 error / 2 usage / 9 busy)                                                                                                                                                                                  |
| `self-status`  | push the caller's own status (healthy/degraded/unavailable) to the **running** core, naming the `participantId` it registered under: request JSON on stdin, reply JSON on stdout, coarse exit code (0 ok / 1 error / 2 usage / 9 busy). Every registered participant kind gets this callback, unlike `ingest-event` (a source's alone) |

`ingest-event` locates the core via `--socket`/`--token`, else `PR_POOL_SOCKET`/`PR_POOL_TOKEN`,
else the discovery record under the log dir. With **no core running it fails** with a
"no running core" error and exit 1 — it never starts one
([ADR 0036](../../docs/adr/0036-pr-pool-cli-never-auto-starts-a-core.md)).

At dispatch, pr-pool stamps each ccpool session with metadata under the `prpool.*`
namespace (`prpool.bead`, `prpool.role`, `prpool.pool`) via `ccpool new --meta`, so a
session's bead/role/owner are first-class and queryable. `pr-pool sessions` reads them
back (`ListByMeta`/`Meta`) from the pool `CCPOOL_POOL` resolves (default XDG pool).

## Roles, prompts & queries (`config.toml`)

Roles, their prompts, and their discovery queries are configured in a repo-local
`<RepoRoot>/.pr-pool/config.toml` (override the path with `PR_POOL_CONFIG`). When
no config file is present, pr-pool uses the **built-in feedback, worker, and
review roles**. Run `pr-pool config --print-defaults` to see the full
schema and the canonical defaults, then copy it and edit.

Roles and queries are typed tagged unions discriminated by a `type` field:

- **role `type`**: `ccpool` (dispatch a Claude session) or `command` (run an
  executable; completion = exit code).
- **query `type`**: `command` (run an executable that emits items as
  JSON/JSONL — an opaque token pr-pool just invokes and never interprets, so
  this is how you wire pr-pool to `bd`, `gh`, Jira, or anything else) or
  `event` (an in-process correlated-event source for the aggregator/saga
  path). `beads-ready` / `beads-list` / `github-issues` / `jira-issues` were
  typed query sources here through pg2-n75tk; each one typed "how another
  tool is configured" into Core, which the config surface is not supposed to
  know, and `jira-issues` specifically was structurally unsatisfiable (its
  backing command exists only in a downstream flake this one cannot depend
  on). See `MIGRATION.md` for converting an old config using one of the four
  removed types to an equivalent `command` block, worked through for each.
  The built-in feedback/worker/review defaults (`pr-pool config
--print-defaults`) are themselves the worked beads-ready -> command example:
  each is now printed as a `command` block shelling to `bd ready | jq ...`.

A `ccpool` role's behavior is set by code-owned enums: `completion`
(`close-only` | `close-or-handback`), `on_failure` (`unclaim` | `add-human`),
`on_dispatch_fail` (`unclaim` | `leave`). When `authorship_guard = true`, pr-pool
prepends a **non-editable** safety preamble (assert author is me, branch starts
with `phillipg.`, never force-push) ahead of the role's task prompt, so
externalizing the prompt never weakens the guardrails. A role's prompt is inline
(`prompt`) or an external file (`prompt_file`, resolved relative to the config
dir) — exactly one.

> **Monorepo config hygiene:** add `.pr-pool/` to your monorepo's
> `.git/info/exclude` so a repo-local pr-pool config (and its prompts) is never
> committed there. `pr-pool drain` warns at pre-flight if `.pr-pool/config.toml`
> is git-tracked.

## Configuration (pool-wide env)

Pool-wide settings come from `PR_POOL_*` environment variables; roles are NOT
configured via env (use `config.toml`). See `internal/config` for the full set.

- `PR_POOL_REPO_ROOT` — monorepo root the drain operates in (default: cwd)
- `PR_POOL_BEADS_PREFIX` — expected bead prefix, asserted at precheck (default `zr`, a
  deployment-specific default — set it to your prefix)
- `PR_POOL_CONFIG` — explicit `config.toml` path (default `<RepoRoot>/.pr-pool/config.toml`)
- `PR_POOL_BUDGET_TOKENS` — per-worker token budget; 0 = unlimited (default 0)
- `PR_POOL_BUDGET_COST` — per-worker cost budget in cents; 0 = unlimited (default 0)
- `PR_POOL_BUDGET_TIME` — per-worker wall-clock budget in seconds (default 1500)
- `PR_POOL_MODEL` — claude model override (default: ccpool's default)
- `PR_POOL_EFFORT` — claude `--effort` value (default `max`)
- `PR_POOL_PERMISSION_MODE` — claude `--permission-mode` for workers (default `dontAsk`: deny-by-default; `bypassPermissions` is the opt-in escape)
- `PR_POOL_ALLOWED_TOOLS` — claude `--allowed-tools` allowlist for workers (default: a conservative set; `git push` excluded. Empty clears the flag)
- `PR_POOL_AUTONOMOUS` — block AskUserQuestion so human-less workers never stall on the picker (default `true`)
- `PR_POOL_LOG_DIR` — override the event-log directory (default: the standard path below)
- `PR_POOL_ACTIVITY_RING` — dispatch-outcome activity ring buffer capacity (`internal/activity.Ring`, Task 3.4); default 512
- `PR_POOL_LOG_DIR` — override the event-log/state directory: gates, `events.jsonl`, the discovery record (default: the standard path below)
- `PR_POOL_ACTIVITY_RING` — dispatch-outcome activity ring buffer capacity (`internal/activity.Ring`, Task 3.4); default 512
- `PR_POOL_QUOTA_PAUSED` — `quota-paused` gate file path override (default `<PR_POOL_LOG_DIR>/gates/quota-paused`)
- `PR_POOL_CICD_DOWN` — `cicd-down` gate file path override (default `<PR_POOL_LOG_DIR>/gates/cicd-down`)
- `PR_POOL_TEST_MODE` — set to `1` by `run-role`/`run-query` for the duration of that one smoke
  test, so a participant it dispatches (or a command-backed source it shells out to) knows a test
  is in flight; advisory only. Not meant to be set by an operator directly.

Precedence for every scalar above that a `[pool]` key can also set (including the two gate
paths): `[pool]` wins over `PR_POOL_*` env, which wins over the built-in default — matching
`internal/config`'s package doc and `config --print-defaults`'s header. The XDG-global config
(`$XDG_CONFIG_HOME/pr-pool/config.toml`, else `~/.config/pr-pool/config.toml`) contributes
`[pool].budget` only, beneath the repo-local file and above env.

**Removed** (now per-role in `config.toml`, not env): `PR_POOL_MAX_WORKER`,
`PR_POOL_MAX_FEEDBACK`, `PR_POOL_FEEDBACK_ENABLED`, `PR_POOL_WORKER_ENABLED`,
`PR_POOL_SKILL_MD`, `PR_POOL_WORKER_SKILL_MD`. Set `role.cap` / `role.enabled` /
the role's prompt in `config.toml` instead; `drain` warns if any are still set.

## Observability

The budget watchdog writes a structured per-run event stream as JSONL (one JSON
object per line) conforming to the phillipgreenii JSONL logging standard: every
record carries `time` (RFC3339Nano, UTC), `level` (lowercase
`debug`/`info`/`warn`/`error`), and `msg`, plus an event-type `kind` field
(`reminder` → `info`, `cancel` → `warn`, `hard_stop` → `error`). This is
pr-pool's own event log, not the Claude transcript.

The log is written to the standard path
`${XDG_STATE_HOME}/pr-pool/events.jsonl` (no `/log` subdirectory), which matches
the default `logSources` glob `${env:XDG_STATE_HOME}/pr-pool/*.jsonl`.

Collection into Loki is pull-based: the darwin module
`darwin/modules/pr-pool/default.nix` registers
`phillipgreenii.observability.logSources.pr-pool` (guarded on `obs.enable`, so it
is a no-op on machines without the observability stack). No OTel code lives in
the binary and no `path` override is needed because the file already sits at the
default glob.
