package provider

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

func TestPR_TracksLiveKeys(t *testing.T) {
	c := New(nil)
	called := 0
	c.PRBackend = func(_ context.Context, _, _ string) (*session.PRInfo, error) {
		called++
		return &session.PRInfo{Number: 5}, nil
	}
	c.BeginScan()
	pr, err := c.PR(context.Background(), "/a", "foo")
	if err != nil || pr == nil || pr.Number != 5 {
		t.Fatalf("PR: %v %v", pr, err)
	}
	if called != 1 {
		t.Fatalf("backend calls: %d", called)
	}
	if !c.prLiveKeys[session.PRCacheKey("/a", "foo")] {
		t.Fatalf("PR did not record the live key: %v", c.prLiveKeys)
	}
}

func TestPR_NilBackendNoPR(t *testing.T) {
	c := New(nil)
	pr, err := c.PR(context.Background(), "/a", "foo")
	if pr != nil || err != nil {
		t.Fatalf("nil backend must yield (nil,nil): %v %v", pr, err)
	}
}

// The pr_lookup metric must fire only on a real gh spawn (a PRCache miss/expiry),
// not on a cache hit — validating the LookupFn-records-on-spawn wiring.
func TestPR_RecordsOnlyOnBackendSpawn(t *testing.T) {
	fixed := time.Unix(1000, 0)
	c := New(func() time.Time { return fixed })
	fr := &fakeRec{}
	c.SetRecorder(fr)

	spawns := 0
	prc := session.NewPRCache(filepath.Join(t.TempDir(), "pr.json"))
	prc.FoundTTL = 15 * time.Minute
	prc.Now = func() time.Time { return fixed }
	prc.LookupFn = func(_ context.Context, _, _ string) (session.PRInfo, bool, error) {
		spawns++
		start := fixed
		defer func() { c.Record("pr_lookup", fixed.Sub(start)) }()
		return session.PRInfo{Number: 1}, true, nil
	}
	c.PRBackend = prc.Get

	c.BeginScan()
	c.PR(context.Background(), "/a", "foo") //nolint:errcheck  // miss → spawn + record
	c.PR(context.Background(), "/a", "foo") //nolint:errcheck  // hit → no spawn, no record
	if spawns != 1 {
		t.Fatalf("expected 1 gh spawn, got %d", spawns)
	}
	if n := countKind(fr, "pr_lookup"); n != 1 {
		t.Fatalf("pr_lookup must fire only on a real spawn: got %d", n)
	}
}
