package corpus

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/corpus/corpustest"

	_ "modernc.org/sqlite"
)

// setFileFactsComplete flips the recorder's marker that its per-file table is
// fully filled (what a database that predates the table lacks until its
// background backfill ends), so the recorder answers inventory degraded.
func setFileFactsComplete(t *testing.T, e *corpustest.Env, complete bool) {
	t.Helper()
	var dbPath string
	_ = filepath.WalkDir(e.StateDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Base(p) == "index.db" {
			dbPath = p
		}
		return nil
	})
	if dbPath == "" {
		t.Fatal("recorder index.db not found")
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	q := `DELETE FROM meta WHERE key='file_facts_complete'`
	if complete {
		q = `INSERT INTO meta(key,value) VALUES('file_facts_complete','1') ON CONFLICT(key) DO UPDATE SET value='1'`
	}
	if _, err := db.ExecContext(context.Background(), q); err != nil {
		t.Fatal(err)
	}
}

// A recorder still backfilling its per-file table answers the inventory
// degraded (a partial, possibly empty, list). That answer must not count as done:
// the request stays pending, the Scan carries on with what it has, and the next
// Scan lists the files once the recorder is complete, so the status files reach
// the limits observer.
func TestInventory_DegradedAnswerStaysPendingUntilComplete(t *testing.T) {
	e := corpustest.Start(t)
	e.Register(111, "s1", cwd, "", true)
	e.Put(corpustest.TranscriptRel(cwd, "s1"), userLine("s1", 0, "hi"))
	e.Put(corpustest.StatusRel(cwd, "s1"), statusLine("s1", t0.Unix(), 40, t0.Add(4*time.Hour).Unix()))
	setFileFactsComplete(t, e, false)

	r := newReader(e.Facts)
	ctx := context.Background()
	c, err := e.Facts.Client(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.bootstrap(ctx, c, t0.Add(-48*time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	total := len(r.pending)
	if err := r.resumeInventory(ctx, c); err != nil {
		t.Fatalf("a degraded answer is not an error: %v", err)
	}
	if len(r.pending) != total {
		t.Fatalf("pending after a degraded answer = %d, want %d (nothing marked done)", len(r.pending), total)
	}

	setFileFactsComplete(t, e, true)
	if err := r.resumeInventory(ctx, c); err != nil {
		t.Fatal(err)
	}
	if len(r.pending) != 0 {
		t.Fatalf("pending after a complete answer = %d, want 0", len(r.pending))
	}
	kinds := map[fileKind]int{}
	for _, f := range r.files {
		kinds[f.kind]++
	}
	if kinds[kindStatus] != 1 || kinds[kindTranscript] != 1 {
		t.Fatalf("files by kind = %v, want the status and the transcript file", kinds)
	}
}
