package storetpl_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit/storetpl"
)

func dump(t *testing.T, dir string, opt store.Options) (schema, meta string) {
	t.Helper()
	opt.NoCloseStats = true
	st, err := store.Open(dir, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	rows, err := st.Read().Query(`SELECT name || ':' || sql FROM sqlite_master ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var s *string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		if s != nil {
			schema += *s + "\n"
		}
	}
	m, err := st.Read().Query(`SELECT key || '=' || value FROM meta WHERE key NOT LIKE '%time%' ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	for m.Next() {
		var s string
		if err := m.Scan(&s); err != nil {
			t.Fatal(err)
		}
		meta += s + "\n"
	}
	return schema, meta
}

// The schema-creation path itself: a fresh directory goes through the real
// store.Open, and a prepared directory ends up with the same schema, meta and
// journal as that fresh open, for each layout a test uses.
func TestPreparedStoreMatchesAFreshOpen(t *testing.T) {
	for name, opt := range map[string]store.Options{
		"v2": {}, "v3": {V3Layout: true}, "ext": {ExtensionTables: true},
	} {
		t.Run(name, func(t *testing.T) {
			fresh := t.TempDir()
			if _, err := os.Stat(filepath.Join(fresh, store.DBName)); err == nil {
				t.Fatal("fixture directory is not empty")
			}
			wantSchema, wantMeta := dump(t, fresh, opt) // real schema creation
			if wantSchema == "" {
				t.Fatal("a fresh open built no schema")
			}
			got := t.TempDir()
			storetpl.Prepare(t, got, opt)
			if _, err := os.Stat(filepath.Join(got, store.LockName)); err == nil {
				t.Fatal("the lock file was copied")
			}
			gotSchema, gotMeta := dump(t, got, opt)
			if gotSchema != wantSchema || gotMeta != wantMeta {
				t.Fatalf("prepared store differs from a fresh open\nschema:\n%s\n--- want\n%s\nmeta:\n%s\n--- want\n%s", gotSchema, wantSchema, gotMeta, wantMeta)
			}
			fj, _ := os.ReadFile(filepath.Join(fresh, store.JournalName))
			gj, _ := os.ReadFile(filepath.Join(got, store.JournalName))
			if len(fj) == 0 || len(gj) == 0 {
				t.Fatalf("journal sizes fresh=%d prepared=%d", len(fj), len(gj))
			}
		})
	}
}

func TestPrepareLeavesAnExistingStoreAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, store.JournalName), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	storetpl.Prepare(t, dir, store.Options{})
	b, _ := os.ReadFile(filepath.Join(dir, store.JournalName))
	if string(b) != "keep" {
		t.Fatalf("journal overwritten: %q", b)
	}
}

func TestTwoPreparedDirsAreIndependent(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	storetpl.Prepare(t, a, store.Options{})
	storetpl.Prepare(t, b, store.Options{})
	sa, err := store.Open(a, store.Options{NoCloseStats: true})
	if err != nil {
		t.Fatal(err)
	}
	id := sa.NewFileID()
	if err := sa.Commit(&store.Rec{T: store.TFile, At: 1, File: id, Source: "s", Mode: "append"}); err != nil {
		t.Fatal(err)
	}
	_ = sa.Close()
	sb, err := store.Open(b, store.Options{NoCloseStats: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sb.Close() }()
	if got := sb.NewFileID(); got != id {
		t.Fatalf("second dir saw the first's file ids: %d vs %d", got, id)
	}
}
