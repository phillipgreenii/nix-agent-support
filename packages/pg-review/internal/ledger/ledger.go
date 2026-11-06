// Package ledger is pg-review's SQLite store: the single source of truth for
// findings, rulings and history. It owns the schema, the forward-only
// migrations, the durability functions (integrity check, JSONL export, backup,
// append-only rulings log), the run-lock table and the lens-pause query.
//
// The package returns sentinel errors; callers map them to exit codes.
package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	sqlite "modernc.org/sqlite"
)

// BusyTimeoutMillis is the SQLite busy timeout applied to every connection.
const BusyTimeoutMillis = 5000

// Sentinel errors.
var (
	// ErrNotFound means a looked-up row does not exist.
	ErrNotFound = errors.New("ledger: not found")
	// ErrDuplicate means a unique index rejected an insert.
	ErrDuplicate = errors.New("ledger: duplicate")
	// ErrLockBusy means the (lens, scope) run lock is held.
	ErrLockBusy = errors.New("ledger: run lock busy")
	// ErrIntegrity means the integrity check failed.
	ErrIntegrity = errors.New("ledger: integrity check failed")
	// ErrSchemaTooNew means the database was written by a newer release.
	ErrSchemaTooNew = errors.New("ledger: database schema is newer than this release")
	// ErrInvalid means an argument had an unacceptable value.
	ErrInvalid = errors.New("ledger: invalid argument")
)

// Ledger is an open ledger database.
type Ledger struct {
	db   *sql.DB
	path string
	now  func() time.Time
}

// StateDir returns the pg-review state directory: $XDG_STATE_HOME/pg-review,
// or $HOME/.local/state/pg-review when XDG_STATE_HOME is unset.
func StateDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home := os.Getenv("HOME")
		if home == "" {
			return "", fmt.Errorf("%w: neither XDG_STATE_HOME nor HOME is set", ErrInvalid)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "pg-review"), nil
}

// DefaultPath returns the default ledger database path.
func DefaultPath() (string, error) {
	d, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "ledger.db"), nil
}

// Open opens (creating and migrating when needed) the ledger at the default path.
func Open(ctx context.Context) (*Ledger, error) {
	p, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	return OpenPath(ctx, p)
}

// OpenPath opens the ledger at path. The directory is created or tightened to
// mode 0700 and the file to 0600.
func OpenPath(ctx context.Context, path string) (*Ledger, error) {
	return openWith(ctx, path, migrations)
}

func openWith(ctx context.Context, path string, migs []migration) (*Ledger, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", BusyTimeoutMillis))
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+(&url.URL{Path: path}).EscapedPath()+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	l := &Ledger{db: db, path: path, now: time.Now}
	if err := l.migrate(ctx, migs); err != nil {
		_ = db.Close()
		return nil, err
	}
	return l, nil
}

// Close closes the database.
func (l *Ledger) Close() error { return l.db.Close() }

// Path returns the database file path.
func (l *Ledger) Path() string { return l.path }

// SetClock replaces the clock (tests).
func (l *Ledger) SetClock(now func() time.Time) { l.now = now }

// DB exposes the underlying handle for read-only inspection by tests.
func (l *Ledger) DB() *sql.DB { return l.db }

// Tx is a BEGIN IMMEDIATE transaction on one dedicated connection.
type Tx struct {
	conn *sql.Conn
	l    *Ledger
}

// WithTx runs fn inside a BEGIN IMMEDIATE transaction. A returned error or a
// panic rolls back; nil commits.
func (l *Ledger) WithTx(ctx context.Context, fn func(*Tx) error) (err error) {
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := fn(&Tx{conn: conn, l: l}); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	done = true
	return nil
}

func (t *Tx) exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	r, err := t.conn.ExecContext(ctx, q, args...)
	return r, mapErr(err)
}

func (t *Tx) queryRow(ctx context.Context, q string, args ...any) *sql.Row {
	return t.conn.QueryRowContext(ctx, q, args...)
}

func (t *Tx) query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return t.conn.QueryContext(ctx, q, args...)
}

// nextSeq returns the next ledger-wide monotonic sequence number, the "since"
// ordering used by the pause query.
func (t *Tx) nextSeq(ctx context.Context) (int64, error) {
	if _, err := t.exec(ctx, `INSERT INTO meta(key,value) VALUES('seq','1')
		ON CONFLICT(key) DO UPDATE SET value = CAST(value AS INTEGER) + 1`); err != nil {
		return 0, err
	}
	var n int64
	err := t.queryRow(ctx, `SELECT CAST(value AS INTEGER) FROM meta WHERE key='seq'`).Scan(&n)
	return n, err
}

func (t *Tx) now() time.Time { return t.l.now().UTC() }

const timeLayout = "2006-01-02T15:04:05.000000000Z"

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timeLayout)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// mapErr turns a SQLite constraint failure into ErrDuplicate.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code()&0xff == 19 {
		return fmt.Errorf("%w: %v", ErrDuplicate, err)
	}
	return err
}
