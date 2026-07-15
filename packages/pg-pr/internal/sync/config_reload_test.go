package sync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
)

// writeReviewConfig writes a minimal, valid pg-pr config file to path with the
// given review.enabled value.
func writeReviewConfig(t *testing.T, path string, reviewEnabled bool) {
	t.Helper()
	body := fmt.Sprintf(`self_login: tester
worktree_root: %s
repos:
  - remote: o/r
review:
  enabled: %t
`, filepath.Dir(path), reviewEnabled)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config %s: %v", path, err)
	}
}

// TestReloadCfgFromDisk_PicksUpReviewEnabledChange is the second half of the
// bead pg2-bw30 fix: the daemon re-reads the SAME config file it loaded from,
// each poll, so an out-of-band rewrite (e.g. a `pn workspace apply` that flips
// review.enabled) takes effect on the next poll without a restart or SIGHUP.
func TestReloadCfgFromDisk_PicksUpReviewEnabledChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeReviewConfig(t, path, true)

	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	e, err := New(Deps{
		Cfg: cfg,
		VCS: map[string]VCSProvider{"github": newFakeVCS()},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !e.cfg().ReviewEnabled() {
		t.Fatalf("precondition: review.enabled must start true")
	}

	// Rewrite the file in place with review.enabled=false and re-read.
	writeReviewConfig(t, path, false)
	e.reloadCfgFromDisk(discardLogger())
	if e.cfg().ReviewEnabled() {
		t.Fatalf("per-poll reload did not pick up review.enabled=false")
	}
}

// TestReloadCfgFromDisk_GracefulOnMissingOrMalformed proves a missing or
// malformed config file mid-poll does not crash the daemon and keeps the last
// good config (the file is briefly absent/partial while home-manager rewrites
// it during `pn workspace apply`).
func TestReloadCfgFromDisk_GracefulOnMissingOrMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeReviewConfig(t, path, true)

	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	e, err := New(Deps{
		Cfg: cfg,
		VCS: map[string]VCSProvider{"github": newFakeVCS()},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Malformed YAML → keep previous (review.enabled stays true), no panic.
	if err := os.WriteFile(path, []byte(":::not yaml:::"), 0o600); err != nil {
		t.Fatalf("write malformed: %v", err)
	}
	e.reloadCfgFromDisk(discardLogger())
	if !e.cfg().ReviewEnabled() {
		t.Fatalf("malformed reload must keep the previous config (review.enabled=true)")
	}

	// Missing file → keep previous, no panic.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove config: %v", err)
	}
	e.reloadCfgFromDisk(discardLogger())
	if !e.cfg().ReviewEnabled() {
		t.Fatalf("missing-file reload must keep the previous config (review.enabled=true)")
	}
}

// TestReloadCfgFromDisk_NoPathIsNoop ensures an engine whose config has no
// source path (constructed in-memory, e.g. tests) is left untouched — there is
// no file to re-read.
func TestReloadCfgFromDisk_NoPathIsNoop(t *testing.T) {
	e, err := New(Deps{
		Cfg: cfgWithReview(true), // Path == "" (built in memory)
		VCS: map[string]VCSProvider{"github": newFakeVCS()},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.reloadCfgFromDisk(discardLogger())
	if !e.cfg().ReviewEnabled() {
		t.Fatalf("no-path reload must be a no-op that keeps the config intact")
	}
}

// TestDaemon_ReReadsConfigFromDiskEachPoll is the end-to-end guarantee for bead
// pg2-bw30: a running daemon picks up an out-of-band rewrite of its config file
// on the next poll, with no SIGHUP and no restart. The fakeVCS is not
// fingerprint-capable, so the detector tick is a no-op; the test isolates the
// per-poll config re-read.
func TestDaemon_ReReadsConfigFromDiskEachPoll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeReviewConfig(t, path, true)

	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	e, err := New(Deps{
		Cfg:   cfg,
		VCS:   map[string]VCSProvider{"github": newFakeVCS()}, // not fingerprint-capable → detector no-ops
		Beads: noopBeads{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !e.cfg().ReviewEnabled() {
		t.Fatalf("precondition: review.enabled must start true")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- e.Daemon(ctx, DaemonOpts{
			Interval: 10 * time.Millisecond, // fast poll so the test is quick
			LockDir:  t.TempDir(),
			Logger:   discardLogger(),
			Sighup:   make(chan os.Signal), // never fires — prove it works WITHOUT SIGHUP
		})
	}()

	// Rewrite the file in place with review.enabled=false. No SIGHUP, no restart.
	writeReviewConfig(t, path, false)

	deadline := time.After(2 * time.Second)
	for e.cfg().ReviewEnabled() {
		select {
		case <-deadline:
			t.Fatal("daemon did not pick up review.enabled=false from disk within 2s (config change latched?)")
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Daemon did not exit after cancel")
	}
}
