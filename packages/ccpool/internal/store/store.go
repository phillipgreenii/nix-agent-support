// Package store is ccpool's only SQLite touchpoint: durable, observed session
// FACTS — identity plus the last observed turn outcome (ADR 0015). It records
// what Claude reported, never a work-done/failed judgment (that lives in bd).
// Liveness is NOT stored (derived from tmux on read); resumability is a fact
// (does the Claude session still exist on disk), not a stored state.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/eventlog"
	_ "modernc.org/sqlite"
)

// State is the last OBSERVED turn outcome — a session FACT, never a work
// judgment (ADR 0015). `cold` is intentionally absent (liveness is derived) and
// there is NO terminal concept: `idle`/`errored` are just the last thing Claude
// reported (Stop / StopFailure), not "the work finished/failed".
type State string

const (
	Starting   State = "starting"
	Ready      State = "ready"
	Working    State = "working"
	NeedsInput State = "needs_input"
	Idle       State = "idle"    // Claude Stop hook — the turn ended (NOT "work done")
	Errored    State = "errored" // Claude StopFailure hook — the turn hit an API error
)

type Session struct {
	ID              int64  // surrogate PK (autoincrement); assigned by Insert
	ExternalID      string // unique; the caller's handle — sessions are ADDRESSED by this
	ClaudeSessionID string // unique; the Claude session UUID — used to RESUME
	Name            string // optional display label; nullable, NON-unique
	CWD             string
	TranscriptPath  string
	State           State
	Generation      int64
	CreatedAt       int64
	LastActivityAt  int64
	TmuxSession     string
	Model           string
	Flags           string
	// PendingQuestion is the AskUserQuestion text recorded by the `ask` hook while
	// the row is NeedsInput. Transition clears it whenever the row moves to any
	// other state, so it never lingers stale past the turn (pg2-7a5b).
	PendingQuestion string
}

// TurnStatus is a fire-and-forget turn's lifecycle: pending at emit, resolved
// once the Stop hook stamps the transcript anchor (pg2-12ko).
type TurnStatus string

const (
	TurnPending  TurnStatus = "pending"
	TurnResolved TurnStatus = "resolved"
)

// Turn records a fire-and-forget (--no-wait/--queue-message) reply so its result
// can be retrieved later by turn-id. The reply itself is NOT stored: a resolved
// turn carries the transcript anchor, and `ccpool result` resolves the reply
// lazily from that transcript (pg2-12ko).
type Turn struct {
	TurnID         string
	ExternalID     string // keyed to sessions.external_id (ADR 0015)
	Prompt         string
	Status         TurnStatus
	TranscriptPath string
	CreatedAt      int64
	ResolvedAt     int64
}

type Store struct {
	db     *sql.DB
	clock  clock.Clock
	events *eventlog.Logger // optional; nil-safe (a nil Logger's methods no-op)
}

// Option configures a Store at Open time (functional options, so existing
// callers stay source-compatible).
type Option func(*Store)

// WithEventLog wires an append-only event log so every Transition is recorded
// as an ordered state-transition event. l may be nil (the Logger is nil-safe).
func WithEventLog(l *eventlog.Logger) Option {
	return func(s *Store) { s.events = l }
}

// Open opens dbPath (":memory:" for tests) with WAL + busy_timeout and migrates.
func Open(dbPath string, c clock.Clock, opts ...Option) (*Store, error) {
	if dbPath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
			return nil, fmt.Errorf("mkdir db parent: %w", err)
		}
	}
	dsn := dbPath + "?" + url.Values{
		"_pragma": []string{
			"journal_mode(WAL)",
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
	if err := migrate(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	s := &Store{db: db, clock: c}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }
