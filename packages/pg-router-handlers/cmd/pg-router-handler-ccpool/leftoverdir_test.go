package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/sessionlock"
	"github.com/phillipgreenii/pg-router-handlers/internal/worktree"
)

// Unregistered leftover directories (leftoverdir.go), against real git via the
// sweep harness: the residue a killed `git worktree remove` leaves is a
// partial checkout with no .git entry that git no longer registers.

// leftover builds the residue: a nested tree of plain files at wtDir/bead, no
// .git entry, aged past the grace window.
func (h *sweepHarness) leftover(bead string) string {
	h.t.Helper()
	path := filepath.Join(h.wtDir, bead)
	for _, f := range []string{"company/store/a.txt", "company/store/deep/b.txt", "top.txt"} {
		full := filepath.Join(path, f)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			h.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			h.t.Fatal(err)
		}
	}
	h.age(path)
	return path
}

func (h *sweepHarness) age(path string) {
	h.t.Helper()
	old := h.nowFixed.Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		h.t.Fatal(err)
	}
}

func (h *sweepHarness) trashEntries() []string {
	entries, _ := os.ReadDir(filepath.Join(h.wtDir, trashDirName))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func (h *sweepHarness) reconciler() poolReconciler {
	d := h.deps
	d.LockDir = h.lockDir
	// The dispatch path probes live processes through lsof, which a sandbox may
	// lack (a failed probe rightly keeps the directory). Tests inject the probe:
	// the case's own, else "no process is inside any directory".
	probe := h.env.cwds
	if probe == nil {
		probe = func(context.Context) ([]string, error) { return nil, nil }
	}
	return poolReconciler{role: h.role, cfg: d.Cfg, deps: d, cwds: probe}
}

// tree snapshots every path under root without following symlinks, so a test
// can assert nothing was deleted.
func snapshot(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, _ os.DirEntry, err error) error {
		if err == nil {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func TestLeftover_sweepReclaimsAnUnregisteredLeftover(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leftover("zr-k")
	if got := h.sweep(); got != 1 {
		t.Fatalf("sweep removed %d, want 1", got)
	}
	if h.exists(path) {
		t.Errorf("leftover %s must be gone from the bead path", path)
	}
	if names := h.trashEntries(); len(names) != 0 {
		t.Errorf("trash must be emptied after a completed delete, got %v", names)
	}
	evs := h.reclaimEvents()
	if len(evs) != 1 || evs[0]["bead"] != "zr-k" || evs[0]["leftover"] != true {
		t.Errorf("want one leftover worktree_reclaimed event, got %v", evs)
	}
}

// The acceptance scenario: the dispatch's own-bead examination heals the
// leftover and git worktree add then registers a fresh worktree.
func TestLeftover_dispatchHealsAndEnsureSucceeds(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leftover("zr-k")
	// Control: without the heal, Ensure fails exactly as in production.
	if _, err := worktree.Ensure(context.Background(), h.opener, h.wtDir, h.repo, "zr-k"); err == nil {
		t.Fatal("fixture: Ensure must fail 'already exists' on the leftover")
	}
	if err := h.reconciler().ReclaimLeakedWorktree(context.Background(), "zr-k"); err != nil {
		t.Fatal(err)
	}
	got, err := worktree.Ensure(context.Background(), h.opener, h.wtDir, h.repo, "zr-k")
	if err != nil || got != path {
		t.Fatalf("Ensure after heal = (%q, %v), want the fresh worktree at %s", got, err, path)
	}
	if _, err := os.Lstat(filepath.Join(path, ".git")); err != nil {
		t.Errorf("fresh worktree must have a .git file: %v", err)
	}
	if h.exists(filepath.Join(path, "top.txt")) {
		t.Error("leftover content must be gone")
	}
}

// A delete killed part-way leaves the trash entry, never a half-deleted tree at
// the bead path; the next sweep purges it.
func TestLeftover_killedDeleteLeavesTrashNotTheBeadPath(t *testing.T) {
	h := newSweepHarness(t)
	trash := filepath.Join(h.wtDir, trashDirName, "zr-k.123")
	if err := os.MkdirAll(filepath.Join(trash, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trash, "sub", "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.age(trash)
	// A young entry (a delete still running) is left alone.
	young := filepath.Join(h.wtDir, trashDirName, "zr-j.456")
	if err := os.MkdirAll(young, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := h.sweep(); got != 0 {
		t.Errorf("removed %d, want 0 (trash is never a candidate)", got)
	}
	if h.exists(trash) {
		t.Error("an old trash entry must be purged")
	}
	if !h.exists(young) {
		t.Error("a young trash entry must be kept")
	}
}

func TestLeftover_refusesAndDeletesNothing(t *testing.T) {
	outside := func(t *testing.T) string {
		d := t.TempDir()
		if err := os.WriteFile(filepath.Join(d, "precious"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return d
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, h *sweepHarness) (path string)
	}{
		{"symlink at the path", func(t *testing.T, h *sweepHarness) string {
			path := filepath.Join(h.wtDir, "zr-k")
			if err := os.Symlink(outside(t), path); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"symlink inside the tree", func(t *testing.T, h *sweepHarness) string {
			path := h.leftover("zr-k")
			if err := os.Symlink(outside(t), filepath.Join(path, "company", "link")); err != nil {
				t.Fatal(err)
			}
			h.age(path)
			return path
		}},
		{"dangling symlink inside the tree", func(t *testing.T, h *sweepHarness) string {
			path := h.leftover("zr-k")
			if err := os.Symlink(filepath.Join(path, "nowhere"), filepath.Join(path, "dangling")); err != nil {
				t.Fatal(err)
			}
			h.age(path)
			return path
		}},
		{"a .git file", func(t *testing.T, h *sweepHarness) string {
			path := h.leftover("zr-k")
			if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: /nowhere/worktrees/x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			h.age(path)
			return path
		}},
		{"a .git directory", func(t *testing.T, h *sweepHarness) string {
			path := h.leftover("zr-k")
			if err := os.MkdirAll(filepath.Join(path, ".git", "objects"), 0o755); err != nil {
				t.Fatal(err)
			}
			h.age(path)
			return path
		}},
		{"a nested repository", func(t *testing.T, h *sweepHarness) string {
			path := h.leftover("zr-k")
			if err := os.MkdirAll(filepath.Join(path, "vendor", "lib", ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			h.age(path)
			return path
		}},
		{"an admin dir under the repo names the path", func(t *testing.T, h *sweepHarness) string {
			path := h.leak("zr-k")
			if err := os.Remove(filepath.Join(path, ".git")); err != nil { // registered, but the .git file is gone
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "left.txt"), []byte("x"), 0o644); err != nil { // non-empty, so only the registration keeps it
				t.Fatal(err)
			}
			h.age(path)
			return path
		}},
		{"younger than the grace window", func(t *testing.T, h *sweepHarness) string {
			path := h.leftover("zr-k")
			if err := os.Chtimes(path, h.nowFixed, h.nowFixed); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"an empty directory", func(t *testing.T, h *sweepHarness) string {
			path := filepath.Join(h.wtDir, "zr-k")
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			h.age(path)
			return path
		}},
		{"a session row names the bead", func(t *testing.T, h *sweepHarness) string {
			h.cc.sessions = []ccpool.Session{{
				ExternalID: "pg-router-review-zr-k-1", State: ccpool.StateIdle, Live: true,
				Meta: map[string]string{ccpool.MetaKeyBead: "zr-k"},
			}}
			return h.leftover("zr-k")
		}},
		{"a session row names the path", func(t *testing.T, h *sweepHarness) string {
			path := h.leftover("zr-k")
			h.cc.sessions = []ccpool.Session{{ExternalID: "pg-router-worker-x-1", State: ccpool.StateWorking, Live: true, CWD: path}}
			return path
		}},
		{"a live process is inside it", func(t *testing.T, h *sweepHarness) string {
			path := h.leftover("zr-k")
			h.env.cwds = func(context.Context) ([]string, error) {
				return []string{filepath.Join(path, "company", "store")}, nil
			}
			return path
		}},
		{"the process probe fails", func(t *testing.T, h *sweepHarness) string {
			h.env.cwds = func(context.Context) ([]string, error) { return nil, errors.New("lsof: boom") }
			return h.leftover("zr-k")
		}},
		{"a dispatch holds the bead's lock", func(t *testing.T, h *sweepHarness) string {
			l, err := sessionlock.TryRLock(h.lockDir, sessionlock.WorktreeKey("zr-k"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(l.Unlock)
			return h.leftover("zr-k")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, via := range []string{"sweep", "dispatch"} {
				t.Run(via, func(t *testing.T) {
					h := newSweepHarness(t)
					path := tc.setup(t, h)
					before := snapshot(t, h.wtDir)
					if via == "sweep" {
						h.sweep()
					} else if err := h.reconciler().ReclaimLeakedWorktree(context.Background(), "zr-k"); err != nil {
						t.Fatal(err)
					}
					after := snapshot(t, h.wtDir)
					if len(before) != len(after) {
						t.Errorf("nothing may be deleted or moved; before=%v after=%v", before, after)
					}
					if _, err := os.Lstat(path); err != nil {
						t.Errorf("%s must still exist: %v", path, err)
					}
					if names := h.trashEntries(); len(names) != 0 {
						t.Errorf("nothing may be moved to the trash, got %v", names)
					}
				})
			}
		})
	}
}

// The symlink cases must also leave the target of the link untouched.
func TestLeftover_symlinkTargetIsNeverTouched(t *testing.T) {
	h := newSweepHarness(t)
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "precious"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := h.leftover("zr-k")
	if err := os.Symlink(target, filepath.Join(path, "link")); err != nil {
		t.Fatal(err)
	}
	h.age(path)
	h.sweep()
	if !h.exists(filepath.Join(target, "precious")) {
		t.Error("the symlink target must never be followed")
	}
}
