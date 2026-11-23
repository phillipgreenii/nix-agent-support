package lifecycle

import (
	"context"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// A reload of the model that a registry record, a Process observation or a
// stage row caused (the status loop's crash count asks for one every few
// seconds on a busy host) MUST NOT scan the history and end-reason records
// again: only a history or control-end commit, a row removal or a rolled-back
// write invalidates them. The reloaded model always equals a fresh engine's.
func TestModelReloadKeepsTheHistoryUntilAHistoryInputChanges(t *testing.T) {
	v := newLiveEnv(t)
	ctx := context.Background()

	histReads := func() int64 { return v.cnt.Matching("claude-history") + v.cnt.Matching("claude-control-end") }
	load := func(step string, wantHistory bool) *model {
		t.Helper()
		v.cnt.Reset()
		m, err := v.e.loadCtx(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got := histReads(); wantHistory != (got > 0) {
			t.Fatalf("%s: %d history queries, want history=%v", step, got, wantHistory)
		}
		fresh, err := (&Engine{St: v.e.St}).loadWith(ctx, false)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(m, fresh) {
			t.Fatalf("%s: model differs from a fresh engine's", step)
		}
		return m
	}

	load("first load", true)

	v.k.AddRegistry("73291", liveRegistry(73291, liveSessA, "idle"))
	v.k.Parse()
	if m := load("registry", false); len(m.segs[liveSessA]) == 0 {
		t.Fatal("the new registry file is not in the model")
	}

	v.writeRegistry(73291, liveSessA, "busy")
	load("registry rewrite", false)

	if err := v.k.Store.Commit(&store.Rec{T: store.TStage, At: 5, Session: liveSessA, State: store.StageSettled}); err != nil {
		t.Fatal(err)
	}
	if m := load("stage", false); m.stages[liveSessA] != store.StageSettled {
		t.Fatalf("stage %q in the model", m.stages[liveSessA])
	}

	if err := v.k.Store.Commit(&store.Rec{T: store.TProc, Kind: "seen", Pid: 73291, ProcStart: liveProcStart, At: 9}); err != nil {
		t.Fatal(err)
	}
	load("process observation", false)

	// A torn-tail fact is no input of the model at all.
	if err := v.k.Store.Commit(&store.Rec{T: store.TTorn, At: 11, File: 1, Gen: 1, From: 0, To: 4}); err != nil {
		t.Fatal(err)
	}
	load("torn tail", false)

	v.k.Add("", "history.jsonl", modelHistoryClear)
	v.k.Parse()
	if m := load("history", true); len(m.clears[liveSessA]) == 0 {
		t.Fatal("the history entry is not in the model")
	}
	load("history poll", false)
	v.writeRegistry(73291, liveSessA, "idle")
	load("registry after history", false)
}
