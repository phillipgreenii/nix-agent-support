package settle

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
)

// oldChangedSinceSettle is the previous implementation, kept as the oracle: it
// reads every generation row of the whole store through AllGenerations.
func oldChangedSinceSettle(st *store.Store, c *conv) bool {
	sr, ok, err := st.SettledOf(c.session)
	if err != nil || !ok {
		return false
	}
	var m store.Manifest
	if json.Unmarshal(sr.Manifest, &m) != nil {
		return true
	}
	byID := map[int64]store.ManifestFile{}
	for _, f := range m.Files {
		byID[f.File] = f
	}
	gens, err := st.AllGenerations()
	if err != nil {
		return false
	}
	cur := map[[2]int64]int64{}
	for _, g := range gens {
		cur[[2]int64{g.File, int64(g.Gen)}] = g.CopyOffset
	}
	for _, f := range c.files {
		mf, ok := byID[f.ID]
		if !ok {
			return true
		}
		if f.Mode == "snapshot" {
			if f.CurGen != mf.Gen || f.LastSize != mf.Size {
				return true
			}
			continue
		}
		if f.CurGen != mf.Gen || cur[[2]int64{f.ID, int64(f.CurGen)}] != mf.Size {
			return true
		}
	}
	return false
}

type csEnv struct {
	t   *testing.T
	st  *store.Store
	s   *Settler
	cnt *countdriver.Counter
	n   int
}

var csDriverSeq int

func newCSEnv(t *testing.T) *csEnv {
	t.Helper()
	csDriverSeq++
	name := fmt.Sprintf("settle-changed-since-%d", csDriverSeq)
	cnt := countdriver.Register(name)
	st, err := store.Open(t.TempDir(), store.Options{DriverName: name})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &csEnv{t: t, st: st, s: New(Config{Store: st}), cnt: cnt}
}

// appendFile commits an append-mode file with one generation (gen) whose copy
// offset is off, and returns its file row.
func (e *csEnv) appendFile(gen int, off int64) store.File {
	e.t.Helper()
	id := e.st.NewFileID()
	e.n++
	recs := []*store.Rec{
		{T: store.TFile, At: 10, File: id, Source: "claude-transcript", Mode: "append", Dev: 1, Ino: uint64(e.n), Birth: 3},
		{T: store.TPath, At: 10, File: id, Path: fmt.Sprintf("/c/projects/p/f%d.jsonl", e.n)},
	}
	for g := 1; g <= gen; g++ {
		recs = append(recs, &store.Rec{T: store.TGen, At: 11, File: id, Gen: g, Method: "bytes", Reason: "first"})
	}
	if off > 0 {
		recs = append(recs, &store.Rec{T: store.TCommit, At: 12, File: id, Gen: gen, From: 0, To: off, Size: off, Tail: 1, TailLen: 1})
	}
	if err := e.st.Commit(recs...); err != nil {
		e.t.Fatal(err)
	}
	return store.File{ID: id, Mode: "append", CurGen: gen, LastSize: off}
}

// missingGen is an append file whose current generation has no row.
func (e *csEnv) missingGen(gen int) store.File {
	return store.File{ID: e.st.NewFileID(), Mode: "append", CurGen: gen}
}

func (e *csEnv) settle(session string, files ...store.ManifestFile) {
	e.t.Helper()
	m, _ := json.Marshal(store.Manifest{Session: session, Method: "bytes", Files: files})
	e.settleRaw(session, m)
}

func (e *csEnv) settleRaw(session string, manifest []byte) {
	e.t.Helper()
	val, _ := json.Marshal(store.SettleRec{Manifest: manifest})
	if err := e.st.Commit(&store.Rec{T: store.TSettle, At: 20, Session: session, Method: "bytes", Val: string(val)}); err != nil {
		e.t.Fatal(err)
	}
}

func mf(f store.File, gen int, size int64) store.ManifestFile {
	return store.ManifestFile{File: f.ID, Mode: f.Mode, Gen: gen, Size: size}
}

func dropTable(t *testing.T, st *store.Store, table string) {
	t.Helper()
	raw, err := sql.Open("sqlite", filepath.Join(st.Dir(), store.DBName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`DROP TABLE ` + table); err != nil {
		t.Fatal(err)
	}
}

func TestChangedSinceSettleEquivalentToTheAllGenerationsOracle(t *testing.T) {
	type fixture struct {
		name string
		want bool
		make func(e *csEnv) *conv
	}
	snap := func(gen int, size int64) store.File {
		return store.File{ID: 9001, Mode: "snapshot", CurGen: gen, LastSize: size}
	}
	fixtures := []fixture{
		{"unlisted file", true, func(e *csEnv) *conv {
			a, b := e.appendFile(1, 10), e.appendFile(1, 5)
			e.settle("s", mf(a, 1, 10))
			return &conv{session: "s", files: []store.File{a, b}}
		}},
		{"snapshot different gen", true, func(e *csEnv) *conv {
			e.settle("s", store.ManifestFile{File: 9001, Mode: "snapshot", Gen: 1, Size: 7})
			return &conv{session: "s", files: []store.File{snap(2, 7)}}
		}},
		{"snapshot different size", true, func(e *csEnv) *conv {
			e.settle("s", store.ManifestFile{File: 9001, Mode: "snapshot", Gen: 1, Size: 7})
			return &conv{session: "s", files: []store.File{snap(1, 8)}}
		}},
		{"snapshot equal", false, func(e *csEnv) *conv {
			e.settle("s", store.ManifestFile{File: 9001, Mode: "snapshot", Gen: 1, Size: 7})
			return &conv{session: "s", files: []store.File{snap(1, 7)}}
		}},
		{"append different gen", true, func(e *csEnv) *conv {
			a := e.appendFile(2, 10)
			e.settle("s", mf(a, 1, 10))
			return &conv{session: "s", files: []store.File{a}}
		}},
		{"append different offset", true, func(e *csEnv) *conv {
			a := e.appendFile(1, 12)
			e.settle("s", mf(a, 1, 10))
			return &conv{session: "s", files: []store.File{a}}
		}},
		{"append equal", false, func(e *csEnv) *conv {
			a := e.appendFile(1, 10)
			e.settle("s", mf(a, 1, 10))
			return &conv{session: "s", files: []store.File{a}}
		}},
		{"no generation row equals offset zero", false, func(e *csEnv) *conv {
			a := e.missingGen(1)
			e.settle("s", mf(a, 1, 0))
			return &conv{session: "s", files: []store.File{a}}
		}},
		{"no generation row against a nonzero manifest size", true, func(e *csEnv) *conv {
			a := e.missingGen(1)
			e.settle("s", mf(a, 1, 5))
			return &conv{session: "s", files: []store.File{a}}
		}},
		{"manifest fails to unmarshal", true, func(e *csEnv) *conv {
			a := e.appendFile(1, 10)
			e.settleRaw("s", []byte(`"not an object"`))
			return &conv{session: "s", files: []store.File{a}}
		}},
		{"no settled row", false, func(e *csEnv) *conv {
			a := e.appendFile(1, 10)
			return &conv{session: "s", files: []store.File{a}}
		}},
	}
	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			e := newCSEnv(t)
			c := fx.make(e)
			old := oldChangedSinceSettle(e.st, c)
			got := e.s.changedSinceSettle(c)
			if got != old {
				t.Fatalf("new %v, oracle %v", got, old)
			}
			if got != fx.want {
				t.Fatalf("got %v, want %v", got, fx.want)
			}
		})
	}

	// Store.Close leaves nil handles (a query after it panics), so the store
	// error is induced by dropping the settled table out from under it.
	t.Run("store error on the settled read is unchanged", func(t *testing.T) {
		e := newCSEnv(t)
		a := e.appendFile(1, 12)
		e.settle("s", mf(a, 1, 10))
		c := &conv{session: "s", files: []store.File{a}}
		dropTable(t, e.st, "settled")
		if old, got := oldChangedSinceSettle(e.st, c), e.s.changedSinceSettle(c); old || got {
			t.Fatalf("new %v, oracle %v, want both false", got, old)
		}
	})
}

// A store whose generations table cannot be read (SettledOf still works): the
// old code returned false before comparing anything; per-file reads may report
// a mismatch found before the failing read. That ordering difference is
// accepted; with no earlier mismatch both return false.
func TestChangedSinceSettleStoreErrorOrderingDifference(t *testing.T) {
	build := func(t *testing.T, firstMismatch bool) (*csEnv, *conv) {
		e := newCSEnv(t)
		a := e.appendFile(1, 10)
		b := e.appendFile(1, 10)
		first := mf(a, 1, 10)
		if firstMismatch {
			first.Gen = 7 // differs from a.CurGen: found before any Generation read
		}
		e.settle("s", first, mf(b, 1, 10))
		dropTable(t, e.st, "generations")
		return e, &conv{session: "s", files: []store.File{a, b}}
	}
	t.Run("no earlier mismatch: both false", func(t *testing.T) {
		e, c := build(t, false)
		if old, got := oldChangedSinceSettle(e.st, c), e.s.changedSinceSettle(c); old || got {
			t.Fatalf("new %v, oracle %v", got, old)
		}
	})
	t.Run("earlier mismatch: old false, new true", func(t *testing.T) {
		e, c := build(t, true)
		if old, got := oldChangedSinceSettle(e.st, c), e.s.changedSinceSettle(c); old || !got {
			t.Fatalf("new %v, oracle %v, want new true and oracle false", got, old)
		}
	})
}

func TestChangedSinceSettleSnapshotFilesReadNoGeneration(t *testing.T) {
	e := newCSEnv(t)
	e.settle("s", store.ManifestFile{File: 9001, Mode: "snapshot", Gen: 1, Size: 7})
	c := &conv{session: "s", files: []store.File{{ID: 9001, Mode: "snapshot", CurGen: 1, LastSize: 7}}}
	e.cnt.Reset()
	if e.s.changedSinceSettle(c) {
		t.Fatal("changed")
	}
	if q := e.cnt.Queries(); q != 1 {
		t.Fatalf("queries = %d, want 1 (the settled read only)", q)
	}
}

func TestChangedSinceSettleWorkIsBoundedByTheConversation(t *testing.T) {
	measure := func(t *testing.T, unrelated int) (rows, queries int64, nAppend int) {
		e := newCSEnv(t)
		var files []store.File
		var mfs []store.ManifestFile
		for i := 0; i < 3; i++ {
			f := e.appendFile(1, int64(10+i))
			files = append(files, f)
			mfs = append(mfs, mf(f, 1, int64(10+i)))
		}
		e.settle("s", mfs...)
		for i := 0; i < unrelated; i += 500 {
			var recs []*store.Rec
			for j := 0; j < 500 && i+j < unrelated; j++ {
				id := e.st.NewFileID()
				recs = append(recs,
					&store.Rec{T: store.TFile, At: 10, File: id, Source: "claude-transcript", Mode: "append", Dev: 2, Ino: uint64(id)},
					&store.Rec{T: store.TGen, At: 11, File: id, Gen: 1, Method: "bytes", Reason: "first"})
			}
			if err := e.st.Commit(recs...); err != nil {
				t.Fatal(err)
			}
		}
		c := &conv{session: "s", files: files}
		e.cnt.Reset()
		if e.s.changedSinceSettle(c) {
			t.Fatal("unexpectedly changed")
		}
		return e.cnt.RowsRead(), e.cnt.Queries(), len(files)
	}
	r0, q0, n := measure(t, 0)
	r2k, q2k, _ := measure(t, 2000)
	if r0 != r2k {
		t.Fatalf("RowsRead depends on the table size: %d with 0 unrelated rows, %d with 2000", r0, r2k)
	}
	if max := int64(n + 1); q0 > max || q2k > max {
		t.Fatalf("queries %d / %d, want at most %d", q0, q2k, max)
	}
}

// changedSinceSettle and the removal path MUST NOT read the whole generations
// table (AllGenerations stays in the store for its other callers).
func TestChangedSinceSettleAndRemoveNeverCallAllGenerations(t *testing.T) {
	calls := func(fn *ast.FuncDecl) bool {
		found := false
		ast.Inspect(fn, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "AllGenerations" {
					found = true
				}
			}
			return true
		})
		return found
	}
	fset := token.NewFileSet()
	seen := false
	for _, name := range []string{"settle.go", "remove.go"} {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if name == "settle.go" && fn.Name.Name == "changedSinceSettle" {
				seen = true
			} else if name == "settle.go" {
				continue
			}
			if calls(fn) {
				t.Errorf("%s: %s calls AllGenerations", name, fn.Name.Name)
			}
		}
	}
	if !seen {
		t.Fatal("changedSinceSettle not found; the guard is stale")
	}
}
