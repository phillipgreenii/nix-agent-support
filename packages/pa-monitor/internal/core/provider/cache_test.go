package provider

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// fakeRec captures RecordSubprocess calls.
type fakeRec struct {
	kinds []string
}

func (f *fakeRec) RecordSubprocess(kind string, _ time.Duration) {
	f.kinds = append(f.kinds, kind)
}

func TestNew_DefaultsNow(t *testing.T) {
	c := New(nil)
	if c == nil {
		t.Fatal("New(nil) returned nil")
	}
	if c.now == nil {
		t.Fatal("New(nil) left now unset")
	}
	// A custom clock is honored.
	fixed := time.Unix(1000, 0)
	c2 := New(func() time.Time { return fixed })
	if !c2.now().Equal(fixed) {
		t.Fatalf("custom now not honored: got %v", c2.now())
	}
}

func TestSetRecorder_NilSafe(t *testing.T) {
	c := New(nil)
	c.SetRecorder(nil)                  // must not panic
	c.SetRecorder(struct{}{})           // not a Recorder: ignored
	c.Record("git_branch", time.Second) // no recorder wired: must not panic

	fr := &fakeRec{}
	c.SetRecorder(fr)
	c.Record("git_branch", time.Second)
	if len(fr.kinds) != 1 || fr.kinds[0] != "git_branch" {
		t.Fatalf("Record did not forward to wired recorder: %v", fr.kinds)
	}
}

// The Cache must NOT satisfy Recorder, so SetRecorder(cache) is a no-op and can
// never cause self-recursion in Record.
func TestSetRecorder_CacheNotRecorder(t *testing.T) {
	c := New(nil)
	fr := &fakeRec{}
	c.SetRecorder(fr)
	c.SetRecorder(c) // must be ignored (Cache is not a Recorder)
	c.Record("child_procs", time.Second)
	if len(fr.kinds) != 1 {
		t.Fatalf("SetRecorder(cache) changed the wired recorder: %v", fr.kinds)
	}
}

func TestBeginScan_ClearsLiveKeys(t *testing.T) {
	c := New(nil)
	c.prLiveKeys["stale"] = true
	c.BeginScan()
	if len(c.prLiveKeys) != 0 {
		t.Fatalf("BeginScan did not clear prLiveKeys: %v", c.prLiveKeys)
	}
}

func TestReconcile_DropsAbsentSession(t *testing.T) {
	c := New(nil)
	c.bySession["s1"] = &sessionNode{pid: 1}
	c.bySession["s2"] = &sessionNode{pid: 2}
	c.Reconcile([]*session.Session{{SessionID: "s1", Cwd: "/a", PID: 1, PidAlive: true}})
	if _, ok := c.bySession["s1"]; !ok {
		t.Error("s1 (present) was evicted")
	}
	if _, ok := c.bySession["s2"]; ok {
		t.Error("s2 (absent) was not evicted")
	}
}

func TestReconcile_DropsZeroRefcountCwd(t *testing.T) {
	c := New(nil)
	c.byCwd["/a"] = &cwdNode{branch: "foo", branchValid: true}
	c.byCwd["/b"] = &cwdNode{branch: "bar", branchValid: true}
	c.Reconcile([]*session.Session{{SessionID: "s1", Cwd: "/a", PID: 1, PidAlive: true}})
	if _, ok := c.byCwd["/a"]; !ok {
		t.Error("/a (referenced) was evicted")
	}
	if _, ok := c.byCwd["/b"]; ok {
		t.Error("/b (unreferenced) was not evicted")
	}
}

func TestReconcile_CallsPRPruneWithLiveKeys(t *testing.T) {
	c := New(nil)
	var got map[string]bool
	c.PRPrune = func(live map[string]bool) { got = live }
	c.BeginScan()
	c.prLiveKeys["/a\x00foo"] = true
	c.Reconcile([]*session.Session{{SessionID: "s1", Cwd: "/a", PID: 1, PidAlive: true}})
	if got == nil {
		t.Fatal("PRPrune was not called")
	}
	if !got["/a\x00foo"] {
		t.Fatalf("PRPrune did not receive the live key: %v", got)
	}
}
