# pr-pool

`pr-pool` runs one drain pass over a monorepo: it discovers ready beads,
dispatches a session per configured role (in config order) up to each role's
cap, waits for completion, then tears down every `pr-pool-*` tmux session. Bare
`pr-pool` is equivalent to `pr-pool drain`.

> **Behavior:** how pr-pool should behave as an **orchestrator** — the drain, roles &
> queries from config, and the agent-runner / query-source contracts — lives in the
> [behavior docs](docs/behavior/README.md). The **workflows** built on pr-pool
> (reviewing PRs, shepherding changes, working a backlog) are defined by the
> deployment, not here. For how the code realizes a review flow today, see the
> downstream reference [`docs/pr-review-flow.md`](../../docs/pr-review-flow.md).

## Subcommands

| Command                   | Description                                                                                                                                                       |
| ------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `run`                     | boot the core and run indefinitely, producing + dispatching on a fixed poll interval, until SIGINT/SIGTERM requests shutdown                                      |
| `run-until-idle`          | boot the core, discover once, drain the queue to idle, then exit (also reachable as `drain`, kept as a deprecated alias)                                          |
| `drain`                   | run one drain pass (the default when omitted)                                                                                                                     |
| `run-query <role>`        | run a role's discovery query and print matches (read-only)                                                                                                        |
| `run-role <role> <bead>`  | dispatch one bead through a role, then tear down (smoke test)                                                                                                     |
| `config --print-defaults` | print the built-in default `config.toml` (a copy-paste start)                                                                                                     |
| `config --show`           | print the resolved config path, role set, and worker dispatch scalars (permission-mode/allowed-tools/budget)                                                      |
| `sessions`                | list this pool's sessions (bead/role) from session metadata                                                                                                       |
| `reconcile`               | report stranded self-owned feedback cycles, then run the pg-pr ACL: ensure a review-pr bead per open PR (reads `pg-pr pr list`; mutates beads; exit-0-on-partial) |
| `push-inject <json>`      | inject one operator-supplied event into the **running** core (text, or JSON with `--json`)                                                                        |
| `version`                 | print the version and exit                                                                                                                                        |
| `help`                    | print help and exit                                                                                                                                               |

`<role>` is the role's configured `name`.

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
