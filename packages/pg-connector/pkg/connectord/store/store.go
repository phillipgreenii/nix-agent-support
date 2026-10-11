// Package store is the persistent store of a pg-connector daemon: a SQLite
// database (modernc.org/sqlite, the engine of pg-desk's store) in WAL mode
// with versioned migrations applied at open.
//
// The daemon is the store's only writer: process exclusivity is the daemon
// lock's job (the process scaffolding takes it before Open), not this
// package's. Inside the process the handle caps the pool at one connection,
// so writes serialise.
//
// # Open outcomes
//
// Open reports exactly one outcome, in its Result:
//
//   - OutcomeOpened: the store was already at SchemaVersion; nothing ran.
//   - OutcomeMigrated: the store was created or brought up to SchemaVersion
//     by the migration ladder.
//   - OutcomeMovedAside: the file was corrupt, or a migration failed, so it
//     was renamed aside with a timestamp (MovedAsideTo) and a fresh empty
//     store was created. The caller raises its alert and lets the governor
//     limit the refill.
//   - OutcomeRefusedNewer: the file's schema is NEWER than this binary
//     (a downgrade). Open returns a *SchemaNewerError (errors.Is
//     ErrSchemaNewer); the file is never moved and never modified, because
//     moving it aside would cost a full refill and the change log.
//
// Moved-aside stores older than Options.Expiry are deleted at every Open.
//
// # Observing a migration
//
// Open blocks while a migration runs. A caller that must keep answering
// meanwhile (the daemon answers `unavailable` with a `migrating:` message)
// creates a Progress, passes it as Options.Progress, runs Open in a
// goroutine, and reads Progress.Snapshot from its request path.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// busyTimeoutMillis is the SQLite busy_timeout applied on every connection;
// pg-desk's store uses the same value.
const busyTimeoutMillis = 5000

// DefaultExpiry is how long a moved-aside store is kept when Options.Expiry
// is zero. The daemon passes its configured `expiry` instead.
const DefaultExpiry = 24 * time.Hour

// Outcome says what Open did to get a usable store (or why it refused).
type Outcome int

const (
	// OutcomeOpened: the store was already at SchemaVersion.
	OutcomeOpened Outcome = iota + 1
	// OutcomeMigrated: the store was created or migrated up.
	OutcomeMigrated
	// OutcomeMovedAside: a corrupt or unmigratable store was moved aside and
	// a fresh empty store was created.
	OutcomeMovedAside
	// OutcomeRefusedNewer: the store's schema is newer than this binary.
	OutcomeRefusedNewer
)

// String names the outcome for logs and events.
func (o Outcome) String() string {
	switch o {
	case OutcomeOpened:
		return "opened"
	case OutcomeMigrated:
		return "migrated"
	case OutcomeMovedAside:
		return "moved_aside"
	case OutcomeRefusedNewer:
		return "refused_newer"
	default:
		return "unknown"
	}
}

// Result is the typed outcome of Open. It is returned even when Open
// returns an error that is a refusal, so a caller can log the outcome.
type Result struct {
	Outcome Outcome
	// FromVersion is the schema version found in the file that was kept (0
	// for a new file, and for a fresh store created after a move-aside).
	FromVersion int
	// ToVersion is the schema version the returned store is at; for a
	// refusal it is the binary's own SchemaVersion.
	ToVersion int
	// MovedAsideTo is the path the old main file was renamed to (set only
	// for OutcomeMovedAside).
	MovedAsideTo string
	// Cause is why the store was moved aside (set only for
	// OutcomeMovedAside): the corruption or migration error.
	Cause error
	// Pruned lists moved-aside paths deleted because they were older than
	// the expiry.
	Pruned []string
}

// Options tune Open. The zero value is production-ready.
type Options struct {
	// Expiry is how long a moved-aside store is kept; zero means
	// DefaultExpiry.
	Expiry time.Duration
	// Progress, when non-nil, is updated while Open migrates.
	Progress *Progress
	// Now overrides the clock (tests); nil means time.Now.
	Now func() time.Time
	// Migrations overrides the built-in ladder (tests); nil means the
	// built-in one, which ends at SchemaVersion. Versions MUST be 1..n with
	// no gap.
	Migrations []Migration
	// Synchronous, when non-empty, is applied as PRAGMA synchronous=<value>
	// on every connection. Production leaves it empty (SQLite's default,
	// FULL, so every commit is durable); tests may set "OFF".
	Synchronous string
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o Options) expiry() time.Duration {
	if o.Expiry > 0 {
		return o.Expiry
	}
	return DefaultExpiry
}

func (o Options) ladder() []Migration {
	if o.Migrations != nil {
		return o.Migrations
	}
	return builtinMigrations
}

// ErrSchemaNewer is the sentinel every *SchemaNewerError matches via
// errors.Is: the store was written by a newer binary.
var ErrSchemaNewer = errors.New("store: schema is newer than this binary")

// SchemaNewerError is the typed refusal Open returns for a downgrade. The
// daemon logs it clearly, raises its alert and refuses to start.
type SchemaNewerError struct {
	Path string
	// Found is the schema version recorded in the file.
	Found int
	// Supported is the newest version this binary knows.
	Supported int
}

// Error implements error.
func (e *SchemaNewerError) Error() string {
	return fmt.Sprintf("store: %s is at schema version %d but this binary supports up to %d; "+
		"refusing to open (a downgrade); the store is left untouched", e.Path, e.Found, e.Supported)
}

// Is makes errors.Is(err, ErrSchemaNewer) true.
func (e *SchemaNewerError) Is(target error) bool { return target == ErrSchemaNewer }

// Store is an open handle. Close it exactly once at the end of the drain.
type Store struct {
	db      *sql.DB
	path    string
	version int
}

// Open opens (creating if absent) the store at path, applies migrations and
// returns the handle with the typed Result. On a *SchemaNewerError the
// handle is nil and Result.Outcome is OutcomeRefusedNewer. See the package
// comment for the outcomes.
func Open(path string, opts Options) (*Store, Result, error) {
	ladder := opts.ladder()
	if err := validateLadder(ladder); err != nil {
		return nil, Result{}, err
	}
	latest := len(ladder)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, Result{}, fmt.Errorf("store: create dir for %s: %w", path, err)
	}

	res := Result{ToVersion: latest}
	// Prune first so an expired store is deleted even when this open
	// refuses or fails.
	res.Pruned = pruneMovedAside(path, opts.now(), opts.expiry())

	opts.Progress.begin("opening store")
	defer opts.Progress.finish()

	// A downgrade is detected from the file header before SQLite touches
	// the file at all, so a refusal never modifies it (not even by a WAL
	// checkpoint on close). A header that is not SQLite at all is corrupt.
	hdr, err := readHeader(path)
	switch {
	case err != nil:
		return nil, res, fmt.Errorf("store: inspect %s: %w", path, err)
	case hdr.exists && hdr.corrupt:
		return reopenAfterMoveAside(path, opts, res, fmt.Errorf("%s is not a SQLite database", path))
	case hdr.exists && hdr.version > latest:
		return refuseNewer(path, latest, hdr.version, res)
	}

	s, from, err := openAndMigrate(path, opts, ladder)
	var newer *SchemaNewerError
	switch {
	case errors.As(err, &newer):
		// The header was stale (the newer version still sat in the WAL).
		return refuseNewer(path, latest, newer.Found, res)
	case err != nil && isCorruption(err):
		return reopenAfterMoveAside(path, opts, res, err)
	case err != nil && errors.Is(err, errMigrationFailed):
		return reopenAfterMoveAside(path, opts, res, err)
	case err != nil:
		return nil, res, err
	}

	res.FromVersion = from
	if from == latest {
		res.Outcome = OutcomeOpened
	} else {
		res.Outcome = OutcomeMigrated
	}
	return s, res, nil
}

func refuseNewer(path string, supported, found int, res Result) (*Store, Result, error) {
	res.Outcome = OutcomeRefusedNewer
	res.FromVersion = found
	return nil, res, &SchemaNewerError{Path: path, Found: found, Supported: supported}
}

// reopenAfterMoveAside moves the unusable store aside and builds a fresh
// one. A failure here is a real error (the disk, not the data).
func reopenAfterMoveAside(path string, opts Options, res Result, cause error) (*Store, Result, error) {
	to, err := moveAside(path, opts.now())
	if err != nil {
		return nil, res, fmt.Errorf("store: move aside unusable store (%v): %w", cause, err)
	}
	s, from, err := openAndMigrate(path, opts, opts.ladder())
	if err != nil {
		return nil, res, fmt.Errorf("store: create fresh store after moving %s aside to %s (%v): %w",
			path, to, cause, err)
	}
	res.Outcome = OutcomeMovedAside
	res.FromVersion = from
	res.MovedAsideTo = to
	res.Cause = cause
	return s, res, nil
}

// openAndMigrate opens the database with the connection pragmas, checks it
// is a database and brings it to the ladder's top. It returns the schema
// version the file had before migrating.
func openAndMigrate(path string, opts Options, ladder []Migration) (*Store, int, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(%d)&_pragma=foreign_keys(ON)",
		path, busyTimeoutMillis)
	if opts.Synchronous != "" {
		dsn += "&_pragma=synchronous(" + opts.Synchronous + ")"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, 0, fmt.Errorf("store: open %s: %w", path, err)
	}
	// modernc serialises writes correctly with one connection; cap the pool
	// so WAL writers do not contend within the process.
	db.SetMaxOpenConns(1)

	from, err := runMigrations(db, path, opts, ladder)
	if err != nil {
		_ = db.Close()
		return nil, 0, err
	}
	return &Store{db: db, path: path, version: len(ladder)}, from, nil
}

// Path is the main database file's path.
func (s *Store) Path() string { return s.path }

// Version is the schema version the open store is at.
func (s *Store) Version() int { return s.version }

// DB exposes the underlying handle for callers that need a transaction
// spanning several accessors. Reads and writes through it MUST go through
// the single pooled connection (do not call SetMaxOpenConns).
func (s *Store) DB() *sql.DB { return s.db }

// Checkpoint folds the WAL into the main file and truncates it. The daemon
// registers it as the last drain hook, before Close.
func (s *Store) Checkpoint() error {
	if _, err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("store: checkpoint %s: %w", s.path, err)
	}
	return nil
}

// Close closes the handle. SQLite checkpoints and removes the WAL when the
// last connection closes.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("store: close %s: %w", s.path, err)
	}
	return nil
}
