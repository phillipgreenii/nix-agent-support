// Package storetpl hands a test a state directory that already holds a store
// with its schema built, copied from a template built once per test process.
// store.Open on such a directory skips schema creation, so the test pays the
// open of an existing store, not the creation of a new one.
//
// The template is the result of a real store.Open and Close on an empty
// directory, so it is exactly what a fresh open produces (the schema and the
// first epoch record). A test of schema creation itself MUST open an empty
// directory directly instead of using Prepare.
package storetpl

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/tpldir"
)

// layout is the part of store.Options that decides the schema of a NEW index.db.
type layout struct{ v3, noTrigram, ext bool }

var (
	mu        sync.Mutex
	templates = map[layout]*tpldir.Template{}
)

func templateFor(opt store.Options) *tpldir.Template {
	l := layout{opt.V3Layout, opt.NoTrigramIndex || opt.V3Layout, opt.ExtensionTables || opt.V3Layout}
	mu.Lock()
	defer mu.Unlock()
	if tp, ok := templates[l]; ok {
		return tp
	}
	tp := tpldir.New(func(dir string) error {
		// NoCloseStats: a fresh store has no statistics, and so must the template.
		st, err := store.Open(dir, store.Options{V3Layout: opt.V3Layout, NoTrigramIndex: opt.NoTrigramIndex, ExtensionTables: opt.ExtensionTables, NoCloseStats: true})
		if err != nil {
			return err
		}
		return st.Close()
	}, store.LockName)
	templates[l] = tp
	return tp
}

// Prepare restores a ready store directory for opt's layout into dir, unless dir
// already holds a database or a journal (a restart of an existing store is left
// alone). It does not open the store.
func Prepare(t testing.TB, dir string, opt store.Options) {
	t.Helper()
	for _, n := range []string{store.DBName, store.JournalName} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return
		}
	}
	templateFor(opt).Restore(t, dir)
}
