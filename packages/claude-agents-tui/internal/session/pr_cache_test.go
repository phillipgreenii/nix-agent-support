package session

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestPRCacheHitFound(t *testing.T) {
	calls := 0
	c := newTestCache(t, func(_ context.Context, _, _ string) (PRInfo, bool, error) {
		calls++
		return PRInfo{Number: 7, Title: "Hello", URL: "https://gh/7"}, true, nil
	})

	// First call — miss, should fetch.
	pr, err := c.Get(context.Background(), "/repo", "feat/a")
	if err != nil {
		t.Fatal(err)
	}
	if pr == nil || pr.Number != 7 {
		t.Fatalf("first Get: want pr.Number=7, got %v", pr)
	}
	if calls != 1 {
		t.Fatalf("expected 1 LookupFn call, got %d", calls)
	}

	// Second call — hit, must NOT call LookupFn again.
	pr2, err := c.Get(context.Background(), "/repo", "feat/a")
	if err != nil {
		t.Fatal(err)
	}
	if pr2 == nil || pr2.Number != 7 {
		t.Fatalf("second Get: want pr.Number=7, got %v", pr2)
	}
	if calls != 1 {
		t.Fatalf("cache hit should not call LookupFn; got %d calls", calls)
	}
}

func TestPRCacheNotFoundWithinTTL(t *testing.T) {
	calls := 0
	now := time.Now()
	c := newTestCache(t, func(_ context.Context, _, _ string) (PRInfo, bool, error) {
		calls++
		return PRInfo{}, false, nil
	})
	c.Now = func() time.Time { return now }

	// First call — miss.
	pr, _ := c.Get(context.Background(), "/repo", "feat/b")
	if pr != nil {
		t.Fatalf("expected nil for not-found, got %v", pr)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}

	// Second call within TTL — must reuse not-found entry.
	pr, _ = c.Get(context.Background(), "/repo", "feat/b")
	if pr != nil {
		t.Fatalf("expected nil within TTL, got %v", pr)
	}
	if calls != 1 {
		t.Fatalf("within TTL should not re-fetch; got %d calls", calls)
	}
}

func TestPRCacheNotFoundExpired(t *testing.T) {
	calls := 0
	now := time.Now()
	c := newTestCache(t, func(_ context.Context, _, _ string) (PRInfo, bool, error) {
		calls++
		return PRInfo{}, false, nil
	})
	c.Now = func() time.Time { return now }

	c.Get(context.Background(), "/repo", "feat/c") //nolint:errcheck

	// Advance clock past TTL.
	c.Now = func() time.Time { return now.Add(prNotFoundTTL + time.Second) }
	c.Get(context.Background(), "/repo", "feat/c") //nolint:errcheck

	if calls != 2 {
		t.Fatalf("expired not-found should re-fetch; got %d calls", calls)
	}
}

func TestPRCacheDifferentBranchesTreatedIndependently(t *testing.T) {
	calls := 0
	c := newTestCache(t, func(_ context.Context, _, branch string) (PRInfo, bool, error) {
		calls++
		return PRInfo{Number: calls, Title: branch, URL: ""}, true, nil
	})

	c.Get(context.Background(), "/repo", "branch-a") //nolint:errcheck
	c.Get(context.Background(), "/repo", "branch-b") //nolint:errcheck

	if calls != 2 {
		t.Fatalf("different branches need independent lookups; got %d calls", calls)
	}
}

func TestPRCacheFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	c1 := &PRCache{
		Path:     path,
		LookupFn: func(_ context.Context, _, _ string) (PRInfo, bool, error) {
			return PRInfo{Number: 99, Title: "Round trip", URL: "u"}, true, nil
		},
		Now:     time.Now,
		entries: map[string]cacheEntry{},
	}

	c1.Get(context.Background(), "/repo", "feat/rt") //nolint:errcheck

	// Load fresh cache from same file.
	c2 := NewPRCache(path)
	c2.LookupFn = func(_ context.Context, _, _ string) (PRInfo, bool, error) {
		t.Fatal("should not call LookupFn — cache loaded from file")
		return PRInfo{}, false, nil
	}
	pr, err := c2.Get(context.Background(), "/repo", "feat/rt")
	if err != nil {
		t.Fatal(err)
	}
	if pr == nil || pr.Number != 99 {
		t.Fatalf("file round-trip: want Number=99, got %v", pr)
	}
}

func TestDefaultPRCachePath(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg")
	p := DefaultPRCachePath()
	want := "/tmp/xdg/claude-agents-tui/pr-cache.json"
	if p != want {
		t.Errorf("DefaultPRCachePath = %q, want %q", p, want)
	}
}

func TestDefaultPRCachePathFallback(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	p := DefaultPRCachePath()
	if p == "" {
		t.Error("DefaultPRCachePath returned empty string")
	}
	if !filepath.IsAbs(p) {
		t.Errorf("expected absolute path, got %q", p)
	}
}

// newTestCache creates a PRCache wired to a given lookup function, using a temp file.
func newTestCache(t *testing.T, fn func(context.Context, string, string) (PRInfo, bool, error)) *PRCache {
	t.Helper()
	c := NewPRCache(filepath.Join(t.TempDir(), "pr-cache.json"))
	c.LookupFn = fn
	return c
}
