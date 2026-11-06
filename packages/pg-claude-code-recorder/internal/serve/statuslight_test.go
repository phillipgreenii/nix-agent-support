package serve

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
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

// countStat makes the daemon's Lstat seam count its calls.
func countStat(d *Daemon) *atomic.Int64 {
	var n atomic.Int64
	d.statFunc = func(p string) (os.FileInfo, error) {
		n.Add(1)
		return os.Lstat(p)
	}
	return &n
}

// With 5,000 files unchanged since their last copy, one status refresh Lstats
// only the exceptions: 2 files whose recorded size differs from the copy offset
// and 3 touched within status.fresh (5 in all), and the cached rows are not
// re-read while the store's commit counter stands still.
func TestStatusRefreshStatsOnlyTheExceptions(t *testing.T) {
	c := countdriver.Register("status-lazy-counting")
	e := newEnv(t, false)
	d, err := Open(Options{Config: e.cfg, Logger: NewLogger(e.log), NoEvents: true, Version: "test", Procs: e.procs, StoreDriver: "status-lazy-counting"})
	if err != nil {
		t.Fatal(err)
	}
	e.d = d
	defer func() { _ = d.Close() }()
	seedFiles(t, d, filepath.Join(e.claude, "projects", "example-proj"), 5000)
	err = d.st.WithWrite("", func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE files SET last_size=10 WHERE id IN (100001,100002)`) // lagging: nothing copied
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	stats := countStat(d)
	now := d.clock.Now()
	for i := 3; i <= 5; i++ {
		d.noteTouched(int64(100000+i), 0, 0, 0, now.Add(-time.Minute))
	}
	d.noteTouched(100006, 0, 0, 0, now.Add(-time.Hour)) // outside status.fresh

	if err := d.refreshStatus(); err != nil {
		t.Fatal(err)
	}
	if got := stats.Load(); got != 5 {
		t.Fatalf("first refresh made %d Lstat calls, want 5 (2 lagging + 3 fresh)", got)
	}
	if got := d.loadStatusSnap().statExceptions; got != 5 {
		t.Fatalf("stat exceptions %d, want 5", got)
	}

	// The second refresh at an unchanged commit counter re-reads no file table.
	c.Reset()
	if err := d.refreshStatus(); err != nil {
		t.Fatal(err)
	}
	if got := stats.Load(); got != 10 {
		t.Fatalf("second refresh: %d Lstat calls in all, want 10", got)
	}
	if q := c.Queries(); q > 3 { // the stage rows only (one per read of them)
		t.Fatalf("second refresh issued %d statements; the file tables were re-read", q)
	}
}

// Lazy mode reports the same aggregates as the coverage command when nothing
// changed behind the daemon's back, does not see a change the watcher missed,
// and the coverage command (all mode) does.
func TestStatusLazyMatchesCoverageAndMissesAChangeBehindItsBack(t *testing.T) {
	e := newEnv(t, false)
	old := time.Now().Add(-time.Hour)
	var first string
	for _, n := range []string{"a.jsonl", "b.jsonl", "c.jsonl"} {
		p := e.transcript(n, "line\n", old)
		if first == "" {
			first = p
		}
	}
	d := e.open(t)
	defer func() { _ = d.Close() }()
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	if err := d.Parser().ParseAll(); err != nil {
		t.Fatal(err)
	}
	stats := countStat(d)
	// Let the hook-fed touched entries age out of status.fresh.
	d.cfg.StatusFresh = time.Nanosecond
	time.Sleep(2 * time.Millisecond)

	all, err := d.computeCoverage("summary", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := stats.Load(); got != 3 {
		t.Fatalf("coverage made %d Lstat calls, want one per file", got)
	}
	stats.Store(0)
	lazy, err := d.computeCoverageMode("summary", false, statLazy)
	if err != nil {
		t.Fatal(err)
	}
	if got := stats.Load(); got != 0 {
		t.Fatalf("lazy made %d Lstat calls over unchanged files", got)
	}
	same := func(a, b wire.Coverage) bool {
		return a.Files == b.Files && a.CopyLagBytesMax == b.CopyLagBytesMax && a.ParseLagBytesMax == b.ParseLagBytesMax &&
			fmt.Sprint(a.Roots) == fmt.Sprint(b.Roots)
	}
	if !same(all, lazy) || all.Files != 3 {
		t.Fatalf("aggregates differ:\nall  %+v\nlazy %+v", all.CoverageSummary, lazy.CoverageSummary)
	}

	growOriginal(t, first, "grew with no watcher event\n", time.Now().Add(-90*time.Second))
	lazy, err = d.computeCoverageMode("summary", false, statLazy)
	if err != nil {
		t.Fatal(err)
	}
	if lazy.CopyLagBytesMax != 0 {
		t.Fatalf("lazy saw a change nobody reported: %d bytes", lazy.CopyLagBytesMax)
	}
	all, err = d.computeCoverage("summary", false)
	if err != nil {
		t.Fatal(err)
	}
	if all.CopyLagBytesMax == 0 || all.CopyLagSecondsMax < 89 {
		t.Fatalf("coverage missed the change: %+v", all.CoverageSummary)
	}
}

// A file whose original grew past what the copier recorded is an exception:
// the lazy pass stats it, reports its lag, and the lag clears after the copy.
func TestStatusRefreshSeesALaggingFileAndItClears(t *testing.T) {
	e := newEnv(t, false)
	old := time.Now().Add(-time.Hour)
	p := e.transcript("a.jsonl", "line\n", old)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	if err := d.Parser().ParseAll(); err != nil {
		t.Fatal(err)
	}
	d.cfg.StatusFresh = time.Nanosecond
	time.Sleep(2 * time.Millisecond)
	// The recorded size moves ahead of the copy (the watcher saw it, the copy
	// has not run): last_size is set directly, as the copier's size record does.
	growOriginal(t, p, "more bytes\n", time.Now().Add(-90*time.Second))
	err := d.st.WithWrite("", func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE files SET last_size=last_size+11`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	d.rowsOK = false // the direct UPDATE bypasses the journal, so the commit counter did not move
	if err := d.refreshStatus(); err != nil {
		t.Fatal(err)
	}
	if got := d.loadStatusSnap().cov; got.CopyLagBytesMax != 11 || got.CopyLagSecondsMax < 89 {
		t.Fatalf("lagging file: %+v", got.CoverageSummary)
	}
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	if err := d.Parser().ParseAll(); err != nil {
		t.Fatal(err)
	}
	if err := d.refreshStatus(); err != nil {
		t.Fatal(err)
	}
	if got := d.loadStatusSnap().cov; got.CopyLagBytesMax != 0 || got.CopyLagSecondsMax != 0 {
		t.Fatalf("after catching up: %+v", got.CoverageSummary)
	}
}

// Copy and parse lag are separate figures with separate clocks: a file the
// copier has caught up on but parse has not reports copy lag 0 and a positive
// parse lag in the lazy pass, so the copy lag is not derived from the parse
// lag origin.
func TestStatusLazyCopyCaughtUpParseBehindReportsZeroCopyLag(t *testing.T) {
	e := newEnv(t, false)
	e.transcript("a.jsonl", "line\n", time.Now().Add(-time.Hour))
	d := e.open(t)
	defer func() { _ = d.Close() }()
	if err := d.Watcher().Rescan(); err != nil { // copy only: parse stays behind
		t.Fatal(err)
	}
	d.cfg.StatusFresh = time.Nanosecond
	time.Sleep(2 * time.Millisecond)
	d.clock = clock.NewFake(time.Now().Add(90 * time.Second))
	lazy, err := d.computeCoverageMode("summary", false, statLazy)
	if err != nil {
		t.Fatal(err)
	}
	if lazy.ParseLagBytesMax == 0 || lazy.ParseLagSecondsMax < 89 {
		t.Fatalf("parse lag not reported: %+v", lazy.CoverageSummary)
	}
	if lazy.CopyLagBytesMax != 0 || lazy.CopyLagSecondsMax != 0 {
		t.Fatalf("copy is caught up but copy lag is %d bytes, %v s: %+v", lazy.CopyLagBytesMax, lazy.CopyLagSecondsMax, lazy.CoverageSummary)
	}
}
