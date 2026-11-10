package settle

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/copier"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/lifecycle"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/scope"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
)

// A poll wake with no dirty work, no due Conversation and nothing new in the
// store sends no statement to the database, with the real lifecycle engine
// answering which Conversations are live. Every Conversation stays settled, so a
// wake that re-derived anything from the store would show.
func TestPollWakeWithNoDirtyWorkReadsNothing(t *testing.T) {
	csDriverSeq++
	name := fmt.Sprintf("settle-poll-%d", csDriverSeq)
	cnt := countdriver.Register(name)
	st, err := store.Open(t.TempDir(), store.Options{DriverName: name})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const n = 200
	var recs []*store.Rec
	for i := 0; i < n; i++ {
		session := fmt.Sprintf("0a1b2c3d-0000-4000-8000-%012d", i)
		id := st.NewFileID()
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
	}
	if err := st.Commit(recs...); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Unix(0, 1e12))
	cp, err := copier.New(copier.Config{Store: st, Scope: scope.New("/c", ""), Clock: clk, Clone: &fakeClone{}, JSONRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	eng := &lifecycle.Engine{St: st}
	s := New(Config{Store: st, Copier: cp, ClaudeDir: "/c", Clock: clk, Mode: ModeEvent, Live: eng.LiveSessions})

	if _, err := s.Tick(); err != nil { // the start-up reconciliation
		t.Fatal(err)
	}
	if _, err := s.Wake(clk.Now()); err != nil { // settles into the steady state
		t.Fatal(err)
	}
	cnt.Reset()
	for i := 0; i < 3; i++ {
		clk.Advance(5 * time.Second)
		ws, err := s.Wake(clk.Now())
		if err != nil {
			t.Fatal(err)
		}
		if ws.Examined != 0 {
			t.Fatalf("poll wake %d examined %d Conversations, want 0", i, ws.Examined)
		}
	}
	if q, r := cnt.Queries(), cnt.RowsRead(); q != 0 || r != 0 {
		t.Fatalf("three idle poll wakes sent %d statements and read %d rows, want none", q, r)
	}
}
