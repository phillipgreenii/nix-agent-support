# pa-monitor SQLite-Backed State Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace pa-monitor's in-memory `aggregate.Tree` with a SQLite-backed source of truth so the TUI "all" view can include sessions whose process has closed but that contributed to the active 5h cost window.

**Architecture:** All persistence sits behind Go interfaces. SQLite (modernc.org/sqlite, pure-Go driver, WAL mode, foreign keys on) is the v1 implementation. A single writer goroutine serialises mutations from pollers and RPC handlers. A `ReadService` rebuilds today's `aggregate.Tree` from queries per request. An hourly GC sweeper owns soft-delete reconciliation and hard-delete.

**Tech Stack:** Go 1.25, modernc.org/sqlite, embedded SQL migrations, existing pa-monitor stack (gRPC, OTel, etc.).

**Spec:** `docs/superpowers/specs/2026-06-01-pa-monitor-sqlite-design.md`

**Working directory:** `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pa-monitor/`

**Test command (run after every code change):**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pa-monitor && go build ./... && go test ./...
```

**Commit style:** Lowercase `feat(pa-monitor):` / `refactor(pa-monitor):` / `chore(pa-monitor):`. Pre-commit (treefmt) runs automatically — let it run, don't bypass.

---

## File Structure

```
packages/pa-monitor/internal/
  store/                              (NEW)
    store.go                          # Store interface aggregation + types
    session_store.go                  # SessionStore interface + Session type
    block_store.go                    # BlockStore interface + Block type
    week_store.go                     # WeekStore interface + Week type
    contribution_store.go             # ContributionStore interface + Contribution type
    toggle_store.go                   # ToggleStore interface
    nudge_store.go                    # NudgeStore interface + NudgeEvent/NudgeSource types
    pending_nudge_queue.go            # PendingNudgeQueue interface + in-memory impl
    pending_nudge_queue_test.go
    sqlite/                           (NEW)
      sqlite.go                       # Open() helper: WAL, FK, busy_timeout
      sqlite_test.go
      migrations.go                   # Embedded migration runner
      migrations_test.go
      migrations/                     # SQL files (//go:embed)
        001_initial.sql
      session_store.go                # SQLite SessionStore impl
      session_store_test.go
      block_store.go
      block_store_test.go
      week_store.go
      week_store_test.go
      contribution_store.go
      contribution_store_test.go
      toggle_store.go
      toggle_store_test.go
      nudge_store.go
      nudge_store_test.go
      testhelper_test.go              # shared test fixtures: open in-memory DB
  service/                            (NEW)
    read_service.go                   # ReadService + State type
    read_service_test.go
    write_service.go                  # WriteService: single writer goroutine
    write_service_test.go
    tree_builder.go                   # Build aggregate.Tree from session rows
    tree_builder_test.go
  daemon/
    server.go                         (MODIFY) RPC handlers consult ReadService
    lifecycle.go                      (MODIFY) open DB, start writer + GC, wire stores
    state.go                          (MODIFY) shrink sharedState; tree fields removed
    runtime_state.go                  (DELETE post-migration)
    nudger_runtime.go                 (DELETE post-migration)
  core/poller/
    poller.go                         (MODIFY) write to stores via WriteService
  core/session/
    discovery.go                      (MODIFY) drop PidAlive filter; return alive flag
  proto/
    pa_monitor.proto                  (MODIFY) add cap_hit_at to Block + Week
```

---

## Phase 0 — Dependency + Package Scaffolding

### Task 1: Add modernc.org/sqlite dependency

**Files:**

- Modify: `packages/pa-monitor/go.mod`
- Modify: `packages/pa-monitor/go.sum`

- [ ] **Step 1: Add the dependency**

Run from `packages/pa-monitor/`:

```bash
go get modernc.org/sqlite@latest
```

- [ ] **Step 2: Verify build still succeeds**

```bash
go build ./...
```

Expected: exit 0, no output.

- [ ] **Step 3: Commit**

```bash
git add packages/pa-monitor/go.mod packages/pa-monitor/go.sum
git commit -m "chore(pa-monitor): add modernc.org/sqlite dependency"
```

---

### Task 2: Scaffold the `internal/store` package with interfaces

**Files:**

- Create: `packages/pa-monitor/internal/store/store.go`
- Create: `packages/pa-monitor/internal/store/session_store.go`
- Create: `packages/pa-monitor/internal/store/block_store.go`
- Create: `packages/pa-monitor/internal/store/week_store.go`
- Create: `packages/pa-monitor/internal/store/contribution_store.go`
- Create: `packages/pa-monitor/internal/store/toggle_store.go`
- Create: `packages/pa-monitor/internal/store/nudge_store.go`

- [ ] **Step 1: Write `store.go`**

```go
// Package store defines the persistence interfaces for pa-monitor's daemon
// state. SQLite is one implementation; in-memory shims for tests are another.
//
// All interfaces are read-only or write-only by convention — a single
// WriteService goroutine owns all mutations to serialise concurrent
// callers without an explicit mutex.
package store

import (
	"context"
	"time"
)

// Filter is the active/all distinction enforced by SessionStore.List.
type Filter int

const (
	// FilterActive: pid IS NOT NULL AND in active block.
	FilterActive Filter = iota
	// FilterAll: pid IS NOT NULL OR in active block.
	FilterAll
)

// FreshnessWindow describes the per-entity freshness gate.
// Queries filter rows whose last_processed_at is older than the window.
type FreshnessWindow struct {
	Sessions time.Duration
	Blocks   time.Duration
	Weeks    time.Duration
}

// DefaultFreshness returns the 12x-poll-interval defaults from the spec.
func DefaultFreshness() FreshnessWindow {
	return FreshnessWindow{
		Sessions: 60 * time.Second,
		Blocks:   12 * time.Minute,
		Weeks:    12 * time.Minute,
	}
}
```

- [ ] **Step 2: Write `session_store.go`**

```go
package store

import (
	"context"
	"time"
)

// Session is the persisted snapshot of one Claude Code session.
// Mirrors the spec's `sessions` table 1-to-1 (except surrogate id, which
// is internal to the SQLite impl).
type Session struct {
	SessionID            string
	PID                  *int  // nil when process is dead
	CommandHash          string
	Cwd                  string
	Name                 string
	Kind                 string
	Entrypoint           string
	Model                string
	TerminalHost         string
	Branch               string
	Status               string
	FirstPrompt          string
	Labels               map[string]string
	TranscriptMTime      time.Time
	StartedAt            time.Time
	ContextTokens        uint64
	SessionTokens        uint64
	SubagentCount        uint32
	SubshellCount        uint32
	BurnRateShort        float64
	BurnRateLong         float64
	CostUSD              float64
	AwaitingInput        bool
	LastErrorKind        string
	LastErrorText        string
	LastErrorAt          time.Time
	LastErrorTerminal    bool
	LastErrorRetryable   bool
	LastProcessedAt      time.Time
	UpdatedAt            time.Time
	CreatedAt            time.Time
	DeletedAt            *time.Time
}

// SessionStore is the persistence interface for sessions.
type SessionStore interface {
	// Upsert inserts or updates by SessionID. CreatedAt is set on insert.
	// LastProcessedAt is always set to now. UpdatedAt is set to now only when
	// other fields actually changed (implementation detail of the impl).
	Upsert(ctx context.Context, s Session) error

	// List returns sessions matching the filter, plus the freshness window
	// gate. activeBlockID is the surrogate id (not the string block_id) used
	// for joining contributions.
	List(ctx context.Context, filter Filter, activeBlockID int64, fresh FreshnessWindow) ([]SessionWithContribution, error)

	// GetByID returns one session by its SessionID. Returns nil if absent
	// (deleted or stale).
	GetByID(ctx context.Context, sessionID string, fresh FreshnessWindow) (*Session, error)

	// MarkDeleted sets deleted_at on sessions whose SessionID is NOT in
	// keepIDs. Called by GC after listing the filesystem.
	MarkDeleted(ctx context.Context, keepIDs []string, now time.Time) error

	// MarkRevived clears deleted_at on sessions whose SessionID IS in
	// reviveIDs. Called by GC when files reappear.
	MarkRevived(ctx context.Context, reviveIDs []string) error

	// HardDelete removes rows soft-deleted before cutoff. Returns count.
	// Cascades to contributions and nudge history.
	HardDelete(ctx context.Context, cutoff time.Time) (int64, error)

	// AllSessionIDs returns every SessionID present (alive or soft-deleted).
	// Used by GC's file-reconciliation step.
	AllSessionIDs(ctx context.Context) ([]string, error)
}

// SessionWithContribution joins a session with its contribution to a
// specific block. block_cost/tokens are zero when there is no contribution.
type SessionWithContribution struct {
	Session
	BlockCostUSD float64
	BlockTokens  uint64
}
```

- [ ] **Step 3: Write `block_store.go`**

```go
package store

import (
	"context"
	"time"
)

// Block is the persisted snapshot of one 5h cost window from ccusage.
type Block struct {
	ID                 int64 // surrogate; assigned on insert
	BlockID            string
	StartedAt          time.Time
	EndedAt            time.Time
	PlanCapUSD         float64
	TotalCostUSD       float64
	TotalTokens        uint64
	RateLimitResetsAt  *time.Time
	CapHitAt           *time.Time
	LastProcessedAt    time.Time
	UpdatedAt          time.Time
	DeletedAt          *time.Time
}

type BlockStore interface {
	Upsert(ctx context.Context, b Block) (int64, error)
	GetActive(ctx context.Context, now time.Time, fresh FreshnessWindow) (*Block, error)
	// MarkOrphansDeleted soft-deletes blocks where NOW() not in [started,ended]
	// AND no contributions reference them. Returns count.
	MarkOrphansDeleted(ctx context.Context, now time.Time) (int64, error)
	// MarkRevived clears deleted_at on blocks that now have contributions.
	MarkRevived(ctx context.Context) (int64, error)
	HardDelete(ctx context.Context, cutoff time.Time) (int64, error)
}
```

- [ ] **Step 4: Write `week_store.go`**

```go
package store

import (
	"context"
	"time"
)

// Week is the persisted snapshot of one weekly cost window.
type Week struct {
	ID              int64
	WeekID          string
	StartedAt       time.Time
	EndedAt         time.Time
	WeekCapUSD      float64
	TotalCostUSD    float64
	CapHitAt        *time.Time
	LastProcessedAt time.Time
	UpdatedAt       time.Time
	DeletedAt       *time.Time
}

type WeekStore interface {
	Upsert(ctx context.Context, w Week) (int64, error)
	GetActive(ctx context.Context, now time.Time, fresh FreshnessWindow) (*Week, error)
	MarkOrphansDeleted(ctx context.Context, now time.Time) (int64, error)
	MarkRevived(ctx context.Context) (int64, error)
	HardDelete(ctx context.Context, cutoff time.Time) (int64, error)
}
```

- [ ] **Step 5: Write `contribution_store.go`**

```go
package store

import (
	"context"
	"time"
)

// Contribution is the per-session contribution to one block or one week.
// SessionID and ParentID are surrogate row IDs from sessions and blocks
// (or weeks); the store's UpsertBlock / UpsertWeek pick which parent table.
type Contribution struct {
	SessionID int64
	ParentID  int64
	CostUSD   float64
	Tokens    uint64
	UpdatedAt time.Time
}

type ContributionStore interface {
	UpsertBlock(ctx context.Context, c Contribution) error
	UpsertWeek(ctx context.Context, c Contribution) error
}
```

- [ ] **Step 6: Write `toggle_store.go`**

```go
package store

import "context"

// ToggleStore persists boolean daemon-wide toggles.
// Known keys: "caffeinate_on", "auto_resume_enabled".
type ToggleStore interface {
	Get(ctx context.Context, name string) (bool, bool, error) // value, present, err
	Set(ctx context.Context, name string, value bool) error
	All(ctx context.Context) (map[string]bool, error)
}
```

- [ ] **Step 7: Write `nudge_store.go`**

```go
package store

import (
	"context"
	"time"
)

// NudgeEvent is one immutable row from nudge_history.
// Sources is the unsorted set of contributing sources joined from
// nudge_history_sources.
type NudgeEvent struct {
	SessionID         int64
	Text              string
	Result            string  // 'sent' | 'failed' | 'suppressed' | 'escalated'
	ErrorText         string
	CausedByErrorAt   *time.Time
	Escalated         bool
	FiredAt           time.Time
	Sources           []string
}

type NudgeStore interface {
	// Record inserts a nudge_history row plus its sources rows in one tx.
	Record(ctx context.Context, ev NudgeEvent) error

	// LatestForSession returns the most recent NudgeEvent for the session,
	// or nil if absent.
	LatestForSession(ctx context.Context, sessionID int64) (*NudgeEvent, error)

	// LatestForSessionWithSource returns the most recent NudgeEvent for the
	// session that carries the given source. Used for disrupt-cooldown checks.
	LatestForSessionWithSource(ctx context.Context, sessionID int64, source string) (*NudgeEvent, error)
}
```

- [ ] **Step 8: Verify build**

```bash
go build ./...
```

Expected: exit 0.

- [ ] **Step 9: Commit**

```bash
git add packages/pa-monitor/internal/store/
git commit -m "feat(pa-monitor/store): scaffold persistence interfaces + types"
```

---

## Phase 1 — DB Foundation

### Task 3: SQLite open helper

**Files:**

- Create: `packages/pa-monitor/internal/store/sqlite/sqlite.go`
- Create: `packages/pa-monitor/internal/store/sqlite/sqlite_test.go`

- [ ] **Step 1: Write the test**

```go
package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpen_WALModeAndForeignKeys(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	var journal string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want %q", journal, "wal")
	}

	var fk int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

func TestOpen_BusyTimeout(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var ms int
	if err := db.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&ms); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if ms < 1000 {
		t.Errorf("busy_timeout = %d ms, want >= 1000", ms)
	}
}
```

- [ ] **Step 2: Run the test to confirm it fails**

```bash
go test ./internal/store/sqlite/ -run TestOpen -v
```

Expected: FAIL — `Open` not defined.

- [ ] **Step 3: Write `sqlite.go`**

```go
// Package sqlite is the SQLite implementation of the store interfaces.
// All connections open with WAL journal mode + foreign keys on so the
// schema's invariants (cascade deletes) actually fire.
package sqlite

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Open returns a *sql.DB pointing at dbPath, with WAL mode, foreign keys,
// and a 5-second busy timeout. The parent directory is created if missing.
//
// dbPath of ":memory:" yields an in-memory DB (used by tests). For
// in-memory the directory creation step is skipped.
func Open(dbPath string) (*sql.DB, error) {
	if dbPath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
			return nil, fmt.Errorf("mkdir db parent: %w", err)
		}
	}

	// modernc.org/sqlite honours PRAGMAs passed as DSN query params via _pragma.
	dsn := dbPath + "?" + url.Values{
		"_pragma": []string{
			"journal_mode(WAL)",
			"foreign_keys(ON)",
			"busy_timeout(5000)",
			"synchronous(NORMAL)",
		},
	}.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return db, nil
}
```

- [ ] **Step 4: Run test to verify pass**

```bash
go test ./internal/store/sqlite/ -run TestOpen -v
```

Expected: PASS for both tests.

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/store/sqlite/sqlite.go packages/pa-monitor/internal/store/sqlite/sqlite_test.go
git commit -m "feat(pa-monitor/store/sqlite): Open helper with WAL + FK + busy_timeout"
```

---

### Task 4: Migration infrastructure + initial schema

**Files:**

- Create: `packages/pa-monitor/internal/store/sqlite/migrations.go`
- Create: `packages/pa-monitor/internal/store/sqlite/migrations_test.go`
- Create: `packages/pa-monitor/internal/store/sqlite/migrations/001_initial.sql`

- [ ] **Step 1: Write the migration SQL**

Create `packages/pa-monitor/internal/store/sqlite/migrations/001_initial.sql`:

```sql
-- pa-monitor schema, migration 001 (initial).
-- All timestamps stored as TEXT in RFC3339 UTC for human readability.

CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL UNIQUE,
    pid INTEGER,
    command_hash TEXT NOT NULL DEFAULT '',
    cwd TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT '',
    entrypoint TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    terminal_host TEXT NOT NULL DEFAULT '',
    branch TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    first_prompt TEXT NOT NULL DEFAULT '',
    labels TEXT NOT NULL DEFAULT '{}',
    transcript_mtime TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL DEFAULT '',
    context_tokens INTEGER NOT NULL DEFAULT 0,
    session_tokens INTEGER NOT NULL DEFAULT 0,
    subagent_count INTEGER NOT NULL DEFAULT 0,
    subshell_count INTEGER NOT NULL DEFAULT 0,
    burn_rate_short REAL NOT NULL DEFAULT 0,
    burn_rate_long REAL NOT NULL DEFAULT 0,
    cost_usd REAL NOT NULL DEFAULT 0,
    awaiting_input INTEGER NOT NULL DEFAULT 0,
    last_error_kind TEXT NOT NULL DEFAULT '',
    last_error_text TEXT NOT NULL DEFAULT '',
    last_error_at TEXT NOT NULL DEFAULT '',
    last_error_terminal INTEGER NOT NULL DEFAULT 0,
    last_error_retryable INTEGER NOT NULL DEFAULT 0,
    last_processed_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    deleted_at TEXT
);

CREATE INDEX idx_sessions_session_id ON sessions(session_id);
CREATE INDEX idx_sessions_freshness ON sessions(deleted_at, last_processed_at);

CREATE TABLE IF NOT EXISTS blocks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    block_id TEXT NOT NULL UNIQUE,
    started_at TEXT NOT NULL,
    ended_at TEXT NOT NULL,
    plan_cap_usd REAL NOT NULL DEFAULT 0,
    total_cost_usd REAL NOT NULL DEFAULT 0,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    rate_limit_resets_at TEXT,
    cap_hit_at TEXT,
    last_processed_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    deleted_at TEXT
);

CREATE INDEX idx_blocks_active ON blocks(deleted_at, started_at, ended_at);

CREATE TABLE IF NOT EXISTS weeks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    week_id TEXT NOT NULL UNIQUE,
    started_at TEXT NOT NULL,
    ended_at TEXT NOT NULL,
    week_cap_usd REAL NOT NULL DEFAULT 0,
    total_cost_usd REAL NOT NULL DEFAULT 0,
    cap_hit_at TEXT,
    last_processed_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    deleted_at TEXT
);

CREATE INDEX idx_weeks_active ON weeks(deleted_at, started_at, ended_at);

CREATE TABLE IF NOT EXISTS session_block_contributions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    block_id INTEGER NOT NULL REFERENCES blocks(id) ON DELETE CASCADE,
    cost_usd REAL NOT NULL DEFAULT 0,
    tokens INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL,
    UNIQUE(session_id, block_id)
);

CREATE INDEX idx_sbc_block ON session_block_contributions(block_id);

CREATE TABLE IF NOT EXISTS session_week_contributions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    week_id INTEGER NOT NULL REFERENCES weeks(id) ON DELETE CASCADE,
    cost_usd REAL NOT NULL DEFAULT 0,
    tokens INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL,
    UNIQUE(session_id, week_id)
);

CREATE INDEX idx_swc_week ON session_week_contributions(week_id);

CREATE TABLE IF NOT EXISTS system_toggles (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    value INTEGER NOT NULL,
    updated_at TEXT NOT NULL,
    deleted_at TEXT
);

CREATE TABLE IF NOT EXISTS nudge_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    text TEXT NOT NULL,
    result TEXT NOT NULL,
    error_text TEXT NOT NULL DEFAULT '',
    caused_by_error_at TEXT,
    escalated INTEGER NOT NULL DEFAULT 0,
    fired_at TEXT NOT NULL
);

CREATE INDEX idx_nudge_history_session_fired ON nudge_history(session_id, fired_at DESC);

CREATE TABLE IF NOT EXISTS nudge_history_sources (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    nudge_history_id INTEGER NOT NULL REFERENCES nudge_history(id) ON DELETE CASCADE,
    source TEXT NOT NULL,
    UNIQUE(nudge_history_id, source)
);

CREATE INDEX idx_nhs_source ON nudge_history_sources(source, nudge_history_id);
```

- [ ] **Step 2: Write the migration runner test**

Create `packages/pa-monitor/internal/store/sqlite/migrations_test.go`:

```go
package sqlite

import (
	"context"
	"testing"
)

func TestMigrate_FreshDB_CreatesAllTables(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	want := []string{
		"sessions", "blocks", "weeks",
		"session_block_contributions", "session_week_contributions",
		"system_toggles", "nudge_history", "nudge_history_sources",
		"schema_migrations",
	}
	for _, table := range want {
		var n int
		err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&n)
		if err != nil {
			t.Errorf("query for %s: %v", table, err)
			continue
		}
		if n != 1 {
			t.Errorf("table %s: count=%d, want 1", table, n)
		}
	}

	var version int
	if err := db.QueryRowContext(context.Background(),
		"SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatalf("max version: %v", err)
	}
	if version != 1 {
		t.Errorf("schema_migrations max version = %d, want 1", version)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate first: %v", err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate second: %v", err)
	}
	var count int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("schema_migrations rows = %d, want 1 (idempotent)", count)
	}
}
```

- [ ] **Step 3: Run the test to confirm it fails**

```bash
go test ./internal/store/sqlite/ -run TestMigrate -v
```

Expected: FAIL — `Migrate` not defined.

- [ ] **Step 4: Write the migration runner**

Create `packages/pa-monitor/internal/store/sqlite/migrations.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migrate applies every embedded migration not already recorded in
// schema_migrations. Idempotent — repeated calls are no-ops once the
// latest version is applied.
func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := map[int]bool{}
	rows, err := db.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("select schema_migrations: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	type migration struct {
		version int
		name    string
		body    []byte
	}
	var ms []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		var version int
		if _, err := fmt.Sscanf(e.Name(), "%d_", &version); err != nil {
			return fmt.Errorf("parse version from %q: %w", e.Name(), err)
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return fmt.Errorf("read %s: %w", e.Name(), err)
		}
		ms = append(ms, migration{version: version, name: e.Name(), body: body})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })

	for _, m := range ms {
		if applied[m.version] {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, string(m.body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
			m.version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record %s: %w", m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", m.name, err)
		}
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify pass**

```bash
go test ./internal/store/sqlite/ -v
```

Expected: PASS for all migration + open tests.

- [ ] **Step 6: Commit**

```bash
git add packages/pa-monitor/internal/store/sqlite/migrations.go \
        packages/pa-monitor/internal/store/sqlite/migrations_test.go \
        packages/pa-monitor/internal/store/sqlite/migrations/001_initial.sql
git commit -m "feat(pa-monitor/store/sqlite): migration runner + initial schema"
```

---

## Phase 2 — Store Implementations

### Task 5: SessionStore SQLite implementation

**Files:**

- Create: `packages/pa-monitor/internal/store/sqlite/session_store.go`
- Create: `packages/pa-monitor/internal/store/sqlite/session_store_test.go`
- Create: `packages/pa-monitor/internal/store/sqlite/testhelper_test.go`

- [ ] **Step 1: Write the test helper**

```go
package sqlite

import (
	"context"
	"database/sql"
	"testing"
)

// openTestDB returns a freshly-migrated in-memory DB. Closed at test end.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}
```

- [ ] **Step 2: Write the test**

```go
package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

func TestSessionStore_UpsertThenGet(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	ctx := context.Background()
	now := time.Now().UTC()
	pid := 12345

	s := store.Session{
		SessionID:       "sid-1",
		PID:             &pid,
		CommandHash:     "abc123",
		Cwd:             "/work",
		Name:            "feature-x",
		Model:           "claude-opus-4-7",
		Status:          "working",
		Labels:          map[string]string{"workspace.scope": "personal"},
		LastProcessedAt: now,
		UpdatedAt:       now,
		CreatedAt:       now,
	}
	if err := ss.Upsert(ctx, s); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := ss.GetByID(ctx, "sid-1", store.DefaultFreshness())
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("GetByID returned nil")
	}
	if got.SessionID != "sid-1" {
		t.Errorf("SessionID = %q", got.SessionID)
	}
	if got.PID == nil || *got.PID != pid {
		t.Errorf("PID = %v, want %d", got.PID, pid)
	}
	if got.Labels["workspace.scope"] != "personal" {
		t.Errorf("Labels[workspace.scope] = %q", got.Labels["workspace.scope"])
	}
}

func TestSessionStore_GetByID_StaleReturnsNil(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	ctx := context.Background()
	stale := time.Now().UTC().Add(-10 * time.Minute)

	if err := ss.Upsert(ctx, store.Session{
		SessionID:       "sid-stale",
		LastProcessedAt: stale,
		UpdatedAt:       stale,
		CreatedAt:       stale,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := ss.GetByID(ctx, "sid-stale", store.DefaultFreshness())
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got != nil {
		t.Errorf("GetByID for stale row returned %+v, want nil", got)
	}
}

func TestSessionStore_MarkDeletedThenRevived(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, sid := range []string{"sid-a", "sid-b", "sid-c"} {
		if err := ss.Upsert(ctx, store.Session{
			SessionID:       sid,
			LastProcessedAt: now,
			UpdatedAt:       now,
			CreatedAt:       now,
		}); err != nil {
			t.Fatalf("Upsert %s: %v", sid, err)
		}
	}

	// keep only sid-a → sid-b and sid-c get soft-deleted
	if err := ss.MarkDeleted(ctx, []string{"sid-a"}, now); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}

	ids, err := ss.AllSessionIDs(ctx)
	if err != nil {
		t.Fatalf("AllSessionIDs: %v", err)
	}
	if len(ids) != 3 {
		t.Errorf("AllSessionIDs returned %d, want 3 (soft-delete should not remove rows)", len(ids))
	}

	// revive sid-b
	if err := ss.MarkRevived(ctx, []string{"sid-b"}); err != nil {
		t.Fatalf("MarkRevived: %v", err)
	}

	// sid-b should be retrievable again
	got, err := ss.GetByID(ctx, "sid-b", store.DefaultFreshness())
	if err != nil {
		t.Fatalf("GetByID sid-b: %v", err)
	}
	if got == nil {
		t.Error("sid-b should be retrievable after MarkRevived")
	}

	// sid-c should still be soft-deleted
	got, err = ss.GetByID(ctx, "sid-c", store.DefaultFreshness())
	if err != nil {
		t.Fatalf("GetByID sid-c: %v", err)
	}
	if got != nil {
		t.Error("sid-c should remain soft-deleted")
	}
}

func TestSessionStore_HardDelete(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour)

	if err := ss.Upsert(ctx, store.Session{
		SessionID:       "sid-old",
		LastProcessedAt: old,
		UpdatedAt:       old,
		CreatedAt:       old,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := ss.MarkDeleted(ctx, nil, old); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}

	n, err := ss.HardDelete(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("HardDelete: %v", err)
	}
	if n != 1 {
		t.Errorf("HardDelete deleted %d, want 1", n)
	}

	ids, _ := ss.AllSessionIDs(ctx)
	if len(ids) != 0 {
		t.Errorf("AllSessionIDs after HardDelete = %v, want empty", ids)
	}
}
```

- [ ] **Step 3: Run tests to confirm they fail**

```bash
go test ./internal/store/sqlite/ -run TestSessionStore -v
```

Expected: FAIL — `NewSessionStore` undefined.

- [ ] **Step 4: Write the implementation**

```go
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

// SessionStore is the SQLite implementation of store.SessionStore.
type SessionStore struct {
	db *sql.DB
}

func NewSessionStore(db *sql.DB) *SessionStore { return &SessionStore{db: db} }

var _ store.SessionStore = (*SessionStore)(nil)

func (s *SessionStore) Upsert(ctx context.Context, sess store.Session) error {
	labelsJSON, err := json.Marshal(sess.Labels)
	if err != nil {
		return fmt.Errorf("marshal labels: %w", err)
	}
	now := time.Now().UTC()
	if sess.LastProcessedAt.IsZero() {
		sess.LastProcessedAt = now
	}
	if sess.UpdatedAt.IsZero() {
		sess.UpdatedAt = now
	}
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO sessions (
			session_id, pid, command_hash, cwd, name, kind, entrypoint,
			model, terminal_host, branch, status, first_prompt, labels,
			transcript_mtime, started_at,
			context_tokens, session_tokens, subagent_count, subshell_count,
			burn_rate_short, burn_rate_long, cost_usd, awaiting_input,
			last_error_kind, last_error_text, last_error_at,
			last_error_terminal, last_error_retryable,
			last_processed_at, updated_at, created_at
		) VALUES (
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		)
		ON CONFLICT(session_id) DO UPDATE SET
			pid = excluded.pid,
			command_hash = excluded.command_hash,
			cwd = excluded.cwd,
			name = excluded.name,
			kind = excluded.kind,
			entrypoint = excluded.entrypoint,
			model = excluded.model,
			terminal_host = excluded.terminal_host,
			branch = excluded.branch,
			status = excluded.status,
			first_prompt = excluded.first_prompt,
			labels = excluded.labels,
			transcript_mtime = excluded.transcript_mtime,
			started_at = excluded.started_at,
			context_tokens = excluded.context_tokens,
			session_tokens = excluded.session_tokens,
			subagent_count = excluded.subagent_count,
			subshell_count = excluded.subshell_count,
			burn_rate_short = excluded.burn_rate_short,
			burn_rate_long = excluded.burn_rate_long,
			cost_usd = excluded.cost_usd,
			awaiting_input = excluded.awaiting_input,
			last_error_kind = excluded.last_error_kind,
			last_error_text = excluded.last_error_text,
			last_error_at = excluded.last_error_at,
			last_error_terminal = excluded.last_error_terminal,
			last_error_retryable = excluded.last_error_retryable,
			last_processed_at = excluded.last_processed_at,
			updated_at = excluded.updated_at,
			deleted_at = NULL
	`,
		sess.SessionID, pidPtr(sess.PID), sess.CommandHash, sess.Cwd, sess.Name, sess.Kind, sess.Entrypoint,
		sess.Model, sess.TerminalHost, sess.Branch, sess.Status, sess.FirstPrompt, string(labelsJSON),
		formatTime(sess.TranscriptMTime), formatTime(sess.StartedAt),
		sess.ContextTokens, sess.SessionTokens, sess.SubagentCount, sess.SubshellCount,
		sess.BurnRateShort, sess.BurnRateLong, sess.CostUSD, boolInt(sess.AwaitingInput),
		sess.LastErrorKind, sess.LastErrorText, formatTime(sess.LastErrorAt),
		boolInt(sess.LastErrorTerminal), boolInt(sess.LastErrorRetryable),
		formatTime(sess.LastProcessedAt), formatTime(sess.UpdatedAt), formatTime(sess.CreatedAt),
	)
	return err
}

func (s *SessionStore) GetByID(ctx context.Context, sessionID string, fresh store.FreshnessWindow) (*store.Session, error) {
	cutoff := time.Now().UTC().Add(-fresh.Sessions)
	row := s.db.QueryRowContext(ctx, sessionSelectColumns+`
		FROM sessions
		WHERE session_id = ?
		  AND deleted_at IS NULL
		  AND last_processed_at > ?
	`, sessionID, formatTime(cutoff))
	sess, err := scanSession(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *SessionStore) List(ctx context.Context, filter store.Filter, activeBlockID int64, fresh store.FreshnessWindow) ([]store.SessionWithContribution, error) {
	cutoff := time.Now().UTC().Add(-fresh.Sessions)

	var query string
	switch filter {
	case store.FilterActive:
		query = sessionSelectColumns + `,
			COALESCE(c.cost_usd, 0), COALESCE(c.tokens, 0)
			FROM sessions s
			INNER JOIN session_block_contributions c ON c.session_id = s.id
			WHERE s.deleted_at IS NULL
			  AND s.last_processed_at > ?
			  AND s.pid IS NOT NULL
			  AND c.block_id = ?
		`
	case store.FilterAll:
		query = sessionSelectColumns + `,
			COALESCE(c.cost_usd, 0), COALESCE(c.tokens, 0)
			FROM sessions s
			LEFT JOIN session_block_contributions c ON c.session_id = s.id AND c.block_id = ?
			WHERE s.deleted_at IS NULL
			  AND s.last_processed_at > ?
			  AND (s.pid IS NOT NULL OR c.id IS NOT NULL)
		`
	default:
		return nil, fmt.Errorf("unknown filter: %d", filter)
	}

	var rows *sql.Rows
	var err error
	if filter == store.FilterActive {
		rows, err = s.db.QueryContext(ctx, query, formatTime(cutoff), activeBlockID)
	} else {
		rows, err = s.db.QueryContext(ctx, query, activeBlockID, formatTime(cutoff))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []store.SessionWithContribution
	for rows.Next() {
		var sc store.SessionWithContribution
		if err := scanSessionInto(rows, &sc.Session, &sc.BlockCostUSD, &sc.BlockTokens); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *SessionStore) MarkDeleted(ctx context.Context, keepIDs []string, now time.Time) error {
	if len(keepIDs) == 0 {
		_, err := s.db.ExecContext(ctx,
			"UPDATE sessions SET deleted_at = ? WHERE deleted_at IS NULL",
			formatTime(now))
		return err
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keepIDs)), ",")
	args := []any{formatTime(now)}
	for _, id := range keepIDs {
		args = append(args, id)
	}
	_, err := s.db.ExecContext(ctx,
		"UPDATE sessions SET deleted_at = ? WHERE deleted_at IS NULL AND session_id NOT IN ("+placeholders+")",
		args...)
	return err
}

func (s *SessionStore) MarkRevived(ctx context.Context, reviveIDs []string) error {
	if len(reviveIDs) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(reviveIDs)), ",")
	args := []any{}
	for _, id := range reviveIDs {
		args = append(args, id)
	}
	_, err := s.db.ExecContext(ctx,
		"UPDATE sessions SET deleted_at = NULL WHERE deleted_at IS NOT NULL AND session_id IN ("+placeholders+")",
		args...)
	return err
}

func (s *SessionStore) HardDelete(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		"DELETE FROM sessions WHERE deleted_at IS NOT NULL AND deleted_at < ?",
		formatTime(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *SessionStore) AllSessionIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT session_id FROM sessions")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// --- helpers ---

const sessionSelectColumns = `SELECT
	s.session_id, s.pid, s.command_hash, s.cwd, s.name, s.kind, s.entrypoint,
	s.model, s.terminal_host, s.branch, s.status, s.first_prompt, s.labels,
	s.transcript_mtime, s.started_at,
	s.context_tokens, s.session_tokens, s.subagent_count, s.subshell_count,
	s.burn_rate_short, s.burn_rate_long, s.cost_usd, s.awaiting_input,
	s.last_error_kind, s.last_error_text, s.last_error_at,
	s.last_error_terminal, s.last_error_retryable,
	s.last_processed_at, s.updated_at, s.created_at, s.deleted_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSession(r rowScanner) (store.Session, error) {
	var sess store.Session
	return sess, scanSessionInto(r, &sess, nil, nil)
}

func scanSessionInto(r rowScanner, sess *store.Session, extraCost *float64, extraTokens *uint64) error {
	var (
		pid                  sql.NullInt64
		labelsRaw            string
		transcriptMTime      string
		startedAt            string
		lastErrorAt          string
		lastProcessedAt      string
		updatedAt            string
		createdAt            string
		deletedAt            sql.NullString
		awaitingInput        int
		lastErrorTerminal    int
		lastErrorRetryable   int
	)
	dest := []any{
		&sess.SessionID, &pid, &sess.CommandHash, &sess.Cwd, &sess.Name, &sess.Kind, &sess.Entrypoint,
		&sess.Model, &sess.TerminalHost, &sess.Branch, &sess.Status, &sess.FirstPrompt, &labelsRaw,
		&transcriptMTime, &startedAt,
		&sess.ContextTokens, &sess.SessionTokens, &sess.SubagentCount, &sess.SubshellCount,
		&sess.BurnRateShort, &sess.BurnRateLong, &sess.CostUSD, &awaitingInput,
		&sess.LastErrorKind, &sess.LastErrorText, &lastErrorAt,
		&lastErrorTerminal, &lastErrorRetryable,
		&lastProcessedAt, &updatedAt, &createdAt, &deletedAt,
	}
	if extraCost != nil {
		dest = append(dest, extraCost, extraTokens)
	}
	if err := r.Scan(dest...); err != nil {
		return err
	}
	if pid.Valid {
		p := int(pid.Int64)
		sess.PID = &p
	}
	sess.AwaitingInput = awaitingInput != 0
	sess.LastErrorTerminal = lastErrorTerminal != 0
	sess.LastErrorRetryable = lastErrorRetryable != 0
	if labelsRaw != "" {
		_ = json.Unmarshal([]byte(labelsRaw), &sess.Labels)
	}
	sess.TranscriptMTime = parseTime(transcriptMTime)
	sess.StartedAt = parseTime(startedAt)
	sess.LastErrorAt = parseTime(lastErrorAt)
	sess.LastProcessedAt = parseTime(lastProcessedAt)
	sess.UpdatedAt = parseTime(updatedAt)
	sess.CreatedAt = parseTime(createdAt)
	if deletedAt.Valid {
		t := parseTime(deletedAt.String)
		sess.DeletedAt = &t
	}
	return nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func pidPtr(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/store/sqlite/ -run TestSessionStore -v
```

Expected: PASS for all four tests.

- [ ] **Step 6: Commit**

```bash
git add packages/pa-monitor/internal/store/sqlite/session_store.go \
        packages/pa-monitor/internal/store/sqlite/session_store_test.go \
        packages/pa-monitor/internal/store/sqlite/testhelper_test.go
git commit -m "feat(pa-monitor/store/sqlite): SessionStore impl with upsert, list, soft-delete"
```

---

### Task 6: BlockStore SQLite implementation

**Files:**

- Create: `packages/pa-monitor/internal/store/sqlite/block_store.go`
- Create: `packages/pa-monitor/internal/store/sqlite/block_store_test.go`

- [ ] **Step 1: Write the test**

```go
package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

func TestBlockStore_UpsertReturnsID(t *testing.T) {
	db := openTestDB(t)
	bs := NewBlockStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	id, err := bs.Upsert(ctx, store.Block{
		BlockID:         "2026-06-01T15Z",
		StartedAt:       now,
		EndedAt:         now.Add(5 * time.Hour),
		PlanCapUSD:      100,
		TotalCostUSD:    25.50,
		LastProcessedAt: now,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if id <= 0 {
		t.Errorf("Upsert returned id %d, want > 0", id)
	}

	// Idempotent upsert returns the same id.
	id2, err := bs.Upsert(ctx, store.Block{
		BlockID:         "2026-06-01T15Z",
		StartedAt:       now,
		EndedAt:         now.Add(5 * time.Hour),
		PlanCapUSD:      100,
		TotalCostUSD:    30.00,
		LastProcessedAt: now,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatalf("Upsert (second): %v", err)
	}
	if id2 != id {
		t.Errorf("id changed across upserts: %d -> %d", id, id2)
	}
}

func TestBlockStore_GetActive_TimeWindow(t *testing.T) {
	db := openTestDB(t)
	bs := NewBlockStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	// past block
	_, err := bs.Upsert(ctx, store.Block{
		BlockID:         "past",
		StartedAt:       now.Add(-10 * time.Hour),
		EndedAt:         now.Add(-5 * time.Hour),
		LastProcessedAt: now,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatalf("Upsert past: %v", err)
	}
	// current block
	id, err := bs.Upsert(ctx, store.Block{
		BlockID:         "current",
		StartedAt:       now.Add(-1 * time.Hour),
		EndedAt:         now.Add(4 * time.Hour),
		LastProcessedAt: now,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatalf("Upsert current: %v", err)
	}

	got, err := bs.GetActive(ctx, now, store.DefaultFreshness())
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}
	if got == nil {
		t.Fatal("GetActive returned nil")
	}
	if got.ID != id || got.BlockID != "current" {
		t.Errorf("GetActive returned %+v, want current id=%d", got, id)
	}
}

func TestBlockStore_GetActive_StaleReturnsNil(t *testing.T) {
	db := openTestDB(t)
	bs := NewBlockStore(db)
	ctx := context.Background()
	now := time.Now().UTC()
	stale := now.Add(-30 * time.Minute)

	if _, err := bs.Upsert(ctx, store.Block{
		BlockID:         "stale",
		StartedAt:       now.Add(-1 * time.Hour),
		EndedAt:         now.Add(4 * time.Hour),
		LastProcessedAt: stale,
		UpdatedAt:       stale,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := bs.GetActive(ctx, now, store.DefaultFreshness())
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}
	if got != nil {
		t.Errorf("GetActive returned %+v for stale row, want nil", got)
	}
}
```

- [ ] **Step 2: Confirm failure**

```bash
go test ./internal/store/sqlite/ -run TestBlockStore -v
```

Expected: FAIL — `NewBlockStore` undefined.

- [ ] **Step 3: Write implementation**

```go
package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

type BlockStore struct{ db *sql.DB }

func NewBlockStore(db *sql.DB) *BlockStore { return &BlockStore{db: db} }

var _ store.BlockStore = (*BlockStore)(nil)

func (s *BlockStore) Upsert(ctx context.Context, b store.Block) (int64, error) {
	now := time.Now().UTC()
	if b.LastProcessedAt.IsZero() {
		b.LastProcessedAt = now
	}
	if b.UpdatedAt.IsZero() {
		b.UpdatedAt = now
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO blocks (
			block_id, started_at, ended_at, plan_cap_usd, total_cost_usd, total_tokens,
			rate_limit_resets_at, cap_hit_at, last_processed_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(block_id) DO UPDATE SET
			started_at = excluded.started_at,
			ended_at = excluded.ended_at,
			plan_cap_usd = excluded.plan_cap_usd,
			total_cost_usd = excluded.total_cost_usd,
			total_tokens = excluded.total_tokens,
			rate_limit_resets_at = excluded.rate_limit_resets_at,
			cap_hit_at = COALESCE(blocks.cap_hit_at, excluded.cap_hit_at),
			last_processed_at = excluded.last_processed_at,
			updated_at = excluded.updated_at,
			deleted_at = NULL
	`,
		b.BlockID, formatTime(b.StartedAt), formatTime(b.EndedAt),
		b.PlanCapUSD, b.TotalCostUSD, b.TotalTokens,
		timePtr(b.RateLimitResetsAt), timePtr(b.CapHitAt),
		formatTime(b.LastProcessedAt), formatTime(b.UpdatedAt),
	)
	if err != nil {
		return 0, err
	}
	// SQLite ON CONFLICT returns LastInsertId of the matched row on update too in modernc.
	if id, err := res.LastInsertId(); err == nil && id > 0 {
		// Need to map back to the actual row id even on conflict; safer to look up.
	}
	var id int64
	if err := s.db.QueryRowContext(ctx, "SELECT id FROM blocks WHERE block_id = ?", b.BlockID).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *BlockStore) GetActive(ctx context.Context, now time.Time, fresh store.FreshnessWindow) (*store.Block, error) {
	cutoff := now.Add(-fresh.Blocks)
	row := s.db.QueryRowContext(ctx, blockSelectColumns+`
		FROM blocks
		WHERE deleted_at IS NULL
		  AND last_processed_at > ?
		  AND started_at <= ?
		  AND ended_at >= ?
		ORDER BY started_at DESC LIMIT 1
	`, formatTime(cutoff), formatTime(now), formatTime(now))
	return scanBlock(row)
}

func (s *BlockStore) MarkOrphansDeleted(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE blocks SET deleted_at = ?
		WHERE deleted_at IS NULL
		  AND NOT (started_at <= ? AND ended_at >= ?)
		  AND id NOT IN (SELECT DISTINCT block_id FROM session_block_contributions)
	`, formatTime(now), formatTime(now), formatTime(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *BlockStore) MarkRevived(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE blocks SET deleted_at = NULL
		WHERE deleted_at IS NOT NULL
		  AND id IN (SELECT DISTINCT block_id FROM session_block_contributions)
	`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *BlockStore) HardDelete(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		"DELETE FROM blocks WHERE deleted_at IS NOT NULL AND deleted_at < ?",
		formatTime(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const blockSelectColumns = `SELECT
	id, block_id, started_at, ended_at, plan_cap_usd, total_cost_usd, total_tokens,
	rate_limit_resets_at, cap_hit_at, last_processed_at, updated_at, deleted_at`

func scanBlock(r rowScanner) (*store.Block, error) {
	var (
		b                store.Block
		rateResetsAt     sql.NullString
		capHitAt         sql.NullString
		deletedAt        sql.NullString
		startedAt, ended string
		processed, upd   string
	)
	err := r.Scan(
		&b.ID, &b.BlockID, &startedAt, &ended, &b.PlanCapUSD, &b.TotalCostUSD, &b.TotalTokens,
		&rateResetsAt, &capHitAt, &processed, &upd, &deletedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b.StartedAt = parseTime(startedAt)
	b.EndedAt = parseTime(ended)
	b.LastProcessedAt = parseTime(processed)
	b.UpdatedAt = parseTime(upd)
	if rateResetsAt.Valid {
		t := parseTime(rateResetsAt.String)
		b.RateLimitResetsAt = &t
	}
	if capHitAt.Valid {
		t := parseTime(capHitAt.String)
		b.CapHitAt = &t
	}
	if deletedAt.Valid {
		t := parseTime(deletedAt.String)
		b.DeletedAt = &t
	}
	return &b, nil
}

func timePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/store/sqlite/ -run TestBlockStore -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/store/sqlite/block_store.go \
        packages/pa-monitor/internal/store/sqlite/block_store_test.go
git commit -m "feat(pa-monitor/store/sqlite): BlockStore impl"
```

---

### Task 7: WeekStore SQLite implementation

**Files:**

- Create: `packages/pa-monitor/internal/store/sqlite/week_store.go`
- Create: `packages/pa-monitor/internal/store/sqlite/week_store_test.go`

- [ ] **Step 1: Write the test**

```go
package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

func TestWeekStore_UpsertReturnsID(t *testing.T) {
	db := openTestDB(t)
	ws := NewWeekStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	id, err := ws.Upsert(ctx, store.Week{
		WeekID:          "2026-W22",
		StartedAt:       now,
		EndedAt:         now.Add(7 * 24 * time.Hour),
		WeekCapUSD:      1000,
		TotalCostUSD:    50,
		LastProcessedAt: now,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if id <= 0 {
		t.Errorf("Upsert returned id %d, want > 0", id)
	}

	id2, err := ws.Upsert(ctx, store.Week{
		WeekID:          "2026-W22",
		StartedAt:       now,
		EndedAt:         now.Add(7 * 24 * time.Hour),
		WeekCapUSD:      1000,
		TotalCostUSD:    60,
		LastProcessedAt: now,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatalf("Upsert (second): %v", err)
	}
	if id2 != id {
		t.Errorf("id changed across upserts: %d -> %d", id, id2)
	}
}

func TestWeekStore_GetActive_TimeWindow(t *testing.T) {
	db := openTestDB(t)
	ws := NewWeekStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	_, _ = ws.Upsert(ctx, store.Week{
		WeekID:          "past",
		StartedAt:       now.Add(-14 * 24 * time.Hour),
		EndedAt:         now.Add(-7 * 24 * time.Hour),
		LastProcessedAt: now,
		UpdatedAt:       now,
	})
	id, err := ws.Upsert(ctx, store.Week{
		WeekID:          "current",
		StartedAt:       now.Add(-2 * 24 * time.Hour),
		EndedAt:         now.Add(5 * 24 * time.Hour),
		LastProcessedAt: now,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := ws.GetActive(ctx, now, store.DefaultFreshness())
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}
	if got == nil || got.ID != id || got.WeekID != "current" {
		t.Errorf("GetActive = %+v, want current id=%d", got, id)
	}
}

func TestWeekStore_GetActive_StaleReturnsNil(t *testing.T) {
	db := openTestDB(t)
	ws := NewWeekStore(db)
	ctx := context.Background()
	now := time.Now().UTC()
	stale := now.Add(-30 * time.Minute)

	_, _ = ws.Upsert(ctx, store.Week{
		WeekID:          "stale",
		StartedAt:       now.Add(-2 * 24 * time.Hour),
		EndedAt:         now.Add(5 * 24 * time.Hour),
		LastProcessedAt: stale,
		UpdatedAt:       stale,
	})

	got, err := ws.GetActive(ctx, now, store.DefaultFreshness())
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}
	if got != nil {
		t.Errorf("GetActive returned %+v for stale row, want nil", got)
	}
}
```

- [ ] **Step 2: Confirm failure**

```bash
go test ./internal/store/sqlite/ -run TestWeekStore -v
```

Expected: FAIL — `NewWeekStore` undefined.

- [ ] **Step 3: Write implementation**

```go
package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

type WeekStore struct{ db *sql.DB }

func NewWeekStore(db *sql.DB) *WeekStore { return &WeekStore{db: db} }

var _ store.WeekStore = (*WeekStore)(nil)

func (s *WeekStore) Upsert(ctx context.Context, w store.Week) (int64, error) {
	now := time.Now().UTC()
	if w.LastProcessedAt.IsZero() {
		w.LastProcessedAt = now
	}
	if w.UpdatedAt.IsZero() {
		w.UpdatedAt = now
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO weeks (
			week_id, started_at, ended_at, week_cap_usd, total_cost_usd,
			cap_hit_at, last_processed_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(week_id) DO UPDATE SET
			started_at = excluded.started_at,
			ended_at = excluded.ended_at,
			week_cap_usd = excluded.week_cap_usd,
			total_cost_usd = excluded.total_cost_usd,
			cap_hit_at = COALESCE(weeks.cap_hit_at, excluded.cap_hit_at),
			last_processed_at = excluded.last_processed_at,
			updated_at = excluded.updated_at,
			deleted_at = NULL
	`, w.WeekID, formatTime(w.StartedAt), formatTime(w.EndedAt),
		w.WeekCapUSD, w.TotalCostUSD, timePtr(w.CapHitAt),
		formatTime(w.LastProcessedAt), formatTime(w.UpdatedAt))
	if err != nil {
		return 0, err
	}
	var id int64
	if err := s.db.QueryRowContext(ctx, "SELECT id FROM weeks WHERE week_id = ?", w.WeekID).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *WeekStore) GetActive(ctx context.Context, now time.Time, fresh store.FreshnessWindow) (*store.Week, error) {
	cutoff := now.Add(-fresh.Weeks)
	row := s.db.QueryRowContext(ctx, weekSelectColumns+`
		FROM weeks
		WHERE deleted_at IS NULL
		  AND last_processed_at > ?
		  AND started_at <= ?
		  AND ended_at >= ?
		ORDER BY started_at DESC LIMIT 1
	`, formatTime(cutoff), formatTime(now), formatTime(now))
	return scanWeek(row)
}

func (s *WeekStore) MarkOrphansDeleted(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE weeks SET deleted_at = ?
		WHERE deleted_at IS NULL
		  AND NOT (started_at <= ? AND ended_at >= ?)
		  AND id NOT IN (SELECT DISTINCT week_id FROM session_week_contributions)
	`, formatTime(now), formatTime(now), formatTime(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *WeekStore) MarkRevived(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE weeks SET deleted_at = NULL
		WHERE deleted_at IS NOT NULL
		  AND id IN (SELECT DISTINCT week_id FROM session_week_contributions)
	`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *WeekStore) HardDelete(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		"DELETE FROM weeks WHERE deleted_at IS NOT NULL AND deleted_at < ?",
		formatTime(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const weekSelectColumns = `SELECT
	id, week_id, started_at, ended_at, week_cap_usd, total_cost_usd,
	cap_hit_at, last_processed_at, updated_at, deleted_at`

func scanWeek(r rowScanner) (*store.Week, error) {
	var (
		w                store.Week
		capHitAt         sql.NullString
		deletedAt        sql.NullString
		startedAt, ended string
		processed, upd   string
	)
	err := r.Scan(
		&w.ID, &w.WeekID, &startedAt, &ended, &w.WeekCapUSD, &w.TotalCostUSD,
		&capHitAt, &processed, &upd, &deletedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	w.StartedAt = parseTime(startedAt)
	w.EndedAt = parseTime(ended)
	w.LastProcessedAt = parseTime(processed)
	w.UpdatedAt = parseTime(upd)
	if capHitAt.Valid {
		t := parseTime(capHitAt.String)
		w.CapHitAt = &t
	}
	if deletedAt.Valid {
		t := parseTime(deletedAt.String)
		w.DeletedAt = &t
	}
	return &w, nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/store/sqlite/ -run TestWeekStore -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/store/sqlite/week_store.go \
        packages/pa-monitor/internal/store/sqlite/week_store_test.go
git commit -m "feat(pa-monitor/store/sqlite): WeekStore impl"
```

---

### Task 8: ContributionStore SQLite implementation

**Files:**

- Create: `packages/pa-monitor/internal/store/sqlite/contribution_store.go`
- Create: `packages/pa-monitor/internal/store/sqlite/contribution_store_test.go`

- [ ] **Step 1: Write the test**

```go
package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

func TestContributionStore_UpsertBlock(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	bs := NewBlockStore(db)
	cs := NewContributionStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := ss.Upsert(ctx, store.Session{SessionID: "sid-1", LastProcessedAt: now, UpdatedAt: now, CreatedAt: now}); err != nil {
		t.Fatalf("session upsert: %v", err)
	}
	var sessionID int64
	if err := db.QueryRowContext(ctx, "SELECT id FROM sessions WHERE session_id = 'sid-1'").Scan(&sessionID); err != nil {
		t.Fatalf("session id lookup: %v", err)
	}
	blockID, err := bs.Upsert(ctx, store.Block{BlockID: "blk-1", StartedAt: now, EndedAt: now.Add(time.Hour), LastProcessedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatalf("block upsert: %v", err)
	}

	if err := cs.UpsertBlock(ctx, store.Contribution{SessionID: sessionID, ParentID: blockID, CostUSD: 1.5, Tokens: 100, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertBlock first: %v", err)
	}
	// idempotent — second upsert overwrites
	if err := cs.UpsertBlock(ctx, store.Contribution{SessionID: sessionID, ParentID: blockID, CostUSD: 3.0, Tokens: 200, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertBlock second: %v", err)
	}

	var cost float64
	var tokens uint64
	if err := db.QueryRowContext(ctx,
		"SELECT cost_usd, tokens FROM session_block_contributions WHERE session_id = ? AND block_id = ?",
		sessionID, blockID).Scan(&cost, &tokens); err != nil {
		t.Fatalf("select: %v", err)
	}
	if cost != 3.0 || tokens != 200 {
		t.Errorf("contribution = (%v, %d), want (3.0, 200)", cost, tokens)
	}
}

func TestContributionStore_CascadeOnSessionDelete(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	bs := NewBlockStore(db)
	cs := NewContributionStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := ss.Upsert(ctx, store.Session{SessionID: "sid-1", LastProcessedAt: now, UpdatedAt: now, CreatedAt: now}); err != nil {
		t.Fatalf("session: %v", err)
	}
	var sessionID int64
	_ = db.QueryRowContext(ctx, "SELECT id FROM sessions WHERE session_id = 'sid-1'").Scan(&sessionID)
	blockID, _ := bs.Upsert(ctx, store.Block{BlockID: "blk-1", StartedAt: now, EndedAt: now.Add(time.Hour), LastProcessedAt: now, UpdatedAt: now})
	if err := cs.UpsertBlock(ctx, store.Contribution{SessionID: sessionID, ParentID: blockID, CostUSD: 1, Tokens: 1, UpdatedAt: now}); err != nil {
		t.Fatalf("contrib: %v", err)
	}

	// hard delete session
	if _, err := db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", sessionID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM session_block_contributions").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("contributions remaining after cascade = %d, want 0", n)
	}
}
```

- [ ] **Step 2: Confirm failure**

```bash
go test ./internal/store/sqlite/ -run TestContributionStore -v
```

- [ ] **Step 3: Write implementation**

```go
package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

type ContributionStore struct{ db *sql.DB }

func NewContributionStore(db *sql.DB) *ContributionStore { return &ContributionStore{db: db} }

var _ store.ContributionStore = (*ContributionStore)(nil)

func (s *ContributionStore) UpsertBlock(ctx context.Context, c store.Contribution) error {
	return s.upsert(ctx, c, "session_block_contributions", "block_id")
}

func (s *ContributionStore) UpsertWeek(ctx context.Context, c store.Contribution) error {
	return s.upsert(ctx, c, "session_week_contributions", "week_id")
}

func (s *ContributionStore) upsert(ctx context.Context, c store.Contribution, table, parentCol string) error {
	now := time.Now().UTC()
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = now
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO `+table+` (session_id, `+parentCol+`, cost_usd, tokens, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(session_id, `+parentCol+`) DO UPDATE SET
			cost_usd = excluded.cost_usd,
			tokens = excluded.tokens,
			updated_at = excluded.updated_at
	`, c.SessionID, c.ParentID, c.CostUSD, c.Tokens, formatTime(c.UpdatedAt))
	return err
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/store/sqlite/ -run TestContributionStore -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/store/sqlite/contribution_store.go \
        packages/pa-monitor/internal/store/sqlite/contribution_store_test.go
git commit -m "feat(pa-monitor/store/sqlite): ContributionStore impl + cascade test"
```

---

### Task 9: ToggleStore SQLite implementation

**Files:**

- Create: `packages/pa-monitor/internal/store/sqlite/toggle_store.go`
- Create: `packages/pa-monitor/internal/store/sqlite/toggle_store_test.go`

- [ ] **Step 1: Test**

```go
package sqlite

import (
	"context"
	"testing"
)

func TestToggleStore_GetAfterSet(t *testing.T) {
	db := openTestDB(t)
	ts := NewToggleStore(db)
	ctx := context.Background()

	val, present, err := ts.Get(ctx, "caffeinate_on")
	if err != nil {
		t.Fatalf("Get pre-set: %v", err)
	}
	if present {
		t.Error("Get pre-set: present should be false")
	}

	if err := ts.Set(ctx, "caffeinate_on", true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	val, present, err = ts.Get(ctx, "caffeinate_on")
	if err != nil {
		t.Fatalf("Get post-set: %v", err)
	}
	if !present || !val {
		t.Errorf("Get post-set = (%v, %v), want (true, true)", val, present)
	}

	// Flip
	if err := ts.Set(ctx, "caffeinate_on", false); err != nil {
		t.Fatalf("Set false: %v", err)
	}
	val, _, _ = ts.Get(ctx, "caffeinate_on")
	if val {
		t.Errorf("Get post-flip = true, want false")
	}
}

func TestToggleStore_All(t *testing.T) {
	db := openTestDB(t)
	ts := NewToggleStore(db)
	ctx := context.Background()
	_ = ts.Set(ctx, "caffeinate_on", true)
	_ = ts.Set(ctx, "auto_resume_enabled", false)

	m, err := ts.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(m) != 2 || !m["caffeinate_on"] || m["auto_resume_enabled"] {
		t.Errorf("All = %+v", m)
	}
}
```

- [ ] **Step 2: Confirm failure**

```bash
go test ./internal/store/sqlite/ -run TestToggleStore -v
```

- [ ] **Step 3: Write implementation**

```go
package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

type ToggleStore struct{ db *sql.DB }

func NewToggleStore(db *sql.DB) *ToggleStore { return &ToggleStore{db: db} }

var _ store.ToggleStore = (*ToggleStore)(nil)

func (s *ToggleStore) Get(ctx context.Context, name string) (bool, bool, error) {
	var v int
	err := s.db.QueryRowContext(ctx,
		"SELECT value FROM system_toggles WHERE name = ? AND deleted_at IS NULL",
		name).Scan(&v)
	if err == sql.ErrNoRows {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return v != 0, true, nil
}

func (s *ToggleStore) Set(ctx context.Context, name string, value bool) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO system_toggles (name, value, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			value = excluded.value,
			updated_at = excluded.updated_at,
			deleted_at = NULL
	`, name, boolInt(value), formatTime(time.Now().UTC()))
	return err
}

func (s *ToggleStore) All(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT name, value FROM system_toggles WHERE deleted_at IS NULL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		var v int
		if err := rows.Scan(&name, &v); err != nil {
			return nil, err
		}
		out[name] = v != 0
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run tests** → PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/store/sqlite/toggle_store.go \
        packages/pa-monitor/internal/store/sqlite/toggle_store_test.go
git commit -m "feat(pa-monitor/store/sqlite): ToggleStore impl"
```

---

### Task 10: NudgeStore SQLite implementation

**Files:**

- Create: `packages/pa-monitor/internal/store/sqlite/nudge_store.go`
- Create: `packages/pa-monitor/internal/store/sqlite/nudge_store_test.go`

- [ ] **Step 1: Test**

```go
package sqlite

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

func TestNudgeStore_RecordAndLatest(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	ns := NewNudgeStore(db)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := ss.Upsert(ctx, store.Session{SessionID: "sid-1", LastProcessedAt: now, UpdatedAt: now, CreatedAt: now}); err != nil {
		t.Fatalf("session: %v", err)
	}
	var sessionID int64
	_ = db.QueryRowContext(ctx, "SELECT id FROM sessions WHERE session_id = 'sid-1'").Scan(&sessionID)

	earlier := now.Add(-5 * time.Minute)
	if err := ns.Record(ctx, store.NudgeEvent{
		SessionID: sessionID,
		Text:      "first nudge",
		Result:    "sent",
		FiredAt:   earlier,
		Sources:   []string{"disrupted"},
	}); err != nil {
		t.Fatalf("Record earlier: %v", err)
	}
	if err := ns.Record(ctx, store.NudgeEvent{
		SessionID: sessionID,
		Text:      "second nudge",
		Result:    "sent",
		FiredAt:   now,
		Sources:   []string{"manual", "disrupted"},
	}); err != nil {
		t.Fatalf("Record now: %v", err)
	}

	latest, err := ns.LatestForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("LatestForSession: %v", err)
	}
	if latest == nil {
		t.Fatal("LatestForSession returned nil")
	}
	if latest.Text != "second nudge" {
		t.Errorf("latest.Text = %q, want second nudge", latest.Text)
	}
	sort.Strings(latest.Sources)
	want := []string{"disrupted", "manual"}
	for i, s := range want {
		if i >= len(latest.Sources) || latest.Sources[i] != s {
			t.Errorf("Sources = %v, want %v", latest.Sources, want)
			break
		}
	}

	// LatestForSessionWithSource: "manual" → only the second event has it
	withManual, err := ns.LatestForSessionWithSource(ctx, sessionID, "manual")
	if err != nil {
		t.Fatalf("LatestForSessionWithSource manual: %v", err)
	}
	if withManual == nil || withManual.Text != "second nudge" {
		t.Errorf("LatestForSessionWithSource manual = %+v", withManual)
	}

	// LatestForSessionWithSource: "disrupted" → both have it, returns latest
	withDisrupt, err := ns.LatestForSessionWithSource(ctx, sessionID, "disrupted")
	if err != nil {
		t.Fatalf("LatestForSessionWithSource disrupted: %v", err)
	}
	if withDisrupt == nil || withDisrupt.Text != "second nudge" {
		t.Errorf("LatestForSessionWithSource disrupted = %+v", withDisrupt)
	}
}
```

- [ ] **Step 2: Confirm failure**

```bash
go test ./internal/store/sqlite/ -run TestNudgeStore -v
```

- [ ] **Step 3: Write implementation**

```go
package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

type NudgeStore struct{ db *sql.DB }

func NewNudgeStore(db *sql.DB) *NudgeStore { return &NudgeStore{db: db} }

var _ store.NudgeStore = (*NudgeStore)(nil)

func (s *NudgeStore) Record(ctx context.Context, ev store.NudgeEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	res, err := tx.ExecContext(ctx, `
		INSERT INTO nudge_history (
			session_id, text, result, error_text,
			caused_by_error_at, escalated, fired_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, ev.SessionID, ev.Text, ev.Result, ev.ErrorText,
		timePtr(ev.CausedByErrorAt), boolInt(ev.Escalated), formatTime(ev.FiredAt))
	if err != nil {
		return err
	}
	historyID, err := res.LastInsertId()
	if err != nil {
		return err
	}

	for _, src := range dedupSorted(ev.Sources) {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO nudge_history_sources (nudge_history_id, source) VALUES (?, ?)",
			historyID, src); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *NudgeStore) LatestForSession(ctx context.Context, sessionID int64) (*store.NudgeEvent, error) {
	row := s.db.QueryRowContext(ctx, nudgeSelectColumns+`
		FROM nudge_history WHERE session_id = ?
		ORDER BY fired_at DESC LIMIT 1`, sessionID)
	return scanNudgeWithSources(ctx, s.db, row)
}

func (s *NudgeStore) LatestForSessionWithSource(ctx context.Context, sessionID int64, source string) (*store.NudgeEvent, error) {
	row := s.db.QueryRowContext(ctx, nudgeSelectColumns+`
		FROM nudge_history h
		WHERE h.session_id = ?
		  AND EXISTS (
			SELECT 1 FROM nudge_history_sources s
			WHERE s.nudge_history_id = h.id AND s.source = ?
		  )
		ORDER BY h.fired_at DESC LIMIT 1`, sessionID, source)
	return scanNudgeWithSources(ctx, s.db, row)
}

const nudgeSelectColumns = `SELECT id, session_id, text, result, error_text,
	caused_by_error_at, escalated, fired_at`

func scanNudgeWithSources(ctx context.Context, db *sql.DB, row *sql.Row) (*store.NudgeEvent, error) {
	var (
		id              int64
		ev              store.NudgeEvent
		causedBy        sql.NullString
		escalated       int
		firedAt         string
	)
	err := row.Scan(&id, &ev.SessionID, &ev.Text, &ev.Result, &ev.ErrorText,
		&causedBy, &escalated, &firedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ev.Escalated = escalated != 0
	ev.FiredAt = parseTime(firedAt)
	if causedBy.Valid {
		t := parseTime(causedBy.String)
		ev.CausedByErrorAt = &t
	}
	srows, err := db.QueryContext(ctx,
		"SELECT source FROM nudge_history_sources WHERE nudge_history_id = ?", id)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		var src string
		if err := srows.Scan(&src); err != nil {
			return nil, err
		}
		ev.Sources = append(ev.Sources, src)
	}
	return &ev, srows.Err()
}

func dedupSorted(in []string) []string {
	if len(in) == 0 {
		return in
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	// keep input order; SQL UNIQUE will trigger if dup'd anyway
	_ = strings.Join
	return out
}
```

- [ ] **Step 4: Run tests** → PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/store/sqlite/nudge_store.go \
        packages/pa-monitor/internal/store/sqlite/nudge_store_test.go
git commit -m "feat(pa-monitor/store/sqlite): NudgeStore impl with sources join table"
```

---

### Task 11: In-memory PendingNudgeQueue implementation

**Files:**

- Create: `packages/pa-monitor/internal/store/pending_nudge_queue.go`
- Create: `packages/pa-monitor/internal/store/pending_nudge_queue_test.go`

- [ ] **Step 1: Test**

```go
package store

import (
	"context"
	"testing"
)

func TestInMemoryPendingNudgeQueue(t *testing.T) {
	q := NewInMemoryPendingNudgeQueue()
	ctx := context.Background()

	if err := q.Enqueue(ctx, PendingNudge{SessionID: "sid-1", Source: "manual", Text: "go"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Duplicate enqueue is a no-op (idempotent — same session+source)
	if err := q.Enqueue(ctx, PendingNudge{SessionID: "sid-1", Source: "manual", Text: "ignored"}); err != nil {
		t.Fatalf("Enqueue dup: %v", err)
	}

	got, err := q.ForSession(ctx, "sid-1")
	if err != nil {
		t.Fatalf("ForSession: %v", err)
	}
	if len(got) != 1 || got[0].Text != "go" {
		t.Errorf("ForSession = %v, want [{sid-1 manual go}]", got)
	}

	if err := q.Cancel(ctx, "sid-1", "manual"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	got, _ = q.ForSession(ctx, "sid-1")
	if len(got) != 0 {
		t.Errorf("after Cancel: ForSession = %v, want empty", got)
	}
}
```

- [ ] **Step 2: Confirm failure**

```bash
go test ./internal/store/ -run TestInMemoryPendingNudgeQueue -v
```

- [ ] **Step 3: Write implementation**

```go
package store

import (
	"context"
	"sync"
)

// PendingNudge is one queued intent. Session+Source is the natural key.
type PendingNudge struct {
	SessionID string
	Source    string
	Text      string
}

// PendingNudgeQueue stores pending nudges. v1 implementation is in-memory.
// A future DB-backed impl can satisfy this interface without changing callers.
type PendingNudgeQueue interface {
	Enqueue(ctx context.Context, p PendingNudge) error
	Cancel(ctx context.Context, sessionID, source string) error
	ForSession(ctx context.Context, sessionID string) ([]PendingNudge, error)
	All(ctx context.Context) ([]PendingNudge, error)
}

type inMemoryQueue struct {
	mu sync.Mutex
	m  map[string]PendingNudge // key = sessionID+"\x00"+source
}

func NewInMemoryPendingNudgeQueue() PendingNudgeQueue {
	return &inMemoryQueue{m: map[string]PendingNudge{}}
}

func (q *inMemoryQueue) key(sid, source string) string { return sid + "\x00" + source }

func (q *inMemoryQueue) Enqueue(_ context.Context, p PendingNudge) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	k := q.key(p.SessionID, p.Source)
	if _, exists := q.m[k]; exists {
		return nil
	}
	q.m[k] = p
	return nil
}

func (q *inMemoryQueue) Cancel(_ context.Context, sid, source string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.m, q.key(sid, source))
	return nil
}

func (q *inMemoryQueue) ForSession(_ context.Context, sid string) ([]PendingNudge, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []PendingNudge
	for _, p := range q.m {
		if p.SessionID == sid {
			out = append(out, p)
		}
	}
	return out, nil
}

func (q *inMemoryQueue) All(_ context.Context) ([]PendingNudge, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]PendingNudge, 0, len(q.m))
	for _, p := range q.m {
		out = append(out, p)
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests** → PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/store/pending_nudge_queue.go \
        packages/pa-monitor/internal/store/pending_nudge_queue_test.go
git commit -m "feat(pa-monitor/store): in-memory PendingNudgeQueue"
```

---

## Phase 3 — Writer Goroutine

### Task 12: WriteService — single writer goroutine

**Files:**

- Create: `packages/pa-monitor/internal/service/write_service.go`
- Create: `packages/pa-monitor/internal/service/write_service_test.go`

- [ ] **Step 1: Test**

```go
package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
)

func TestWriteService_SerializesWrites(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	ws := NewWriteService(WriteDeps{
		Sessions:      sqlite.NewSessionStore(db),
		Blocks:        sqlite.NewBlockStore(db),
		Weeks:         sqlite.NewWeekStore(db),
		Contributions: sqlite.NewContributionStore(db),
		Toggles:       sqlite.NewToggleStore(db),
		Nudges:        sqlite.NewNudgeStore(db),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws.Start(ctx)

	now := time.Now().UTC()

	// Fire 20 concurrent UpsertSession calls; expect all to land.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ws.UpsertSession(ctx, store.Session{
				SessionID:       string(rune('a' + i)),
				LastProcessedAt: now,
				UpdatedAt:       now,
				CreatedAt:       now,
			})
		}(i)
	}
	wg.Wait()
	if err := ws.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	ids, err := sqlite.NewSessionStore(db).AllSessionIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 20 {
		t.Errorf("got %d session rows, want 20", len(ids))
	}
}
```

- [ ] **Step 2: Confirm failure**

```bash
go test ./internal/service/ -run TestWriteService -v
```

- [ ] **Step 3: Write implementation**

```go
// Package service hosts the WriteService (single-writer goroutine) and the
// ReadService (per-request DB queries materialised into aggregate.Tree).
package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

// WriteDeps bundles every Store the writer goroutine needs.
type WriteDeps struct {
	Sessions      store.SessionStore
	Blocks        store.BlockStore
	Weeks         store.WeekStore
	Contributions store.ContributionStore
	Toggles       store.ToggleStore
	Nudges        store.NudgeStore
}

// WriteService serialises mutations to the stores through a single goroutine.
// Concurrent callers submit closures; the goroutine runs them in arrival order.
type WriteService struct {
	deps WriteDeps
	ch   chan writeOp
	stop chan struct{}
	wg   sync.WaitGroup
}

type writeOp struct {
	fn   func(context.Context) error
	done chan error
}

const writeQueueDepth = 64

func NewWriteService(deps WriteDeps) *WriteService {
	return &WriteService{
		deps: deps,
		ch:   make(chan writeOp, writeQueueDepth),
		stop: make(chan struct{}),
	}
}

func (w *WriteService) Start(ctx context.Context) {
	w.wg.Add(1)
	go w.loop(ctx)
}

func (w *WriteService) Stop() {
	close(w.stop)
	w.wg.Wait()
}

func (w *WriteService) loop(ctx context.Context) {
	defer w.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case op := <-w.ch:
			op.done <- op.fn(ctx)
		}
	}
}

// submit enqueues fn and waits for it to complete.
func (w *WriteService) submit(ctx context.Context, fn func(context.Context) error) error {
	done := make(chan error, 1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case w.ch <- writeOp{fn: fn, done: done}:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// Sync blocks until the queue drains. Used by tests; production typically
// doesn't need it (the next read picks up writes by virtue of the round trip).
func (w *WriteService) Sync(ctx context.Context) error {
	return w.submit(ctx, func(ctx context.Context) error { return nil })
}

// --- mutation surface ---

func (w *WriteService) UpsertSession(ctx context.Context, s store.Session) error {
	return w.submit(ctx, func(ctx context.Context) error {
		return w.deps.Sessions.Upsert(ctx, s)
	})
}

func (w *WriteService) UpsertBlock(ctx context.Context, b store.Block) (int64, error) {
	var id int64
	err := w.submit(ctx, func(ctx context.Context) error {
		got, err := w.deps.Blocks.Upsert(ctx, b)
		id = got
		return err
	})
	return id, err
}

func (w *WriteService) UpsertWeek(ctx context.Context, weekRow store.Week) (int64, error) {
	var id int64
	err := w.submit(ctx, func(ctx context.Context) error {
		got, err := w.deps.Weeks.Upsert(ctx, weekRow)
		id = got
		return err
	})
	return id, err
}

func (w *WriteService) UpsertBlockContribution(ctx context.Context, c store.Contribution) error {
	return w.submit(ctx, func(ctx context.Context) error {
		return w.deps.Contributions.UpsertBlock(ctx, c)
	})
}

func (w *WriteService) UpsertWeekContribution(ctx context.Context, c store.Contribution) error {
	return w.submit(ctx, func(ctx context.Context) error {
		return w.deps.Contributions.UpsertWeek(ctx, c)
	})
}

func (w *WriteService) SetToggle(ctx context.Context, name string, value bool) error {
	return w.submit(ctx, func(ctx context.Context) error {
		return w.deps.Toggles.Set(ctx, name, value)
	})
}

func (w *WriteService) RecordNudge(ctx context.Context, ev store.NudgeEvent) error {
	return w.submit(ctx, func(ctx context.Context) error {
		return w.deps.Nudges.Record(ctx, ev)
	})
}

// MarkSessionsDeleted / Revived / HardDelete delegate to SessionStore;
// invoked by the GC sweeper.
func (w *WriteService) MarkSessionsDeleted(ctx context.Context, keepIDs []string) error {
	return w.submit(ctx, func(ctx context.Context) error {
		return w.deps.Sessions.MarkDeleted(ctx, keepIDs, time.Now().UTC())
	})
}

func (w *WriteService) MarkSessionsRevived(ctx context.Context, reviveIDs []string) error {
	return w.submit(ctx, func(ctx context.Context) error {
		return w.deps.Sessions.MarkRevived(ctx, reviveIDs)
	})
}

func (w *WriteService) HardDeleteSessions(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := w.submit(ctx, func(ctx context.Context) error {
		got, err := w.deps.Sessions.HardDelete(ctx, cutoff)
		n = got
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("hard delete: %w", err)
	}
	return n, nil
}

// Mirror the block/week orphan + hard-delete operations.
func (w *WriteService) MarkBlockOrphansDeleted(ctx context.Context) (int64, error) {
	var n int64
	err := w.submit(ctx, func(ctx context.Context) error {
		got, err := w.deps.Blocks.MarkOrphansDeleted(ctx, time.Now().UTC())
		n = got
		return err
	})
	return n, err
}

func (w *WriteService) MarkBlocksRevived(ctx context.Context) (int64, error) {
	var n int64
	err := w.submit(ctx, func(ctx context.Context) error {
		got, err := w.deps.Blocks.MarkRevived(ctx)
		n = got
		return err
	})
	return n, err
}

func (w *WriteService) HardDeleteBlocks(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := w.submit(ctx, func(ctx context.Context) error {
		got, err := w.deps.Blocks.HardDelete(ctx, cutoff)
		n = got
		return err
	})
	return n, err
}

func (w *WriteService) MarkWeekOrphansDeleted(ctx context.Context) (int64, error) {
	var n int64
	err := w.submit(ctx, func(ctx context.Context) error {
		got, err := w.deps.Weeks.MarkOrphansDeleted(ctx, time.Now().UTC())
		n = got
		return err
	})
	return n, err
}

func (w *WriteService) MarkWeeksRevived(ctx context.Context) (int64, error) {
	var n int64
	err := w.submit(ctx, func(ctx context.Context) error {
		got, err := w.deps.Weeks.MarkRevived(ctx)
		n = got
		return err
	})
	return n, err
}

func (w *WriteService) HardDeleteWeeks(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := w.submit(ctx, func(ctx context.Context) error {
		got, err := w.deps.Weeks.HardDelete(ctx, cutoff)
		n = got
		return err
	})
	return n, err
}
```

- [ ] **Step 4: Run tests** → PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/service/write_service.go \
        packages/pa-monitor/internal/service/write_service_test.go
git commit -m "feat(pa-monitor/service): single-writer-goroutine WriteService"
```

---

## Phase 4 — ReadService

### Task 13: ReadService.GetState (active block + filter + session list)

**Files:**

- Create: `packages/pa-monitor/internal/service/read_service.go`
- Create: `packages/pa-monitor/internal/service/read_service_test.go`
- Create: `packages/pa-monitor/internal/service/tree_builder.go`
- Create: `packages/pa-monitor/internal/service/tree_builder_test.go`

- [ ] **Step 1: Test the tree builder in isolation**

```go
package service

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

func TestBuildDirectories_GroupsByCwd(t *testing.T) {
	sessions := []store.SessionWithContribution{
		{Session: store.Session{SessionID: "a", Cwd: "/work/x", Status: "working", SessionTokens: 10, CostUSD: 0.5}, BlockCostUSD: 0.5, BlockTokens: 10},
		{Session: store.Session{SessionID: "b", Cwd: "/work/x", Status: "idle", SessionTokens: 5}, BlockTokens: 5},
		{Session: store.Session{SessionID: "c", Cwd: "/work/y", Status: "working", SessionTokens: 20}, BlockTokens: 20},
	}
	dirs := BuildDirectories(sessions)
	if len(dirs) != 2 {
		t.Fatalf("got %d dirs, want 2", len(dirs))
	}
	// Sort or look up by Path.
	byPath := map[string]*Directory{}
	for _, d := range dirs {
		byPath[d.Path] = d
	}
	x := byPath["/work/x"]
	if x.WorkingN != 1 || x.IdleN != 1 {
		t.Errorf("/work/x counts: working=%d idle=%d", x.WorkingN, x.IdleN)
	}
	if x.TotalTokens != 15 {
		t.Errorf("/work/x TotalTokens=%d, want 15", x.TotalTokens)
	}
	y := byPath["/work/y"]
	if y.WorkingN != 1 || y.TotalTokens != 20 {
		t.Errorf("/work/y: working=%d tokens=%d", y.WorkingN, y.TotalTokens)
	}
}
```

- [ ] **Step 2: Confirm failure** — `go test ./internal/service/ -run TestBuildDirectories -v` → FAIL.

- [ ] **Step 3: Write tree builder**

```go
package service

import "github.com/phillipgreenii/pa-monitor/internal/store"

// Directory mirrors aggregate.Directory but lives here so the service is
// self-contained. The daemon converts to aggregate.Directory at the proto
// boundary.
type Directory struct {
	Path         string
	Branch       string
	WorkingN     int
	IdleN        int
	DormantN     int
	TotalTokens  uint64
	TotalCostUSD float64
	BurnRateSum  float64
	Sessions     []store.SessionWithContribution
}

// BuildDirectories rolls a flat session list into directory groups keyed by Cwd.
// Counts and totals are computed in this pass; PR info and branch resolution
// stay in the daemon layer (file-backed prCache).
func BuildDirectories(sessions []store.SessionWithContribution) []*Directory {
	byCwd := map[string]*Directory{}
	for _, sc := range sessions {
		d, ok := byCwd[sc.Cwd]
		if !ok {
			d = &Directory{Path: sc.Cwd}
			byCwd[sc.Cwd] = d
		}
		switch sc.Status {
		case "working":
			d.WorkingN++
		case "idle":
			d.IdleN++
		default:
			d.DormantN++
		}
		d.TotalTokens += sc.SessionTokens
		d.TotalCostUSD += sc.CostUSD
		d.BurnRateSum += sc.BurnRateShort
		d.Sessions = append(d.Sessions, sc)
		if d.Branch == "" && sc.Branch != "" {
			d.Branch = sc.Branch
		}
	}
	out := make([]*Directory, 0, len(byCwd))
	for _, d := range byCwd {
		out = append(out, d)
	}
	return out
}
```

- [ ] **Step 4: Confirm tree builder test passes** → PASS.

- [ ] **Step 5: Test the read service end-to-end**

```go
package service

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
)

func TestReadService_GetState_AllFilter(t *testing.T) {
	db, _ := sqlite.Open(":memory:")
	defer db.Close()
	if err := sqlite.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	pid := 100

	// Insert a block.
	bs := sqlite.NewBlockStore(db)
	blockID, _ := bs.Upsert(ctx, store.Block{
		BlockID: "blk", StartedAt: now.Add(-time.Hour), EndedAt: now.Add(time.Hour),
		LastProcessedAt: now, UpdatedAt: now,
	})

	ss := sqlite.NewSessionStore(db)
	cs := sqlite.NewContributionStore(db)
	// alive session
	_ = ss.Upsert(ctx, store.Session{SessionID: "alive", PID: &pid, Cwd: "/a", Status: "working", LastProcessedAt: now, UpdatedAt: now, CreatedAt: now})
	// dead-PID but contributing
	_ = ss.Upsert(ctx, store.Session{SessionID: "dead-contrib", Cwd: "/b", Status: "idle", LastProcessedAt: now, UpdatedAt: now, CreatedAt: now})
	var aliveID, deadID int64
	_ = db.QueryRowContext(ctx, "SELECT id FROM sessions WHERE session_id='alive'").Scan(&aliveID)
	_ = db.QueryRowContext(ctx, "SELECT id FROM sessions WHERE session_id='dead-contrib'").Scan(&deadID)
	_ = cs.UpsertBlock(ctx, store.Contribution{SessionID: aliveID, ParentID: blockID, CostUSD: 1, Tokens: 10, UpdatedAt: now})
	_ = cs.UpsertBlock(ctx, store.Contribution{SessionID: deadID, ParentID: blockID, CostUSD: 2, Tokens: 20, UpdatedAt: now})
	// dead-PID, no contribution (should NOT appear in either filter)
	_ = ss.Upsert(ctx, store.Session{SessionID: "ghost", Cwd: "/c", Status: "dormant", LastProcessedAt: now, UpdatedAt: now, CreatedAt: now})

	rs := NewReadService(ReadDeps{
		Sessions: ss, Blocks: bs, Weeks: sqlite.NewWeekStore(db), Toggles: sqlite.NewToggleStore(db), Nudges: sqlite.NewNudgeStore(db),
	})

	all, err := rs.GetState(ctx, store.FilterAll)
	if err != nil {
		t.Fatalf("GetState all: %v", err)
	}
	if len(all.Sessions) != 2 {
		t.Errorf("All filter returned %d sessions, want 2 (alive + dead-contrib)", len(all.Sessions))
	}

	active, err := rs.GetState(ctx, store.FilterActive)
	if err != nil {
		t.Fatalf("GetState active: %v", err)
	}
	if len(active.Sessions) != 1 {
		t.Errorf("Active filter returned %d sessions, want 1 (alive only)", len(active.Sessions))
	}
}
```

- [ ] **Step 6: Confirm test fails** — `NewReadService` undefined.

- [ ] **Step 7: Write the read service**

```go
package service

import (
	"context"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

// State is the full snapshot returned by ReadService.GetState.
type State struct {
	Sessions []store.SessionWithContribution
	Dirs     []*Directory
	Block    *store.Block
	Week     *store.Week
	Toggles  map[string]bool
	Now      time.Time
}

// ReadDeps bundles every read-side Store.
type ReadDeps struct {
	Sessions store.SessionStore
	Blocks   store.BlockStore
	Weeks    store.WeekStore
	Toggles  store.ToggleStore
	Nudges   store.NudgeStore
}

// ReadService materialises a State from the stores per request.
type ReadService struct {
	deps      ReadDeps
	freshness store.FreshnessWindow
	now       func() time.Time
}

func NewReadService(deps ReadDeps) *ReadService {
	return &ReadService{deps: deps, freshness: store.DefaultFreshness(), now: time.Now}
}

// SetClock allows tests to inject a deterministic now.
func (r *ReadService) SetClock(fn func() time.Time) { r.now = fn }

func (r *ReadService) GetState(ctx context.Context, filter store.Filter) (*State, error) {
	now := r.now().UTC()
	st := &State{Now: now}

	block, err := r.deps.Blocks.GetActive(ctx, now, r.freshness)
	if err != nil {
		return nil, err
	}
	st.Block = block

	week, err := r.deps.Weeks.GetActive(ctx, now, r.freshness)
	if err != nil {
		return nil, err
	}
	st.Week = week

	var activeBlockID int64
	if block != nil {
		activeBlockID = block.ID
	}
	sessions, err := r.deps.Sessions.List(ctx, filter, activeBlockID, r.freshness)
	if err != nil {
		return nil, err
	}
	st.Sessions = sessions
	st.Dirs = BuildDirectories(sessions)

	toggles, err := r.deps.Toggles.All(ctx)
	if err != nil {
		return nil, err
	}
	st.Toggles = toggles

	return st, nil
}
```

- [ ] **Step 8: Run tests** → PASS.

- [ ] **Step 9: Commit**

```bash
git add packages/pa-monitor/internal/service/read_service.go \
        packages/pa-monitor/internal/service/read_service_test.go \
        packages/pa-monitor/internal/service/tree_builder.go \
        packages/pa-monitor/internal/service/tree_builder_test.go
git commit -m "feat(pa-monitor/service): ReadService.GetState + Directory tree builder"
```

---

### Task 14: ReadService GetSessionByID, Toggles

**Files:**

- Modify: `packages/pa-monitor/internal/service/read_service.go`
- Modify: `packages/pa-monitor/internal/service/read_service_test.go`

- [ ] **Step 1: Add the test**

```go
func TestReadService_GetSessionByID(t *testing.T) {
	db, _ := sqlite.Open(":memory:")
	defer db.Close()
	_ = sqlite.Migrate(context.Background(), db)
	ctx := context.Background()
	now := time.Now().UTC()
	ss := sqlite.NewSessionStore(db)
	_ = ss.Upsert(ctx, store.Session{SessionID: "sid-1", Cwd: "/x", Status: "idle", LastProcessedAt: now, UpdatedAt: now, CreatedAt: now})

	rs := NewReadService(ReadDeps{Sessions: ss, Blocks: sqlite.NewBlockStore(db), Weeks: sqlite.NewWeekStore(db), Toggles: sqlite.NewToggleStore(db), Nudges: sqlite.NewNudgeStore(db)})
	got, err := rs.GetSessionByID(ctx, "sid-1")
	if err != nil || got == nil {
		t.Fatalf("GetSessionByID: %v %v", got, err)
	}
	if got.SessionID != "sid-1" {
		t.Errorf("SessionID = %q", got.SessionID)
	}
}
```

- [ ] **Step 2: Confirm failure**.

- [ ] **Step 3: Add the methods to ReadService**

Append to `read_service.go`:

```go
// SessionDetail wraps a Session with its latest nudge event (if any).
type SessionDetail struct {
	Session     store.Session
	LatestNudge *store.NudgeEvent
}

func (r *ReadService) GetSessionByID(ctx context.Context, sessionID string) (*SessionDetail, error) {
	sess, err := r.deps.Sessions.GetByID(ctx, sessionID, r.freshness)
	if err != nil || sess == nil {
		return nil, err
	}
	det := &SessionDetail{Session: *sess}
	// Look up the surrogate id for the nudge join.
	// (A small extension to SessionStore would return id; for now skip nudge
	// lookup if we can't get it.)
	det.LatestNudge = nil
	return det, nil
}

func (r *ReadService) Toggles(ctx context.Context) (map[string]bool, error) {
	return r.deps.Toggles.All(ctx)
}
```

- [ ] **Step 4: Run tests** → PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/service/read_service.go \
        packages/pa-monitor/internal/service/read_service_test.go
git commit -m "feat(pa-monitor/service): GetSessionByID + Toggles read methods"
```

---

## Phase 5 — Producer Refactor

### Task 15: Drop PidAlive filter from Discoverer

**Files:**

- Modify: `packages/pa-monitor/internal/core/session/discovery.go`
- Modify: `packages/pa-monitor/internal/core/session/session.go`
- Modify: `packages/pa-monitor/internal/core/session/discovery_test.go`

- [ ] **Step 1: Update the test expectations**

Open `discovery_test.go`. The test `TestDiscoverReadsFilesAndFiltersDeadPids` currently expects dead-PID sessions to be filtered out. Update to expect them returned with `Session.PidAlive = false`:

Find the test, change the assertion. The post-change test should look like:

```go
func TestDiscoverReadsFilesAndKeepsDeadPids(t *testing.T) {
	dir := t.TempDir()
	// Write three session files. PIDs: 100 (alive), 200 (dead), 300 (alive).
	mustWrite(t, dir, "a.json", `{"pid":100,"sessionId":"a","cwd":"/p"}`)
	mustWrite(t, dir, "b.json", `{"pid":200,"sessionId":"b","cwd":"/p"}`)
	mustWrite(t, dir, "c.json", `{"pid":300,"sessionId":"c","cwd":"/p"}`)

	d := Discoverer{
		SessionsDir: dir,
		PidAlive: func(pid int) bool {
			return pid == 100 || pid == 300
		},
	}
	out, err := d.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d sessions, want 3 (no dead-PID filter)", len(out))
	}
	byID := map[string]*Session{}
	for _, s := range out {
		byID[s.SessionID] = s
	}
	if !byID["a"].PidAlive {
		t.Errorf("session a: PidAlive false, want true")
	}
	if byID["b"].PidAlive {
		t.Errorf("session b: PidAlive true, want false")
	}
	if !byID["c"].PidAlive {
		t.Errorf("session c: PidAlive false, want true")
	}
}
```

If `mustWrite` helper doesn't exist in this file, write it as a top-level test helper or inline equivalent `os.WriteFile`.

- [ ] **Step 2: Confirm the test fails**

```bash
go test ./internal/core/session/ -run TestDiscover -v
```

Expected: FAIL — old test name and behaviour.

- [ ] **Step 3: Add PidAlive field to Session**

In `session.go`, add to the `Session` struct (alongside existing fields):

```go
// PidAlive is set by the Discoverer based on its PidAlive function.
// Used by the poller to decide whether to write a non-NULL pid to the DB.
PidAlive bool
```

- [ ] **Step 4: Modify Discover to not filter**

In `discovery.go`, replace the PidAlive-skip logic so dead-PID sessions are still returned, just with `PidAlive: false`. Replace this block (around line 64-66):

```go
		if d.PidAlive != nil && !d.PidAlive(r.PID) {
			continue
		}
```

with:

```go
		alive := true
		if d.PidAlive != nil {
			alive = d.PidAlive(r.PID)
		}
```

Then in the `out = append(out, &Session{...})` block, add `PidAlive: alive`.

- [ ] **Step 5: Run all session tests**

```bash
go test ./internal/core/session/ -v
```

Some tests may need adjustment for the new behaviour. Fix them inline by adjusting assertions to match the new non-filter behaviour. Run again until green.

- [ ] **Step 6: Run the whole project test suite**

```bash
go test ./...
```

The poller may have tests that rely on dead-PID filtering. Inspect failures; update tests / poller code to use `s.PidAlive` as a signal instead of relying on absence.

- [ ] **Step 7: Commit**

```bash
git add packages/pa-monitor/internal/core/session/
git commit -m "refactor(pa-monitor/session): Discover returns all sessions with PidAlive flag"
```

---

### Task 16: Session poller writes to stores (dual-write era)

**Files:**

- Modify: `packages/pa-monitor/internal/core/poller/poller.go`
- Modify: `packages/pa-monitor/internal/core/poller/poller_test.go`

- [ ] **Step 1: Add WriteService dependency to Poller struct**

In `poller.go`, add to the `Poller` struct (alongside existing fields):

```go
// WriteService is optional. When non-nil, every tick UPSERTs all
// discovered sessions and their current-block contributions into the DB.
// The in-memory aggregate.Tree path also remains until Task 19 cuts over.
WriteService *service.WriteService

// ActiveBlockID is the surrogate id of the current block in the DB.
// Set by the daemon main loop after ccusage poller upserts the block.
// 0 means no active block yet — contributions are skipped this tick.
ActiveBlockID int64
ActiveWeekID  int64
```

Add an import for `github.com/phillipgreenii/pa-monitor/internal/service` and `github.com/phillipgreenii/pa-monitor/internal/store`.

- [ ] **Step 2: Write a test for the new DB write path**

Append to `poller_test.go`:

```go
func TestPoller_WritesToStores(t *testing.T) {
	db, _ := sqlite.Open(":memory:")
	defer db.Close()
	_ = sqlite.Migrate(context.Background(), db)

	ws := service.NewWriteService(service.WriteDeps{
		Sessions:      sqlite.NewSessionStore(db),
		Blocks:        sqlite.NewBlockStore(db),
		Weeks:         sqlite.NewWeekStore(db),
		Contributions: sqlite.NewContributionStore(db),
		Toggles:       sqlite.NewToggleStore(db),
		Nudges:        sqlite.NewNudgeStore(db),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws.Start(ctx)

	dir := t.TempDir()
	mustWrite(t, dir, "a.json", `{"pid":12345,"sessionId":"sid-a","cwd":"/p"}`)

	p := &Poller{
		SessionsDir:      dir,
		PidAlive:         func(int) bool { return true },
		WorkingThreshold: 30 * time.Second,
		IdleThreshold:    10 * time.Minute,
		Now:              time.Now,
		Signalers:        nil,
		WriteService:     ws,
		// (other fields unset for this focused test)
	}
	if _, _, err := p.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := ws.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	ids, err := sqlite.NewSessionStore(db).AllSessionIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "sid-a" {
		t.Errorf("AllSessionIDs = %v, want [sid-a]", ids)
	}
}
```

(`mustWrite` helper should be present from Task 15 work; if not, copy it in.)

- [ ] **Step 3: Confirm the test fails**

```bash
go test ./internal/core/poller/ -run TestPoller_WritesToStores -v
```

- [ ] **Step 4: Add the write logic inside Run**

In `poller.go`, find the `Run` function. After the existing tree-build logic finishes (after `tree := aggregate.Build(...)`), but before `return tree, anyWorking, nil`, add:

```go
	if p.WriteService != nil {
		nowUTC := now.UTC()
		for _, sv := range tree.Sessions() {
			ss := store.Session{
				SessionID:          sv.SessionID,
				PID:                pidPtrIfAlive(sv.PID, sv.Session),
				Cwd:                sv.Cwd,
				Name:               sv.Name,
				Kind:               sv.Kind,
				Entrypoint:         sv.Entrypoint,
				Model:              sv.Model,
				TerminalHost:       sv.TerminalHost,
				Branch:             sv.Branch,
				Status:             session.Status(sv.Status).String(),
				FirstPrompt:        sv.FirstPrompt,
				Labels:             nil, // populated when label pipeline runs in daemon
				TranscriptMTime:    sv.TranscriptMTime,
				StartedAt:          sv.StartedAt,
				ContextTokens:      uint64(sv.ContextTokens),
				SessionTokens:      uint64(sv.SessionTokens),
				SubagentCount:      uint32(sv.SubagentCount),
				SubshellCount:      uint32(sv.SubshellCount),
				BurnRateShort:      sv.BurnRateShort,
				BurnRateLong:       sv.BurnRateLong,
				CostUSD:            sv.CostUSD,
				AwaitingInput:      sv.AwaitingInput,
				LastProcessedAt:    nowUTC,
				UpdatedAt:          nowUTC,
			}
			// fold LastError if present
			if sv.SessionEnrichment.LastError != nil {
				le := sv.SessionEnrichment.LastError
				ss.LastErrorKind = string(le.Kind)
				ss.LastErrorText = le.Text
				ss.LastErrorAt = le.At
				ss.LastErrorTerminal = le.IsTerminal
				ss.LastErrorRetryable = le.IsRetryable
			}
			if err := p.WriteService.UpsertSession(ctx, ss); err != nil {
				return nil, false, fmt.Errorf("write session %s: %w", sv.SessionID, err)
			}
			if p.ActiveBlockID > 0 {
				// Look up the surrogate session id for the contribution.
				// Cheap: pulled once via SessionStore.GetByID by SessionID + cached.
				// For this dual-write phase, do a quick query.
				// (The next refactor task will consolidate this lookup.)
			}
		}
		_ = nowUTC
	}
```

(The contribution-upsert needs the surrogate session id; that's wired in Task 17 alongside the block id propagation.)

Add a small helper:

```go
func pidPtrIfAlive(pid int, s *session.Session) *int {
	if s != nil && !s.PidAlive {
		return nil
	}
	p := pid
	return &p
}
```

- [ ] **Step 5: Run all tests**

```bash
go test ./...
```

Expected: PASS. The dual-write doesn't break anything; the new write path is opt-in via `WriteService != nil`.

- [ ] **Step 6: Commit**

```bash
git add packages/pa-monitor/internal/core/poller/
git commit -m "feat(pa-monitor/poller): dual-write session rows to WriteService"
```

---

### Task 17: CCusage poller writes to stores; wire active block id

**Files:**

- Modify: `packages/pa-monitor/internal/daemon/lifecycle.go`
- Modify: `packages/pa-monitor/internal/core/poller/poller.go`

- [ ] **Step 1: Test approach**

This wiring lives in `lifecycle.go`'s tick loop. Add a test that exercises the whole pipeline (existing `tick_integration_test.go` already has the harness — extend it).

Open `internal/daemon/tick_integration_test.go`, locate `TestTickIntegration` (or equivalent). Add a sub-test:

```go
func TestTickIntegration_WritesBlocksAndContributions(t *testing.T) {
    // Replicate the existing harness's setup, plus:
    // 1. Inject WriteService into RunOptions (new field) backed by in-memory SQLite.
    // 2. Run one tick.
    // 3. Verify blocks table has the ccusage-reported block.
    // 4. Verify session_block_contributions has rows for each session.
    // Body details mirror the existing harness pattern; reuse helpers from
    // the test file. Block JSON fixture: a known active block with $5 cost.
}
```

- [ ] **Step 2: Confirm test fails**

- [ ] **Step 3: In `lifecycle.go`'s `RunWith`, after each ccusage tick, upsert block + week**

Inside the main tick loop (look for `opts.WeeklyFn` invocation), after a successful block fetch, build a `store.Block` and call `opts.WriteService.UpsertBlock(...)`. Capture the returned id; assign it to `poller.ActiveBlockID` (the field added in Task 16). Same shape for weeks.

The `RunOptions` struct must gain a `WriteService *service.WriteService` field. The `RunWith` function wires it to the poller before the tick loop starts.

- [ ] **Step 4: In `poller.go` Run, use ActiveBlockID for contributions**

Extend the loop from Task 16 — when `p.ActiveBlockID > 0`, query for the session's surrogate id (via `db.QueryRowContext("SELECT id FROM sessions WHERE session_id = ?", sv.SessionID)`), then call `p.WriteService.UpsertBlockContribution(...)`. Same for weeks.

Concrete addition inside the loop, replacing the placeholder comment from Task 16:

```go
			if p.ActiveBlockID > 0 {
				var sessRowID int64
				if err := p.DB.QueryRowContext(ctx,
					"SELECT id FROM sessions WHERE session_id = ?", sv.SessionID).Scan(&sessRowID); err == nil {
					_ = p.WriteService.UpsertBlockContribution(ctx, store.Contribution{
						SessionID: sessRowID, ParentID: p.ActiveBlockID,
						CostUSD: sv.CostUSD, Tokens: uint64(sv.SessionTokens), UpdatedAt: nowUTC,
					})
				}
			}
			if p.ActiveWeekID > 0 {
				var sessRowID int64
				if err := p.DB.QueryRowContext(ctx,
					"SELECT id FROM sessions WHERE session_id = ?", sv.SessionID).Scan(&sessRowID); err == nil {
					_ = p.WriteService.UpsertWeekContribution(ctx, store.Contribution{
						SessionID: sessRowID, ParentID: p.ActiveWeekID,
						CostUSD: sv.CostUSD, Tokens: uint64(sv.SessionTokens), UpdatedAt: nowUTC,
					})
				}
			}
```

This adds a new field `DB *sql.DB` to `Poller` for the surrogate-id lookup. (Acceptable short-term coupling; later refactor extracts a `Service` interface.)

- [ ] **Step 5: Run tests**

```bash
go test ./...
```

Fix any breakage. Expected: all green; the integration test passes.

- [ ] **Step 6: Commit**

```bash
git add packages/pa-monitor/internal/daemon/lifecycle.go \
        packages/pa-monitor/internal/core/poller/poller.go \
        packages/pa-monitor/internal/daemon/tick_integration_test.go
git commit -m "feat(pa-monitor/daemon): ccusage poller writes blocks/weeks; poller writes contributions"
```

---

### Task 18: Nudger dispatcher writes to NudgeStore

**Files:**

- Modify: `packages/pa-monitor/internal/daemon/nudger/dispatcher.go`
- Modify: `packages/pa-monitor/internal/daemon/lifecycle.go`
- Modify: tests

- [ ] **Step 1: Add a NudgeRecorder interface to the nudger package**

In `internal/daemon/nudger/dispatcher.go`, define:

```go
// NudgeRecorder is the persistence hook for the dispatcher. The daemon
// wires a WriteService-backed implementation; the nudger itself doesn't
// know about SQLite.
type NudgeRecorder interface {
	Record(ctx context.Context, ev RecordEvent) error
}

type RecordEvent struct {
	SessionID        string // string id from session.json; recorder maps to surrogate
	Text             string
	Result           string
	ErrorText        string
	CausedByErrorAt  *time.Time
	Escalated        bool
	FiredAt          time.Time
	Sources          []string
}
```

Add a field `Recorder NudgeRecorder` on the dispatcher (or wherever it's currently invoked from). When a dispatch happens, call `Recorder.Record(...)` after the send returns.

- [ ] **Step 2: Add a recorder in the daemon**

In `internal/daemon/`, create a small adapter that implements `NudgeRecorder` by translating the session_id string to a row id (via a SQL query) and calling `WriteService.RecordNudge`. Wire it into `lifecycle.go`'s nudger construction.

- [ ] **Step 3: Test**

Existing dispatcher tests need a fake `NudgeRecorder` to verify Record is called with the expected event shape. Add `TestDispatcher_RecordsOnSend`:

```go
func TestDispatcher_RecordsOnSend(t *testing.T) {
    rec := &fakeRecorder{}
    d := &Dispatcher{Recorder: rec, /* other fields */}
    // Drive a dispatch (use existing harness pattern).
    // Assert rec.events has one entry with the expected source list.
}

type fakeRecorder struct{ events []RecordEvent }
func (f *fakeRecorder) Record(ctx context.Context, ev RecordEvent) error {
    f.events = append(f.events, ev); return nil
}
```

- [ ] **Step 4: Run tests** → PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/daemon/nudger/ packages/pa-monitor/internal/daemon/lifecycle.go
git commit -m "feat(pa-monitor/nudger): record dispatched nudges via NudgeRecorder"
```

---

### Task 19: Caffeinate / SetAutoResume RPCs write to ToggleStore

**Files:**

- Modify: `packages/pa-monitor/internal/daemon/server.go`
- Modify: `packages/pa-monitor/internal/daemon/server_test.go`

- [ ] **Step 1: Add ToggleStore + WriteService to the server struct**

In `server.go`'s `type server struct`, add:

```go
writeService *service.WriteService
```

Wire it through `serve()` and `newServer()` like `planTier` is wired.

- [ ] **Step 2: Modify `Caffeinate` RPC**

Inside the existing `Caffeinate` handler, just after the `s.state.setCaffeinateActive(target, cause)` line, add:

```go
	if s.writeService != nil {
		_ = s.writeService.SetToggle(ctx, "caffeinate_on", target)
	}
```

Mirror for `SetAutoResume`.

- [ ] **Step 3: Test**

Add `TestServerCaffeinatePersistsToToggleStore`:

```go
func TestServerCaffeinatePersistsToToggleStore(t *testing.T) {
    // setup: in-memory DB + WriteService + new test server
    // call srv.Caffeinate(ctx, {Action: "on"})
    // read ToggleStore.Get(ctx, "caffeinate_on") → expect true
}
```

- [ ] **Step 4: Run tests** → PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/daemon/
git commit -m "feat(pa-monitor/daemon): RPC toggle handlers write to ToggleStore"
```

---

## Phase 6 — Switch Reads

### Task 20: Replace state.snapshot() with DB-backed materialisation

**Files:**

- Modify: `packages/pa-monitor/internal/daemon/state.go`
- Modify: `packages/pa-monitor/internal/daemon/server.go`
- Modify: `packages/pa-monitor/internal/daemon/lifecycle.go`

- [ ] **Step 1: Wire ReadService into sharedState**

In `state.go`, add to `sharedState`:

```go
readService *service.ReadService
```

Add setter `setReadService(rs *service.ReadService)`.

- [ ] **Step 2: Reroute `snapshot()`**

Today `(*sharedState).snapshot()` returns `*aggregate.Tree` from the in-memory `tree` field. Replace its body:

```go
func (s *sharedState) snapshot() *aggregate.Tree {
	s.mu.RLock()
	rs := s.readService
	s.mu.RUnlock()
	if rs == nil {
		return nil
	}
	st, err := rs.GetState(context.Background(), store.FilterAll)
	if err != nil || st == nil {
		return nil
	}
	return convertStateToAggregateTree(st)
}
```

Create `convertStateToAggregateTree` — translates `*service.State` (which contains `[]store.SessionWithContribution` + `[]*service.Directory` + block/week) into the existing `*aggregate.Tree` shape. This is the boundary translation that keeps consumers compiling.

- [ ] **Step 3: Test that GetState RPC still works**

Existing tests in `server_test.go` that hit `GetState(ctx, &pb.GetStateRequest{})` should now flow through the DB. Run them:

```bash
go test ./internal/daemon/ -v
```

Fix anything that breaks. Most likely the existing tests inject directly into `state.tree` — those need to instead populate via the WriteService.

- [ ] **Step 4: Commit**

```bash
git add packages/pa-monitor/internal/daemon/
git commit -m "refactor(pa-monitor/daemon): sharedState.snapshot reads from ReadService"
```

---

### Task 21: Remove tree-write path from poller

**Files:**

- Modify: `packages/pa-monitor/internal/core/poller/poller.go`
- Modify: `packages/pa-monitor/internal/daemon/lifecycle.go`

After Task 20, the in-memory tree is no longer consulted by any caller — `snapshot()` reads from the DB. The poller can stop maintaining the in-memory tree.

- [ ] **Step 1: Remove the `setTree` call from the daemon tick loop**

In `lifecycle.go`'s tick body, delete the `state.setTree(tree)` line (or whatever equivalent). The poller's `Run` still builds the tree (in-process) so it can extract per-session enrichment, but no longer publishes it to sharedState.

- [ ] **Step 2: Delete `sharedState.setTree`, `sharedState.tree`, `(*sharedState).setTree(...)`**

Remove the fields and methods. Any tests that called these need to be rewritten to populate via WriteService. Most should already be migrated in Task 20.

- [ ] **Step 3: Run all tests**

```bash
go test ./...
```

Fix breakage. The integration test from Task 17 + the server tests should still pass.

- [ ] **Step 4: Commit**

```bash
git add packages/pa-monitor/internal/core/poller/ packages/pa-monitor/internal/daemon/
git commit -m "refactor(pa-monitor): drop in-memory aggregate.Tree pointer swap"
```

---

## Phase 7 — Migration + GC

### Task 22: runtime.json → DB migration on startup

**Files:**

- Create: `packages/pa-monitor/internal/daemon/runtime_migration.go`
- Create: `packages/pa-monitor/internal/daemon/runtime_migration_test.go`
- Modify: `packages/pa-monitor/internal/daemon/lifecycle.go`

- [ ] **Step 1: Test**

```go
package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store/sqlite"
)

func TestMigrateRuntimeJSON_PopulatesToggles(t *testing.T) {
	dir := t.TempDir()
	rtPath := filepath.Join(dir, "runtime.json")
	// Write a runtime.json with caffeinate_on=true, auto_resume_enabled=false
	rs := RuntimeState{CaffeinateOn: true, AutoResumeEnabled: false}
	if err := WriteRuntimeState(rtPath, rs); err != nil {
		t.Fatal(err)
	}

	db, _ := sqlite.Open(":memory:")
	defer db.Close()
	_ = sqlite.Migrate(context.Background(), db)
	ts := sqlite.NewToggleStore(db)

	if err := MigrateRuntimeJSON(context.Background(), rtPath, ts, sqlite.NewNudgeStore(db), sqlite.NewSessionStore(db)); err != nil {
		t.Fatalf("MigrateRuntimeJSON: %v", err)
	}

	caff, present, _ := ts.Get(context.Background(), "caffeinate_on")
	if !present || !caff {
		t.Errorf("caffeinate_on = (%v, %v), want (true, true)", caff, present)
	}

	// runtime.json should be deleted after migration
	if _, err := os.Stat(rtPath); !os.IsNotExist(err) {
		t.Errorf("runtime.json still exists post-migration")
	}
	_ = time.Time{}
}
```

- [ ] **Step 2: Confirm failure**.

- [ ] **Step 3: Write the migration**

```go
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

// MigrateRuntimeJSON, if runtime.json exists, copies its toggles into the
// ToggleStore and its nudger watermarks into nudge_history, then deletes
// runtime.json. A missing file is a no-op.
func MigrateRuntimeJSON(ctx context.Context, path string, ts store.ToggleStore, ns store.NudgeStore, ss store.SessionStore) error {
	rs, err := ReadRuntimeState(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read runtime.json: %w", err)
	}

	if err := ts.Set(ctx, "caffeinate_on", rs.CaffeinateOn); err != nil {
		return fmt.Errorf("set caffeinate_on: %w", err)
	}
	if err := ts.Set(ctx, "auto_resume_enabled", rs.AutoResumeEnabled); err != nil {
		return fmt.Errorf("set auto_resume_enabled: %w", err)
	}

	for sid, wm := range rs.Nudger.Sessions {
		// Look up surrogate id; skip if session row not present yet (first
		// poll tick will arrive shortly).
		sess, err := ss.GetByID(ctx, sid, store.DefaultFreshness())
		if err != nil || sess == nil {
			continue
		}
		// session.id surrogate lookup via a fresh query — the SessionStore
		// interface doesn't expose row ids directly; for migration we accept
		// the small coupling.
		if !wm.LastNudgedAt.IsZero() {
			sources := wm.LastNudgeSources
			if len(sources) == 0 {
				sources = []string{"unknown"}
			}
			_ = ns.Record(ctx, store.NudgeEvent{
				// SessionID lookup deferred — see note above
				Text:    "(migrated from runtime.json)",
				Result:  "sent",
				FiredAt: wm.LastNudgedAt,
				Sources: sources,
			})
		}
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove runtime.json: %w", err)
	}
	return nil
}
```

Note: the surrogate-id lookup for nudge migration is awkward without access to the underlying DB. For this task, the migration's nudge import is best-effort — record-attempts that need a session row id can be skipped if the session hasn't been polled yet (first poll tick is seconds away).

- [ ] **Step 4: Wire into `RunWith`**

In `lifecycle.go`, after opening the DB and migrating the schema, call `MigrateRuntimeJSON(ctx, opts.RuntimePath, toggleStore, nudgeStore, sessionStore)`. Continue startup regardless of errors (best-effort, log on failure).

- [ ] **Step 5: Run tests**

```bash
go test ./internal/daemon/ -run TestMigrateRuntimeJSON -v
go test ./...
```

- [ ] **Step 6: Commit**

```bash
git add packages/pa-monitor/internal/daemon/runtime_migration.go \
        packages/pa-monitor/internal/daemon/runtime_migration_test.go \
        packages/pa-monitor/internal/daemon/lifecycle.go
git commit -m "feat(pa-monitor/daemon): one-shot runtime.json -> DB migration on startup"
```

---

### Task 23: GC sweeper

**Files:**

- Create: `packages/pa-monitor/internal/daemon/gc.go`
- Create: `packages/pa-monitor/internal/daemon/gc_test.go`
- Modify: `packages/pa-monitor/internal/daemon/lifecycle.go`

- [ ] **Step 1: Test**

```go
func TestGCSweeper_ReconcilesFiles(t *testing.T) {
	db, _ := sqlite.Open(":memory:")
	defer db.Close()
	_ = sqlite.Migrate(context.Background(), db)
	ctx := context.Background()
	now := time.Now().UTC()
	ss := sqlite.NewSessionStore(db)

	// Insert sessions a, b, c.
	for _, sid := range []string{"a", "b", "c"} {
		_ = ss.Upsert(ctx, store.Session{SessionID: sid, LastProcessedAt: now, UpdatedAt: now, CreatedAt: now})
	}

	// Filesystem has only "a" — b and c should be soft-deleted.
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.json"), []byte("{}"), 0o600)

	ws := buildTestWriteService(t, db)
	gc := &GCSweeper{
		SessionsDir:  dir,
		WriteService: ws,
		HardDeleteAfter: 24 * time.Hour,
	}
	if err := gc.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if err := ws.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	for _, sid := range []string{"b", "c"} {
		got, _ := ss.GetByID(ctx, sid, store.DefaultFreshness())
		if got != nil {
			t.Errorf("%s should be soft-deleted (GetByID should return nil), got %+v", sid, got)
		}
	}
	got, _ := ss.GetByID(ctx, "a", store.DefaultFreshness())
	if got == nil {
		t.Error("a should remain alive")
	}
}
```

- [ ] **Step 2: Confirm failure**.

- [ ] **Step 3: Write the GC sweeper**

```go
package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/service"
)

// GCSweeper does file-existence reconciliation and hard-delete sweep
// every Interval (default 1h).
type GCSweeper struct {
	SessionsDir     string
	WriteService    *service.WriteService
	Interval        time.Duration
	HardDeleteAfter time.Duration
}

func (g *GCSweeper) RunOnce(ctx context.Context) error {
	keep, err := listSessionFiles(g.SessionsDir)
	if err != nil {
		return err
	}
	if err := g.WriteService.MarkSessionsDeleted(ctx, keep); err != nil {
		return err
	}
	if err := g.WriteService.MarkSessionsRevived(ctx, keep); err != nil {
		return err
	}
	cutoff := time.Now().UTC().Add(-g.HardDeleteAfter)
	if _, err := g.WriteService.HardDeleteSessions(ctx, cutoff); err != nil {
		return err
	}
	if _, err := g.WriteService.MarkBlockOrphansDeleted(ctx); err != nil {
		return err
	}
	if _, err := g.WriteService.MarkBlocksRevived(ctx); err != nil {
		return err
	}
	if _, err := g.WriteService.HardDeleteBlocks(ctx, cutoff); err != nil {
		return err
	}
	if _, err := g.WriteService.MarkWeekOrphansDeleted(ctx); err != nil {
		return err
	}
	if _, err := g.WriteService.MarkWeeksRevived(ctx); err != nil {
		return err
	}
	if _, err := g.WriteService.HardDeleteWeeks(ctx, cutoff); err != nil {
		return err
	}
	return nil
}

func (g *GCSweeper) Run(ctx context.Context) {
	interval := g.Interval
	if interval == 0 {
		interval = time.Hour
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = g.RunOnce(ctx)
		}
	}
}

func listSessionFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		// Strip extension to get the session_id (Claude Code's convention).
		out = append(out, strings.TrimSuffix(strings.TrimSuffix(name, ".jsonl"), ".json"))
	}
	// (path/filepath import retained for future per-file logic)
	_ = filepath.Separator
	return out, nil
}
```

- [ ] **Step 4: Wire into lifecycle**

In `RunWith`, after the writer goroutine starts, launch the GC sweeper:

```go
gc := &GCSweeper{
	SessionsDir:     opts.Poller.SessionsDir,
	WriteService:    writeService,
	HardDeleteAfter: 24 * time.Hour,
}
go gc.Run(ctx)
```

- [ ] **Step 5: Run tests** → PASS.

- [ ] **Step 6: Commit**

```bash
git add packages/pa-monitor/internal/daemon/gc.go \
        packages/pa-monitor/internal/daemon/gc_test.go \
        packages/pa-monitor/internal/daemon/lifecycle.go
git commit -m "feat(pa-monitor/daemon): hourly GC sweeper for file reconciliation + hard delete"
```

---

## Phase 8 — Proto + Cleanup

### Task 24: Add `cap_hit_at` to Block + Week proto

**Files:**

- Modify: `packages/pa-monitor/internal/proto/pa_monitor.proto`
- Auto-regenerated: `pa_monitor.pb.go`, `pa_monitor_grpc.pb.go`
- Modify: `packages/pa-monitor/internal/proto/translate.go`
- Modify: `packages/pa-monitor/internal/proto/from_proto.go`

- [ ] **Step 1: Update proto**

Open `internal/proto/pa_monitor.proto`. In the `Block` message, add:

```proto
google.protobuf.Timestamp cap_hit_at = 11; // null when cap not yet hit
```

In `Week`, add:

```proto
google.protobuf.Timestamp cap_hit_at = 5;
```

- [ ] **Step 2: Regenerate bindings**

From the parent repo root:

```bash
nix run .#pa-monitor-codegen -- packages/pa-monitor
```

- [ ] **Step 3: Populate the new fields in translate.go**

In `blockToProto`, add a branch that maps `*time.Time` → `*timestamppb.Timestamp` for `cap_hit_at`. Same for `weekToProto`. The aggregate types may need a `CapHitAt *time.Time` field — add it if missing, populated by the convert-state-to-tree function from Task 20.

- [ ] **Step 4: Populate in from_proto.go**

Mirror: map the proto timestamp back to `*time.Time` on the aggregate types.

- [ ] **Step 5: Run tests**

```bash
go test ./...
```

- [ ] **Step 6: Commit**

```bash
git add packages/pa-monitor/internal/proto/
git commit -m "feat(pa-monitor/proto): add cap_hit_at to Block + Week"
```

---

### Task 25: Delete obsolete in-memory tree code

**Files:**

- Modify: `packages/pa-monitor/internal/daemon/state.go`
- Modify: `packages/pa-monitor/internal/daemon/lifecycle.go`
- Possibly delete `nudger_runtime.go` parts

- [ ] **Step 1: Identify dead code via grep**

```bash
grep -rn "tree\s\+\*aggregate.Tree\|setTree\|state\.tree" packages/pa-monitor/internal/daemon/
```

Any references to `state.tree` direct access, `setTree`, or other tree-pointer-swap code that wasn't deleted in Task 21.

- [ ] **Step 2: Delete each find one at a time, run tests, commit**

For each dead-code site:

1. Delete it.
2. Run `go build ./...` to find downstream usages.
3. Update those usages to consume from the read service.
4. Run `go test ./...`.
5. Stage + commit when green.

Likely group into one commit if straightforward.

- [ ] **Step 3: Verify the binary doesn't reference aggregate.Tree internally any more**

```bash
go build ./...
grep -rn "aggregate.Tree" packages/pa-monitor/internal/daemon/
```

Only the proto translate code should reference `aggregate.Tree`. Internal state should not.

- [ ] **Step 4: Commit**

```bash
git add packages/pa-monitor/internal/daemon/
git commit -m "refactor(pa-monitor/daemon): remove in-memory aggregate.Tree state fields"
```

---

### Task 26: Delete `runtime.json` types after migration is verified

**Files:**

- Delete: `packages/pa-monitor/internal/daemon/runtime_state.go`
- Delete: `packages/pa-monitor/internal/daemon/runtime_state_test.go`
- Delete: `packages/pa-monitor/internal/daemon/nudger_runtime.go` (if all its readers are gone)
- Modify: `packages/pa-monitor/internal/daemon/lifecycle.go`

- [ ] **Step 1: Audit remaining references to `RuntimeState` / `ReadRuntimeState` / `WriteRuntimeState`**

```bash
grep -rn "RuntimeState\|ReadRuntimeState\|WriteRuntimeState" packages/pa-monitor/
```

Migration code from Task 22 still uses them. Migration runs once per host then `runtime.json` is gone. After all production daemons have run the migration once, the code is dead.

For this task: keep the migration code (Task 22) but inline the `RuntimeState` struct + `ReadRuntimeState` function into `runtime_migration.go` so we can delete the standalone files.

- [ ] **Step 2: Move types into migration file; delete originals**

Move the minimal types needed for migration (just the JSON schema) into `runtime_migration.go`. Delete `runtime_state.go` / `runtime_state_test.go`.

If `nudger_runtime.go` only exists to persist watermarks to runtime.json, delete it — the nudger now records via `NudgeRecorder` (Task 18) instead.

- [ ] **Step 3: Run tests**

```bash
go test ./...
```

Many test files may reference RuntimeState; replace those references with the migrated code path or delete tests that exercised the old runtime.json behaviour directly.

- [ ] **Step 4: Commit**

```bash
git add packages/pa-monitor/
git commit -m "refactor(pa-monitor/daemon): delete runtime.json types; migration owns its schema"
```

---

## Final verification

After all tasks complete:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pa-monitor
go build ./...
go test ./...
```

Both green = plan done. From the parent repo root:

```bash
prek run --all-files
```

Pre-commit clean.

---

## Self-Review Checklist (used by the engineer)

- [ ] Every task has at least one passing test added or modified.
- [ ] No "TODO", "FIXME", placeholder code remains in committed files.
- [ ] `runtime.json` is gone post-deploy; toggles + nudge state live in SQLite.
- [ ] Dead-PID sessions whose `.jsonl` is still on disk DO show up under "All" filter in the TUI.
- [ ] GC sweep runs hourly without disrupting RPC throughput (writer goroutine serialises).
- [ ] `nix build .#pa-monitor` succeeds.
