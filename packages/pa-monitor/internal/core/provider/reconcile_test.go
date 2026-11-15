package provider

import (
	"context"
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

func sess(id, cwd string, pid int, alive bool) *session.Session {
	return &session.Session{SessionID: id, Cwd: cwd, PID: pid, PidAlive: alive}
}

func TestReconcile_CwdRefcountKeepsAliveWithSecondSession(t *testing.T) {
	c := New(nil)
	c.byCwd["/a"] = &cwdNode{branch: "main", branchValid: true}
	s1 := sess("s1", "/a", 1, true)
	s2 := sess("s2", "/a", 2, true)

	c.Reconcile([]*session.Session{s1, s2})
	if _, ok := c.byCwd["/a"]; !ok {
		t.Fatal("two sessions in /a → node must be kept")
	}
	c.Reconcile([]*session.Session{s1})
	if _, ok := c.byCwd["/a"]; !ok {
		t.Fatal("one session still in /a → node must be kept")
	}
	c.Reconcile(nil)
	if _, ok := c.byCwd["/a"]; ok {
		t.Fatal("no sessions in /a → node must be evicted")
	}
}

func TestReconcile_CascadeDropsBranchAndRepoLabel(t *testing.T) {
	c := New(nil)
	c.byCwd["/a"] = &cwdNode{branch: "main", branchValid: true, repoLabel: "acme/x", repoKnown: true}
	c.Reconcile(nil) // no session references /a
	if _, ok := c.byCwd["/a"]; ok {
		t.Fatal("evicting the cwd node must cascade-drop branch AND repo-label (no orphans)")
	}
}

func TestReconcile_PrunesVanishedPRKeys(t *testing.T) {
	c := New(nil)
	var pruned map[string]bool
	c.PRPrune = func(live map[string]bool) { pruned = live }
	c.PRBackend = func(_ context.Context, _, _ string) (*session.PRInfo, error) {
		return &session.PRInfo{Number: 1}, nil
	}
	c.BeginScan()
	c.PR(context.Background(), "/a", "foo") //nolint:errcheck
	c.Reconcile([]*session.Session{sess("s1", "/a", 1, true)})
	if pruned == nil {
		t.Fatal("PRPrune not called")
	}
	if !pruned[session.PRCacheKey("/a", "foo")] || len(pruned) != 1 {
		t.Fatalf("prune must receive exactly the live PR key: %v", pruned)
	}
}

// End-to-end PID reuse: an old session's env never leak into a
// new session that inherits its PID after the old one leaves the set.
func TestReconcile_PidReuseEndToEnd(t *testing.T) {
	c := New(nil)
	c.PidAlive = func(int) bool { return true }
	envWhich := "A"
	c.FetchEnv = func(int) (map[string]string, error) { return map[string]string{"W": envWhich}, nil }

	oldEnv, _ := c.Env("old", 42)
	if oldEnv["W"] != "A" {
		t.Fatalf("old env: %v", oldEnv)
	}

	c.Reconcile(nil) // old session gone; its node evicted

	envWhich = "B"
	newEnv, _ := c.Env("new", 42) // same pid, new session-id
	if newEnv["W"] != "B" {
		t.Fatalf("reused pid served stale env: %v", newEnv)
	}
}
