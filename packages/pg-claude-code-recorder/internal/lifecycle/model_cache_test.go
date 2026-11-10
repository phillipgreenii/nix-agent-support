package lifecycle

import (
	"context"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

const modelHistoryClear = `{"display":"/clear","timestamp":1761768000000,"project":"/work/example","sessionId":"` + liveSessA + `"}` + "\n"

// A request the engine already answered reads nothing until an input of the
// model changes: the registry, the history, the Process observations or the
// stages; the answer always equals a fresh load.
func TestLoadCtxServesTheCachedModelUntilAModelInputChanges(t *testing.T) {
	v := newLiveEnv(t)
	ctx := context.Background()

	load := func(step string, wantReads bool) *model {
		t.Helper()
		v.cnt.Reset()
		m, err := v.e.loadCtx(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if reads := v.cnt.Queries(); wantReads != (reads > 0) {
			t.Fatalf("%s: %d queries, want reads=%v", step, reads, wantReads)
		}
		fresh, err := v.e.loadWith(ctx, false)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(m, fresh) {
			t.Fatalf("%s: cached model differs from a fresh load", step)
		}
		return m
	}

	load("first load", true)
	for i := 0; i < 5; i++ {
		load("poll", false)
	}

	v.k.AddRegistry("73291", liveRegistry(73291, liveSessA, "idle"))
	v.k.Parse()
	if m := load("registry", true); len(m.segs[liveSessA]) == 0 {
		t.Fatal("the new registry file is not in the model")
	}
	load("registry poll", false)

	// A transcript write cannot change the model.
	v.k.Add("p", liveSessA+".jsonl", `{"type":"user","sessionId":"`+liveSessA+`","timestamp":"2026-11-04T15:57:00Z","message":{"role":"user","content":"hi"}}`+"\n")
	v.k.Parse()
	load("transcript", false)

	v.k.Add("", "history.jsonl", modelHistoryClear)
	v.k.Parse()
	if m := load("history", true); len(m.clears[liveSessA]) == 0 {
		t.Fatal("the history entry is not in the model")
	}
	load("history poll", false)

	if err := v.k.Store.Commit(&store.Rec{T: store.TStage, At: 5, Session: liveSessA, State: store.StageSettled}); err != nil {
		t.Fatal(err)
	}
	if m := load("stage", true); m.stages[liveSessA] != store.StageSettled {
		t.Fatalf("stage %q in the model", m.stages[liveSessA])
	}
	load("stage poll", false)

	if err := v.k.Store.Commit(&store.Rec{T: store.TProc, Kind: "exit", Pid: 73291, ProcStart: liveProcStart, At: 9, Reason: store.ExitObserved}); err != nil {
		t.Fatal(err)
	}
	load("process exit", true)
	load("exit poll", false)
}

// The list, show and process requests of a poll with nothing new read no model
// data; this is the per-request SQLite work the cache removes.
func TestRepeatedRequestsReadNoModelDataFromTheStore(t *testing.T) {
	v := newLiveEnv(t)
	v.k.AddRegistry("73291", liveRegistry(73291, liveSessA, "idle"))
	v.k.Parse()
	ctx := context.Background()
	run := func() {
		if _, err := v.e.Processes(ctx, ProcessFilter{}); err != nil {
			t.Fatal(err)
		}
		if _, err := v.e.Process(ctx, 73291, ""); err != nil {
			t.Fatal(err)
		}
	}
	run()
	v.cnt.Reset()
	for i := 0; i < 20; i++ {
		run()
	}
	if q := v.cnt.Queries(); q != 0 {
		t.Fatalf("40 repeated requests ran %d queries, want 0", q)
	}
}
