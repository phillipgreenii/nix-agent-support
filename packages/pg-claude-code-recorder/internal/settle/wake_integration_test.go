//go:build integration

package settle

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/copier"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/scope"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
)

// wakeQueryBound is the most store queries a wake over one dirty session may
// issue with thousands of settled Conversations in the store. Measured: 5 (the
// dirty file's group re-read, the stage row, the reads of changedSinceSettle and
// the stage read after the evaluation); the bound leaves a margin of 15 for a
// statement added to those paths.
const wakeQueryBound = 20

func TestWakeCostDoesNotDependOnTheNumberOfSettledConversations(t *testing.T) {
	csDriverSeq++
	name := fmt.Sprintf("settle-wake-%d", csDriverSeq)
	cnt := countdriver.Register(name)
	st, err := store.Open(t.TempDir(), store.Options{DriverName: name})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const n = 8000
	var recs []*store.Rec
	files := make([]int64, n)
	for i := 0; i < n; i++ {
		session := fmt.Sprintf("0a1b2c3d-0000-4000-8000-%012d", i)
		id := st.NewFileID()
		files[i] = id
		m, _ := json.Marshal(store.Manifest{Session: session, Method: "bytes", Files: []store.ManifestFile{{File: id, Mode: "append", Gen: 1, Size: 10}}})
		val, _ := json.Marshal(store.SettleRec{Manifest: m})
		recs = append(
			recs,
			&store.Rec{T: store.TFile, At: 10, File: id, Source: "claude-transcript", Mode: "append", Dev: 1, Ino: uint64(i + 1), Birth: 3},
			&store.Rec{T: store.TPath, At: 10, File: id, Path: "/c/projects/p/" + session + ".jsonl"},
			&store.Rec{T: store.TGen, At: 11, File: id, Gen: 1, Method: "bytes", Reason: "first"},
			&store.Rec{T: store.TCommit, At: 12, File: id, Gen: 1, From: 0, To: 10, Size: 10, Tail: 1, TailLen: 1},
			&store.Rec{T: store.TSettle, At: 20, Session: session, Method: "bytes", Val: string(val)},
			&store.Rec{T: store.TStage, At: 30, Session: session, State: store.StageSettled},
		)
		if len(recs) >= 6000 {
			if err := st.Commit(recs...); err != nil {
				t.Fatal(err)
			}
			recs = nil
		}
	}
	if len(recs) > 0 {
		if err := st.Commit(recs...); err != nil {
			t.Fatal(err)
		}
	}
	clk := clock.NewFake(time.Unix(0, 1e12))
	cp, err := copier.New(copier.Config{Store: st, Scope: scope.New("/c", ""), Clock: clk, Clone: &fakeClone{}, JSONRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{
		Store: st, Copier: cp, ClaudeDir: "/c", Clock: clk, Mode: ModeEvent,
		Live: func() (map[string]bool, error) { return map[string]bool{}, nil },
	})

	cnt.Reset()
	stats, err := s.Tick() // the start-up reconciliation
	if err != nil {
		t.Fatal(err)
	}
	scanRows := cnt.RowsRead()
	if stats.Examined != n || stats.Unchanged != n {
		t.Fatalf("tick %+v, want %d examined and unchanged", stats, n)
	}
	if want := int64(100 * wakeQueryBound); scanRows <= want {
		t.Fatalf("the scan tick read %d rows, not more than %d: the bound would no longer mean anything", scanRows, want)
	}

	s.Dirty(files[4321], 10)
	cnt.Reset()
	ws, err := s.Wake(clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	q := cnt.Queries()
	t.Logf("wake: %d queries, %d rows, %d examined; scan tick read %d rows", q, cnt.RowsRead(), ws.Examined, scanRows)
	if ws.Examined != 1 {
		t.Fatalf("wake examined %d Conversations, want the 1 dirtied", ws.Examined)
	}
	// The gauges come from the kept maps: no statement that reads a whole table.
	for _, whole := range []string{
		"FROM files ORDER BY id",
		"FROM generations ORDER BY file_id, gen",
		"copy_offset FROM generations",
		"FROM ranges ORDER BY file_id,gen,from_off",
		"FROM torn_tails ORDER BY file_id,gen",
	} {
		if n := cnt.Matching(whole); n != 0 {
			t.Errorf("wake issued %d whole-table statements %q", n, whole)
		}
	}
	if q > wakeQueryBound {
		t.Fatalf("wake issued %d store queries with %d settled Conversations, bound %d", q, n, wakeQueryBound)
	}
}
