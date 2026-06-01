# pa-monitor: SQLite-backed state design

**Status**: Draft
**Date**: 2026-06-01
**Deciders**: Phillip Green II, Claude

## Context

The TUI's "all" view today cannot show sessions whose process has closed: pa-monitor's in-memory `aggregate.Tree` only contains sessions returned by `Discoverer.Discover()`, which filters out dead PIDs before they reach the tree. We want "all" to include sessions whose process is gone but that contributed to the active 5-hour cost window.

Beyond that immediate goal, the daemon's state has accumulated a mix of in-memory data structures, `runtime.json`, in-process caches, and on-disk files that the readers (RPC handlers, future TUI/CLI queries) have to thread through. A SQLite-backed source of truth simplifies the read paths, gives us durable state across daemon restarts (for free as a byproduct), and creates a clean producer/consumer split.

Out of scope:

- The `pa-monitor-decorator-gc` Gas City decorator binary (separate bead, separate package).
- The TUI active/all UX (separate bead — this design changes the **data** definitions of active and all; the UX bead handles the rendering / empty-subtree behaviour).
- File-system watcher integration (interface allows it later; not implemented in this design).

## Decision

Introduce SQLite as the durable source of truth for pa-monitor state. The daemon's in-memory `aggregate.Tree` goes away; reads come from a `ReadService` that queries SQLite. Writers (pollers, RPC handlers, nudger dispatcher) update SQLite through a single writer goroutine. All persistence is behind Go interfaces — SQLite is one implementation.

## Architecture

```
              ┌──────────────────────┐
              │  Session poller      │ 5s
              │  CCusage poller      │ 60s
              │  RPC: Caffeinate     │ on-event
              │  RPC: SetAutoResume  │ on-event
              │  Nudger dispatcher   │ on-event
              │  GC sweeper          │ 1h
              └────────┬─────────────┘
                       │ writes (single writer goroutine)
                       ▼
              ┌──────────────────────┐
              │ Store interfaces:    │
              │   SessionStore       │
              │   BlockStore         │
              │   WeekStore          │
              │   ContributionStore  │
              │   ToggleStore        │
              │   NudgeStore         │
              │   PendingNudgeQueue  │ (in-memory impl)
              └────────┬─────────────┘
                       │  SQLite (WAL, FK on)
                       │  ~/.local/state/pa-monitor/state.db
                       ▲
                       │ reads
              ┌────────┴─────────────┐
              │ ReadService          │
              │   GetState           │
              │   GetSessionByID     │
              │   GetPathInfo        │
              │   Toggles            │
              └────────┬─────────────┘
                       │ aggregate.Tree + Block + Week + Toggles
                       ▼
              ┌──────────────────────┐
              │ gRPC handlers        │ thin wrappers
              └──────────────────────┘
```

### Repository interface principle

Every store is a Go interface; SQLite is one implementation. Pending nudges live behind a `PendingNudgeQueue` interface whose v1 implementation is in-memory (matches today's `nudger.Store` behaviour). Future swaps — file watcher for the session poller, push-based providers for ccusage, a DB-backed nudge queue — are wiring changes, not architectural ones.

### Concurrency

Single writer goroutine fed by a channel from each writer caller. SQLite opens in WAL mode; readers (`ReadService`) do not block writers and vice versa. `PRAGMA foreign_keys = ON` enforces FK constraints at runtime.

### DB location

`$XDG_STATE_HOME/pa-monitor/state.db` (default `~/.local/state/pa-monitor/state.db`). The path tracks the existing `cache_dir` convention; one file per host.

## Schema

Conventions applied to every table:

- `id INTEGER PRIMARY KEY AUTOINCREMENT` — surrogate primary key.
- Natural keys become `NOT NULL UNIQUE` columns.
- Main entities carry `updated_at`, `deleted_at TIMESTAMP NULLABLE` (soft-delete).
- Main entities with a per-tick poller writer carry `last_processed_at TIMESTAMP` (freshness gate).
- Relation tables use FK references with `ON DELETE CASCADE`.

### `sessions`

| Column                                                                 | Type                     | Notes                                                                 |
| ---------------------------------------------------------------------- | ------------------------ | --------------------------------------------------------------------- |
| `id`                                                                   | INTEGER PK AUTOINCREMENT |                                                                       |
| `session_id`                                                           | TEXT NOT NULL UNIQUE     | from session.jsonl                                                    |
| `pid`                                                                  | INTEGER NULLABLE         | NULL when the process is dead                                         |
| `command_hash`                                                         | TEXT                     | hash of `ps -o command= -p <pid>` — guards against PID reuse          |
| `cwd`, `name`, `kind`, `entrypoint`                                    | TEXT                     | identity                                                              |
| `model`, `terminal_host`, `branch`                                     | TEXT                     | enrichment                                                            |
| `status`                                                               | TEXT                     | working / idle / dormant                                              |
| `first_prompt`                                                         | TEXT                     |                                                                       |
| `labels`                                                               | TEXT (JSON)              | merged output of label detectors + decorators (env is **not** stored) |
| `transcript_mtime`, `started_at`                                       | TIMESTAMP                |                                                                       |
| `context_tokens`, `session_tokens`, `subagent_count`, `subshell_count` | INTEGER                  |                                                                       |
| `burn_rate_short`, `burn_rate_long`, `cost_usd`                        | REAL                     | calculated snapshot; ring-buffer inputs stay in poller memory         |
| `awaiting_input`                                                       | BOOLEAN                  |                                                                       |
| `last_error_kind`, `last_error_text`                                   | TEXT                     |                                                                       |
| `last_error_at`                                                        | TIMESTAMP                |                                                                       |
| `last_error_terminal`, `last_error_retryable`                          | BOOLEAN                  |                                                                       |
| `last_processed_at`, `updated_at`                                      | TIMESTAMP NOT NULL       | freshness + change tracking                                           |
| `created_at`, `deleted_at`                                             | TIMESTAMP                | soft-delete                                                           |

### `blocks`

| Column                            | Type                     | Notes                                                                |
| --------------------------------- | ------------------------ | -------------------------------------------------------------------- |
| `id`                              | INTEGER PK AUTOINCREMENT |                                                                      |
| `block_id`                        | TEXT NOT NULL UNIQUE     | e.g. `2026-06-01T15Z`                                                |
| `started_at`, `ended_at`          | TIMESTAMP NOT NULL       | "active" derived from `NOW() BETWEEN started_at AND ended_at`        |
| `plan_cap_usd`, `total_cost_usd`  | REAL                     |                                                                      |
| `total_tokens`                    | INTEGER                  |                                                                      |
| `rate_limit_resets_at`            | TIMESTAMP NULLABLE       | moved from sessions; equals what ccusage reports                     |
| `cap_hit_at`                      | TIMESTAMP NULLABLE       | set the first tick `total_cost_usd ≥ plan_cap_usd`; never re-cleared |
| `last_processed_at`, `updated_at` | TIMESTAMP NOT NULL       |                                                                      |
| `deleted_at`                      | TIMESTAMP NULLABLE       |                                                                      |

### `weeks`

| Column                            | Type                     | Notes           |
| --------------------------------- | ------------------------ | --------------- |
| `id`                              | INTEGER PK AUTOINCREMENT |                 |
| `week_id`                         | TEXT NOT NULL UNIQUE     | e.g. `2026-W22` |
| `started_at`, `ended_at`          | TIMESTAMP NOT NULL       |                 |
| `week_cap_usd`, `total_cost_usd`  | REAL                     |                 |
| `cap_hit_at`                      | TIMESTAMP NULLABLE       |                 |
| `last_processed_at`, `updated_at` | TIMESTAMP NOT NULL       |                 |
| `deleted_at`                      | TIMESTAMP NULLABLE       |                 |

### `session_block_contributions`

| Column                            | Type                                                | Notes                                    |
| --------------------------------- | --------------------------------------------------- | ---------------------------------------- |
| `id`                              | INTEGER PK AUTOINCREMENT                            |                                          |
| `session_id`                      | INTEGER NOT NULL FK → sessions.id ON DELETE CASCADE |                                          |
| `block_id`                        | INTEGER NOT NULL FK → blocks.id ON DELETE CASCADE   |                                          |
| UNIQUE (`session_id`, `block_id`) |                                                     |                                          |
| `cost_usd`                        | REAL                                                | snapshot, replaces prior value each tick |
| `tokens`                          | INTEGER                                             |                                          |
| `updated_at`                      | TIMESTAMP NOT NULL                                  |                                          |

### `session_week_contributions`

Same shape, FK to `weeks.id`.

### `system_toggles`

Boolean daemon-wide toggles. Replaces today's `runtime.json` toggles (`caffeinate_on`, `auto_resume_enabled`).

| Column       | Type                     | Notes                                                 |
| ------------ | ------------------------ | ----------------------------------------------------- |
| `id`         | INTEGER PK AUTOINCREMENT |                                                       |
| `name`       | TEXT NOT NULL UNIQUE     | `'caffeinate_on'` / `'auto_resume_enabled'`           |
| `value`      | BOOLEAN NOT NULL         |                                                       |
| `updated_at` | TIMESTAMP NOT NULL       |                                                       |
| `deleted_at` | TIMESTAMP NULLABLE       | unused in practice; present for convention uniformity |

`auto_resume_delay_s` stays in `config.toml` — there is no RPC to mutate it.

### `nudge_history`

Append-only log of every nudge dispatch. "Latest state per (session [, source])" is `MAX(fired_at) WHERE …`. Rows are immutable events — no `updated_at`, no `deleted_at`; cascade-deleted with their session.

| Column               | Type                                                | Notes                                                           |
| -------------------- | --------------------------------------------------- | --------------------------------------------------------------- |
| `id`                 | INTEGER PK AUTOINCREMENT                            |                                                                 |
| `session_id`         | INTEGER NOT NULL FK → sessions.id ON DELETE CASCADE |                                                                 |
| `text`               | TEXT NOT NULL                                       | message sent                                                    |
| `result`             | TEXT NOT NULL                                       | `'sent'` / `'failed'` / `'suppressed'` / `'escalated'`          |
| `error_text`         | TEXT NULLABLE                                       | when `result='failed'`                                          |
| `caused_by_error_at` | TIMESTAMP NULLABLE                                  | the `ErrorRecord.At` that triggered (for `'disrupted'` sources) |
| `escalated`          | BOOLEAN NOT NULL                                    | this dispatch represents a give-up rather than a send           |
| `fired_at`           | TIMESTAMP NOT NULL                                  | indexed `(session_id, fired_at DESC)`                           |

### `nudge_history_sources`

One row per source that contributed to a dispatch. A single nudge may carry multiple sources (the dispatcher combines simultaneous requests into one send).

| Column                                | Type                                                     | Notes                                        |
| ------------------------------------- | -------------------------------------------------------- | -------------------------------------------- |
| `id`                                  | INTEGER PK AUTOINCREMENT                                 |                                              |
| `nudge_history_id`                    | INTEGER NOT NULL FK → nudge_history.id ON DELETE CASCADE |                                              |
| `source`                              | TEXT NOT NULL                                            | `'manual'` / `'disrupted'` / `'auto_resume'` |
| UNIQUE (`nudge_history_id`, `source`) |                                                          |                                              |

Indexes: `(source, nudge_history_id)` for filter-by-source queries; `(nudge_history_id)` for the cascade direction.

### Cascade tree

```
sessions
  ├── session_block_contributions       (CASCADE)
  ├── session_week_contributions        (CASCADE)
  └── nudge_history                     (CASCADE)
        └── nudge_history_sources       (CASCADE)

blocks
  └── session_block_contributions       (CASCADE)

weeks
  └── session_week_contributions        (CASCADE)
```

## Freshness + soft-delete model

### Two timestamps per main entity

| Column              | Bumps when                                                         |
| ------------------- | ------------------------------------------------------------------ |
| `updated_at`        | a data field actually changed                                      |
| `last_processed_at` | the poll loop visited this row this tick (even if nothing changed) |

The freshness filter uses `last_processed_at`. `updated_at` is informational only (audit trail of when content last shifted).

### 12× freshness rule

| Writer         | Poll interval | Freshness window |
| -------------- | ------------- | ---------------- |
| Session poller | 5s            | 60s              |
| CCusage poller | 60s           | 720s (12 min)    |

Future pollers default to 12× their poll interval. The filter is layered on top of every read query that touches sessions / blocks / weeks:

```sql
WHERE deleted_at IS NULL
  AND last_processed_at > NOW() − <window>
```

System toggles, nudge history, and contribution rows are exempt — they have no per-tick poller and inherit relevance from their parents.

### Soft-delete triggers

- **Sessions**: GC sweep marks `deleted_at = NOW()` when the underlying `.jsonl` file is gone from `~/.claude/sessions/`. Un-soft-deleted on the next GC if the file reappears before hard-delete.
- **Blocks / weeks**: GC sweep marks `deleted_at = NOW()` when `NOW() NOT BETWEEN started_at AND ended_at` **AND** there are no associated `*_contributions` rows. Un-soft-deleted on the next GC if a contribution later attaches.

### Hard-delete

GC hard-deletes rows where `deleted_at < NOW() − 24h`. Cascades clear contributions and `nudge_history*`.

## Writers

| Writer            | Cadence         | Tables written                                                                       |
| ----------------- | --------------- | ------------------------------------------------------------------------------------ |
| Session poller    | 5s              | `sessions` (UPSERT all), `session_block_contributions`, `session_week_contributions` |
| CCusage poller    | 60s             | `blocks`, `weeks`                                                                    |
| Caffeinate RPC    | on toggle       | `system_toggles` (`caffeinate_on`)                                                   |
| SetAutoResume RPC | on toggle       | `system_toggles` (`auto_resume_enabled`)                                             |
| Nudger dispatcher | per nudge fired | `nudge_history` + `nudge_history_sources` (single tx)                                |
| GC sweeper        | 1h              | file-reconciliation + hard-deletes (single tx per stage)                             |

### Session poller (one tx per tick)

1. `ListSessionIDs(SessionsDir)` — list every `.jsonl` filename regardless of PID state.
2. For each file: read raw session metadata, check `PidAlive(r.PID)`, scan transcript, apply detectors + decorators, compute burn-rate snapshot.
3. UPSERT into `sessions` for every file:
   - `pid = (alive ? actual : NULL)`
   - `last_processed_at = NOW()`
   - `updated_at = NOW()` iff any data field changed
4. UPSERT per-session contributions for the current block / week.

`Discoverer.Discover()` is modified: it no longer drops dead-PID sessions. The PidAlive check still exists but its output becomes a flag on the returned `Session`, not a filter.

### CCusage poller (one tx per run)

1. Shell out to ccusage (active block + weekly).
2. UPSERT the current block / week:
   - `last_processed_at = NOW()`
   - `updated_at = NOW()` iff any field changed
   - `cap_hit_at = NOW()` when `total_cost_usd ≥ plan_cap_usd` and `cap_hit_at` is currently NULL

No soft-delete of past blocks/weeks here — that's GC's job.

### Caffeinate / SetAutoResume RPC handlers

Single-row UPSERT into `system_toggles`. The in-memory `Caffeinate.Manager` subprocess is still driven from the RPC; the DB write is for persistence.

### Nudger dispatcher (one tx per dispatch)

1. INSERT into `nudge_history`.
2. INSERT one `nudge_history_sources` row per contributing source.

The pending nudge queue stays in memory (behind `PendingNudgeQueue` interface). A pending intent is dropped if its session's PID dies before dispatch — the design accepts this loss; if the session comes back, a future write source can re-queue.

### GC sweeper (1 h, one tx per stage)

1. **Session file reconciliation**
   - `UPDATE sessions SET deleted_at = NOW() WHERE deleted_at IS NULL AND session_id NOT IN (current file list)`
   - `UPDATE sessions SET deleted_at = NULL WHERE deleted_at IS NOT NULL AND session_id IN (current file list)` — file reappeared
2. **Hard-delete sessions**: `DELETE FROM sessions WHERE deleted_at < NOW() − 24h`. Cascades.
3. **Soft-delete orphan blocks / weeks**
   - `UPDATE blocks SET deleted_at = NOW() WHERE deleted_at IS NULL AND NOT (NOW() BETWEEN started_at AND ended_at) AND id NOT IN (SELECT DISTINCT block_id FROM session_block_contributions)`
   - Same shape for weeks.
   - Inverse: `UPDATE blocks SET deleted_at = NULL WHERE deleted_at IS NOT NULL AND id IN (SELECT DISTINCT block_id FROM session_block_contributions)`
4. **Hard-delete blocks / weeks**: `DELETE FROM blocks WHERE deleted_at < NOW() − 24h`. Same for weeks.

## Readers

### `ReadService`

```go
type SessionFilter int

const (
    FilterActive SessionFilter = iota // pid IS NOT NULL AND in active block
    FilterAll                         // pid IS NOT NULL OR in active block
)

type ReadService interface {
    GetState(ctx, filter SessionFilter) (*State, error) // full snapshot
    GetSessionByID(ctx, id string) (*SessionDetail, error)
    GetPathInfo(ctx, path string) (*PathRollup, error)
    Toggles(ctx) (Toggles, error)
}
```

RPC handlers stay thin: call the service, serialise to proto.

### Active/All on the wire

Two options:

- **v1 (recommended)**: `GetState` / `WatchState` call `ReadService.GetState(FilterAll, …)` unconditionally. The wire response contains the broader set; the TUI applies the user's active/all toggle client-side (matches today's behaviour — no proto change needed).
- **v2 (future)**: add `SessionFilter filter = …` to `GetStateRequest` and `WatchStateRequest`. Server runs the chosen query. Smaller payloads when the user picks "active".

This design ships v1. The `SessionFilter` enum is internal to the service for v1, ready to be promoted to the wire later.

### Invariants on every read

```sql
WHERE deleted_at IS NULL
  AND last_processed_at > NOW() − <window>
```

…layered on top of every query against sessions / blocks / weeks. Stale or soft-deleted rows are invisible to the API.

### Active vs All

First, resolve the active block id:

```sql
SELECT id FROM blocks
WHERE deleted_at IS NULL
  AND last_processed_at > NOW() − 720s
  AND NOW() BETWEEN started_at AND ended_at
ORDER BY started_at DESC LIMIT 1
```

**Active** (= `pid IS NOT NULL AND in active block`):

```sql
SELECT s.*, c.cost_usd AS block_cost, c.tokens AS block_tokens
FROM sessions s
INNER JOIN session_block_contributions c ON c.session_id = s.id
WHERE s.deleted_at IS NULL
  AND s.last_processed_at > NOW() − 60s
  AND s.pid IS NOT NULL
  AND c.block_id = ?    -- active block id
```

**All** (= `pid IS NOT NULL OR in active block`):

```sql
SELECT s.*, c.cost_usd AS block_cost, c.tokens AS block_tokens
FROM sessions s
LEFT JOIN session_block_contributions c ON c.session_id = s.id AND c.block_id = ?
WHERE s.deleted_at IS NULL
  AND s.last_processed_at > NOW() − 60s
  AND (s.pid IS NOT NULL OR c.id IS NOT NULL)
```

The TUI's active/all flag is passed to the RPC; the server runs the appropriate query.

### What's queried vs computed

**Queried straight from DB**:

- Session row contents (PID, name, status, model, errors, burn-rate snapshot)
- Per-session contributions to active block / week
- Block / week current row (cost, cap, `cap_hit_at`, `started_at`, `ended_at`)
- System toggles
- Latest `nudge_history` row per session (for the nudge feedback indicator)

**Computed in service from queried rows**:

- `aggregate.PathNode` tree — built per request from the session list (today's `BuildPathTree`)
- `aggregate.Directory` rollups (WorkingN / IdleN / DormantN / TotalTokens / TotalCostUSD / BurnRateSum) — `GROUP BY cwd` over sessions
- `WindowResetsAt` — derived from active block's `rate_limit_resets_at` (or `ended_at` fallback)
- `TopupShouldDisplay` — `active_block.total_cost_usd ≥ active_block.plan_cap_usd`
- PR info per dir — current file-backed `prCache`, untouched by this design

**Not in DB and not computed at read time** — burn-rate ring buffer inputs stay in poller memory; only the calculated snapshot reaches the DB.

### Caching

None in v1. Every RPC hits the DB. Single-host volume + WAL is comfortable. If profiling later shows hot-path queries dominating, a process-local read cache (invalidated by the writer) fits behind the same `ReadService` interface.

### Empty-state behaviour

If `last_processed_at` is stale across the board (poller hung, daemon just started), queries return empty. RPC responses report the empty state honestly — there is no fallback to "last known good." Stale data is worse than no data for an operational tool.

## Wire format additions

`pa_monitor.proto`:

- `Block.cap_hit_at` (Timestamp)
- `Week.cap_hit_at` (Timestamp)

Everything else (`SessionView`, `Directory`, etc.) is identical content from a new source.

## Migration plan

The repo policy is local-only / no backward compatibility, so migration is simple:

1. **Schema bootstrap**: on first daemon start with this code, the SQLite file is absent. The DB layer creates the schema via embedded migrations and inserts defaults.
2. **`runtime.json` → DB**: on first start, if `runtime.json` exists, read its `CaffeinateOn` + `AutoResumeEnabled` + `Nudger.Sessions` watermarks; populate `system_toggles` and seed `nudge_history` with synthetic rows so existing cooldown windows survive the migration. Then `runtime.json` is deleted.
   - Seeding strategy depends on whether `feat/tui-nudge-feedback` (which added `LastNudgeSources` to `runtime.json`) merged before this work. If yes: synthesise one row per session with `fired_at = LastNudgedAt` and source rows from `LastNudgeSources`; a second row per session with `sources=['disrupted']`, `fired_at = LastDisruptNudgeAt`, `caused_by_error_at = LastDisruptNudgeFor`, `escalated = DisruptEscalated`. If no: fall back to `sources=['unknown']` for the first row.
3. **First poll cycle**: 5s after start, sessions populate. 60s after, blocks/weeks populate. Until then, RPCs honestly return empty.

No long-running parallel state; the cut-over is one release.

## Consequences

### Positive

- "All" view can include dead-PID sessions that contributed to the active block.
- Daemon state survives restarts (free byproduct).
- Read paths simplify — RPC handlers go through one service, one set of interfaces.
- Adding new write sources (file watcher, push-based providers, alternative cost data) is a wiring change, not a rewrite.
- Schema documents the data shape that today is split across in-memory structs, `runtime.json`, and on-disk caches.

### Negative

- More moving parts: schema migrations, a writer goroutine, the GC sweeper.
- Tests grow heavier (DB setup overhead, even in-memory SQLite is non-trivial).
- The freshness rule introduces a new failure mode (everything goes stale if the poller hangs) that didn't exist before.
- One more file on disk to back up / understand / corrupt.

### Neutral

- Performance is comparable for single-host single-daemon use. SQLite reads in WAL mode are sub-millisecond at this row count.
- The wire format hardly changes; existing TUI/CLI consumers see the same shape.

## Alternatives considered

### Add a "remembered sessions" cache without a DB

Keep today's in-memory tree, but also keep a separate in-memory map of "sessions that contributed to the active block" with their last-known fields. Smaller change, solves the stated goal, but leaves the daemon's runtime state in `runtime.json` and the rest of the read-side awkward (TUI/CLI still cannot read anything without the daemon being up).

Rejected because the desired evolution is broader than "all view"; this would be a half-measure that gets revisited.

### Event log + materialisation (pure CQRS)

Append-only observation rows; read service walks the log for every query. Maximum auditability.

Rejected because read costs balloon and there is no second consumer that would benefit from the audit trail at this point.

### Daemon-optional reads

Move ccusage results, runtime state, etc. into the DB so the TUI/CLI can read without the daemon running. Specifically rejected for this design — Phil's current priority is just session persistence; daemon-optional reads come later as a smaller follow-up once the DB exists.

## Related decisions

See also `docs/adr/0011-pa-monitor-daemon-otel-split.md` — keeps the OTel pipeline orthogonal to this design. Metrics emission still flows through the existing emitter; the DB is the operational state store, not a metrics sink.
