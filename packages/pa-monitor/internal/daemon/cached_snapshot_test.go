package daemon

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
)

// TestBuildStateServesCachedSnapshot proves buildState reads the in-memory
// cache rather than hitting the DB on every call. No ReadService is wired, so
// the DB path (snapshot) yields an empty tree; a non-empty result can only come
// from the cache. This is the crux of W1b: the SQLite read is off the gRPC
// writer goroutine (refreshed on the tick goroutine instead).
func TestBuildStateServesCachedSnapshot(t *testing.T) {
	st := newSharedState()
	st.setCachedSnapshot(&aggregate.Tree{
		Dirs: []*aggregate.Directory{{Path: "/proj", WorkingN: 1}},
	})
	srv := newServer(st)

	ds := srv.buildState()
	if len(ds.GetDirs()) != 1 {
		t.Fatalf("buildState should serve the cached tree's 1 dir, got %d", len(ds.GetDirs()))
	}
	if got := ds.GetDirs()[0].GetWorkingN(); got != 1 {
		t.Errorf("cached dir WorkingN = %d, want 1", got)
	}
}

// TestBuildStateFallsBackWhenCacheEmpty: on cold start (cache nil, no
// ReadService) buildState must not panic and returns an empty tree — the
// synchronous snapshot() fallback that runs only until the first tick refresh
// populates the cache.
func TestBuildStateFallsBackWhenCacheEmpty(t *testing.T) {
	st := newSharedState()
	srv := newServer(st)

	ds := srv.buildState()
	if len(ds.GetDirs()) != 0 {
		t.Fatalf("expected empty dirs on cold start, got %d", len(ds.GetDirs()))
	}
}
