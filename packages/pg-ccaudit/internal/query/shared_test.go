package query

import (
	"context"
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
