package query

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
)

// Each fixture corpus is built once per test process and shared read-only by
// every test that needs it: building one (a temp directory, a real SQLite file,
// the production schema, an ingest) costs far more than the queries run over
// it, and every handle the builders return is read-only, so no test can change
// what another sees. A test that mutates its database MUST build its own.

var (
	sharedMu      sync.Mutex
	sharedEntries = map[string]*sharedEntry{}
	sharedDir     string
	sharedClosers []func()
)

type sharedEntry struct {
	once sync.Once
	val  any
}

// sharedTB lends a builder the test it was first requested from for failures
// only: the temp directory, the context and the cleanups outlive that test and
// are released by TestMain.
type sharedTB struct {
	testing.TB
	dir string
}

func (s sharedTB) TempDir() string {
	d, err := os.MkdirTemp(s.dir, "fx")
	if err != nil {
		s.Fatalf("shared temp dir: %v", err)
	}
	return d
}

func (sharedTB) Context() context.Context { return context.Background() }

func (sharedTB) Cleanup(f func()) {
	sharedMu.Lock()
	sharedClosers = append(sharedClosers, f)
	sharedMu.Unlock()
}

// shared returns the value build produced for key, building it on first use.
func shared[T any](t testing.TB, key string, build func(testing.TB) T) T {
	t.Helper()
	sharedMu.Lock()
	e := sharedEntries[key]
	if e == nil {
		e = &sharedEntry{}
		sharedEntries[key] = e
	}
	if sharedDir == "" {
		d, err := os.MkdirTemp("", "pg-ccaudit-query-shared")
		if err != nil {
			sharedMu.Unlock()
			t.Fatalf("shared dir: %v", err)
		}
		sharedDir = d
	}
	dir := sharedDir
	sharedMu.Unlock()
	e.once.Do(func() { e.val = build(sharedTB{TB: t, dir: dir}) })
	v, ok := e.val.(T)
	if !ok {
		t.Fatalf("shared fixture %q failed to build", key)
	}
	return v
}

// runOnce is Run with each distinct (database, request) executed once per test
// process: under the race detector the cost of a canned query is dominated by
// SQLite preparing its large statement text, and several tests and subtests ask
// the same question of the same shared read-only database. The caller MUST NOT
// modify the returned rows.
func runOnce(t testing.TB, db *sql.DB, req Request) *Result {
	t.Helper()
	key := fmt.Sprintf("%p|%s|%q|%q|%q|%s|%q|%q|%s|%s", db, req.Query.Name, req.Args, req.Since, req.Until, req.Format, req.CwdPrefixes, req.ExcludeCwdPrefixes, req.Query.SQL, req.Query.UnboundedSQL)
	runMu.Lock()
	e := runCache[key]
	if e == nil {
		e = &runEntry{db: db}
		runCache[key] = e
	}
	runMu.Unlock()
	e.once.Do(func() { e.res, e.err = Run(context.Background(), db, req) })
	if e.err != nil {
		t.Fatalf("%s: %v", req.Query.Name, e.err)
	}
	return e.res
}

var (
	runMu    sync.Mutex
	runCache = map[string]*runEntry{}
)

type runEntry struct {
	once sync.Once
	res  *Result
	err  error
	// db keeps the handle reachable for as long as its key is cached. The key
	// embeds the handle's address, and a per-test database that was garbage
	// collected could otherwise have its address reused by a later one, which
	// would then be served the earlier database's cached rows.
	db *sql.DB
}

func TestMain(m *testing.M) {
	code := m.Run()
	sharedMu.Lock()
	for i := len(sharedClosers) - 1; i >= 0; i-- {
		sharedClosers[i]()
	}
	if sharedDir != "" {
		_ = os.RemoveAll(sharedDir)
	}
	sharedMu.Unlock()
	os.Exit(code)
}
