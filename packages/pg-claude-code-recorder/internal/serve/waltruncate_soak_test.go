//go:build soak

package serve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// TestSoakWALTruncatePastLoweredThreshold grows a real WAL past a lowered
// wal.truncate_above (1 MiB instead of 512 MiB: the unit-gate test at the
// default is vacuous) and checks the periodic TRUNCATE empties it. It sits
// behind the soak tag because it commits tens of thousands of rows.
func TestSoakWALTruncatePastLoweredThreshold(t *testing.T) {
	const limit = 1 << 20
	d, _, _ := tickEnv()
	dir := t.TempDir()
	s, err := store.Open(dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	wal := filepath.Join(dir, store.DBName+"-wal")
	for i := 0; i < 100000; i++ {
		if err := s.Commit(&store.Rec{T: store.TFile, At: 1, File: s.NewFileID(), Source: "x", Mode: "append"}); err != nil {
			t.Fatal(err)
		}
		if i%1000 == 999 {
			if fi, err := os.Stat(wal); err == nil && fi.Size() > limit {
				break
			}
		}
	}
	fi, err := os.Stat(wal)
	if err != nil || fi.Size() <= limit {
		t.Fatalf("setup: WAL %v %v, want over %d", fi, err, limit)
	}
	d.st = s
	d.cfg = config.Config{StateDir: dir, WALTruncateAbove: limit}
	d.backfillDone.Store(true)
	d.maybeTruncate()
	if v, _ := d.obs.Reg.Counter(obs.MWALTruncateTotal, map[string]string{"outcome": "ok"}); v != 1 {
		t.Fatalf("no ok truncate recorded")
	}
	if fi, err = os.Stat(wal); err != nil || fi.Size() != 0 {
		t.Fatalf("WAL after TRUNCATE %v %v, want empty", fi, err)
	}
}
