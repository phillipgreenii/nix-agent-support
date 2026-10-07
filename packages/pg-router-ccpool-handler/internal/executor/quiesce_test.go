package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

func quietCC() *dtest.FakeCC {
	return &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "s", State: ccpool.StateIdle, TranscriptPath: "/t/s.jsonl"}}}}
}

// pg2-9fwft: a subagent still writing its transcript must block removal.
func TestCleanupWorktree_defersWhileSubagentActive(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeQuietWindow = time.Minute
	cfg.WorktreeQuietMax = 5 * time.Millisecond
	e, opener := newExecWithOpener(quietCC(), &dtest.ScriptBD{}, cfg)
	e.deps.LatestActivity = func(string) (time.Time, bool) { return e.deps.clock(), true } // always fresh
	e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{}, "s", "zr-w", "/tmp/wt/zr-w")
	if len(opener.WTM.Calls) != 0 {
		t.Fatalf("worktree must not be removed while subagents are active; calls=%v", opener.WTM.Calls)
	}
}

func TestCleanupWorktree_removesOnceQuiet(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeQuietWindow = time.Minute
	cfg.WorktreeQuietMax = time.Hour
	e, opener := newExecWithOpener(quietCC(), &dtest.ScriptBD{}, cfg)
	start := e.deps.clock()
	// active until the manual clock advances past the window
	e.deps.LatestActivity = func(string) (time.Time, bool) { return start, true }
	e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{}, "s", "zr-w", "/tmp/wt/zr-w")
	if len(opener.WTM.Calls) == 0 {
		t.Fatalf("expected removal once transcript activity is older than the window")
	}
}

func TestCleanupWorktree_windowZeroDisablesCheck(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeQuietWindow = 0
	e, opener := newExecWithOpener(quietCC(), &dtest.ScriptBD{}, cfg)
	e.deps.LatestActivity = func(string) (time.Time, bool) { t.Fatal("must not probe"); return time.Time{}, false }
	e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{}, "s", "zr-w", "/tmp/wt/zr-w")
	if len(opener.WTM.Calls) == 0 {
		t.Fatal("expected legacy immediate removal")
	}
}

func TestLatestTranscriptActivity_includesSubagents(t *testing.T) {
	dir := t.TempDir()
	tr := filepath.Join(dir, "abc.jsonl")
	sub := filepath.Join(dir, "abc", "subagents", "agent-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(sub), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{tr, sub} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old, fresh := time.Now().Add(-time.Hour), time.Now()
	_ = os.Chtimes(tr, old, old)
	_ = os.Chtimes(sub, fresh, fresh)
	got, ok := LatestTranscriptActivity(tr)
	if !ok || got.Before(fresh.Add(-time.Second)) {
		t.Fatalf("got %v ok=%v, want subagent mtime %v", got, ok, fresh)
	}
	if _, ok := LatestTranscriptActivity(filepath.Join(dir, "nope.jsonl")); ok {
		t.Fatal("missing transcript must report ok=false")
	}
}

// pg2-aqpqx: two sessions of one bead share a single per-bead worktree path.
// When the first finishes its dispatch it must NOT remove the worktree while
// the other is still live and not idle; once the peer has detached (idle),
// the last session out removes it.
func TestCleanupWorktree_keepsWorktreeSharedWithLivePeer(t *testing.T) {
	const wt = "/tmp/wt/zr-shared"
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "review", State: ccpool.StateIdle, Live: true, CWD: wt},
		{ExternalID: "feedback", State: ccpool.StateWorking, Live: true, CWD: wt},
	}}}
	e, opener := newExecWithOpener(cc, &dtest.ScriptBD{}, fastCfg())
	e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{}, "review", "zr-shared", wt)
	if len(opener.WTM.Calls) != 0 {
		t.Fatalf("worktree shared with a working peer must survive the first session closing; calls=%v", opener.WTM.Calls)
	}

	// The peer detaches (goes idle): the last session out removes the worktree.
	cc2 := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "review", State: ccpool.StateIdle, Live: true, CWD: wt},
		{ExternalID: "feedback", State: ccpool.StateIdle, Live: true, CWD: wt},
	}}}
	e2, opener2 := newExecWithOpener(cc2, &dtest.ScriptBD{}, fastCfg())
	e2.cleanupWorktree(context.Background(), &roles.CCPoolConfig{}, "feedback", "zr-shared", wt)
	if len(opener2.WTM.Calls) == 0 {
		t.Fatal("worktree must be removed once the last session detaches")
	}
}

// A dead (not live) or differently-rooted session never blocks removal.
func TestCleanupWorktree_ignoresDeadOrUnrelatedPeers(t *testing.T) {
	const wt = "/tmp/wt/zr-solo"
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "s", State: ccpool.StateIdle, Live: true, CWD: wt},
		{ExternalID: "dead", State: ccpool.StateWorking, Live: false, CWD: wt},
		{ExternalID: "other", State: ccpool.StateWorking, Live: true, CWD: "/tmp/wt/zr-other"},
	}}}
	e, opener := newExecWithOpener(cc, &dtest.ScriptBD{}, fastCfg())
	e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{}, "s", "zr-solo", wt)
	if len(opener.WTM.Calls) == 0 {
		t.Fatal("dead or unrelated peers must not block removal")
	}
}

// --- per-role quiet-window override (bead pg2-uyahp, INV-CCH-23) ---

// quietElapsed runs cleanupWorktree for role cc against a transcript that is
// "fresh" at t0 forever and returns how much manual-clock time passed before
// the quiet verdict (one poll tick = cfg.PollInterval), and whether the
// worktree was removed.
func quietElapsed(t *testing.T, cfg config.Config, cc *roles.CCPoolConfig) (time.Duration, bool) {
	t.Helper()
	e, opener := newExecWithOpener(quietCC(), &dtest.ScriptBD{}, cfg)
	start := e.deps.clock()
	e.deps.LatestActivity = func(string) (time.Time, bool) { return start, true }
	e.cleanupWorktree(context.Background(), cc, "s", "zr-w", "/tmp/wt/zr-w")
	return e.deps.clock().Sub(start), len(opener.WTM.Calls) != 0
}

func overrideCfg() config.Config {
	cfg := fastCfg()
	cfg.PollInterval = time.Second
	cfg.WorktreeQuietWindow = 2 * time.Minute
	cfg.WorktreeQuietMax = time.Hour
	return cfg
}

func TestQuietWindow_roleOverrideIsApplied(t *testing.T) {
	got, removed := quietElapsed(t, overrideCfg(), &roles.CCPoolConfig{WorktreeQuietWindow: 20 * time.Second})
	if !removed {
		t.Fatal("worktree must be removed once the role's own window has elapsed")
	}
	if got != 20*time.Second {
		t.Errorf("waited %v before the quiet verdict, want the role's 20s override", got)
	}
}

func TestQuietWindow_defaultRetainedWithoutOverride(t *testing.T) {
	got, removed := quietElapsed(t, overrideCfg(), &roles.CCPoolConfig{})
	if !removed {
		t.Fatal("worktree must be removed once the default window has elapsed")
	}
	if got != 2*time.Minute {
		t.Errorf("waited %v, want the unchanged 2m default", got)
	}
	if w := newExec(quietCC(), &dtest.ScriptBD{}, overrideCfg()).quietWindow(nil); w != 2*time.Minute {
		t.Errorf("quietWindow(nil) = %v, want the 2m default", w)
	}
}

// A role MAY also lengthen the window beyond the default.
func TestQuietWindow_roleOverrideMayLengthen(t *testing.T) {
	got, _ := quietElapsed(t, overrideCfg(), &roles.CCPoolConfig{WorktreeQuietWindow: 5 * time.Minute})
	if got != 5*time.Minute {
		t.Errorf("waited %v, want the role's 5m override", got)
	}
}

// The handler-wide disable (<= 0) is an operator kill switch no role overrides.
func TestQuietWindow_globalDisableNotOverridable(t *testing.T) {
	cfg := overrideCfg()
	cfg.WorktreeQuietWindow = 0
	e, opener := newExecWithOpener(quietCC(), &dtest.ScriptBD{}, cfg)
	e.deps.LatestActivity = func(string) (time.Time, bool) { t.Fatal("must not probe"); return time.Time{}, false }
	e.cleanupWorktree(context.Background(), &roles.CCPoolConfig{WorktreeQuietWindow: 20 * time.Second}, "s", "zr-w", "/tmp/wt/zr-w")
	if len(opener.WTM.Calls) == 0 {
		t.Fatal("expected immediate removal while the check is disabled handler-wide")
	}
}

// The settled-session close polls with the role's window too, including for
// isolation types whose cleanupWorktree never ran the check.
func TestQuietWindow_roleOverrideAppliesToSettledClose(t *testing.T) {
	cfg := overrideCfg()
	cc := newSettleCC("sess-1", fixedState(ccpool.StateIdle))
	cc.transcript = "/t/s.jsonl"
	e := newExecCC(cc, &dtest.ScriptBD{}, cfg)
	start := e.deps.clock()
	e.deps.LatestActivity = func(string) (time.Time, bool) { return start, true }
	role := noBudget(workerRole(cfg))
	role.CCPool.Isolation = roles.IsolationConfig{Type: "none"}
	role.CCPool.WorktreeQuietWindow = 30 * time.Second
	d := DispatchContext{Role: role, Item: item.Item{ID: "zr-w"}}
	if _, err := e.finishWait(context.Background(), role.CCPool, d, "sess-1", "/repo", nil); err != nil {
		t.Fatal(err)
	}
	if got := e.deps.clock().Sub(start); got != 30*time.Second {
		t.Errorf("settled close waited %v, want the role's 30s override", got)
	}
	assertClosedOnceNonPurge(t, cc.FakeCC, "sess-1")
}
