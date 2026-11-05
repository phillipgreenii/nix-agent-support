package serve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

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
