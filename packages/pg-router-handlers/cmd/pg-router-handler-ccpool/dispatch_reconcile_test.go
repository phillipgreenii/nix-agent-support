package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// captureSlog temporarily replaces slog's default logger with one that
// records every record's message into a slice, runs fn, restores the prior
// default, and returns the captured messages in emission order. Used to observe
// the sweep passes' log lines without depending on os.Stderr pipe timing.
func captureSlog(t *testing.T, fn func()) []string {
	t.Helper()
	rec := &recordingHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(rec))
	defer slog.SetDefault(old)
	fn()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := make([]string, len(rec.messages))
	copy(out, rec.messages)
	return out
}

type recordingHandler struct {
	mu       sync.Mutex
	messages []string
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = append(h.messages, r.Message)
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func containsSubstring(msgs []string, substr string) bool {
	for _, m := range msgs {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}

// worktreeRoleCfg is a ccpool role config that leaves isolation at its default,
// "worktree" (extraCC is spliced at the front of the ccpool table).
func worktreeRoleCfg(extraCC string) map[string]any {
	return mustCfg(`{"ccpool":{` + extraCC + `"actor":"test-actor","completion":"close-only","onFailure":"unclaim","onDispatchFail":"unclaim","promptBody":"hello"}}`)
}

// sweepDispatch runs one dispatch of the named role against a launch config
// holding extraLaunch, with PATH broken so every ccpool call fails soft and is
// observable only through its log line, and returns the log messages.
func sweepDispatch(t *testing.T, cfg map[string]any, extraLaunch string) []string {
	t.Helper()
	t.Setenv("PATH", "/usr/bin")
	cfgPath, _ := launchConfig(t, extraLaunch)
	var msgs []string
	msgs = captureSlog(t, func() {
		dispatchWith(t, "worker", cfg, cfgPath, defaultEvent)
	})
	return msgs
}

// TestDispatch_reconciliationRunsForCcpoolRole proves the closed-bead and orphan
// reconciles are wired into the dispatch path: dispatching a ccpool role
// reaches reconcileClosedBeadSessions, which attempts a real `ccpool list`,
// observable here because PATH is broken (so that attempt fails soft with the
// reconcile's own "reconcile: list failed" log line rather than ever touching a
// real pool). The role-scoped orphan reconcile (pg2-g2u9m, INV-CCH-18) runs
// right after it.
func TestDispatch_reconciliationRunsForCcpoolRole(t *testing.T) {
	_, cfg := workerRole("", "")
	msgs := sweepDispatch(t, cfg, "")
	if !containsSubstring(msgs, "reconcile: list failed") {
		t.Fatalf("a ccpool dispatch must reach reconcileClosedBeadSessions (observed via its list-failure log); got messages=%v", msgs)
	}
	if !containsSubstring(msgs, "orphan reconcile: list failed") {
		t.Fatalf("a ccpool dispatch must also reach reconcileOrphanSessions (observed via its list-failure log); got messages=%v", msgs)
	}
}

// TestDispatch_worktreeSweepRunsForCcpoolRole proves the worktree-keyed sweep
// (pg2-ganjb, INV-CCH-19) is wired into the dispatch path: with a non-empty
// worktree directory, dispatching a worktree-isolation ccpool role reaches
// sweepLeakedWorktrees, which asks ccpool for the session rows first.
func TestDispatch_worktreeSweepRunsForCcpoolRole(t *testing.T) {
	wtDir := filepath.Join(t.TempDir(), "worktrees")
	if err := os.MkdirAll(filepath.Join(wtDir, "zr-leaked"), 0o755); err != nil {
		t.Fatal(err)
	}
	msgs := sweepDispatch(t, worktreeRoleCfg(""), `,"WorktreeDir":"`+wtDir+`"`)
	if !containsSubstring(msgs, "worktree sweep: session list failed") {
		t.Fatalf("a worktree-isolation ccpool dispatch must reach sweepLeakedWorktrees; got messages=%v", msgs)
	}
}

// pg2-wqi3e: a role with its own pool must ALSO sweep ccpool's default pool
// (guarded), observed through the default-pool pass's own list-failure line.
func TestDispatch_defaultPoolReconcileRunsForRoleWithOwnPool(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	msgs := sweepDispatch(t, worktreeRoleCfg(`"poolDir":"`+t.TempDir()+`",`), "")
	if !containsSubstring(msgs, "reconcile: list failed (default pool)") {
		t.Fatalf("a role with its own pool must also reconcile the default pool; got messages=%v", msgs)
	}
}

// ... and a role already on the default pool must not run it a second time.
func TestDispatch_defaultPoolReconcileSkippedWhenRoleUsesDefaultPool(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	msgs := sweepDispatch(t, worktreeRoleCfg(""), "")
	if containsSubstring(msgs, "reconcile: list failed (default pool)") {
		t.Fatalf("the default pool is already swept by the role's own pass; got messages=%v", msgs)
	}
	if !containsSubstring(msgs, "reconcile: list failed") {
		t.Fatalf("the role's own pass must still run; got messages=%v", msgs)
	}
}
