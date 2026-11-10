// Package store is pg-desk's SQLite-backed datastore: the system of record
// for gathered facts, derived interpretation, cross-references, human/agent
// annotations, the sync ledger, and store-level metadata (design doc
// section 7.6, docs/superpowers/specs/2026-09-09-pg-desk-and-connector-discovery-design.md
// lines 913-930).
//
// This phase (docket pg2-2j5ac.32, Phase 9, packet 3) implements the SCHEMA
// and per-table writer/reader methods for exactly six tables: entity,
// interpretation, xref, annotation, ledger, meta. It does NOT implement the
// sync logic that populates/consumes the ledger table beyond its bare
// schema (Phase 10), and it does NOT populate xref (Phase 13). No pipeline
// stage in this docket calls the annotation writer — packet 8's CLI
// commands (hide/unhide/wip/feedback set) are its only callers.
//
// # Schema version
//
// A fresh store is created directly at schema version 2, the entity-change-flow
// schema (schema.go). The older version-1 shape, its migration ladder and the
// cutover verb were retired once every store had been cut over: Open refuses a
// store still at version 1 (ErrOldSchema) and a store newer than this binary.
// OpenRaw and OpenReadOnly open a store of any version, for the commands that
// must inspect one, and RequireNewSchema is the guard those handles use.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// busyTimeoutMillis is the SQLite busy_timeout applied on every connection,
// letting `serve`, the CLI, and `run` overlap without "database is locked"
// errors. The design doc's section 7.6 states WAL + a busy timeout but no
// specific value, so this ports the retired PR tool's own store's existing precedent
// (the retired PR tool's internal/store/store.go's Open) verbatim: 5000ms.
const busyTimeoutMillis = 5000

// DefaultPath returns the canonical store file path, honouring
// XDG_STATE_HOME per the design doc's section 7.6 ("SQLite at
// $XDG_STATE_HOME/pg-desk/store.db"). Fallback: ~/.local/state/pg-desk/store.db,
// mirroring the retired PR tool's internal/store.DefaultPath.
func DefaultPath() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "pg-desk", "store.db")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "pg-desk", "store.db")
}

// Store wraps the sql.DB handle plus pg-desk's store operations.
type Store struct {
	sql  *sql.DB
	path string

	// conflicts counts entity writes that lost an optimistic-version race
	// (WriteEntityWithLog returning ErrVersionConflict) on this handle.
	conflicts atomic.Int64

	// betweenBumpAndAppend, when set, runs inside WriteEntityWithLog's
	// transaction after the entity write and before the change_log append;
	// a non-nil return aborts the write. Test seam for the atomicity test.
	betweenBumpAndAppend func() error

	// betweenAnnotationAndAppend, when set, runs inside an annotation write's
	// transaction after the annotation row changed and before the
	// annotation_changed append; a non-nil return aborts the write. Test
	// seam for the annotation atomicity test.
	betweenAnnotationAndAppend func() error

	// consumerLocker overrides the Locker LockConsumer uses; nil means a
	// default Locker (runtime-dir lock files). Tests set it via
	// SetConsumerLockerOptions to point at a temp dir.
	consumerLocker *Locker

	// gateScope is the delivery gate's scope predicate (SetGateScope); nil
	// means no gate, so nothing is withheld.
	gateScope atomic.Pointer[GateScope]

	// interpStamp is the stamp the handle writes into interpretation rows and
	// compares against (SetInterpStamp); nil means none. Atomic, like
	// gateScope, because the handle is shared.
	interpStamp atomic.Pointer[string]

	// interpWriteHook, when set, is consulted before each step of the
	// interpretation write transactions (SetInterpretationWriteHook). TEST
	// SEAM, nil in production.
	interpWriteHook func(step InterpretationWriteStep) error

	// faultTableColumns, when set, is consulted at the start of tableColumns;
	// a non-nil return is that call's error. TEST SEAM, nil in production:
	// the pragma read cannot otherwise fail on an open handle.
	faultTableColumns func(table string) error
}

// synchronousPragma, when non-empty, is applied as `PRAGMA synchronous=<value>`
// immediately after open, before the WAL conversion and before the schema check, so
// it governs every fsync those steps would otherwise perform.
//
// Production leaves it EMPTY, so SQLite keeps its default (FULL) and every
// commit to the store is durably flushed. Tests set it to "OFF" via
// SetSynchronousForTests (directly, or transitively through OpenForTest),
// mirroring the retired PR tool's internal/store — durability is meaningless for a
// database deleted at test exit, and skipping the fsyncs keeps a package
// with many tests well inside go test's default timeout.
var synchronousPragma string

// SetSynchronousForTests sets the synchronous pragma applied by Open. Exists
// as the cross-package seam for this package's own tests.
func SetSynchronousForTests(v string) {
	synchronousPragma = v
}

// openDB opens (creating if absent) the SQLite database at path, creating
// its parent directory if needed, and applies the connection pragmas (WAL
// mode, a busy timeout, foreign keys on). It runs no migration and checks
// no schema version: Open and OpenRaw differ only in what they do next.
// The modernc driver name is "sqlite".
func openDB(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: create dir for %s: %w", path, err)
	}

	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(%d)&_pragma=foreign_keys(ON)",
		path, busyTimeoutMillis)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// modernc serializes writes correctly with a single connection; cap the
	// pool so WAL writers don't contend within one process.
	sqlDB.SetMaxOpenConns(1)
	if synchronousPragma != "" {
		if _, err := sqlDB.Exec("PRAGMA synchronous=" + synchronousPragma); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("store: set synchronous %s: %w", path, err)
		}
	}
	return &Store{sql: sqlDB, path: path}, nil
}

// Open opens (creating if absent) the SQLite database at path, applies the
// connection pragmas and checks the schema (schema.go): a new file is created
// at schema version 2, a version-2 store is accepted, and a store at the
// retired version 1 or at a version newer than this binary is refused.
func Open(path string) (*Store, error) {
	s, err := openDB(path)
	if err != nil {
		return nil, err
	}
	if err := checkSchema(s); err != nil {
		_ = s.sql.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := ensureIssueFeedStorage(s); err != nil {
		_ = s.sql.Close()
		return nil, fmt.Errorf("store: ensure issue feed storage %s: %w", path, err)
	}
	if err := ensureInterpretationStamp(s); err != nil {
		_ = s.sql.Close()
		return nil, fmt.Errorf("store: ensure interpretation stamp %s: %w", path, err)
	}
	return s, nil
}

// OpenRaw opens the database at path with the connection pragmas but with
// NO schema creation and NO schema-version gate, so it opens a store of any
// version (including one Open would refuse, and a file with no schema at
// all, which it leaves empty). It exists for the callers that must inspect a
// store whatever state it is in: `doctor` and `status`. Everything else opens
// through Open.
//
// A caller of OpenRaw is responsible for tolerating the schema it finds:
// SchemaVersion reports which one that is.
func OpenRaw(path string) (*Store, error) {
	s, err := openDB(path)
	if err != nil {
		return nil, err
	}
	// sql.Open is lazy; touch the file now so a path that is not a usable
	// database fails here rather than on the caller's first query.
	if err := s.sql.Ping(); err != nil {
		_ = s.sql.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	return s, nil
}

// OpenReadOnly opens an EXISTING store for reading only: it never creates
// the file or its directory, never runs a migration, never changes the
// journal mode, and the handle rejects every write (SQLite's read-only open
// mode plus query_only). It opens a store of any schema version, like
// OpenRaw; the caller tolerates whatever shape it finds. A missing file is
// an error. It exists for the read-only verbs (`links`), whose contract is
// that they MUST NOT write the store.
func OpenReadOnly(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("store: open read-only %s: %w", path, err)
	}
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(%d)&_pragma=query_only(ON)", path, busyTimeoutMillis)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open read-only %s: %w", path, err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("store: open read-only %s: %w", path, err)
	}
	return &Store{sql: sqlDB, path: path}, nil
}

// Path returns the database file the store was opened on, so a caller that
// needs a second handle on the same file (concurrent writers in a test) can
// open one.
func (s *Store) Path() string { return s.path }

// Close closes the underlying handle.
func (s *Store) Close() error { return s.sql.Close() }

// testT is the part of *testing.T (or testing.TB) the open helpers use.
type testT interface {
	TempDir() string
	Cleanup(func())
	Fatalf(string, ...any)
}

// OpenForTest opens an in-temp-dir store for tests, registering cleanup. The
// store is created at schema version 2.
func OpenForTest(t testT) *Store {
	SetSynchronousForTests("OFF")
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenForTest: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// OpenNewSchemaForTest is OpenForTest, kept under its older name.
func OpenNewSchemaForTest(t testT) *Store { return OpenForTest(t) }

// ErrOldSchema is wrapped by RequireNewSchema's and Open's error, so a caller
// can tell "this store is on the retired schema" from any other failure.
var ErrOldSchema = errors.New("store is on the retired schema")

// SchemaVersion returns the store's schema version: SQLite's user_version,
// which is the source of truth (meta.schema_version mirrors it for
// `status`). 0 means the file holds no schema at all.
func (s *Store) SchemaVersion() (int, error) {
	var v int
	if err := s.sql.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("store: read user_version: %w", err)
	}
	return v, nil
}

// RequireNewSchema returns nil when the store is on schema version 2 (or
// later), and otherwise an error wrapping ErrOldSchema. Open never returns a
// handle on an older store, so the guard matters for the handles that do
// (OpenRaw, OpenReadOnly): every command that needs the current schema calls
// it right after opening the store.
func (s *Store) RequireNewSchema() error {
	v, err := s.SchemaVersion()
	if err != nil {
		return err
	}
	if v < schemaVersion {
		return fmt.Errorf("%w (schema version %d, need %d): %s", ErrOldSchema, v, schemaVersion, oldSchemaAdvice)
	}
	return nil
}
