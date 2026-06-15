package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/launch"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/wait"
)

// fakes
type fakeTmux struct {
	live     map[string]bool
	newCalls []newCall
	pane     string
}
type newCall struct {
	name string
	cwd  string
	env  map[string]string
	argv []string
}

func (f *fakeTmux) HasSession(name string) bool { return f.live[name] }
func (f *fakeTmux) NewSession(name, cwd string, env map[string]string, argv []string) error {
	f.newCalls = append(f.newCalls, newCall{name, cwd, env, argv})
	f.live[name] = true
	return nil
}
func (f *fakeTmux) SendKeys(string, ...string) error   { return nil }
func (f *fakeTmux) Paste(string, string) error         { return nil }
func (f *fakeTmux) KillSession(string) error           { return nil }
func (f *fakeTmux) CapturePane(string) (string, error) { return f.pane, nil }

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

	h, err := s.Ensure(ctx, "alpha", "/tmp/proj", "", EnsureOpts{})
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
	if nc.cwd != "/tmp/proj" {
		t.Errorf("tmux session cwd = %q, want /tmp/proj (the --cwd must set the session working dir)", nc.cwd)
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

// TestEnsure_mergesCallerEnv asserts that EnsureOpts.Env is injected into the
// session at launch alongside ccpool's own correlation markers (the pool worker
// stalls without BEADS_ACTOR/BEADS_DIR/WORKSPACE_ROOT). Merge policy: ccpool's
// reserved markers (CCPOOL_NAME/CCPOOL_UUID/PA_MONITOR_NO_NUDGE) are
// authoritative and win over a colliding caller key — hooks correlate the store
// row off those markers, so a caller must never be able to clobber them.
func TestEnsure_mergesCallerEnv(t *testing.T) {
	tests := []struct {
		name      string
		callerEnv map[string]string
		wantEnv   map[string]string // subset assertions against the launched env
	}{
		{
			name:      "caller env injected alongside markers",
			callerEnv: map[string]string{"BEADS_ACTOR": "worker-1", "BEADS_DIR": "/repo/.beads", "WORKSPACE_ROOT": "/repo"},
			wantEnv: map[string]string{
				"BEADS_ACTOR": "worker-1", "BEADS_DIR": "/repo/.beads", "WORKSPACE_ROOT": "/repo",
				"CCPOOL_NAME": "alpha", "CCPOOL_UUID": "uuid-1", "PA_MONITOR_NO_NUDGE": "1",
			},
		},
		{
			name:      "reserved ccpool markers win over caller override attempts",
			callerEnv: map[string]string{"CCPOOL_UUID": "hijack", "CCPOOL_NAME": "hijack", "BEADS_DIR": "/repo/.beads"},
			wantEnv:   map[string]string{"CCPOOL_UUID": "uuid-1", "CCPOOL_NAME": "alpha", "BEADS_DIR": "/repo/.beads"},
		},
		{
			name:      "nil caller env keeps only the hardcoded markers",
			callerEnv: nil,
			wantEnv:   map[string]string{"CCPOOL_NAME": "alpha", "CCPOOL_UUID": "uuid-1", "PA_MONITOR_NO_NUDGE": "1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			st := newMemStore(t)
			ft := &fakeTmux{live: map[string]bool{}}
			waiter := waitFunc(func(_ context.Context, name string, since int64) (wait.Outcome, error) {
				_, _ = st.Transition(ctx, name, store.Ready, "", "/p/t.jsonl")
				return wait.Outcome{State: store.Ready}, nil
			})
			s := New(Deps{
				Tmux: ft, Trust: &fakeTrust{}, Store: st, Wait: waiter,
				Socket: "ccpool", Prefix: "cc-", PluginDir: "/plugin", ClaudeBin: "claude",
				NewUUID: func() string { return "uuid-1" },
				Now:     func() time.Time { return time.Unix(100, 0) },
			})
			if _, err := s.Ensure(ctx, "alpha", "/tmp/proj", "", EnsureOpts{Env: tt.callerEnv}); err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			if len(ft.newCalls) != 1 {
				t.Fatalf("NewSession calls = %d, want 1", len(ft.newCalls))
			}
			got := ft.newCalls[0].env
			for k, want := range tt.wantEnv {
				if got[k] != want {
					t.Errorf("env[%q] = %q, want %q (full env: %v)", k, got[k], want, got)
				}
			}
		})
	}
}

// TestEnsure_threadsLaunchFlagsToArgv asserts that EnsureOpts launch flags reach
// the claude argv that ensureLocked hands to tmux — the glue between the CLI
// flags and launch.BuildNew. Without a permission mode that bypasses prompts a
// dispatched worker stalls on the first tool prompt.
func TestEnsure_threadsLaunchFlagsToArgv(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	ft := &fakeTmux{live: map[string]bool{}}
	waiter := waitFunc(func(_ context.Context, name string, since int64) (wait.Outcome, error) {
		_, _ = st.Transition(ctx, name, store.Ready, "", "/p/t.jsonl")
		return wait.Outcome{State: store.Ready}, nil
	})
	s := New(Deps{
		Tmux: ft, Trust: &fakeTrust{}, Store: st, Wait: waiter,
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/plugin", ClaudeBin: "claude",
		NewUUID: func() string { return "uuid-1" },
		Now:     func() time.Time { return time.Unix(100, 0) },
	})
	if _, err := s.Ensure(ctx, "alpha", "/tmp/proj", "", EnsureOpts{PermissionMode: launch.ModeBypassPermissions, Effort: "max"}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(ft.newCalls) != 1 {
		t.Fatalf("NewSession calls = %d, want 1", len(ft.newCalls))
	}
	joined := strings.Join(ft.newCalls[0].argv, " ")
	if !strings.Contains(joined, "--permission-mode bypassPermissions") {
		t.Errorf("argv missing --permission-mode bypassPermissions: %q", joined)
	}
	if !strings.Contains(joined, "--effort max") {
		t.Errorf("argv missing --effort max: %q", joined)
	}
}

// TestEnsure_threadsPermissionModeToResumeArgv closes the resume-path gap: a
// cold (existing) row resumes by name, and the permission mode must still reach
// the claude argv via launch.BuildResume.
func TestEnsure_threadsPermissionModeToResumeArgv(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	// Seed a cold row so ensureLocked takes the resume branch.
	if err := st.Insert(ctx, store.Session{Name: "alpha", UUID: "uuid-1", CWD: "/tmp/proj", State: store.Done, TmuxSession: "cc-alpha"}); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	ft := &fakeTmux{live: map[string]bool{}} // not live → resume path
	waiter := waitFunc(func(_ context.Context, name string, since int64) (wait.Outcome, error) {
		_, _ = st.Transition(ctx, name, store.Ready, "", "/p/t.jsonl")
		return wait.Outcome{State: store.Ready}, nil
	})
	s := New(Deps{
		Tmux: ft, Trust: &fakeTrust{}, Store: st, Wait: waiter,
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/plugin", ClaudeBin: "claude",
		NewUUID: func() string { return "uuid-1" },
		Now:     func() time.Time { return time.Unix(100, 0) },
	})
	if _, err := s.Ensure(ctx, "alpha", "/tmp/proj", "", EnsureOpts{PermissionMode: launch.ModeBypassPermissions, Effort: "max"}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(ft.newCalls) != 1 {
		t.Fatalf("NewSession calls = %d, want 1", len(ft.newCalls))
	}
	joined := strings.Join(ft.newCalls[0].argv, " ")
	if !strings.Contains(joined, "--resume alpha") {
		t.Errorf("resume argv expected; got %q", joined)
	}
	if !strings.Contains(joined, "--permission-mode bypassPermissions") {
		t.Errorf("resume argv missing --permission-mode bypassPermissions: %q", joined)
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
	if _, err := s.Ensure(ctx, "alpha", "/tmp/proj", "", EnsureOpts{}); err != nil {
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

	h, err := s.Ensure(ctx, "beta", "/tmp/proj", "", EnsureOpts{})
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
