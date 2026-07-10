package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/labels"
)

// fakeFailable is a no-op labels.FailableDetector used to assert that the
// reloader swaps in a freshly-rebuilt decorator pipeline.
type fakeFailable struct{ name string }

func (f fakeFailable) Name() string                               { return f.name }
func (f fakeFailable) Detect(labels.Session) labels.Set           { return nil }
func (f fakeFailable) DetectOK(labels.Session) (labels.Set, bool) { return nil, true }

// fakeScopeDecorator is a FailableDetector that always emits a workspace.scope
// label, standing in for the shell-out scope decorator.
type fakeScopeDecorator struct{ scope string }

func (d fakeScopeDecorator) Name() string { return "scope" }
func (d fakeScopeDecorator) Detect(labels.Session) labels.Set {
	return labels.Set{"workspace.scope": d.scope}
}

func (d fakeScopeDecorator) DetectOK(labels.Session) (labels.Set, bool) {
	return labels.Set{"workspace.scope": d.scope}, true
}

// The reload path MUST clear the per-session label cache; otherwise the
// "personal" scope cached during the boot race (before the decorator existed)
// would persist for the session's whole lifetime — the exact pg2-r1f1j.8 bug.
// This exercises the labelsForSession+cache invariant the reload branch relies
// on: clearing the cache forces a recompute that now sees the new decorator.
func TestLabelCacheClearForcesRecomputeWithNewDecorator(t *testing.T) {
	cap := labels.NewCardinalityCap(10)
	cache := map[string]labels.Set{}
	sv := &aggregate.SessionView{Session: &session.Session{SessionID: "s1", Cwd: "/Volumes/ziprecruiter/x"}}
	def := fakeDetector{key: "workspace.scope", fn: func(labels.Session) string { return "personal" }}

	// Boot-race state: DefaultScope only, decorator not yet loaded.
	if got := labelsForSession(sv, []labels.Detector{def}, nil, cap, cache)["workspace.scope"]; got != "personal" {
		t.Fatalf("pre-reload scope = %q, want personal", got)
	}

	// Decorator arrives via config reload -> the reload branch clears the cache.
	clear(cache)
	dec := []labels.FailableDetector{fakeScopeDecorator{scope: "ziprecruiter"}}
	if got := labelsForSession(sv, []labels.Detector{def}, dec, cap, cache)["workspace.scope"]; got != "ziprecruiter" {
		t.Fatalf("post-reload scope = %q, want ziprecruiter (cache not honouring new decorator)", got)
	}
}

// The apply race (bead pg2-r1f1j.8): the daemon reads config once at startup
// and can boot BEFORE home-manager writes ~/.config/pa-monitor/config.toml, so
// the [[decorator]] block is missing and workspace.scope stays "personal"
// forever. The reloader closes this by re-reading the config each tick: the
// first tick always rebuilds (last-seen fingerprint starts empty), and every
// later change is picked up — without a manual daemon restart.
func TestConfigReloaderFiresOnFirstReadThenOnlyOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("v=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	calls := 0
	r := &configReloader{
		path: path,
		rebuild: func() ([]labels.FailableDetector, error) {
			calls++
			return []labels.FailableDetector{fakeFailable{name: "d"}}, nil
		},
	}

	// First check: fingerprint goes ""->fp1, so it reloads (closes the boot race).
	decs, ok := r.reloadIfChanged()
	if !ok || len(decs) != 1 {
		t.Fatalf("first reloadIfChanged: ok=%v decs=%d, want true/1", ok, len(decs))
	}
	if calls != 1 {
		t.Fatalf("rebuild calls=%d, want 1", calls)
	}

	// Second check, unchanged file: no reload, no rebuild.
	if _, ok := r.reloadIfChanged(); ok {
		t.Fatalf("unchanged file should not reload")
	}
	if calls != 1 {
		t.Fatalf("rebuild calls=%d after no-op, want 1", calls)
	}

	// Content change: reload fires again.
	if err := os.WriteFile(path, []byte("v=2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.reloadIfChanged(); !ok {
		t.Fatalf("changed file should reload")
	}
	if calls != 2 {
		t.Fatalf("rebuild calls=%d after change, want 2", calls)
	}
}

// A transient rebuild failure (config mid-write / parse error) MUST NOT be
// reported as a successful reload (which would blow away the working decorator
// pipeline) and MUST NOT advance the fingerprint, so the next tick retries.
func TestConfigReloaderKeepsPreviousOnRebuildError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("v=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &configReloader{
		path: path,
		rebuild: func() ([]labels.FailableDetector, error) {
			return nil, errors.New("boom")
		},
	}
	if _, ok := r.reloadIfChanged(); ok {
		t.Fatalf("rebuild error must NOT report reloaded")
	}

	// Fingerprint was not advanced, so the very next tick retries — and now
	// succeeds.
	calls := 0
	r.rebuild = func() ([]labels.FailableDetector, error) { calls++; return nil, nil }
	if _, ok := r.reloadIfChanged(); !ok {
		t.Fatalf("after an error, the next check must retry and reload")
	}
	if calls != 1 {
		t.Fatalf("rebuild calls=%d, want 1", calls)
	}
}

func TestFileFingerprint(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c")

	if got := fileFingerprint(p); got != "" {
		t.Fatalf("absent file fingerprint = %q, want empty", got)
	}
	if err := os.WriteFile(p, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	fpA := fileFingerprint(p)
	if fpA == "" {
		t.Fatalf("present file should have a non-empty fingerprint")
	}
	if err := os.WriteFile(p, []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fpB := fileFingerprint(p); fpB == fpA {
		t.Fatalf("content change should change fingerprint (%q == %q)", fpB, fpA)
	}
}
