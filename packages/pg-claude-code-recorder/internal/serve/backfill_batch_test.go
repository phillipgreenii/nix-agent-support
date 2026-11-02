package serve

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/watcher"
)

// The first backfill group-commits: over N new files the journal commits are at
// most about N/backfillBatch (plus the files that fall back to the ordinary
// path, none here), not one or more per file. Group commit applies in clone
// mode; on a volume without clone the files take the byte-mode path and the
// test has nothing to measure.
func TestBackfillGroupCommitsFirstCopies(t *testing.T) {
	e := newEnv(t, false)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	if m, _ := d.cp.Method(); m != "clone" {
		t.Skipf("copy method %s: group commit needs clone mode", m)
	}
	n := 3*backfillBatch + 5
	var files []watcher.FileInfo
	base := time.Now()
	for i := 0; i < n; i++ {
		p := e.transcript(fmt.Sprintf("s-%03d.jsonl", i), fmt.Sprintf("{\"n\":%d}\n", i), base.Add(-time.Duration(i)*time.Second))
		files = append(files, watcher.FileInfo{Path: p, ModTime: base.Add(-time.Duration(i) * time.Second), Size: 1})
	}
	var commits atomic.Int64
	d.st.OnCommit(func() { commits.Add(1) })
	d.backfill(files)
	if !d.backfillDone.Load() {
		t.Fatal("backfill did not finish")
	}
	got := commits.Load()
	max := int64((n + backfillBatch - 1) / backfillBatch)
	if got > max {
		t.Fatalf("journal commits = %d for %d files, want at most %d (one per batch of %d)", got, n, max, backfillBatch)
	}
	all, err := d.st.AllFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != n {
		t.Fatalf("recorded files = %d, want %d", len(all), n)
	}
	// Every file is whole.
	for _, f := range all {
		g, err := d.st.Generation(f.ID, f.CurGen)
		if err != nil || g.CopyOffset == 0 {
			t.Fatalf("file %d generation %+v err %v", f.ID, g, err)
		}
		if _, err := os.Stat(filepath.Join(d.st.Dir(), "copies", fmt.Sprintf("%d.%d", f.ID, f.CurGen))); err != nil {
			t.Fatal(err)
		}
	}
}
