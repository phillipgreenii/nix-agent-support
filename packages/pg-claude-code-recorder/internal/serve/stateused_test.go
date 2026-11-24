package serve

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// The refresh pass recomputes the state-used figure (a whole-table scan of the
// generations and snapshots) at most once per stateUsedEvery, not every pass.
func TestRefreshGaugesPassThrottlesStateUsedScan(t *testing.T) {
	d, _, c := openCounting(t, "state-used-throttle-counting")
	clk := clock.NewFake(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
	d.clock = clk
	const scan = "FROM generations g"
	c.Reset()
	d.refreshGaugesPass()
	if n := c.Matching(scan); n != 1 {
		t.Fatalf("first pass ran the scan %d times, want 1", n)
	}
	for i := 0; i < 11; i++ { // 11 passes, 5 s apart: 55 s, inside the interval
		clk.Advance(5 * time.Second)
		d.refreshGaugesPass()
	}
	if n := c.Matching(scan); n != 1 {
		t.Fatalf("scan ran %d times within %v, want 1", n, stateUsedEvery)
	}
	clk.Advance(5 * time.Second) // 60 s since the first
	d.refreshGaugesPass()
	if n := c.Matching(scan); n != 2 {
		t.Fatalf("scan ran %d times after the interval, want 2", n)
	}
}

func TestStateUsedBytesIsDatabaseDerivedNoWalk(t *testing.T) {
	e := newEnv(t, false)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	dir := e.cfg.StateDir
	id := d.Store().NewFileID()
	if err := d.Store().Commit(
		&store.Rec{T: store.TFile, At: 10, File: id, Source: "claude-transcript", Mode: "append", Dev: 1, Ino: 2, Birth: 3},
		&store.Rec{T: store.TPath, At: 10, File: id, Path: "/c/projects/p/s.jsonl"},
		&store.Rec{T: store.TGen, At: 11, File: id, Gen: 1, Method: "bytes", Reason: "first"},
		&store.Rec{T: store.TCommit, At: 12, File: id, Gen: 1, From: 0, To: 10, Size: 10, Tail: 77, TailLen: 10},
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.Store().CopyPath(id, 1), make([]byte, 10), 0o600); err != nil {
		t.Fatal(err)
	}
	// Orphan negative control: a file in the state tree no table names is NOT counted.
	if err := os.WriteFile(filepath.Join(dir, store.CopiesDir, "999.1"), make([]byte, 4<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	// The oracle: what the figure promises to count, summed from real files.
	want := int64(10 + 10) // copy + its tail sidecar length recorded in the row
	for _, n := range stateFiles {
		if fi, err := os.Stat(filepath.Join(dir, n)); err == nil {
			want += fi.Size()
		}
	}
	got, err := StateUsedBytes(&d.Store().Reader, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("StateUsedBytes %d, want %d", got, want)
	}
	if full := DirBytes(dir); got >= full {
		t.Fatalf("database figure %d should be below the full walk %d (orphan, archives excluded)", got, full)
	}

	// Quarantine is added on top exactly once: the quarantined originals live
	// beside the Claude files, outside every summed table. index.db-wal drifts
	// between two reads, so a large quarantine value makes a double count (or a
	// missing add) obvious against a loose tolerance.
	const q = 1 << 40
	d.obs.Reg.Set(obs.MQuarantineBytes, nil, q)
	d.refreshGaugesPass()
	v, _ := d.obs.Reg.Gauge(obs.MStateUsedBytes, nil)
	g2, _ := StateUsedBytes(&d.Store().Reader, dir)
	if diff := int64(v) - q - g2; diff < -1<<20 || diff > 1<<20 {
		t.Fatalf("gauge %v, want about %d (figure %d plus quarantine %d)", v, g2+q, g2, int64(q))
	}
}
