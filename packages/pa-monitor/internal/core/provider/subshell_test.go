package provider

import (
	"testing"
	"time"
)

func TestSubshell_CachesUntilMtimeChanges(t *testing.T) {
	c := New(nil)
	calls := 0
	c.FetchSubshell = func(int) (int, error) { calls++; return 3, nil }
	m1 := time.Unix(1000, 0)

	if got := c.Subshell("s", 42, "/t.jsonl", m1); got != 3 {
		t.Fatalf("first: got %d want 3", got)
	}
	if got := c.Subshell("s", 42, "/t.jsonl", m1); got != 3 { // unchanged (path,mtime)
		t.Fatalf("cached: got %d want 3", got)
	}
	if calls != 1 {
		t.Fatalf("expected 1 fetch while (path,mtime) unchanged, got %d", calls)
	}
	m2 := time.Unix(2000, 0)
	if got := c.Subshell("s", 42, "/t.jsonl", m2); got != 3 {
		t.Fatalf("after mtime bump: got %d want 3", got)
	}
	if calls != 2 {
		t.Fatalf("expected re-fetch after mtime change, got %d fetches", calls)
	}
}

func TestSubshell_PathEmptyAlwaysFetches(t *testing.T) {
	c := New(nil)
	calls := 0
	c.FetchSubshell = func(int) (int, error) { calls++; return 1, nil }
	m := time.Unix(1000, 0)
	c.Subshell("s", 42, "", m)
	c.Subshell("s", 42, "", m)
	if calls != 2 {
		t.Fatalf("path==\"\" must always fetch, got %d fetches", calls)
	}
}

func TestSubshell_RecordsOnlyOnFetch(t *testing.T) {
	c := New(nil)
	fr := &fakeRec{}
	c.SetRecorder(fr)
	c.FetchSubshell = func(int) (int, error) { return 2, nil }
	m := time.Unix(1000, 0)
	c.Subshell("s", 42, "/t.jsonl", m)
	c.Subshell("s", 42, "/t.jsonl", m) // cache hit
	if n := countKind(fr, "subshell"); n != 1 {
		t.Fatalf("subshell metric should fire only on fetch: got %d", n)
	}
}

func TestSubshell_TwoSessionsSamePidIndependent(t *testing.T) {
	c := New(nil)
	c.FetchSubshell = func(int) (int, error) { return 5, nil }
	m := time.Unix(1000, 0)
	a := c.Subshell("a", 42, "/a.jsonl", m)
	b := c.Subshell("b", 42, "/b.jsonl", m) // same pid, different path/session
	if a != 5 || b != 5 {
		t.Fatalf("independent counts: a=%d b=%d", a, b)
	}
	// Each session cached its own path — a's cache still holds under a's path.
	if _, ok := c.bySession["a"]; !ok {
		t.Fatal("session a node missing")
	}
	if _, ok := c.bySession["b"]; !ok {
		t.Fatal("session b node missing")
	}
	if c.bySession["a"].subPath != "/a.jsonl" || c.bySession["b"].subPath != "/b.jsonl" {
		t.Fatalf("paths cross-contaminated: a=%q b=%q", c.bySession["a"].subPath, c.bySession["b"].subPath)
	}
}
