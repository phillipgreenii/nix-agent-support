package session

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/wait"
)

// fakes
type fakeTmux struct {
	live     map[string]bool
	newCalls []newCall
}
type newCall struct {
	name string
	env  map[string]string
	argv []string
}

func (f *fakeTmux) HasSession(name string) bool { return f.live[name] }
func (f *fakeTmux) NewSession(name string, env map[string]string, argv []string) error {
	f.newCalls = append(f.newCalls, newCall{name, env, argv})
	f.live[name] = true
	return nil
}

type fakeTrust struct{ trusted []string }

func (f *fakeTrust) EnsureTrusted(cwd string) error { f.trusted = append(f.trusted, cwd); return nil }

// a store-backed test using the real store + a hook-like transition to ready.
func TestEnsure_new_insertsBeforeLaunch_waitsReady(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t) // helper below
	ft := &fakeTmux{live: map[string]bool{}}
	ftr := &fakeTrust{}

	// waiter that, on first poll, simulates the SessionStart hook having flipped
	// the row to ready (advance generation in the store, then report it).
	waiter := waitFunc(func(_ context.Context, name string, since int64) (wait.Outcome, error) {
		_, _ = st.Transition(ctx, name, store.Ready, "", "/p/t.jsonl")
		return wait.Outcome{State: store.Ready}, nil
	})

	s := New(Deps{
		Tmux: ft, Trust: ftr, Store: st, Wait: waiter,
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/plugin", ClaudeBin: "claude",
		NewUUID: func() string { return "uuid-1" },
		Now:     func() time.Time { return time.Unix(100, 0) },
	})

	h, err := s.Ensure(ctx, "alpha", "/tmp/proj", "")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if h.Name != "alpha" || h.State != store.Ready {
		t.Errorf("handle = %+v", h)
	}
	// Row inserted before launch.
	if len(ft.newCalls) != 1 {
		t.Fatalf("NewSession calls = %d, want 1", len(ft.newCalls))
	}
	nc := ft.newCalls[0]
	if nc.name != "cc-alpha" {
		t.Errorf("tmux session name = %q, want cc-alpha", nc.name)
	}
	if nc.env["CCPOOL_NAME"] != "alpha" || nc.env["CCPOOL_UUID"] != "uuid-1" || nc.env["PA_MONITOR_NO_NUDGE"] != "1" {
		t.Errorf("env markers missing: %v", nc.env)
	}
	if nc.argv[0] != "claude" || nc.argv[1] != "--session-id" || nc.argv[2] != "uuid-1" {
		t.Errorf("argv = %v", nc.argv)
	}
	if len(ftr.trusted) != 1 || ftr.trusted[0] != "/tmp/proj" {
		t.Errorf("cwd not pre-trusted: %v", ftr.trusted)
	}
	row, _, _ := st.GetByName(ctx, "alpha")
	if row.UUID != "uuid-1" {
		t.Errorf("row uuid = %q, want uuid-1", row.UUID)
	}
}

func TestEnsure_liveSessionReused_noLaunch(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "alpha", UUID: "u", State: store.Ready, TmuxSession: "cc-alpha"})
	ft := &fakeTmux{live: map[string]bool{"cc-alpha": true}}

	s := New(Deps{
		Tmux: ft, Trust: &fakeTrust{}, Store: st, Wait: waitFunc(nil),
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/p", ClaudeBin: "claude",
		NewUUID: func() string { return "x" }, Now: func() time.Time { return time.Unix(1, 0) },
	})
	if _, err := s.Ensure(ctx, "alpha", "/tmp/proj", ""); err != nil {
		t.Fatal(err)
	}
	if len(ft.newCalls) != 0 {
		t.Errorf("reused live session must not launch; got %d launches", len(ft.newCalls))
	}
}

func newMemStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:", fixedClock{})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Unix(100, 0) }

func TestEnsure_resume_flipsToStartingBeforeLaunch_thenReady(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	// Cold row in a terminal state; not live → resume path. Insert sets generation=1.
	if err := st.Insert(ctx, store.Session{Name: "beta", UUID: "u-beta", State: store.Done, TmuxSession: "cc-beta", Model: "opus"}); err != nil {
		t.Fatal(err)
	}
	ft := &fakeTmux{live: map[string]bool{}}
	var sawState store.State
	var sawSince int64
	waiter := waitFunc(func(_ context.Context, name string, since int64) (wait.Outcome, error) {
		// At wait time the row must already be `starting` (flipped before launch, §8.2 step 3).
		row, _, _ := st.GetByName(ctx, name)
		sawState = row.State
		sawSince = since
		_, _ = st.Transition(ctx, name, store.Ready, "", "/p/beta.jsonl")
		return wait.Outcome{State: store.Ready}, nil
	})
	s := New(Deps{
		Tmux: ft, Trust: &fakeTrust{}, Store: st, Wait: waiter,
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/p", ClaudeBin: "claude",
		NewUUID: func() string { return "must-not-be-used" },
		Now:     func() time.Time { return time.Unix(1, 0) },
	})

	h, err := s.Ensure(ctx, "beta", "/tmp/proj", "")
	if err != nil {
		t.Fatalf("Ensure resume: %v", err)
	}
	if h.UUID != "u-beta" {
		t.Errorf("resume must preserve uuid u-beta, got %q", h.UUID)
	}
	if sawState != store.Starting {
		t.Errorf("row state at launch = %q, want starting (flipped before launch)", sawState)
	}
	if sawSince != 2 { // gen 1 after Insert, 2 after the starting-flip
		t.Errorf("since = %d, want 2 (read-back post-flip generation, not a magic literal)", sawSince)
	}
	if len(ft.newCalls) != 1 || ft.newCalls[0].argv[1] != "--resume" {
		t.Errorf("resume must use BuildResume argv; got %v", ft.newCalls)
	}
	if h.State != store.Ready {
		t.Errorf("final state = %q, want ready", h.State)
	}
}
