package lifecycle

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

const (
	liveProcStart = "Wed Nov  4 15:56:56 2026"
	liveSessA     = "d503eec4-6b2c-4372-a989-bb1d28e84e80"
	liveSessB     = "e614f5d5-7c3d-4483-b09a-cc2e39f95f91"
)

func liveRegistry(pid int, session, status string) string {
	return fmt.Sprintf(`{"pid": %d, "sessionId": %q, "cwd": "/work/example", "startedAt": 1793807817432, "procStart": %q, "version": "2.1.314", "kind": "bg", "entrypoint": "cli", "status": %q, "updatedAt": 1793993674849}`,
		pid, session, liveProcStart, status)
}

type liveEnv struct {
	t   *testing.T
	k   *testkit.Kit
	e   *Engine
	cnt *countdriver.Counter
}

func newLiveEnv(t *testing.T) *liveEnv {
	t.Helper()
	csName := fmt.Sprintf("lifecycle-live-%s", t.Name())
	cnt := countdriver.Register(csName)
	k := testkit.NewWithStoreOptions(t, store.Options{DriverName: csName})
	return &liveEnv{t: t, k: k, e: &Engine{St: k.Store}, cnt: cnt}
}

// full is the answer derived from the complete model, with no cache.
func (v *liveEnv) full() map[string]bool {
	v.t.Helper()
	m, err := v.e.loadCtx(context.Background())
	if err != nil {
		v.t.Fatal(err)
	}
	out := map[string]bool{}
	for session := range m.segs {
		if c, ok := m.conversation(session); ok && c.State == StateOpen {
			out[session] = true
		}
	}
	return out
}

// live asks the engine and checks the answer against the uncached derivation.
func (v *liveEnv) live(step string) (got map[string]bool, queries int64) {
	v.t.Helper()
	v.cnt.Reset()
	got, err := v.e.LiveSessions()
	if err != nil {
		v.t.Fatal(err)
	}
	queries = v.cnt.Queries()
	if want := v.full(); !reflect.DeepEqual(got, want) {
		v.t.Fatalf("%s: LiveSessions %v, uncached derivation %v", step, got, want)
	}
	return got, queries
}

// A poll with no registry record or Process observation since the last answer
// reads nothing; every input that decides the answer reloads it, and the answer
// always equals the uncached derivation.
func TestLiveSessionsReadsNothingUntilALiveInputChanges(t *testing.T) {
	v := newLiveEnv(t)

	if got, q := v.live("empty store"); len(got) != 0 || q == 0 {
		t.Fatalf("empty store: %v after %d queries, want none after a first load", got, q)
	}
	if got, q := v.live("empty again"); len(got) != 0 || q != 0 {
		t.Fatalf("repeat on an empty store: %v after %d queries, want no reads", got, q)
	}

	v.k.AddRegistry("73291", liveRegistry(73291, liveSessA, "idle"))
	v.k.Parse()
	if got, q := v.live("registry parsed"); !got[liveSessA] || q == 0 {
		t.Fatalf("registry parsed: %v after %d queries, want %s reloaded", got, q, liveSessA)
	}
	for i := 0; i < 3; i++ {
		if got, q := v.live("poll"); !got[liveSessA] || q != 0 {
			t.Fatalf("poll %d: %v after %d queries, want a cached answer without reads", i, got, q)
		}
	}

	// Writes that cannot change the answer: a transcript file and its parse, a
	// copy commit, a stage row.
	v.k.Add("p", liveSessA+".jsonl", `{"type":"user","sessionId":"`+liveSessA+`","timestamp":"2026-11-04T15:57:00Z","message":{"role":"user","content":"hi"}}`+"\n")
	v.k.Parse()
	if err := v.k.Store.Commit(&store.Rec{T: store.TStage, At: 5, Session: liveSessA, State: store.StageSettled}); err != nil {
		t.Fatal(err)
	}
	if got, q := v.live("unrelated writes"); !got[liveSessA] || q != 0 {
		t.Fatalf("unrelated writes: %v after %d queries, want a cached answer", got, q)
	}

	// A second registry file for another Process.
	v.k.AddRegistry("73292", liveRegistry(73292, liveSessB, "busy"))
	v.k.Parse()
	if got, q := v.live("second registry"); !got[liveSessA] || !got[liveSessB] || q == 0 {
		t.Fatalf("second registry: %v after %d queries, want both reloaded", got, q)
	}

	// The caller owns the map it gets.
	got, _ := v.live("before mutation")
	delete(got, liveSessA)
	got["bogus"] = true
	if again, q := v.live("after mutation"); !again[liveSessA] || again["bogus"] || q != 0 {
		t.Fatalf("a caller's edit reached the cache: %v after %d queries", again, q)
	}

	// A Process observation: the first Process exits.
	if err := v.k.Store.Commit(&store.Rec{T: store.TProc, Kind: "exit", Pid: 73291, ProcStart: liveProcStart, At: 9, Reason: store.ExitObserved}); err != nil {
		t.Fatal(err)
	}
	if got, q := v.live("process exit"); got[liveSessA] || !got[liveSessB] || q == 0 {
		t.Fatalf("process exit: %v after %d queries, want only %s, reloaded", got, q, liveSessB)
	}
	if _, q := v.live("after exit"); q != 0 {
		t.Fatalf("after exit: %d queries, want a cached answer", q)
	}
}
