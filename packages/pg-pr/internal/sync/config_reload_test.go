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

// writeThresholdConfig writes a minimal, valid pg-pr config file to path with
// the given ci_only_attempts_threshold value. This field is used purely as an
// OBSERVABLE marker for the reload tests below — any live config field that
// round-trips through YAML would do; ci_only_attempts_threshold was picked
// because it is a plain scalar with no validation beyond parsing. (Formerly
// review.enabled served this role; that field was removed by pg2-ynhr.5 along
// with the review machinery it gated.)
func writeThresholdConfig(t *testing.T, path string, threshold int) {
	t.Helper()
	body := fmt.Sprintf(`self_login: tester
worktree_root: %s
repos:
  - remote: o/r
ci_only_attempts_threshold: %d
`, filepath.Dir(path), threshold)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config %s: %v", path, err)
	}
}

// cfgWithThreshold builds an in-memory config.Config with no source Path
// (mirrors the retired cfgWithReview helper's shape).
func cfgWithThreshold(n int) *config.Config {
	return &config.Config{
		Repos:                   []config.RepoConfig{{Remote: "o/r"}},
		CIOnlyAttemptsThreshold: n,
	}
}

// TestReloadCfgFromDisk_PicksUpDiskChange is the second half of the bead
// pg2-bw30 fix: the daemon re-reads the SAME config file it loaded from, each
// poll, so an out-of-band rewrite (e.g. a `pn workspace apply`) takes effect
// on the next poll without a restart or SIGHUP.
func TestReloadCfgFromDisk_PicksUpDiskChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeThresholdConfig(t, path, 1)

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
	if e.cfg().CIOnlyAttemptsThreshold != 1 {
		t.Fatalf("precondition: threshold must start at 1")
	}

	// Rewrite the file in place with a new value and re-read.
	writeThresholdConfig(t, path, 2)
	e.reloadCfgFromDisk(discardLogger())
	if e.cfg().CIOnlyAttemptsThreshold != 2 {
		t.Fatalf("per-poll reload did not pick up the disk change; got %d, want 2", e.cfg().CIOnlyAttemptsThreshold)
	}
}

// TestReloadCfgFromDisk_GracefulOnMissingOrMalformed proves a missing or
// malformed config file mid-poll does not crash the daemon and keeps the last
// good config (the file is briefly absent/partial while home-manager rewrites
// it during `pn workspace apply`).
func TestReloadCfgFromDisk_GracefulOnMissingOrMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeThresholdConfig(t, path, 1)

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

	// Malformed YAML → keep previous (threshold stays 1), no panic.
	if err := os.WriteFile(path, []byte(":::not yaml:::"), 0o600); err != nil {
		t.Fatalf("write malformed: %v", err)
	}
	e.reloadCfgFromDisk(discardLogger())
	if e.cfg().CIOnlyAttemptsThreshold != 1 {
		t.Fatalf("malformed reload must keep the previous config (threshold=1), got %d", e.cfg().CIOnlyAttemptsThreshold)
	}

	// Missing file → keep previous, no panic.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove config: %v", err)
	}
	e.reloadCfgFromDisk(discardLogger())
	if e.cfg().CIOnlyAttemptsThreshold != 1 {
		t.Fatalf("missing-file reload must keep the previous config (threshold=1), got %d", e.cfg().CIOnlyAttemptsThreshold)
	}
}

// TestReloadCfgFromDisk_NoPathIsNoop ensures an engine whose config has no
// source path (constructed in-memory, e.g. tests) is left untouched — there is
// no file to re-read.
func TestReloadCfgFromDisk_NoPathIsNoop(t *testing.T) {
	e, err := New(Deps{
		Cfg: cfgWithThreshold(1), // Path == "" (built in memory)
		VCS: map[string]VCSProvider{"github": newFakeVCS()},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.reloadCfgFromDisk(discardLogger())
	if e.cfg().CIOnlyAttemptsThreshold != 1 {
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
	writeThresholdConfig(t, path, 1)

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
	if e.cfg().CIOnlyAttemptsThreshold != 1 {
		t.Fatalf("precondition: threshold must start at 1")
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

	// Rewrite the file in place with a new value. No SIGHUP, no restart.
	writeThresholdConfig(t, path, 2)

	deadline := time.After(2 * time.Second)
	for e.cfg().CIOnlyAttemptsThreshold != 2 {
		select {
		case <-deadline:
			t.Fatal("daemon did not pick up the disk change within 2s (config change latched?)")
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
