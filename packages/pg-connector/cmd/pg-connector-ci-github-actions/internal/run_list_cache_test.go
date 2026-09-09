package internal

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// newTestRunListCache returns a RunListCache backed by a file in a fresh
// t.TempDir() — isolated per this repo's Unit Tests convention, mirroring
// run_store_test.go's own newTestRunStore.
func newTestRunListCache(t *testing.T) *RunListCache {
	t.Helper()
	return NewRunListCache(filepath.Join(t.TempDir(), "run-list-cache.json"))
}

func TestRunListCache_GetOnUnknownPRReturnsNotOK(t *testing.T) {
	c := newTestRunListCache(t)
	runs, asOf, ok, err := c.Get("foo/bar#1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok || runs != nil || !asOf.IsZero() {
		t.Fatalf("unknown PR should be not-ok/zero-value, got runs=%v asOf=%v ok=%v", runs, asOf, ok)
	}
}

func TestRunListCache_SetThenGetRoundTrips(t *testing.T) {
	c := newTestRunListCache(t)
	want := []schema.CIRun{{ID: "1001", PRID: "foo/bar#42", Status: "completed", Conclusion: "failure"}}
	asOf := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	if err := c.Set("foo/bar#42", want, asOf); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, gotAsOf, ok, err := c.Get("foo/bar#42")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || len(got) != 1 || got[0].ID != "1001" {
		t.Fatalf("Get = %+v, %v, want a single run 1001, true", got, ok)
	}
	if !gotAsOf.Equal(asOf) {
		t.Fatalf("Get asOf = %v, want %v", gotAsOf, asOf)
	}
}

func TestRunListCache_SetOverwrites(t *testing.T) {
	c := newTestRunListCache(t)
	first := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	second := time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC)
	if err := c.Set("foo/bar#42", []schema.CIRun{{ID: "1001"}}, first); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := c.Set("foo/bar#42", []schema.CIRun{{ID: "1002"}}, second); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, gotAsOf, ok, err := c.Get("foo/bar#42")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || len(got) != 1 || got[0].ID != "1002" || !gotAsOf.Equal(second) {
		t.Fatalf("Get = %+v, %v, %v, want a single run 1002 at %v, true", got, gotAsOf, ok, second)
	}
}

func TestRunListCache_DifferentPRsAreIndependent(t *testing.T) {
	c := newTestRunListCache(t)
	asOf := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	if err := c.Set("foo/bar#1", []schema.CIRun{{ID: "1001"}}, asOf); err != nil {
		t.Fatalf("Set 1: %v", err)
	}
	if err := c.Set("foo/bar#2", []schema.CIRun{{ID: "2001"}}, asOf); err != nil {
		t.Fatalf("Set 2: %v", err)
	}
	got1, _, ok1, err := c.Get("foo/bar#1")
	if err != nil {
		t.Fatalf("Get 1: %v", err)
	}
	got2, _, ok2, err := c.Get("foo/bar#2")
	if err != nil {
		t.Fatalf("Get 2: %v", err)
	}
	if !ok1 || len(got1) != 1 || got1[0].ID != "1001" || !ok2 || len(got2) != 1 || got2[0].ID != "2001" {
		t.Fatalf("got 1=%+v(%v) 2=%+v(%v)", got1, ok1, got2, ok2)
	}
}

func TestRunListCache_PersistsAcrossInstances(t *testing.T) {
	// Simulates the scriptout protocol's one-process-per-call reality: a
	// fresh RunListCache value opened against the same path must see an
	// earlier process's writes — exactly the scenario this cache exists for
	// (one ListRuns call populating it, a LATER ListRuns call — in a
	// separate process — serving it back during a simulated outage).
	dir := t.TempDir()
	path := filepath.Join(dir, "run-list-cache.json")
	asOf := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

	first := NewRunListCache(path)
	if err := first.Set("foo/bar#42", []schema.CIRun{{ID: "1001"}}, asOf); err != nil {
		t.Fatalf("Set: %v", err)
	}

	second := NewRunListCache(path)
	got, gotAsOf, ok, err := second.Get("foo/bar#42")
	if err != nil {
		t.Fatalf("Get (second instance): %v", err)
	}
	if !ok || len(got) != 1 || got[0].ID != "1001" || !gotAsOf.Equal(asOf) {
		t.Fatalf("state did not survive across RunListCache instances: got=%+v asOf=%v ok=%v", got, gotAsOf, ok)
	}
}

func TestDefaultRunListCachePath_HonoursXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg-state")
	got := DefaultRunListCachePath()
	want := filepath.Join("/xdg-state", "pg-connector-ci-github-actions", "run-list-cache.json")
	if got != want {
		t.Fatalf("DefaultRunListCachePath() = %q, want %q", got, want)
	}
}
