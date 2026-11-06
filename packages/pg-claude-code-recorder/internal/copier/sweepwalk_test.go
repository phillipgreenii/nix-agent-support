package copier

import (
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
)

var sweepDriverSeq atomic.Int64

// walkOf builds what the daemon passes to SweepMissingWalk from the files that
// exist on disk now.
func walkOf(t *testing.T, paths ...string) (map[Identity]bool, map[string]Identity) {
	t.Helper()
	found := map[Identity]bool{}
	walked := map[string]Identity{}
	for _, p := range paths {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := identityOf(p, fi)
		found[id] = true
		walked[p] = id
	}
	return found, walked
}

const sweepDir = "projects/example-project/"

// A sweep that finds nothing missing makes no source-side lstat and reads the
// current paths once (not once per file).
func TestSweepMissingWalkNothingMissingMakesNoLstatAndOneQuery(t *testing.T) {
	cnt := &CountingSrcFS{}
	drv := fmt.Sprintf("copier-sweep-%d", sweepDriverSeq.Add(1))
	c := countdriver.Register(drv)
	e := newEnv(t, func(e *env) {
		e.drv = drv
		e.cfgMod = func(cfg *Config) { cfg.SrcFS = cnt }
	})
	var paths []string
	for i := 0; i < 5; i++ {
		p := e.write(fmt.Sprintf("%ss%d.jsonl", sweepDir, i), "x\n")
		e.ingest(p)
		paths = append(paths, p)
	}
	found, walked := walkOf(t, paths...)
	cnt.Reset()
	c.Reset()
	stats, err := e.cp.SweepMissingWalk(found, walked)
	if err != nil {
		t.Fatal(err)
	}
	if n := cnt.Lstats(); n != 0 {
		t.Fatalf("source lstat calls = %d, want 0", n)
	}
	if n := c.Matching("FROM paths"); n != 1 {
		t.Fatalf("path queries = %d, want 1 (AllCurrentPaths)", n)
	}
	if stats.Present != 5 || stats.LstatFallbacks != 0 || stats.Tombstoned != 0 {
		t.Fatalf("stats %+v", stats)
	}
}

// A file the walk missed but that is on disk (a filter excluded it, or a walk
// race) is rechecked with one lstat and not tombstoned.
func TestSweepMissingWalkRechecksFileMissingFromWalk(t *testing.T) {
	cnt := &CountingSrcFS{}
	e := newEnv(t, func(e *env) { e.cfgMod = func(cfg *Config) { cfg.SrcFS = cnt } })
	p := e.write(sweepDir+"a.jsonl", "x\n")
	r := e.ingest(p)
	cnt.Reset()
	stats, err := e.cp.SweepMissingWalk(map[Identity]bool{}, map[string]Identity{})
	if err != nil {
		t.Fatal(err)
	}
	if n := cnt.Lstats(); n != 1 || stats.LstatFallbacks != 1 {
		t.Fatalf("lstats=%d stats=%+v, want one fallback", n, stats)
	}
	if ts, _ := e.st.Tombstones(r.File); len(ts) != 0 {
		t.Fatal("a file present on disk was tombstoned")
	}
	if f := e.file(r.File); f.State == "deleted" {
		t.Fatal("file marked deleted")
	}
}

// A file missing from both the walk and the disk is tombstoned.
func TestSweepMissingWalkTombstonesFileMissingEverywhere(t *testing.T) {
	e := newEnv(t)
	p := e.write(sweepDir+"a.jsonl", "x\n")
	r := e.ingest(p)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	stats, err := e.cp.SweepMissingWalk(map[Identity]bool{}, map[string]Identity{})
	if err != nil {
		t.Fatal(err)
	}
	if ts, _ := e.st.Tombstones(r.File); len(ts) != 1 || stats.Tombstoned != 1 {
		t.Fatalf("tombstones=%v stats=%+v", ts, stats)
	}
}

// A renamed file keeps its identity: the walk has the new path, the stored old
// path falls back to lstat, is closed, and nothing is tombstoned.
func TestSweepMissingWalkRenamedFileIsNotDeleted(t *testing.T) {
	e := newEnv(t)
	p := e.write(sweepDir+"a.jsonl", "x\n")
	r := e.ingest(p)
	np := e.abs(sweepDir + "b.jsonl")
	if err := os.Rename(p, np); err != nil {
		t.Fatal(err)
	}
	found, walked := walkOf(t, np)
	if _, err := e.cp.SweepMissingWalk(found, walked); err != nil {
		t.Fatal(err)
	}
	if ts, _ := e.st.Tombstones(r.File); len(ts) != 0 {
		t.Fatal("rename tombstoned")
	}
	if cur, _ := e.st.CurrentPaths(r.File); len(cur) != 0 {
		t.Fatalf("old path not closed: %v", cur)
	}
}

// The path -> identity comparison: the same path now holding a different inode
// is not present, takes the lstat fallback, and the old file is recorded gone.
func TestSweepMissingWalkReplacedInodeIsNotPresent(t *testing.T) {
	e := newEnv(t)
	p := e.write(sweepDir+"a.jsonl", "old\n")
	r := e.ingest(p)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("brand new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	found, walked := walkOf(t, p)
	stats, err := e.cp.SweepMissingWalk(found, walked)
	if err != nil {
		t.Fatal(err)
	}
	if stats.LstatFallbacks != 1 || stats.Present != 0 {
		t.Fatalf("stats %+v", stats)
	}
	if cur, _ := e.st.CurrentPaths(r.File); len(cur) != 0 {
		t.Fatalf("path of the replaced inode not closed: %v", cur)
	}
}
