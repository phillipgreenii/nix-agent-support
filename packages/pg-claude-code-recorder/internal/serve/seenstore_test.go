package serve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
)

var seenDriverSeq int

// openCounted opens the env's daemon over a counting driver.
func (e *env) openCounted(t *testing.T) (*Daemon, *countdriver.Counter) {
	t.Helper()
	seenDriverSeq++
	name := fmt.Sprintf("seen-counting-%d", seenDriverSeq)
	c := countdriver.Register(name)
	d, err := Open(Options{Config: e.cfg, Logger: NewLogger(e.log), NoEvents: true, Version: "test", Procs: e.procs, StoreDriver: name})
	if err != nil {
		t.Fatal(err)
	}
	e.d = d
	return d, c
}

func totalIngests(d *Daemon) float64 {
	var n float64
	for _, r := range obs.IngestResults {
		n += ingestCount(d, r)
	}
	return n
}

func rescan(t *testing.T, d *Daemon) {
	t.Helper()
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
}

// Two sweeps catch every file up and cache it; after a restart the cache is
// read back and the first sweep ingests nothing.
func TestSeenTablePersistsTheSkipCacheAcrossARestart(t *testing.T) {
	e := newEnv(t, false)
	old := time.Now().Add(-time.Hour)
	names := []string{"a.jsonl", "b.jsonl", "c.jsonl"}
	for _, n := range names {
		e.transcript(n, "{\"type\":\"user\"}\n", old)
	}
	d, _ := e.openCounted(t)
	if !strings.Contains(e.log.String(), "seen empty: full verification sweep") {
		t.Fatalf("no empty-table event on first start: %s", e.log.String())
	}
	rescan(t, d)
	rescan(t, d)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, c := e.openCounted(t)
	defer func() { _ = d.Close() }()
	if v, _ := d.obs.Reg.Gauge(obs.MSeenRows, nil); v != float64(len(names)) {
		t.Fatalf("seen_rows after restart = %v, want %d", v, len(names))
	}
	c.Reset()
	rescan(t, d)
	if got := totalIngests(d); got != 0 {
		t.Fatalf("the first sweep after a restart ingested %v files, want 0", got)
	}
	if got := sweepCount(d, obs.SweepSkipped); got != float64(len(names)) {
		t.Fatalf("skipped = %v, want %d", got, len(names))
	}
	// No copier work: nothing was looked up per file, only the sweep's own reads.
	if n := c.Matching("FROM generations"); n != 0 {
		t.Fatalf("%d generation reads during the skipping sweep", n)
	}
}

// Each change a file can show invalidates its row: an append (size) and a
// same-size rewrite with a new mtime are both ingested after a restart.
func TestSeenTableChangedFilesAreIngestedAfterARestart(t *testing.T) {
	e := newEnv(t, false)
	old := time.Now().Add(-time.Hour)
	grown := e.transcript("grown.jsonl", "{\"type\":\"user\"}\n", old)
	touched := e.transcript("touched.jsonl", "{\"type\":\"user\"}\n", old)
	e.transcript("same.jsonl", "{\"type\":\"user\"}\n", old)
	d, _ := e.openCounted(t)
	rescan(t, d)
	rescan(t, d)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(grown, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("{\"type\":\"user\"}\n")
	_ = f.Close()
	later := old.Add(time.Minute)
	if err := os.Chtimes(touched, later, later); err != nil {
		t.Fatal(err)
	}
	d, _ = e.openCounted(t)
	defer func() { _ = d.Close() }()
	rescan(t, d)
	// The first sweep after a start copies in the backfill and then ingests each
	// event, so a changed file is ingested twice and the unchanged one not at all.
	if got := totalIngests(d); got != 4 {
		t.Fatalf("ingests after a restart with two changed files = %v, want 4 (two each)", got)
	}
	if got := sweepCount(d, obs.SweepSkipped); got != 1 {
		t.Fatalf("skipped = %v, want 1", got)
	}
}

// A crash between the ingest's commit and the seen write loses only the row:
// the next start re-ingests that file and caches it again.
func TestSeenTableLostWriteCostsOnlyAReingest(t *testing.T) {
	e := newEnv(t, false)
	e.transcript("a.jsonl", "{\"type\":\"user\"}\n", time.Now().Add(-time.Hour))
	d, _ := e.openCounted(t)
	rescan(t, d)
	rescan(t, d)
	// The batch never reached the table.
	d.seenDB.mu.Lock()
	d.seenDB.put, d.seenDB.del = nil, nil
	d.seenDB.mu.Unlock()
	if err := d.st.SeenTruncate(); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, _ = e.openCounted(t)
	rescan(t, d)
	first := totalIngests(d)
	if first == 0 {
		t.Fatal("a lost seen write did not cost a re-ingest")
	}
	rescan(t, d)
	if got := totalIngests(d); got != first {
		t.Fatalf("ingests after the second sweep = %v, want still %v (cached again)", got, first)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

// A path the walk no longer finds is pruned from the table.
func TestSeenTablePrunesPathsThatAreGone(t *testing.T) {
	e := newEnv(t, false)
	old := time.Now().Add(-time.Hour)
	e.transcript("a.jsonl", "{\"type\":\"user\"}\n", old)
	gone := e.transcript("b.jsonl", "{\"type\":\"user\"}\n", old)
	d, _ := e.openCounted(t)
	defer func() { _ = d.Close() }()
	rescan(t, d)
	rescan(t, d)
	d.seenDB.flush()
	if n, _ := d.st.SeenCount(); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	rescan(t, d)
	if n, _ := d.st.SeenCount(); n != 1 {
		t.Fatalf("rows after a removed file = %d, want 1", n)
	}
}

// A snapshot file whose stored identity went stale while its content did not
// change (the device number of a volume changes across a reboot) is re-stamped
// by the sweep that finds it unchanged, so the table written then survives the
// next restart: that restart's first sweep skips every walked file, sees none
// as new and ingests none.
func TestSeenTableSurvivesARestartAfterTheDeviceNumberChanged(t *testing.T) {
	e := newEnv(t, false)
	old := time.Now().Add(-time.Hour)
	dir := filepath.Join(e.claude, "projects", "example-proj", "0f8fad5b-d9cb-469f-a165-70867728950e", "tool-results")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"one.txt", "two.txt"} {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("result "+n+"\n"+strings.Repeat("x", 64<<10)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	e.transcript("a.jsonl", "{\"type\":\"user\"}\n", old)
	const walked = 3

	d, _ := e.openCounted(t)
	rescan(t, d)
	rescan(t, d)
	// Age every snapshot file's stored device, as another boot would have.
	all, err := d.st.AllFiles()
	if err != nil {
		t.Fatal(err)
	}
	aged := 0
	for _, f := range all {
		if f.Mode == "snapshot" {
			if err := d.st.Commit(&store.Rec{T: store.TIdent, At: time.Now().UnixNano(), File: f.ID, Dev: f.Dev + 2, Ino: f.Ino, Birth: f.Birth}); err != nil {
				t.Fatal(err)
			}
			aged++
		}
	}
	if aged != 2 {
		t.Fatalf("aged %d snapshot files, want 2", aged)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	// Start with the stale identities: the rows of the aged files do not load,
	// the sweep re-stamps them.
	d, _ = e.openCounted(t)
	rescan(t, d)
	rescan(t, d)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, _ = e.openCounted(t)
	defer func() { _ = d.Close() }()
	if v, _ := d.obs.Reg.Gauge(obs.MSeenRows, nil); v != walked {
		t.Fatalf("seen_rows loaded after the restart = %v, want %d", v, walked)
	}
	rescan(t, d)
	if got := changedCount(d, "new"); got != 0 {
		t.Fatalf("%v walked files were new on the first sweep after a restart, want 0", got)
	}
	if got := sweepCount(d, obs.SweepSkipped); got != walked {
		t.Fatalf("skipped = %v of %d walked", got, walked)
	}
	if got := totalIngests(d); got != 0 {
		t.Fatalf("the first sweep after a restart ingested %v files, want 0", got)
	}
}
