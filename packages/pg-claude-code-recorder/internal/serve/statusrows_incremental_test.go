package serve

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"sync"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

func smallChunks(t *testing.T, n int) {
	t.Helper()
	old := fileChunk
	fileChunk = n
	t.Cleanup(func() { fileChunk = old })
}

func flatten(v fileView) []store.File {
	var out []store.File
	for f := range v.All() {
		out = append(out, f)
	}
	return out
}

// viewNow reads the published rows the way the status loop does.
func viewNow(t *testing.T, d *Daemon) fileView {
	t.Helper()
	var v fileView
	err := d.st.ReadTxCtx(context.Background(), func(rd *store.Reader) (err error) {
		v, _, err = d.statusRows(context.Background(), rd)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func checkAgainstFresh(t *testing.T, d *Daemon, label string) {
	t.Helper()
	got := flatten(viewNow(t, d))
	want, err := d.st.AllFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) || (len(got) > 0 && !reflect.DeepEqual(got, want)) {
		t.Fatalf("%s: cache has %d rows, a fresh AllFiles %d; first difference %s", label, len(got), len(want), firstDiff(got, want))
	}
}

func firstDiff(a, b []store.File) string {
	for i := 0; i < len(a) && i < len(b); i++ {
		if !reflect.DeepEqual(a[i], b[i]) {
			return fmt.Sprintf("row %d: %+v vs %+v", i, a[i], b[i])
		}
	}
	return "in length"
}

// After every step of a randomized sequence of inserts (ids arriving out of
// order), copy commits, state changes, deletes, un-deletes by identity, path
// changes, a large batch and rolled back writes, the cache equals a fresh
// AllFiles. The chunks are tiny so the splits and merges happen.
func TestStatusRowsCacheEqualsAFreshAllFilesThroughARandomSequence(t *testing.T) {
	smallChunks(t, 4)
	e := newEnv(t, false)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	rng := rand.New(rand.NewSource(7))
	dir := filepath.Join(e.claude, "projects", "example-proj")
	var ids []int64
	have := map[int64]bool{}
	newID := func() int64 {
		for {
			id := int64(500000 + rng.Intn(400)) // out of order, with gaps
			if !have[id] {
				have[id] = true
				ids = append(ids, id)
				return id
			}
		}
	}
	at := int64(1)
	insert := func() {
		id := newID()
		at++
		recs := []*store.Rec{
			{T: store.TFile, At: at, File: id, Source: "transcript", Mode: "append", Size: 10},
			{T: store.TPath, At: at, File: id, Path: filepath.Join(dir, strconv.FormatInt(id, 10)+".jsonl")},
			{T: store.TGen, At: at, File: id, Gen: 1},
		}
		if err := d.st.Commit(recs...); err != nil {
			t.Fatal(err)
		}
	}
	for range 6 {
		insert()
	}
	checkAgainstFresh(t, d, "initial")
	for step := 0; step < 200; step++ {
		at++
		pick := func() int64 { return ids[rng.Intn(len(ids))] }
		var recs []*store.Rec
		label := ""
		switch op := rng.Intn(11); op {
		case 0, 1:
			insert()
			label = "insert"
		case 2:
			f := pick()
			recs = []*store.Rec{{T: store.TCommit, At: at, File: f, Gen: 1, From: 0, To: at, Size: at}}
			label = "commit"
		case 3:
			recs = []*store.Rec{{T: store.TState, At: at, File: pick(), State: "copy_mismatch"}}
			label = "state"
		case 4:
			f := pick()
			recs = []*store.Rec{{T: store.TTomb, At: at, File: f, Path: "/gone"}}
			label = "deleted"
		case 5:
			recs = []*store.Rec{{T: store.TIdent, At: at, File: pick(), Dev: 3, Ino: uint64(at), Birth: at}}
			label = "ident (un-delete)"
		case 6:
			f := pick()
			recs = []*store.Rec{{T: store.TPath, At: at, File: f, Path: filepath.Join(dir, "moved"+strconv.FormatInt(at, 10)+".jsonl")}}
			label = "path"
		case 7:
			recs = []*store.Rec{{T: store.TSnap, At: at, File: pick(), Version: int(at), Size: at}}
			label = "snapshot"
		case 8: // a large batch of mixed records, the shape of a bulk rebuild
			for range 40 {
				f := pick()
				recs = append(recs, &store.Rec{T: store.TCommit, At: at, File: f, Gen: 1, From: 0, To: at, Size: at})
			}
			label = "batch"
		case 9: // a rolled back write: the epoch moves and the cache must start over
			if err := d.st.WithWrite("", func(*sql.Tx) error { return errors.New("undo") }); err == nil {
				t.Fatal("the failing write returned nil")
			}
			label = "rollback"
		default:
			recs = []*store.Rec{{T: store.TStage, At: at, Session: "s", State: store.StageHeld}}
			label = "stage (no file)"
		}
		if len(recs) > 0 {
			if err := d.st.Commit(recs...); err != nil {
				t.Fatal(err)
			}
		}
		checkAgainstFresh(t, d, "step "+strconv.Itoa(step)+" "+label)
	}
	// The forced full path (the flag the existing tests use) also equals a fresh read.
	d.rowsOK = false
	checkAgainstFresh(t, d, "forced full")
}

// A change committed between the tracker's Take and the read transaction leaves
// no stale row: the Take that precedes the snapshot never clears an id the
// snapshot cannot see, so the next Take reports the new commit and the next
// refresh reads it.
func TestStatusRowsCommitBetweenTakeAndReadLeavesNoStaleRow(t *testing.T) {
	e := newEnv(t, false)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	commitFixture(t, d, filepath.Join(e.claude, "projects", "example-proj"), 5)
	checkAgainstFresh(t, d, "warm")
	commit := func(file, size int64) {
		t.Helper()
		if err := d.st.Commit(&store.Rec{T: store.TCommit, At: 50 + size, File: file, Gen: 1, From: 0, To: size, Size: size}); err != nil {
			t.Fatal(err)
		}
	}
	commit(200001, 111)
	fired := 0
	d.afterTake = func() {
		if fired++; fired == 1 {
			commit(200002, 222) // after Take: this id is reported by the NEXT Take
		}
	}
	_ = viewNow(t, d) // reads 200001 only: 200002 is dirty again for the next Take
	if fired == 0 {
		t.Fatal("the hook never ran")
	}
	d.afterTake = nil
	checkAgainstFresh(t, d, "next refresh")
	for f := range viewNow(t, d).All() {
		if f.ID == 200002 && f.LastSize != 222 {
			t.Fatalf("file 200002 holds size %d, want 222", f.LastSize)
		}
	}
}

// Readers iterate a published view while refreshes publish new ones (run with
// -race): a view is never written after it is published.
func TestStatusRowsReadersIterateAPublishedViewDuringARefresh(t *testing.T) {
	smallChunks(t, 8)
	e := newEnv(t, false)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	commitFixture(t, d, filepath.Join(e.claude, "projects", "example-proj"), 60)
	v := viewNow(t, d)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				sum := int64(0)
				for f := range v.All() {
					sum += f.LastSize
				}
				_ = sum
			}
		}()
	}
	for i := int64(0); i < 60; i++ {
		if err := d.st.Commit(&store.Rec{T: store.TCommit, At: 100 + i, File: 200001 + i%60, Gen: 1, From: 0, To: 1000 + i, Size: 1000 + i}); err != nil {
			t.Fatal(err)
		}
		_ = viewNow(t, d)
	}
	close(stop)
	wg.Wait()
	checkAgainstFresh(t, d, "after the readers")
}

// Rows arriving out of id order keep the view id-ascending, so Worst still
// picks the first maximum in id order.
func TestFileViewKeepsIDOrderForOutOfOrderArrival(t *testing.T) {
	smallChunks(t, 3)
	mk := func(ids ...int64) []store.File {
		out := make([]store.File, len(ids))
		for i, id := range ids {
			out[i] = store.File{ID: id}
		}
		return out
	}
	v := fileView{}.with(mk(50, 60, 70))
	v = v.with(mk(10, 20, 55, 65, 80, 90)) // below, between and above
	v = v.with(mk(5, 55, 95))              // replaces 55, new ids at both ends
	var got []int64
	for f := range v.All() {
		got = append(got, f.ID)
	}
	want := []int64{5, 10, 20, 50, 55, 60, 65, 70, 80, 90, 95}
	if !reflect.DeepEqual(got, want) || v.Len() != len(want) || v.maxID() != 95 {
		t.Fatalf("ids %v len %d max %d, want %v", got, v.Len(), v.maxID(), want)
	}
	for _, c := range v.chunks {
		if len(c) == 0 || len(c) > fileChunk {
			t.Fatalf("chunk of %d rows", len(c))
		}
	}

	// Worst through the daemon: two files with the same copy lag, ids arriving
	// high first; the lower id wins.
	e := newEnv(t, false)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	dir := filepath.Join(e.claude, "projects", "example-proj")
	for _, id := range []int64{700002, 700001} {
		p := filepath.Join(dir, strconv.FormatInt(id, 10)+".jsonl")
		if err := os.WriteFile(p, make([]byte, 50), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := d.st.Commit(
			&store.Rec{T: store.TFile, At: 1, File: id, Source: "transcript", Mode: "append", Size: 50},
			&store.Rec{T: store.TPath, At: 1, File: id, Path: p},
			&store.Rec{T: store.TGen, At: 1, File: id, Gen: 1},
		); err != nil {
			t.Fatal(err)
		}
	}
	cov, err := d.computeCoverageMode("summary", false, statAll)
	if err != nil {
		t.Fatal(err)
	}
	if cov.Worst == nil || cov.Worst.FileID != 700001 {
		t.Fatalf("worst %+v, want file 700001 (the first maximum in id order)", cov.Worst)
	}
	checkAgainstFresh(t, d, "after the two inserts")
}

// A failed read leaves the cache invalid, and the next refresh starts over.
func TestStatusRowsFailedReadInvalidatesTheCache(t *testing.T) {
	e := newEnv(t, false)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	commitFixture(t, d, filepath.Join(e.claude, "projects", "example-proj"), 5)
	checkAgainstFresh(t, d, "warm")
	if err := d.st.Commit(&store.Rec{T: store.TState, At: 9, File: 200003, State: "copy_mismatch"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.rowsMu.Lock()
	err := d.syncFileRows(ctx)
	ok := d.rowsOK
	d.rowsMu.Unlock()
	if err == nil || ok {
		t.Fatalf("a cancelled read returned %v with the cache valid=%v, want an error and an invalid cache", err, ok)
	}
	checkAgainstFresh(t, d, "after the failed read")
}

// One file-only commit must allocate a small fraction of what rebuilding the
// whole table did (the figure the old per-commit AllFiles rebuild cost).
func TestStatusRowsOneChangeAllocatesAFractionOfAWholeRead(t *testing.T) {
	const files = 3000
	e := newEnv(t, false)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	commitFixture(t, d, filepath.Join(e.claude, "projects", "example-proj"), files)
	checkAgainstFresh(t, d, "warm")
	bytesOf := func(fn func()) uint64 {
		var a, b runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&a)
		fn()
		runtime.ReadMemStats(&b)
		return b.TotalAlloc - a.TotalAlloc
	}
	whole := bytesOf(func() {
		if _, err := d.st.AllFiles(); err != nil {
			t.Fatal(err)
		}
	})
	if err := d.st.Commit(&store.Rec{T: store.TCommit, At: 77, File: 200100, Gen: 1, From: 0, To: 7, Size: 7}); err != nil {
		t.Fatal(err)
	}
	incr := bytesOf(func() { _ = viewNow(t, d) })
	t.Logf("whole read %d bytes, one-change refresh %d bytes (%.1f%%)", whole, incr, 100*float64(incr)/float64(whole))
	if incr*5 > whole {
		t.Fatalf("one-change refresh allocated %d bytes, more than 20%% of the %d of a whole read", incr, whole)
	}
}
