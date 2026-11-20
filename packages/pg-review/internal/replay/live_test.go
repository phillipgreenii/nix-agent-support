//go:build live

package replay

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ccpool"
)

// maxAttempts bounds the fresh sessions of one pass: a reply that is rejected
// even after the one repair Run sends starts over once.
const maxAttempts = 2

// TestLiveReplay runs one lens against a real analyst twice. It is an
// on-demand check (go test -tags live) and never part of the unit gate. Select
// the lens with PG_REVIEW_REPLAY_LENS.
func TestLiveReplay(t *testing.T) {
	lens := os.Getenv("PG_REVIEW_REPLAY_LENS")
	if lens == "" {
		t.Skip("PG_REVIEW_REPLAY_LENS is not set")
	}
	f, err := LoadFixture(fixtureRoot, lens)
	if err != nil {
		t.Fatal(err)
	}
	a := &SessionAnalyst{
		Client: ccpool.CLI{PoolDir: os.Getenv(ccpool.PoolEnv)}, Cwd: t.TempDir(),
		Model: os.Getenv("PG_REVIEW_REPLAY_MODEL"), Effort: os.Getenv("PG_REVIEW_REPLAY_EFFORT"),
	}
	ctx := context.Background()
	// Close every session this test opened, on pass, failure and Fatal alike.
	t.Cleanup(func() {
		if err := a.CloseAll(ctx); err != nil {
			t.Errorf("sessions left open (close them with ccpool close): %v", err)
		}
	})
	var outs []Outcome
	for i := 0; i < 2; i++ {
		var o Outcome
		var err error
		for try := 1; try <= maxAttempts; try++ {
			o, err = Run(ctx, f, a, filepath.Join(t.TempDir(), "ledger.db"))
			if err == nil {
				break
			}
			t.Logf("pass %d attempt %d: %v", i+1, try, err)
		}
		if err != nil {
			t.Fatalf("pass %d: %v", i+1, err)
		}
		outs = append(outs, o)
	}
	if err := Evaluate(f, outs[0], outs[1]); err != nil {
		t.Fatal(err)
	}
}
