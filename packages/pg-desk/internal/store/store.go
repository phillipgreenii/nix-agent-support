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
// commands (hide/unhide/wip/feedback set) and packet 9's
// import-pg-pr-annotations command are its only callers.
//
// # Two schema versions
//
// The store exists in two shapes. Version 1 is the six tables above and is
// what Open builds. Version 2 is the entity-change-flow schema: entity gains
// version/hydrated_at/active, change_log and consumer are added, annotation
// becomes key/value, xref gains origin/relation, and interpretation.sync_error
// and the ledger table are gone. A store moves from 1 to 2 only through the
// explicit Cutover (cutover.go, `pg-desk migrate --cutover`), never through
// Open. Until the old code paths are deleted, Open accepts either version,
// the entity/interpretation/xref accessors work on both (schema-dual), and
// commands that need version 2 call RequireNewSchema. OpenRaw opens a store
// of any version for the commands that must inspect or repair it.
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
// specific value, so this ports pg-pr's own store's existing precedent
// (packages/pg-pr/internal/store/store.go's Open) verbatim: 5000ms.
const busyTimeoutMillis = 5000

// DefaultPath returns the canonical store file path, honouring
// XDG_STATE_HOME per the design doc's section 7.6 ("SQLite at
// $XDG_STATE_HOME/pg-desk/store.db"). Fallback: ~/.local/state/pg-desk/store.db,
// mirroring pg-pr's internal/store.DefaultPath.
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
}

// synchronousPragma, when non-empty, is applied as `PRAGMA synchronous=<value>`
// immediately after open, before the WAL conversion and before migrate, so
// it governs every fsync those steps would otherwise perform.
//
// Production leaves it EMPTY, so SQLite keeps its default (FULL) and every
// commit to the store is durably flushed. Tests set it to "OFF" via
// SetSynchronousForTests (directly, or transitively through OpenForTest),
// mirroring pg-pr's internal/store — durability is meaningless for a
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
// connection pragmas, and runs the migration ladder (migrations.go). It
// accepts a store at schema version 1 or 2 and NEVER runs the schema
// cutover: that is an explicit, separate step (Cutover, cutover.go).
func Open(path string) (*Store, error) {
	s, err := openDB(path)
	if err != nil {
		return nil, err
	}
	if err := migrate(s); err != nil {
		_ = s.sql.Close()
		return nil, fmt.Errorf("store: migrate %s: %w", path, err)
	}
	return s, nil
}

// OpenRaw opens the database at path with the connection pragmas but with
// NO migrations and NO schema-version gate, so it opens a store of any
// version (including one Open would refuse, and a file with no schema at
// all). It exists for the callers that must inspect or repair a store
// whatever state it is in: `pg-desk migrate --cutover`, `doctor` and
// `status`. Everything else opens through Open.
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

// Close closes the underlying handle.
func (s *Store) Close() error { return s.sql.Close() }

// OpenForTest opens an in-temp-dir store for tests, registering cleanup.
// The store is at the OLD schema (version 1); use OpenNewSchemaForTest for
// one that has already been cut over.
func OpenForTest(t interface {
	TempDir() string
	Cleanup(func())
	Fatalf(string, ...any)
},
) *Store {
	SetSynchronousForTests("OFF")
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenForTest: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// OpenNewSchemaForTest is OpenForTest for a store that has already been cut
// over to the new schema (version 2): change_log, consumer and the
// key/value annotation exist. It takes the same t parameter shape as
// OpenForTest.
func OpenNewSchemaForTest(t interface {
	TempDir() string
	Cleanup(func())
	Fatalf(string, ...any)
},
) *Store {
	s := OpenForTest(t)
	if err := s.Cutover(); err != nil {
		t.Fatalf("OpenNewSchemaForTest: cutover: %v", err)
	}
	return s
}

// ErrOldSchema is wrapped by RequireNewSchema's error, so a caller can
// tell "this store has not been cut over yet" from any other failure.
var ErrOldSchema = errors.New("store is on the old schema")

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

// RequireNewSchema returns nil when the store has been cut over to the new
// schema (version 2 or later), and otherwise an error wrapping ErrOldSchema
// that says how to fix it ("run pg-desk migrate --cutover"). Every command
// that needs the new schema calls it right after opening the store.
func (s *Store) RequireNewSchema() error {
	v, err := s.SchemaVersion()
	if err != nil {
		return err
	}
	if v < NewSchemaVersion {
		return fmt.Errorf("%w (schema version %d, need %d): run pg-desk migrate --cutover", ErrOldSchema, v, NewSchemaVersion)
	}
	return nil
}

// isNewSchema reports whether the store is on the new schema. The
// schema-dual accessors (entity, interpretation, xref) ask it on every call
// rather than caching the answer at Open, so a handle that was opened
// before a cutover follows the store to its new shape. It MUST be called
// before a query is issued, never while a result set is still open: the
// pool holds one connection.
func (s *Store) isNewSchema() (bool, error) {
	v, err := s.SchemaVersion()
	if err != nil {
		return false, err
	}
	return v >= NewSchemaVersion, nil
}
