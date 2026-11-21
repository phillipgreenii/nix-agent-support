package serve

import (
	"os"
	"testing"
	"time"
)

// No coverage pass may keep a read snapshot open across a filesystem call: a
// slow Lstat (a behind daemon runs many) would hold the connection, and with
// it the reader pool, for as long as the loop runs. The statFunc seam samples
// the read pool's in-use count at every Lstat; it MUST read 0 each time, and
// the count MUST return to 0 after the pass.
func TestCoverageHoldsNoReadSnapshotAcrossLstat(t *testing.T) {
	e := newEnv(t, false)
	old := time.Now().Add(-time.Hour)
	for _, n := range []string{"a.jsonl", "b.jsonl", "c.jsonl"} {
		e.transcript(n, "line\n", old)
	}
	d := e.open(t)
	defer func() { _ = d.Close() }()
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	if err := d.Parser().ParseAll(); err != nil {
		t.Fatal(err)
	}
	var calls, inUse int
	d.statFunc = func(p string) (os.FileInfo, error) {
		calls++
		if n := d.st.PoolStats().Read.InUse; n > inUse {
			inUse = n
		}
		return os.Lstat(p)
	}

	if _, err := d.computeCoverage("summary", true); err != nil { // statAll: one Lstat per file
		t.Fatal(err)
	}
	if calls == 0 {
		t.Fatal("the statAll pass made no Lstat call, the seam is not exercised")
	}
	statAllCalls := calls
	if inUse != 0 {
		t.Fatalf("a read snapshot was open across an Lstat in the coverage pass (in use %d)", inUse)
	}

	// The status loop's pass: force the stat path for every file.
	d.cfg.StatusFresh = time.Hour
	d.noteTouched(100001, 0, 0, 0, time.Now())
	for _, f := range []int64{1, 2, 3} {
		d.noteTouched(f, 0, 0, 0, time.Now())
	}
	if err := d.refreshStatusCtx(t.Context()); err != nil {
		t.Fatal(err)
	}
	if inUse != 0 {
		t.Fatalf("a read snapshot was open across an Lstat in the status refresh (in use %d)", inUse)
	}
	t.Logf("statAll made %d Lstat calls, status refresh %d", statAllCalls, calls-statAllCalls)
	if got := d.st.PoolStats().Read.InUse; got != 0 {
		t.Fatalf("read pool in use after the passes = %d, want 0", got)
	}
}
