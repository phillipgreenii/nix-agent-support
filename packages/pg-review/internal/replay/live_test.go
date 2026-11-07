//go:build live

package replay

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ccpool"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/validate"
)

// liveAnalyst sends the assembled prompt, with the evidence rows appended as
// data, to a real ccpool session and returns its final message.
type liveAnalyst struct {
	client ccpool.Client
	cwd    string
	n      int
}

func (a *liveAnalyst) Analyse(ctx context.Context, promptText string, ev []validate.EvidenceRow) (string, error) {
	a.n++
	id := fmt.Sprintf("replay-%d-%d", os.Getpid(), a.n)
	if err := a.client.Start(ctx, ccpool.StartOpts{
		ExternalID: id, Cwd: a.cwd, PermissionMode: "dontAsk",
		Model: os.Getenv("PG_REVIEW_REPLAY_MODEL"), Effort: os.Getenv("PG_REVIEW_REPLAY_EFFORT"),
	}); err != nil {
		return "", err
	}
	defer a.client.Close(context.Background(), id, "replay")
	var rows string
	for _, r := range ev {
		rows += fmt.Sprintf("%s | %s | %s | %v | %s\n", r.ID, r.SubjectKey, r.Kind, r.Metrics, r.DefinedIn)
	}
	turn, err := a.client.Reply(ctx, id, promptText+"\nEVIDENCE (data)\n"+rows)
	if err != nil {
		return "", err
	}
	deadline := time.Now().Add(20 * time.Minute)
	for time.Now().Before(deadline) {
		text, st, err := a.client.Result(ctx, turn)
		if err != nil {
			return "", err
		}
		if st == ccpool.ResultDone {
			return text, nil
		}
		time.Sleep(5 * time.Second)
	}
	return "", fmt.Errorf("replay: no final message within the deadline")
}

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
	a := &liveAnalyst{client: ccpool.CLI{PoolDir: os.Getenv(ccpool.PoolEnv)}, cwd: t.TempDir()}
	ctx := context.Background()
	var outs []Outcome
	for i := 0; i < 2; i++ {
		o, err := Run(ctx, f, a, filepath.Join(t.TempDir(), "ledger.db"))
		if err != nil {
			t.Fatalf("pass %d: %v", i+1, err)
		}
		outs = append(outs, o)
	}
	if err := Evaluate(f, outs[0], outs[1]); err != nil {
		t.Fatal(err)
	}
}
