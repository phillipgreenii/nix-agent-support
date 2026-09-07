package internal

import (
	"path/filepath"
	"testing"
)

func TestRunStore_GetOnUnknownRunReturnsNotOK(t *testing.T) {
	s := newTestRunStore(t)
	repo, ok, err := s.GetRepo("1001")
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if ok || repo != "" {
		t.Fatalf("unknown run should be not-ok/zero-value, got repo=%q ok=%v", repo, ok)
	}
}

func TestRunStore_SetRepoThenGetRoundTrips(t *testing.T) {
	s := newTestRunStore(t)
	if err := s.SetRepo("1001", "foo/bar"); err != nil {
		t.Fatalf("SetRepo: %v", err)
	}
	repo, ok, err := s.GetRepo("1001")
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if !ok || repo != "foo/bar" {
		t.Fatalf("GetRepo = %q, %v, want foo/bar, true", repo, ok)
	}
}

func TestRunStore_SetRepoOverwrites(t *testing.T) {
	s := newTestRunStore(t)
	if err := s.SetRepo("1001", "foo/bar"); err != nil {
		t.Fatalf("SetRepo: %v", err)
	}
	if err := s.SetRepo("1001", "foo/baz"); err != nil {
		t.Fatalf("SetRepo: %v", err)
	}
	repo, ok, err := s.GetRepo("1001")
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if !ok || repo != "foo/baz" {
		t.Fatalf("GetRepo = %q, %v, want foo/baz (plain set/overwrite)", repo, ok)
	}
}

func TestRunStore_DifferentRunsAreIndependent(t *testing.T) {
	s := newTestRunStore(t)
	if err := s.SetRepo("1001", "foo/bar"); err != nil {
		t.Fatalf("SetRepo 1001: %v", err)
	}
	if err := s.SetRepo("1002", "foo/baz"); err != nil {
		t.Fatalf("SetRepo 1002: %v", err)
	}
	repo1, ok1, err := s.GetRepo("1001")
	if err != nil {
		t.Fatalf("GetRepo 1001: %v", err)
	}
	repo2, ok2, err := s.GetRepo("1002")
	if err != nil {
		t.Fatalf("GetRepo 1002: %v", err)
	}
	if !ok1 || repo1 != "foo/bar" || !ok2 || repo2 != "foo/baz" {
		t.Fatalf("got 1001=%q(%v) 1002=%q(%v)", repo1, ok1, repo2, ok2)
	}
}

func TestRunStore_PersistsAcrossInstances(t *testing.T) {
	// Simulates the scriptout protocol's one-process-per-call reality: a
	// fresh RunStore value opened against the same path must see an
	// earlier process's writes — exactly the scenario this store exists
	// for (ListRuns and a later GetLogs run in separate processes).
	dir := t.TempDir()
	path := filepath.Join(dir, "run-repo.json")

	first := NewRunStore(path)
	if err := first.SetRepo("1001", "foo/bar"); err != nil {
		t.Fatalf("SetRepo: %v", err)
	}

	second := NewRunStore(path)
	repo, ok, err := second.GetRepo("1001")
	if err != nil {
		t.Fatalf("GetRepo (second instance): %v", err)
	}
	if !ok || repo != "foo/bar" {
		t.Fatalf("state did not survive across RunStore instances: repo=%q ok=%v", repo, ok)
	}
}

func TestDefaultRunStorePath_HonoursXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg-state")
	got := DefaultRunStorePath()
	want := filepath.Join("/xdg-state", "pg-connector-ci-github-actions", "run-repo.json")
	if got != want {
		t.Fatalf("DefaultRunStorePath() = %q, want %q", got, want)
	}
}
