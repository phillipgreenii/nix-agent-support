package serve

import (
	"os"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
)

func growOriginal(t *testing.T, p, s string, mtime time.Time) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(s)
	_ = f.Close()
	_ = os.Chtimes(p, mtime, mtime)
}

// Between two sweeps the status refresh re-reads only the files that were
// lagging at the last sweep: a refresh over files that are all caught up issues
// far fewer statements than a sweep, and a lagging file stays tracked, its lag
// figure moving with the original, until the copy catches up.
func TestStatusRefreshBetweenSweepsReadsOnlyTheHotSet(t *testing.T) {
	c := countdriver.Register("status-light-counting")
	e := newEnv(t, false)
	old := time.Now().Add(-time.Hour)
	var lagging string
	for _, n := range []string{"a.jsonl", "b.jsonl", "c.jsonl", "d.jsonl"} {
		p := e.transcript(n, "line\n", old)
		if n == "a.jsonl" {
			lagging = p
		}
	}
	d, err := Open(Options{Config: e.cfg, Logger: NewLogger(e.log), NoEvents: true, Version: "test", Procs: e.procs, StoreDriver: "status-light-counting"})
	if err != nil {
		t.Fatal(err)
	}
	e.d = d
	defer func() { _ = d.Close() }()
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	if err := d.Parser().ParseAll(); err != nil {
		t.Fatal(err)
	}
	growOriginal(t, lagging, "more bytes\n", time.Now().Add(-90*time.Second))

	c.Reset()
	if err := d.refreshStatus(); err != nil { // the first refresh is a sweep
		t.Fatal(err)
	}
	sweepQ := c.Queries()
	snap := d.loadStatusSnap()
	if snap.cov.Files != 4 || snap.cov.CopyLagSecondsMax < 89 || len(d.statusHot) != 1 {
		t.Fatalf("sweep: files %d lag %v hot %v", snap.cov.Files, snap.cov.CopyLagSecondsMax, d.statusHot)
	}

	// The lagging original keeps growing: the hot refresh sees it, with few statements.
	growOriginal(t, lagging, "even more\n", time.Now().Add(-120*time.Second))
	c.Reset()
	if err := d.refreshStatus(); err != nil {
		t.Fatal(err)
	}
	lightQ := c.Queries()
	if got := d.loadStatusSnap().cov.CopyLagSecondsMax; got < 119 {
		t.Fatalf("hot refresh lag %v, want about 120", got)
	}
	if got := d.loadStatusSnap().cov.Files; got != 4 {
		t.Fatalf("hot refresh lost the sweep's file count: %d", got)
	}
	if lightQ >= sweepQ {
		t.Fatalf("hot refresh issued %d statements, sweep %d", lightQ, sweepQ)
	}

	// A file that begins to lag after the sweep is noticed at the next sweep, not before.
	other := e.transcript("d.jsonl", "line\n", old)
	growOriginal(t, other, "late growth\n", time.Now().Add(-100*time.Second))
	if err := d.refreshStatus(); err != nil {
		t.Fatal(err)
	}
	if len(d.statusHot) != 1 {
		t.Fatalf("a file that began lagging after the sweep was tracked early: %v", d.statusHot)
	}
	d.statusSweepAt = d.statusSweepAt.Add(-statusSweepEvery)
	if err := d.refreshStatus(); err != nil {
		t.Fatal(err)
	}
	if len(d.statusHot) != 2 {
		t.Fatalf("next sweep missed the lagging file: %v", d.statusHot)
	}

	// Once the copy catches up the files leave the hot set and the lag clears.
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	if err := d.Parser().ParseAll(); err != nil {
		t.Fatal(err)
	}
	if err := d.refreshStatus(); err != nil {
		t.Fatal(err)
	}
	if got := d.loadStatusSnap().cov.CopyLagSecondsMax; got != 0 || len(d.statusHot) != 0 {
		t.Fatalf("after catching up: lag %v hot %v", got, d.statusHot)
	}
}
