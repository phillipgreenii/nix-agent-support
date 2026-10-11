package store

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	sqlite "modernc.org/sqlite"
)

// Migration is one step of the schema ladder. Versions are 1..n with no
// gap: the migration at index i has Version i+1. The schema version of a
// store is SQLite's PRAGMA user_version, bumped inside the same transaction
// as the step, so a step either fully applies or leaves the version alone.
type Migration struct {
	Version int
	Name    string
	// Apply runs inside a transaction. It MUST NOT commit or roll back, and
	// MUST NOT set user_version. report publishes progress text (visible
	// through Progress while the migration runs).
	Apply func(tx *sql.Tx, report func(text string)) error
}

// errMigrationFailed marks an error from a migration step (as opposed to the
// file being unreadable), so Open moves the store aside for it.
var errMigrationFailed = errors.New("store: migration failed")

// errCorrupt marks a store the integrity check rejected.
var errCorrupt = errors.New("store: database is corrupt")

func validateLadder(l []Migration) error {
	for i, m := range l {
		if m.Version != i+1 {
			return fmt.Errorf("store: migration %d (%q) has version %d; versions must run 1..n with no gap",
				i, m.Name, m.Version)
		}
		if m.Apply == nil {
			return fmt.Errorf("store: migration %d (%q) has no Apply", m.Version, m.Name)
		}
	}
	return nil
}

// Status is a point-in-time view of an Open in progress.
type Status struct {
	// Migrating is true from the start of Open until it returns.
	Migrating bool
	// Text is the latest progress text, for example
	// "applying schema 2 of 3: add change log".
	Text string
}

// Message is the unavailable-answer text: "migrating: <text>". It is empty
// when nothing is migrating.
func (s Status) Message() string {
	if !s.Migrating {
		return ""
	}
	return "migrating: " + s.Text
}

// Progress lets a caller observe Open from another goroutine. Create it
// with NewProgress, pass it as Options.Progress, then call Snapshot from
// the request path while Open runs. All methods are safe for concurrent use
// and a nil *Progress is a valid no-op sink.
type Progress struct {
	mu sync.Mutex
	st Status
}

// NewProgress returns an idle Progress.
func NewProgress() *Progress { return &Progress{} }

// Snapshot returns the current status.
func (p *Progress) Snapshot() Status {
	if p == nil {
		return Status{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st
}

func (p *Progress) begin(text string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.st = Status{Migrating: true, Text: text}
	p.mu.Unlock()
}

func (p *Progress) set(text string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.st.Text = text
	p.mu.Unlock()
}

func (p *Progress) finish() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.st = Status{}
	p.mu.Unlock()
}

// runMigrations reads the file's schema version, refuses a newer one,
// checks integrity of an existing store and applies the missing steps. It
// returns the version the file had on entry.
func runMigrations(db *sql.DB, path string, opts Options, ladder []Migration) (int, error) {
	latest := len(ladder)

	var from int
	if err := db.QueryRow("PRAGMA user_version").Scan(&from); err != nil {
		return 0, fmt.Errorf("store: read schema version of %s: %w", path, err)
	}
	if from > latest {
		// Reached only when the header pre-check saw a stale version (the
		// newer one was still in the WAL).
		return from, &SchemaNewerError{Path: path, Found: from, Supported: latest}
	}
	if from > 0 {
		if err := quickCheck(db); err != nil {
			return from, fmt.Errorf("store: %s: %w", path, err)
		}
	}

	for _, m := range ladder[from:] {
		opts.Progress.set(fmt.Sprintf("applying schema %d of %d: %s", m.Version, latest, m.Name))
		if err := applyOne(db, m, opts.Progress); err != nil {
			return from, fmt.Errorf("%w: schema %d (%s): %w", errMigrationFailed, m.Version, m.Name, err)
		}
	}
	return from, nil
}

func applyOne(db *sql.DB, m Migration, p *Progress) (err error) {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = m.Apply(tx, p.set); err != nil {
		return err
	}
	// PRAGMA does not take bound parameters; Version is an int we control.
	if _, err = tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.Version)); err != nil {
		return err
	}
	return tx.Commit()
}

func quickCheck(db *sql.DB) error {
	rows, err := db.Query("PRAGMA quick_check")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return err
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: quick_check: %s", errCorrupt, problems[0])
	}
	return nil
}

// SQLite primary result codes that mean "the file is not a usable
// database" (https://sqlite.org/rescode.html).
const (
	sqliteCorrupt = 11
	sqliteNotADB  = 26
)

// isCorruption reports whether err says the database file itself is bad.
func isCorruption(err error) bool {
	if errors.Is(err, errCorrupt) {
		return true
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() & 0xff {
		case sqliteCorrupt, sqliteNotADB:
			return true
		}
	}
	return false
}

// header is what the first 100 bytes of the main file say.
type header struct {
	exists  bool // the file exists and is not empty
	corrupt bool // not a SQLite database file
	version int  // PRAGMA user_version recorded in the main file
}

var sqliteMagic = []byte("SQLite format 3\x00")

// readHeader inspects the main file without involving SQLite, so a refusal
// can be decided without opening (and possibly checkpointing) the file. A
// missing or empty file is "does not exist": SQLite will create it.
func readHeader(path string) (header, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return header{}, nil
	}
	if err != nil {
		return header{}, err
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, 100)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return header{}, err
	}
	if n == 0 {
		return header{}, nil
	}
	if n < len(buf) || string(buf[:len(sqliteMagic)]) != string(sqliteMagic) {
		return header{exists: true, corrupt: true}, nil
	}
	// user_version: 4-byte big-endian integer at offset 60.
	return header{exists: true, version: int(binary.BigEndian.Uint32(buf[60:64]))}, nil
}
