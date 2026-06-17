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

// fakeExister is the SessionExister test seam: ok reports whether the recorded
// transcript path is "on disk" without touching a real filesystem.
type fakeExister struct{ ok bool }

func (f fakeExister) Exists(string) bool { return f.ok }

// TestTmuxName_sanitizesTargetSeparators locks the session-name sanitizer: tmux
// treats '.' and ':' as target separators (session.window.pane), silently
// rewriting them to '_' on new-session and failing to resolve them on
// has-session. A sub-bead external_id like "...zr-fy5j5.1..." must therefore be
// mapped to the '_' form so create/has-session/kill all address one stored name.
func TestTmuxName_sanitizesTargetSeparators(t *testing.T) {
	if got := TmuxName("cc-", "pr-pool-worker-zr-fy5j5.1-stamp"); got != "cc-pr-pool-worker-zr-fy5j5_1-stamp" {
		t.Errorf("TmuxName dotted = %q, want cc-pr-pool-worker-zr-fy5j5_1-stamp", got)
	}
	if got := TmuxName("cc-", "a:b.c"); got != "cc-a_b_c" {
		t.Errorf("TmuxName colon+dot = %q, want cc-a_b_c", got)
	}
	if got := TmuxName("cc-", "plain-ext"); got != "cc-plain-ext" {
		t.Errorf("TmuxName plain = %q, want cc-plain-ext (unchanged)", got)
	}
}

// TestEnsure_sanitizesDottedExternalIDInTmuxName is the regression for the
// sub-bead dispatch failure: an external_id with a '.' (e.g. worker bead
// "zr-fy5j5.1") must reach tmux as the '_'-sanitized session name, so the name
// ccpool creates, finds, and kills all match the one tmux stores. Without the
// fix, NewSession is called with the dotted name; tmux rewrites it to '_' and a
// later has-session can't find it, producing "duplicate session" on re-create.
func TestEnsure_sanitizesDottedExternalIDInTmuxName(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	ft := &fakeTmux{live: map[string]bool{}}
	waiter := waitFunc(func(_ context.Context, externalID string, since int64) (wait.Outcome, error) {
		_, _ = st.Transition(ctx, externalID, store.Ready, "", "/p/t.jsonl")
		return wait.Outcome{State: store.Ready}, nil
	})
	s := New(Deps{
		Tmux: ft, Trust: &fakeTrust{}, Store: st, Wait: waiter,
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/plugin", ClaudeBin: "claude",
		NewUUID: func() string { return "csid-1" },
		Now:     func() time.Time { return time.Unix(100, 0) },
	})

	if _, err := s.Ensure(ctx, "pr-pool-worker-zr-fy5j5.1-stamp", "/tmp/proj", "", EnsureOpts{}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(ft.newCalls) != 1 {
		t.Fatalf("NewSession calls = %d, want 1", len(ft.newCalls))
	}
	if got := ft.newCalls[0].name; got != "cc-pr-pool-worker-zr-fy5j5_1-stamp" {
		t.Errorf("tmux session name = %q, want sanitized cc-pr-pool-worker-zr-fy5j5_1-stamp", got)
	}
}

// a store-backed test using the real store + a hook-like transition to ready.
func TestEnsure_brandNewWhenNoRow(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t) // helper below
	ft := &fakeTmux{live: map[string]bool{}}
	ftr := &fakeTrust{}

	// waiter that, on first poll, simulates the SessionStart hook having flipped
	// the row to ready (advance generation in the store, then report it).
	waiter := waitFunc(func(_ context.Context, externalID string, since int64) (wait.Outcome, error) {
		_, _ = st.Transition(ctx, externalID, store.Ready, "", "/p/t.jsonl")
		return wait.Outcome{State: store.Ready}, nil
	})

	s := New(Deps{
		Tmux: ft, Trust: ftr, Store: st, Wait: waiter,
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/plugin", ClaudeBin: "claude",
		NewUUID: func() string { return "csid-1" },
		Now:     func() time.Time { return time.Unix(100, 0) },
	})

	h, err := s.Ensure(ctx, "ext-alpha", "/tmp/proj", "", EnsureOpts{Name: "display-alpha"})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if h.ExternalID != "ext-alpha" || h.State != store.Ready || h.ClaudeSessionID != "csid-1" {
		t.Errorf("handle = %+v", h)
	}
	if len(ft.newCalls) != 1 {
		t.Fatalf("NewSession calls = %d, want 1", len(ft.newCalls))
	}
	nc := ft.newCalls[0]
	if nc.name != "cc-ext-alpha" {
		t.Errorf("tmux session name = %q, want cc-ext-alpha", nc.name)
	}
	if nc.cwd != "/tmp/proj" {
		t.Errorf("tmux session cwd = %q, want /tmp/proj", nc.cwd)
	}
	if nc.env["CCPOOL_EXTERNAL_ID"] != "ext-alpha" || nc.env["PA_MONITOR_NO_NUDGE"] != "1" {
		t.Errorf("env markers missing: %v", nc.env)
	}
	if nc.argv[0] != "claude" || nc.argv[1] != "--session-id" || nc.argv[2] != "csid-1" {
		t.Errorf("argv = %v", nc.argv)
	}
	if !strings.Contains(strings.Join(nc.argv, " "), "--name display-alpha") {
		t.Errorf("argv missing --name display-alpha: %v", nc.argv)
	}
	if len(ftr.trusted) != 1 || ftr.trusted[0] != "/tmp/proj" {
		t.Errorf("cwd not pre-trusted: %v", ftr.trusted)
	}
	row, _, _ := st.GetByExternalID(ctx, "ext-alpha")
	if row.ClaudeSessionID != "csid-1" || row.Name != "display-alpha" {
		t.Errorf("row = %+v, want csid-1/display-alpha", row)
	}
}

// TestEnsure_mergesCallerEnv asserts that EnsureOpts.Env is injected into the
// session at launch alongside ccpool's own correlation markers. Merge policy:
// ccpool's reserved markers (CCPOOL_EXTERNAL_ID/PA_MONITOR_NO_NUDGE) are
// authoritative and win over a colliding caller key.
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
				"CCPOOL_EXTERNAL_ID": "ext-alpha", "PA_MONITOR_NO_NUDGE": "1",
			},
		},
		{
			name:      "reserved ccpool markers win over caller override attempts",
			callerEnv: map[string]string{"CCPOOL_EXTERNAL_ID": "hijack", "BEADS_DIR": "/repo/.beads"},
			wantEnv:   map[string]string{"CCPOOL_EXTERNAL_ID": "ext-alpha", "BEADS_DIR": "/repo/.beads"},
		},
		{
			name:      "nil caller env keeps only the hardcoded markers",
			callerEnv: nil,
			wantEnv:   map[string]string{"CCPOOL_EXTERNAL_ID": "ext-alpha", "PA_MONITOR_NO_NUDGE": "1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			st := newMemStore(t)
			ft := &fakeTmux{live: map[string]bool{}}
			waiter := waitFunc(func(_ context.Context, externalID string, since int64) (wait.Outcome, error) {
				_, _ = st.Transition(ctx, externalID, store.Ready, "", "/p/t.jsonl")
				return wait.Outcome{State: store.Ready}, nil
			})
			s := New(Deps{
				Tmux: ft, Trust: &fakeTrust{}, Store: st, Wait: waiter,
				Socket: "ccpool", Prefix: "cc-", PluginDir: "/plugin", ClaudeBin: "claude",
				NewUUID: func() string { return "csid-1" },
				Now:     func() time.Time { return time.Unix(100, 0) },
			})
			if _, err := s.Ensure(ctx, "ext-alpha", "/tmp/proj", "", EnsureOpts{Env: tt.callerEnv}); err != nil {
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

// TestEnsure_neutralizesClaudeChildMarkers asserts that ccpool blanks the Claude
// "child/nested session" env markers in the launched session env. Claude Code does
// NOT persist the conversation transcript .jsonl when CLAUDE_CODE_CHILD_SESSION is
// set — it treats itself as a nested session (verified live, claude 2.1.177). So a
// ccpool session launched from INSIDE a Claude session (e.g. an agent driving
// ccpool, or `go test` running the contract suite) inherits the marker and never
// persists a transcript, breaking resume-by-claude_session_id and `ccpool result`
// (pg2-lki6). ccpool emits each marker as an EMPTY -e override (tmux `-e VAR=`
// neutralizes it — verified) so every managed session starts as a fresh top-level
// claude. The neutralizer is AUTHORITATIVE: it wins even when a caller leaks the
// marker, exactly like the reserved CCPOOL_* markers.
func TestEnsure_neutralizesClaudeChildMarkers(t *testing.T) {
	markers := []string{
		"CLAUDE_CODE_CHILD_SESSION", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT",
		"CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_EXECPATH",
	}
	ctx := context.Background()
	st := newMemStore(t)
	ft := &fakeTmux{live: map[string]bool{}}
	waiter := waitFunc(func(_ context.Context, externalID string, _ int64) (wait.Outcome, error) {
		_, _ = st.Transition(ctx, externalID, store.Ready, "", "/p/t.jsonl")
		return wait.Outcome{State: store.Ready}, nil
	})
	s := New(Deps{
		Tmux: ft, Trust: &fakeTrust{}, Store: st, Wait: waiter,
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/plugin", ClaudeBin: "claude",
		NewUUID: func() string { return "csid-1" },
		Now:     func() time.Time { return time.Unix(100, 0) },
	})
	// A caller (e.g. a Claude session driving ccpool) leaks the child marker; the
	// neutralizer must still win and blank it.
	callerEnv := map[string]string{"CLAUDE_CODE_CHILD_SESSION": "1", "BEADS_DIR": "/repo/.beads"}
	if _, err := s.Ensure(ctx, "ext-alpha", "/tmp/proj", "", EnsureOpts{Env: callerEnv}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(ft.newCalls) != 1 {
		t.Fatalf("NewSession calls = %d, want 1", len(ft.newCalls))
	}
	got := ft.newCalls[0].env
	for _, k := range markers {
		v, ok := got[k]
		if !ok {
			t.Errorf("marker %q absent from launched env; it must be present and set to \"\" to neutralize the inherited value (full env: %v)", k, got)
			continue
		}
		if v != "" {
			t.Errorf("marker %q = %q, want \"\" (neutralized)", k, v)
		}
	}
	// Sanity: an ordinary caller key still passes through untouched.
	if got["BEADS_DIR"] != "/repo/.beads" {
		t.Errorf("caller env clobbered: BEADS_DIR=%q, want /repo/.beads", got["BEADS_DIR"])
	}
}

// TestEnsure_threadsLaunchFlagsToArgv asserts that EnsureOpts launch flags reach
// the claude argv that ensureLocked hands to tmux.
func TestEnsure_threadsLaunchFlagsToArgv(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	ft := &fakeTmux{live: map[string]bool{}}
	waiter := waitFunc(func(_ context.Context, externalID string, since int64) (wait.Outcome, error) {
		_, _ = st.Transition(ctx, externalID, store.Ready, "", "/p/t.jsonl")
		return wait.Outcome{State: store.Ready}, nil
	})
	s := New(Deps{
		Tmux: ft, Trust: &fakeTrust{}, Store: st, Wait: waiter,
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/plugin", ClaudeBin: "claude",
		NewUUID: func() string { return "csid-1" },
		Now:     func() time.Time { return time.Unix(100, 0) },
	})
	if _, err := s.Ensure(ctx, "ext-alpha", "/tmp/proj", "", EnsureOpts{PermissionMode: launch.ModeBypassPermissions, Effort: "max"}); err != nil {
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

// TestEnsure_resumesWhenSessionExists: tmux gone, row exists, exister=true →
// launch contains --resume <csid>; waits ready; permission mode threads through.
func TestEnsure_resumesWhenSessionExists(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-alpha", ClaudeSessionID: "csid-1", CWD: "/tmp/proj", TranscriptPath: "/p/ext-alpha.jsonl", State: store.Idle, TmuxSession: "cc-ext-alpha"}); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	ft := &fakeTmux{live: map[string]bool{}} // not live → resume path
	waiter := waitFunc(func(_ context.Context, externalID string, since int64) (wait.Outcome, error) {
		_, _ = st.Transition(ctx, externalID, store.Ready, "", "/p/t.jsonl")
		return wait.Outcome{State: store.Ready}, nil
	})
	s := New(Deps{
		Tmux: ft, Trust: &fakeTrust{}, Store: st, Wait: waiter, Exister: fakeExister{ok: true},
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/plugin", ClaudeBin: "claude",
		NewUUID: func() string { return "must-not-be-used" },
		Now:     func() time.Time { return time.Unix(100, 0) },
	})
	h, err := s.Ensure(ctx, "ext-alpha", "/tmp/proj", "", EnsureOpts{PermissionMode: launch.ModeBypassPermissions, Effort: "max"})
	if err != nil {
		t.Fatalf("Ensure resume: %v", err)
	}
	if h.ClaudeSessionID != "csid-1" {
		t.Errorf("resume must preserve csid-1, got %q", h.ClaudeSessionID)
	}
	if len(ft.newCalls) != 1 {
		t.Fatalf("NewSession calls = %d, want 1", len(ft.newCalls))
	}
	joined := strings.Join(ft.newCalls[0].argv, " ")
	if !strings.Contains(joined, "--resume csid-1") {
		t.Errorf("resume argv expected --resume csid-1; got %q", joined)
	}
	if !strings.Contains(joined, "--permission-mode bypassPermissions") {
		t.Errorf("resume argv missing --permission-mode bypassPermissions: %q", joined)
	}
}

// TestEnsure_prunesAndCreatesFreshWhenSessionGone: tmux gone, row exists,
// exister=false, row NOT fresh-starting → row Deleted, then a brand-new
// --session-id launch with a freshly generated csid.
func TestEnsure_prunesAndCreatesFreshWhenSessionGone(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	// An idle (settled) row whose Claude session is gone from disk.
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-alpha", ClaudeSessionID: "csid-OLD", CWD: "/tmp/proj", State: store.Idle, TmuxSession: "cc-ext-alpha"}); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	ft := &fakeTmux{live: map[string]bool{}}
	waiter := waitFunc(func(_ context.Context, externalID string, since int64) (wait.Outcome, error) {
		_, _ = st.Transition(ctx, externalID, store.Ready, "", "/p/t.jsonl")
		return wait.Outcome{State: store.Ready}, nil
	})
	s := New(Deps{
		Tmux: ft, Trust: &fakeTrust{}, Store: st, Wait: waiter, Exister: fakeExister{ok: false},
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/plugin", ClaudeBin: "claude",
		NewUUID: func() string { return "csid-NEW" },
		Now:     func() time.Time { return time.Unix(100, 0) },
	})
	h, err := s.Ensure(ctx, "ext-alpha", "/tmp/proj", "", EnsureOpts{})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if h.ClaudeSessionID != "csid-NEW" {
		t.Errorf("phantom prune must launch a FRESH session; got csid %q", h.ClaudeSessionID)
	}
	if len(ft.newCalls) != 1 {
		t.Fatalf("NewSession calls = %d, want 1", len(ft.newCalls))
	}
	joined := strings.Join(ft.newCalls[0].argv, " ")
	if !strings.Contains(joined, "--session-id csid-NEW") {
		t.Errorf("expected a fresh --session-id launch; got %q", joined)
	}
	if strings.Contains(joined, "--resume") {
		t.Errorf("a pruned phantom must NOT resume; got %q", joined)
	}
	row, _, _ := st.GetByExternalID(ctx, "ext-alpha")
	if row.ClaudeSessionID != "csid-NEW" {
		t.Errorf("row csid = %q, want the fresh csid-NEW after prune+recreate", row.ClaudeSessionID)
	}
}

// TestEnsure_doesNotPruneFreshStartingRow: a row State=Starting younger than the
// prune grace with exister=false is NOT deleted (fresh-session race guard); it
// resume-launches by its existing csid.
func TestEnsure_doesNotPruneFreshStartingRow(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	now := time.Unix(1000, 0)
	// created_at == now (age 0) → within the default 30s grace.
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-alpha", ClaudeSessionID: "csid-1", CWD: "/tmp/proj", State: store.Starting, CreatedAt: now.Unix(), LastActivityAt: now.Unix()}); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	ft := &fakeTmux{live: map[string]bool{}}
	waiter := waitFunc(func(_ context.Context, externalID string, since int64) (wait.Outcome, error) {
		_, _ = st.Transition(ctx, externalID, store.Ready, "", "/p/t.jsonl")
		return wait.Outcome{State: store.Ready}, nil
	})
	s := New(Deps{
		Tmux: ft, Trust: &fakeTrust{}, Store: st, Wait: waiter, Exister: fakeExister{ok: false},
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/plugin", ClaudeBin: "claude",
		NewUUID: func() string { return "must-not-be-used" },
		Now:     func() time.Time { return now },
	})
	if _, err := s.Ensure(ctx, "ext-alpha", "/tmp/proj", "", EnsureOpts{}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// The original row must survive (not pruned): its csid is unchanged.
	row, ok, _ := st.GetByExternalID(ctx, "ext-alpha")
	if !ok {
		t.Fatal("fresh starting row must NOT be pruned")
	}
	if row.ClaudeSessionID != "csid-1" {
		t.Errorf("fresh row csid = %q, want unchanged csid-1 (no prune+recreate)", row.ClaudeSessionID)
	}
}

func TestEnsure_reusesLiveTmux_noLaunch(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{ExternalID: "ext-alpha", ClaudeSessionID: "csid", State: store.Ready, TmuxSession: "cc-ext-alpha"})
	ft := &fakeTmux{live: map[string]bool{"cc-ext-alpha": true}}

	s := New(Deps{
		Tmux: ft, Trust: &fakeTrust{}, Store: st, Wait: waitFunc(nil),
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/p", ClaudeBin: "claude",
		NewUUID: func() string { return "x" }, Now: func() time.Time { return time.Unix(1, 0) },
	})
	h, err := s.Ensure(ctx, "ext-alpha", "/tmp/proj", "", EnsureOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ft.newCalls) != 0 {
		t.Errorf("reused live session must not launch; got %d launches", len(ft.newCalls))
	}
	if h.ExternalID != "ext-alpha" || h.ClaudeSessionID != "csid" {
		t.Errorf("reuse handle = %+v", h)
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
	// Settled row whose Claude session is still on disk; not live → resume path.
	// Insert sets generation=1.
	if err := st.Insert(ctx, store.Session{ExternalID: "beta", ClaudeSessionID: "csid-beta", TranscriptPath: "/p/beta.jsonl", State: store.Idle, TmuxSession: "cc-beta", Model: "opus"}); err != nil {
		t.Fatal(err)
	}
	ft := &fakeTmux{live: map[string]bool{}}
	var sawState store.State
	var sawSince int64
	waiter := waitFunc(func(_ context.Context, externalID string, since int64) (wait.Outcome, error) {
		// At wait time the row must already be `starting` (flipped before launch).
		row, _, _ := st.GetByExternalID(ctx, externalID)
		sawState = row.State
		sawSince = since
		_, _ = st.Transition(ctx, externalID, store.Ready, "", "/p/beta.jsonl")
		return wait.Outcome{State: store.Ready}, nil
	})
	s := New(Deps{
		Tmux: ft, Trust: &fakeTrust{}, Store: st, Wait: waiter, Exister: fakeExister{ok: true},
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/p", ClaudeBin: "claude",
		NewUUID: func() string { return "must-not-be-used" },
		Now:     func() time.Time { return time.Unix(1, 0) },
	})

	h, err := s.Ensure(ctx, "beta", "/tmp/proj", "", EnsureOpts{})
	if err != nil {
		t.Fatalf("Ensure resume: %v", err)
	}
	if h.ClaudeSessionID != "csid-beta" {
		t.Errorf("resume must preserve csid csid-beta, got %q", h.ClaudeSessionID)
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
