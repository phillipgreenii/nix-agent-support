// Package sqliteopen is the one place that decides how this repository's Go
// programs open a SQLite database through the pure-Go modernc driver: the
// connection pragma set, the pool size and the read-only variant. Its standard
// is recorded in the repository's ADR "sqlite stores open through one shared
// helper with one standard pragma set".
//
// Every pragma is carried in the DSN, so the driver applies it on EVERY
// connection the pool opens, not only the first.
package sqliteopen

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// The standard pragma values. Callers override a field of Options only with a
// reason that belongs next to the override.
const (
	// DefaultBusyTimeoutMillis lets several processes (a daemon, the CLI and a
	// test) overlap without "database is locked" errors.
	DefaultBusyTimeoutMillis = 5000
	// DefaultCacheKiB is the per-connection page cache: 64 MiB, so a store of
	// a hundred megabytes stays largely cached instead of being re-read from
	// disk on every pass. SQLite reads a negative cache_size as KiB.
	DefaultCacheKiB = 64 * 1024
	// DefaultMmapBytes lets SQLite map the first 256 MiB of the file, which
	// serves reads without copying pages through the cache.
	DefaultMmapBytes int64 = 256 << 20
	// DefaultJournalSizeLimit truncates the WAL back to 64 MiB after a
	// checkpoint, so one large transaction cannot leave a huge file behind.
	DefaultJournalSizeLimit int64 = 64 << 20
	// DefaultSynchronous is NORMAL: in WAL mode it cannot corrupt the store, and
	// a power loss can lose only the last commits, never earlier ones.
	DefaultSynchronous = "NORMAL"
)

// Options says how to open a store. The zero value of every field except Path
// means "the standard value"; use Standard for the full writer set.
type Options struct {
	// Path is the database file.
	Path string
	// ReadOnly opens an EXISTING file with mode=ro and query_only: it never
	// creates the file or its directory, never changes the journal mode and
	// rejects every write. The pool may hold several connections.
	ReadOnly bool
	// MaxOpenConns caps the pool. 0 means 1 for a writer (modernc serializes
	// writes correctly with one connection) and 3 for a read-only pool.
	MaxOpenConns int
	// BusyTimeoutMillis is the busy_timeout; 0 means DefaultBusyTimeoutMillis.
	BusyTimeoutMillis int
	// Synchronous is the synchronous pragma of a writer; "" means
	// DefaultSynchronous. Ignored when ReadOnly.
	Synchronous string
	// CacheKiB is the page cache in KiB; 0 means DefaultCacheKiB.
	CacheKiB int
	// MmapBytes is mmap_size; 0 means DefaultMmapBytes, negative disables it.
	MmapBytes int64
	// JournalSizeLimit is a writer's journal_size_limit in bytes; 0 means
	// DefaultJournalSizeLimit. Ignored when ReadOnly.
	JournalSizeLimit int64
	// NoForeignKeys leaves foreign key enforcement off; the standard is on.
	NoForeignKeys bool
	// TxLockImmediate adds _txlock=immediate, so a transaction takes the write
	// lock when it begins instead of at its first write. Ignored when ReadOnly.
	TxLockImmediate bool
}

// Standard returns the writer Options for path with every field at its
// standard value.
func Standard(path string) Options { return Options{Path: path} }

// ReadOnly returns the read-only pool Options for path, with conns connections
// (0 means 3).
func ReadOnly(path string, conns int) Options {
	return Options{Path: path, ReadOnly: true, MaxOpenConns: conns}
}

// DSN returns the driver connection string for o.
func DSN(o Options) string {
	busy := o.BusyTimeoutMillis
	if busy == 0 {
		busy = DefaultBusyTimeoutMillis
	}
	cache := o.CacheKiB
	if cache == 0 {
		cache = DefaultCacheKiB
	}
	mmap := o.MmapBytes
	if mmap == 0 {
		mmap = DefaultMmapBytes
	}
	if mmap < 0 {
		mmap = 0
	}
	var q []string
	if o.ReadOnly {
		q = append(q, "mode=ro", "_pragma=query_only(ON)")
	} else {
		sync := o.Synchronous
		if sync == "" {
			sync = DefaultSynchronous
		}
		limit := o.JournalSizeLimit
		if limit == 0 {
			limit = DefaultJournalSizeLimit
		}
		q = append(q, "_pragma=journal_mode(WAL)", "_pragma=synchronous("+sync+")",
			fmt.Sprintf("_pragma=journal_size_limit(%d)", limit))
		if o.TxLockImmediate {
			q = append(q, "_txlock=immediate")
		}
	}
	q = append(
		q,
		fmt.Sprintf("_pragma=busy_timeout(%d)", busy),
		fmt.Sprintf("_pragma=cache_size(-%d)", cache),
		fmt.Sprintf("_pragma=mmap_size(%d)", mmap),
		"_pragma=temp_store(MEMORY)",
	)
	if !o.NoForeignKeys {
		q = append(q, "_pragma=foreign_keys(ON)")
	}
	return "file:" + o.Path + "?" + strings.Join(q, "&")
}

// Open opens the database described by o and pings it, so a path that is not a
// usable database fails here rather than on the caller's first query. A writer
// creates the file's directory; a read-only open of a missing file is an error
// and creates nothing.
func Open(o Options) (*sql.DB, error) {
	if o.Path == "" {
		return nil, fmt.Errorf("sqliteopen: empty path")
	}
	if o.ReadOnly {
		if _, err := os.Stat(o.Path); err != nil {
			return nil, fmt.Errorf("sqliteopen: open read-only %s: %w", o.Path, err)
		}
	} else if err := os.MkdirAll(filepath.Dir(o.Path), 0o755); err != nil {
		return nil, fmt.Errorf("sqliteopen: create dir for %s: %w", o.Path, err)
	}
	db, err := sql.Open("sqlite", DSN(o))
	if err != nil {
		return nil, fmt.Errorf("sqliteopen: open %s: %w", o.Path, err)
	}
	conns := o.MaxOpenConns
	if conns == 0 {
		conns = 1
		if o.ReadOnly {
			conns = 3
		}
	}
	db.SetMaxOpenConns(conns)
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqliteopen: open %s: %w", o.Path, err)
	}
	return db, nil
}

// Pragmas is the effective value of each standard pragma on one connection.
type Pragmas struct {
	JournalMode      string
	Synchronous      int // 0 OFF, 1 NORMAL, 2 FULL, 3 EXTRA
	BusyTimeout      int
	CacheSize        int // negative: KiB
	MmapSize         int64
	JournalSizeLimit int64
	TempStore        int // 0 default, 1 file, 2 memory
	ForeignKeys      bool
	QueryOnly        bool
}

// Effective reads the pragma values in effect on a connection of db. It exists
// so a test can prove the standard set is applied.
func Effective(ctx context.Context, db *sql.DB) (Pragmas, error) {
	var p Pragmas
	var fk, qo int
	for _, r := range []struct {
		name string
		dst  any
	}{
		{"journal_mode", &p.JournalMode},
		{"synchronous", &p.Synchronous},
		{"busy_timeout", &p.BusyTimeout},
		{"cache_size", &p.CacheSize},
		{"mmap_size", &p.MmapSize},
		{"journal_size_limit", &p.JournalSizeLimit},
		{"temp_store", &p.TempStore},
		{"foreign_keys", &fk},
		{"query_only", &qo},
	} {
		if err := db.QueryRowContext(ctx, "PRAGMA "+r.name).Scan(r.dst); err != nil {
			return Pragmas{}, fmt.Errorf("sqliteopen: read pragma %s: %w", r.name, err)
		}
	}
	p.ForeignKeys, p.QueryOnly = fk != 0, qo != 0
	return p, nil
}
