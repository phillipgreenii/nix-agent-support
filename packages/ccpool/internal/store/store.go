// Package store is ccpool's only SQLite touchpoint: durable session identity
// plus the last turn outcome. Liveness is NOT stored (derived from tmux on read).
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/ccpool/internal/clock"
	_ "modernc.org/sqlite"
)

// State is the last turn outcome. `cold` is intentionally absent — liveness is derived.
type State string

const (
	Starting   State = "starting"
	Ready      State = "ready"
	Working    State = "working"
	NeedsInput State = "needs_input"
	Done       State = "done"
	Failed     State = "failed"
)

// Terminal reports whether s is a settled terminal outcome (for retention §11).
func (s State) Terminal() bool { return s == Done || s == Failed }

type Session struct {
	Name           string
	UUID           string
	CWD            string
	TranscriptPath string
	State          State
	Generation     int64
	CreatedAt      int64
	LastActivityAt int64
	TmuxSession    string
	Model          string
	Flags          string
}

type Store struct {
	db    *sql.DB
	clock clock.Clock
}

// Open opens dbPath (":memory:" for tests) with WAL + busy_timeout and migrates.
func Open(dbPath string, c clock.Clock) (*Store, error) {
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
	return &Store{db: db, clock: c}, nil
}

func (s *Store) Close() error { return s.db.Close() }
