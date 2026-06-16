# pr-pool

`pr-pool` runs one drain pass over a monorepo: it discovers ready beads,
dispatches a Claude session per role (feedback, then worker) up to each role's
cap, waits for completion, then tears down every `pr-pool-*` tmux session. Bare
`pr-pool` is equivalent to `pr-pool drain`.

## Subcommands

| Command                  | Description                                                   |
| ------------------------ | ------------------------------------------------------------- |
| `drain`                  | run one drain pass (the default when omitted)                 |
| `run-query <role>`       | run a role's discovery query and print matches (read-only)    |
| `run-role <role> <bead>` | dispatch one bead through a role, then tear down (smoke test) |
| `version`                | print the version and exit                                    |
| `help`                   | print help and exit                                           |

## Configuration

Configuration is via `PR_POOL_*` environment variables (there are no flags). See
`internal/config` for the full set and defaults. Common ones:

- `PR_POOL_MAX_WORKER` — max concurrent worker dispatches (default 1)
- `PR_POOL_MAX_FEEDBACK` — max concurrent feedback dispatches (default 1)
- `PR_POOL_BUDGET_TOKENS` — per-worker token budget; 0 = unlimited (default 0)
- `PR_POOL_BUDGET_COST` — per-worker cost budget in cents; 0 = unlimited (default 0)
- `PR_POOL_BUDGET_TIME` — per-worker wall-clock budget in seconds (default 1500)
- `PR_POOL_MODEL` — claude model override (default: ccpool's default)
- `PR_POOL_EFFORT` — claude `--effort` value (default `max`)
- `PR_POOL_PERMISSION_MODE` — claude `--permission-mode` for workers (default `bypassPermissions`)
- `PR_POOL_REPO_ROOT` — monorepo root the drain operates in (default: cwd)
- `PR_POOL_BEADS_PREFIX` — expected bead prefix, asserted at precheck (default `zr`)
- `PR_POOL_LOG_DIR` — override the event-log directory (default: the standard path below)

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
