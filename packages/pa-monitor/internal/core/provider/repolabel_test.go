package provider

import "testing"

func TestRepoLabel_FetchesOncePerCwd(t *testing.T) {
	c := New(nil)
	calls := 0
	c.FetchRepoLabel = func(string) (string, bool) { calls++; return "acme/x", true }
	if v, ok := c.RepoLabel("/a"); !ok || v != "acme/x" {
		t.Fatalf("first: %q %v", v, ok)
	}
	if v, ok := c.RepoLabel("/a"); !ok || v != "acme/x" {
		t.Fatalf("cached: %q %v", v, ok)
	}
	if calls != 1 {
		t.Fatalf("LongLived: expected 1 fetch per cwd, got %d", calls)
	}
}

func TestRepoLabel_DistinctCwds(t *testing.T) {
	c := New(nil)
	calls := 0
	c.FetchRepoLabel = func(cwd string) (string, bool) { calls++; return "repo:" + cwd, true }
	c.RepoLabel("/a")
	c.RepoLabel("/b")
	if calls != 2 {
		t.Fatalf("distinct cwds each fetch: got %d", calls)
	}
}

func TestRepoLabel_NilFetchEmpty(t *testing.T) {
	c := New(nil)
	if v, ok := c.RepoLabel("/a"); ok || v != "" {
		t.Fatalf("nil fetch: %q %v want empty/false", v, ok)
	}
}

func TestRepoLabel_EmptyResultCachedLongLived(t *testing.T) {
	c := New(nil)
	calls := 0
	c.FetchRepoLabel = func(string) (string, bool) { calls++; return "", false }
	if v, ok := c.RepoLabel("/a"); ok || v != "" {
		t.Fatalf("first: %q %v", v, ok)
	}
	if _, _ = c.RepoLabel("/a"); calls != 1 {
		t.Fatalf("empty result must be cached LongLived: got %d fetches", calls)
	}
}
