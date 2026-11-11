package settle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
)

const (
	gSessA = "11111111-1111-4111-8111-111111111111"
	gSessB = "22222222-2222-4222-8222-222222222222"
	gSessC = "33333333-3333-4333-8333-333333333333"
)

type grpEnv struct {
	t   *testing.T
	st  *store.Store
	s   *Settler
	cnt *countdriver.Counter
	at  int64
}

func newGrpEnv(t *testing.T) *grpEnv {
	t.Helper()
	csDriverSeq++
	name := fmt.Sprintf("settle-groups-%d", csDriverSeq)
	cnt := countdriver.Register(name)
	st, err := store.Open(t.TempDir(), store.Options{DriverName: name})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &grpEnv{t: t, st: st, cnt: cnt, s: New(Config{Store: st, ClaudeDir: "/c"})}
}

func (e *grpEnv) commit(recs ...*store.Rec) {
	e.t.Helper()
	for _, r := range recs {
		e.at++
		r.At = e.at
	}
	if err := e.st.Commit(recs...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *grpEnv) add(mode, path string) int64 {
	e.t.Helper()
	id := e.st.NewFileID()
	e.commit(
		&store.Rec{T: store.TFile, File: id, Source: "claude-transcript", Mode: mode, Dev: 1, Ino: uint64(id)},
		&store.Rec{T: store.TPath, File: id, Path: path},
		&store.Rec{T: store.TGen, File: id, Gen: 1, Method: "bytes", Reason: "first"},
	)
	return id
}

func (e *grpEnv) rename(id int64, from, to string) {
	e.t.Helper()
	e.commit(&store.Rec{T: store.TPathClose, File: id, Path: from}, &store.Rec{T: store.TPath, File: id, Path: to})
}

// same asserts the incrementally kept map equals a full rebuild, exactly.
func (e *grpEnv) same(step string) map[string]*conv {
	e.t.Helper()
	got, err := e.s.groups(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	want, _, err := e.s.groupsFull()
	if err != nil {
		e.t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		e.t.Fatalf("%s: incremental map differs from a full rebuild\n got %s\nwant %s", step, dump(got), dump(want))
	}
	return got
}

func dump(m map[string]*conv) string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for _, k := range keys {
		out += fmt.Sprintf("[%s", k[:2])
		for _, f := range m[k].files {
			out += fmt.Sprintf(" %d:%s:g%d:s%d:%s", f.ID, f.State, f.CurGen, f.LastSize, m[k].rel[f.ID])
		}
		out += "]"
	}
	return out
}

func TestGroupsIncrementalEqualsFullRebuildAcrossChanges(t *testing.T) {
	e := newGrpEnv(t)
	pa := "/c/projects/p/" + gSessA + ".jsonl"
	pb := "/c/projects/p/" + gSessB + ".jsonl"
	pc := "/c/projects/p/" + gSessC + ".jsonl"
	if got := e.same("empty"); len(got) != 0 {
		t.Fatalf("empty store grouped: %v", got)
	}

	// Adds: two files for one Conversation, one for another, and three that
	// belong to no Conversation (a link, an unrelated path, outside the root).
	a1 := e.add("append", pa)
	a2 := e.add("snapshot", "/c/projects/p/"+gSessA+"/subagents/x.jsonl")
	b1 := e.add("append", pb)
	e.add("link", "/c/projects/p/"+gSessC+".jsonl")
	e.add("append", "/c/history.jsonl")
	e.add("append", "/elsewhere/projects/p/"+gSessC+".jsonl")
	if got := e.same("adds"); len(got) != 2 || len(got[gSessA].files) != 2 {
		t.Fatalf("adds: %s", dump(got))
	}

	// A file row changes without any path change.
	e.commit(&store.Rec{T: store.TCommit, File: a1, Gen: 1, From: 0, To: 40, Size: 40, Tail: 1, TailLen: 1})
	e.commit(&store.Rec{T: store.TGen, File: b1, Gen: 2, Method: "bytes", Reason: "truncate"})
	e.same("size and generation")

	// Rename inside a Conversation, then to another Conversation (A loses a
	// file, C appears), then back to a path that names no Conversation.
	e.rename(a2, "/c/projects/p/"+gSessA+"/subagents/x.jsonl", "/c/projects/p/"+gSessA+"/subagents/y.jsonl")
	e.same("rename within")
	e.rename(a2, "/c/projects/p/"+gSessA+"/subagents/y.jsonl", "/c/projects/q/"+gSessC+"/tool-results/z.json")
	if got := e.same("rename across"); got[gSessC] == nil || len(got[gSessA].files) != 1 {
		t.Fatalf("rename across: %s", dump(got))
	}
	e.rename(a2, "/c/projects/q/"+gSessC+"/tool-results/z.json", "/c/projects/q/notes.txt")
	if got := e.same("rename out"); got[gSessC] != nil {
		t.Fatalf("rename out: %s", dump(got))
	}
	// A second path appears on a file that already has one.
	e.commit(&store.Rec{T: store.TPath, File: b1, Path: pc})
	e.same("second path")
	e.rename(a2, "/c/projects/q/notes.txt", pa+".status.jsonl")
	e.same("rename back")

	// Removals: a tombstone closes every path and the file keeps its history
	// path, so it stays grouped as deleted; a file is never dropped.
	e.commit(&store.Rec{T: store.TTomb, File: b1, Path: pb})
	got := e.same("tombstone")
	if got[gSessB] == nil || got[gSessB].files[0].State != "deleted" {
		t.Fatalf("tombstone: %s", dump(got))
	}
	e.commit(&store.Rec{T: store.TIdent, File: b1, Dev: 1, Ino: 99, Birth: 5})
	e.same("revived")
	e.add("append", "/c/projects/p/"+gSessC+".jsonl")
	e.same("add after removal")
	e.same("no change")
}

// A write that failed bumps the rollback epoch, after which the tracker reports
// full and the map is rebuilt rather than trusted.
func TestGroupsRebuildsFullyWhenTheTrackerReportsFull(t *testing.T) {
	e := newGrpEnv(t)
	e.add("append", "/c/projects/p/"+gSessA+".jsonl")
	e.same("start")
	// Put a stale entry in the cache; only a full rebuild can remove it.
	e.s.gcache["stale"] = &conv{session: "stale"}
	if got, err := e.s.groups(context.Background()); err != nil || got["stale"] == nil {
		t.Fatal("control: an incremental pass with nothing changed rebuilt the map")
	}
	_ = e.st.WithWrite("", func(*sql.Tx) error { return errors.New("induced failure") })
	if got := e.same("after rollback"); got["stale"] != nil {
		t.Fatal("the map was not rebuilt after a rolled-back write")
	}
}

func TestGroupsTickReadsOnlyWhatChanged(t *testing.T) {
	e := newGrpEnv(t)
	for i := 0; i < 300; i++ {
		e.add("append", fmt.Sprintf("/c/projects/p/%08d-1111-4111-8111-111111111111.jsonl", i))
	}
	e.same("populated")
	e.cnt.Reset()
	if _, err := e.s.groups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r, q := e.cnt.RowsRead(), e.cnt.Queries(); r != 0 || q != 0 {
		t.Fatalf("idle groups read %d rows in %d queries, want none", r, q)
	}
	id := e.add("append", "/c/projects/p/"+gSessA+".jsonl")
	e.cnt.Reset()
	if _, err := e.s.groups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := e.cnt.RowsRead(); r > 4 {
		t.Fatalf("one new file read %d rows, want a handful", r)
	}
	e.commit(&store.Rec{T: store.TCommit, File: id, Gen: 1, From: 0, To: 9, Size: 9, Tail: 1, TailLen: 1})
	e.same("after commit")
}

// Only groupsFull and Recover (an operator-run recovery, not a tick) may read
// every file and path; Tick, its refresh, Where and the removal pass go through
// groups or read by id, so a per-tick rebuild cannot creep back in.
func TestOnlyGroupsFullReadsAllFilesAndPaths(t *testing.T) {
	fset := token.NewFileSet()
	callers := map[string]bool{}
	for _, name := range []string{"settle.go", "remove.go", "where.go"} {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "AllFiles" || sel.Sel.Name == "AllPaths") {
						callers[fn.Name.Name] = true
					}
				}
				return true
			})
		}
	}
	if !callers["groupsFull"] {
		t.Fatal("groupsFull no longer reads AllFiles and AllPaths; the guard is stale")
	}
	delete(callers, "groupsFull")
	delete(callers, "Recover")
	if len(callers) != 0 {
		t.Fatalf("these also read every file or path: %v (use groups)", callers)
	}
}
