//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Tier 2 scenarios 26, 27, 28, 35, 47, 48, 49 and 50 of section 15.3:
// resolver failures, a real mergiraf merge driver, a failing after hook, the
// trust rules for the repo file, max-conflict-rounds and --skip, the details
// of cannot-resume, transient stops, and the warnings. Each runs the real
// binary against real git; the outcome subset, the snapshot and the
// post-condition must all hold.

// resolverConfig is a user config with one conflict rule for the given glob.
func resolverConfig(glob string) string {
	return "[[repo]]\nroot = \"{repo}\"\n\n[[repo.conflict]]\nglob = \"" + glob + "\"\nresolve = [\"{hooks}/resolver\"]\n"
}

// hasWarning reports whether the outcome's warnings name the word.
func hasWarning(r *result, word string) bool {
	ws, _ := r.json["warnings"].([]any)
	for _, w := range ws {
		if m, ok := w.(map[string]any); ok && m["warning"] == word {
			return true
		}
	}
	return false
}

// commitIn writes a file in dir and commits it.
func commitIn(t *testing.T, h *harness, dir, file, content, msg string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, file), content)
	h.mustGit(dir, "add", file)
	h.mustGit(dir, "commit", "-q", "-m", msg)
}

// abortAndCheck aborts the stopped operation and checks the post-condition.
func abortAndCheck(t *testing.T, h *harness) {
	t.Helper()
	r := h.run("land", "feature/x", "--abort")
	expect(t, r, 0, "aborted", "")
	h.noProblems()
}

// stopRefs snapshots the lock refs and the work branches, to prove a refusal
// leaves the stopped record unchanged.
func stopRefs(h *harness) string {
	return h.mustGit(h.repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/pg-git-lander/", "refs/heads/pg-git-lander/")
}

// Scenario 26: a resolver that exits non-zero, one that leaves conflict
// markers, and a verify command that fails each stop the land with
// resolve-failed and the matching detail; --abort restores everything.
func TestTier2ResolverFailsOrLeavesMarkers(t *testing.T) {
	for _, tc := range []struct {
		name, resolver, extra, detail string
	}{
		{"leaves-markers", `printf '<<<<<<< ours\nx\n=======\ny\n>>>>>>> theirs\n' > "$PG_GIT_LANDER_PATH"`, "", "markers-left"},
		{"exits-non-zero", `exit 3`, "", "resolver-failed"},
		{"verify-fails", `printf 'line1\nresolved\nline3\n' > "$PG_GIT_LANDER_PATH"`, "verify = [\"{hooks}/verify\"]\n", "verify-failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Scenario{
				Shape:  ShapeConflict,
				Layout: srcOnBr,
				Config: resolverConfig("app.txt") + tc.extra,
				Hooks:  map[string]string{"resolver": tc.resolver, "verify": "exit 1"},
			})
			r := h.run("land", "feature/x")
			expect(t, r, 4, "stopped", "resolve-failed")
			checkNow(t, h, r, Expected{
				Outcome:  map[string]any{"stage": "rebasing", "detail": tc.detail, "retryable": false},
				Snapshot: Snapshot{Refs: startRefs()},
				Stopped:  true,
			})
			abortAndCheck(t, h)
			checkNow(t, h, r, Expected{Snapshot: Snapshot{Refs: startRefs(), TargetTree: "@tree:main"}})
		})
	}
}

// Scenario 27: the real mergiraf runs as a merge driver inside the tool's
// rebase, with the environment of section 9.2, and settles a conflict that
// git's text merge cannot (two branches add different keys at the same place
// of a JSON object). The scenario skips only in a local run without mergiraf;
// the nix check carries mergiraf and fails on a skip.
func TestTier2RealMergirafDriver(t *testing.T) {
	if _, err := exec.LookPath("mergiraf"); err != nil {
		t.Skip("mergiraf is not on PATH")
	}
	h := newHarness(t, Scenario{Shape: ShapeAhead, Layout: srcOnBr})
	base := "{\n  \"a\": 1\n}\n"
	commitIn(t, h, h.repo, "data.json", base, "add data")
	src := h.wtPath("src")
	h.mustGit(src, "rebase", "-q", "main")
	commitIn(t, h, h.repo, "data.json", "{\n  \"a\": 1,\n  \"c\": 3\n}\n", "main adds c")
	commitIn(t, h, src, "data.json", "{\n  \"a\": 1,\n  \"b\": 2\n}\n", "branch adds b")
	writeFile(t, filepath.Join(h.repo, ".git", "info", "attributes"), "*.json merge=mergiraf\n")
	h.mustGit(h.repo, "config", "--local", "merge.mergiraf.name", "mergiraf")
	h.mustGit(h.repo, "config", "--local", "merge.mergiraf.driver", "mergiraf merge --git %O %A %B -s %S -x %X -y %Y -p %P -l %L")

	r := h.run("land", "feature/x")
	done(t, h, r, Expected{
		Outcome:  map[string]any{"outcome": "landed", "exit": 0, "path_taken": "rebase", "branch_fate": "deleted"},
		Snapshot: Snapshot{Refs: map[string]string{"refs/heads/feature/x": ""}, Worktrees: cleanMain, NoWorktrees: []string{"src"}},
	})
	got := h.mustGit(h.repo, "show", "main:data.json")
	for _, key := range []string{`"a": 1`, `"b": 2`, `"c": 3`} {
		if !strings.Contains(got, key) {
			t.Errorf("main:data.json lacks %s: %q", key, got)
		}
	}
	if strings.Contains(got, "<<<<<<<") {
		t.Errorf("main:data.json holds conflict markers: %q", got)
	}
}

// Scenario 28: an after hook that fails never stops a land that already moved
// TARGET: it lands with the warning after-hook-failed.
func TestTier2AfterHookFailsLandsWithWarning(t *testing.T) {
	h := newHarness(t, Scenario{
		Shape:  ShapeAhead,
		Layout: srcOnBr,
		Config: "[[repo]]\nroot = \"{repo}\"\n\n[repo.hooks.land]\nafter = [{ run = [\"{hooks}/after\"], timeout = \"60s\" }]\n",
		Hooks:  map[string]string{"after": `echo ran > "$PGL_MARKERS/after-ran"; echo boom >&2; exit 1`},
	})
	r := h.run("land", "feature/x")
	done(t, h, r, Expected{
		Outcome: map[string]any{"outcome": "landed", "exit": 0, "branch_fate": "deleted"},
		Snapshot: Snapshot{
			Refs:        mainMoveTo,
			Worktrees:   cleanMain,
			NoWorktrees: []string{"src"},
			TargetTree:  "@tree:feature/x",
		},
	})
	if !hasWarning(r, "after-hook-failed") {
		t.Errorf("the outcome carries no after-hook-failed warning: %s", r.stdout)
	}
	if _, err := os.Stat(filepath.Join(h.marks, "after-ran")); err != nil {
		t.Errorf("the after hook did not run: %v", err)
	}
}

// Scenario 35: a repo file that names commands is refused when the repo is not
// trusted, applied when the XDG entry trusts it, and skipped entirely under
// ignore_repo_config.
func TestTier2RepoConfigTrust(t *testing.T) {
	repoFile := "[hooks.land]\nbefore_move = [{ run = [\"sh\", \"-c\", \"echo ran > \\\"$PGL_MARKERS/repo-hook\\\"\"] }]\n"
	entry := func(extra string) string { return "[[repo]]\nroot = \"{repo}\"\n" + extra }
	setup := func(t *testing.T, cfg string) *harness {
		h := newHarness(t, Scenario{Shape: ShapeAhead, Layout: srcOnBr, Config: cfg})
		commitIn(t, h, h.repo, ".pg-git-lander.toml", repoFile, "add repo config")
		return h
	}
	landed := Expected{
		Outcome: map[string]any{"outcome": "landed", "exit": 0},
		Snapshot: Snapshot{
			Refs:        map[string]string{"refs/heads/feature/x": ""},
			Worktrees:   cleanMain,
			NoWorktrees: []string{"src"},
		},
	}
	ranHook := func(h *harness) bool {
		_, err := os.Stat(filepath.Join(h.marks, "repo-hook"))
		return err == nil
	}
	t.Run("untrusted-is-refused", func(t *testing.T) {
		h := setup(t, entry(""))
		r := h.run("land", "feature/x")
		expect(t, r, 5, "refused", "untrusted-config")
		done(t, h, r, Expected{Snapshot: Snapshot{
			Refs:      map[string]string{"refs/heads/feature/x": "@feature/x"},
			Worktrees: map[string]WorktreeState{"main": {Head: "branch:main", CheckStatus: true}, "src": {Head: "branch:feature/x", CheckStatus: true}},
		}})
		if ranHook(h) {
			t.Error("the repo file's command ran in an untrusted repo")
		}
	})
	t.Run("trusted-is-applied", func(t *testing.T) {
		h := setup(t, entry("trust_repo_config = true\n"))
		done(t, h, h.run("land", "feature/x"), landed)
		if !ranHook(h) {
			t.Error("the trusted repo file's hook did not run")
		}
	})
	t.Run("ignore-repo-config-skips-the-file", func(t *testing.T) {
		h := setup(t, entry("ignore_repo_config = true\n"))
		done(t, h, h.run("land", "feature/x"), landed)
		if ranHook(h) {
			t.Error("the repo file was read under ignore_repo_config")
		}
	})
}

// twoConflictSetup builds main and feature/x so that feature/x's two commits
// each conflict with main: app.txt (already in the conflict shape) and b.txt.
func twoConflictSetup(t *testing.T, cfg string) *harness {
	t.Helper()
	h := newHarness(t, Scenario{Shape: ShapeConflict, Layout: srcOnBr, Config: cfg, Hooks: map[string]string{
		"resolver": `printf 'resolved\n' > "$PG_GIT_LANDER_PATH"`,
	}})
	commitIn(t, h, h.repo, "b.txt", "main\n", "main adds b")
	commitIn(t, h, h.wtPath("src"), "b.txt", "feature\n", "feature adds b")
	return h
}

// Scenario 47: after max_conflict_rounds conflict stops the operation stops
// with max-conflict-rounds and only --abort and --quit are valid; --skip
// drops the replayed commit and lists it in dropped_commits.
func TestTier2MaxConflictRoundsAndSkip(t *testing.T) {
	t.Run("max-conflict-rounds", func(t *testing.T) {
		cfg := "[[repo]]\nroot = \"{repo}\"\nmax_conflict_rounds = 1\n\n[[repo.conflict]]\nglob = \"*.txt\"\nresolve = [\"{hooks}/resolver\"]\n"
		h := twoConflictSetup(t, cfg)
		r := h.run("land", "feature/x")
		expect(t, r, 4, "stopped", "max-conflict-rounds")
		checkNow(t, h, r, Expected{
			Outcome: map[string]any{"stage": "rebasing", "retryable": false, "stats": map[string]any{"conflict_rounds": 2}},
			Stopped: true,
		})
		next := strings.Join(strs(r.json["next"]), "\n")
		if !strings.Contains(next, "--abort") || !strings.Contains(next, "--quit") || strings.Contains(next, "--continue") || strings.Contains(next, "--skip") {
			t.Errorf("next = %q, want only --abort and --quit", next)
		}
		abortAndCheck(t, h)
	})
	t.Run("skip-drops-the-replayed-commit", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeConflict, Layout: srcOnBr})
		commitIn(t, h, h.wtPath("src"), "x2.txt", "two\n", "feature two")
		r := h.run("land", "feature/x")
		expect(t, r, 4, "stopped", "conflict")
		r = h.run("land", "feature/x", "--skip")
		done(t, h, r, Expected{
			Outcome: map[string]any{"outcome": "landed", "exit": 0, "branch_fate": "deleted"},
			Snapshot: Snapshot{
				Refs:        map[string]string{"refs/heads/feature/x": ""},
				Worktrees:   cleanMain,
				NoWorktrees: []string{"src"},
			},
		})
		if got := len(strs(r.json["dropped_commits"])); got != 1 {
			t.Errorf("dropped_commits lists %d commits, want 1: %s", got, r.stdout)
		}
		if got := h.mustGit(h.repo, "show", "main:app.txt"); got != "line1\nmain\nline3" {
			t.Errorf("main:app.txt = %q, want main's version (the conflicting commit was dropped)", got)
		}
		if got := h.mustGit(h.repo, "show", "main:x2.txt"); got != "two" {
			t.Errorf("main:x2.txt = %q, want the second commit to have landed", got)
		}
	})
}

// Scenario 48: each cannot-resume detail, and operation-mismatch. Every
// refusal leaves the stopped record and the work branch unchanged.
func TestTier2CannotResumeDetails(t *testing.T) {
	stopped := func(t *testing.T) (*harness, *result) {
		h := newHarness(t, Scenario{Shape: ShapeConflict, Layout: srcOnBr})
		r := h.run("land", "feature/x")
		expect(t, r, 4, "stopped", "conflict")
		return h, r
	}
	refusal := func(t *testing.T, h *harness, r *result, detail string) {
		t.Helper()
		before := stopRefs(h)
		expect(t, r, 5, "refused", "cannot-resume")
		if r.json["detail"] != detail {
			t.Errorf("detail = %v, want %s: %s", r.json["detail"], detail, r.stdout)
		}
		if after := stopRefs(h); after != before {
			t.Errorf("the stopped record changed:\nbefore %s\nafter  %s", before, after)
		}
	}
	t.Run("unresolved", func(t *testing.T) {
		h, _ := stopped(t)
		refusal(t, h, h.run("land", "feature/x", "--continue"), "unresolved")
		abortAndCheck(t, h)
	})
	t.Run("markers-left", func(t *testing.T) {
		h, _ := stopped(t)
		src := h.wtPath("src")
		writeFile(t, filepath.Join(src, "app.txt"), "<<<<<<< ours\nx\n=======\ny\n>>>>>>> theirs\n")
		h.mustGit(src, "add", "app.txt")
		refusal(t, h, h.run("land", "feature/x", "--continue"), "markers-left")
		abortAndCheck(t, h)
	})
	t.Run("rebase-state-missing", func(t *testing.T) {
		h, r := stopped(t)
		src := h.wtPath("src")
		work, _ := r.json["work_branch"].(string)
		if work == "" {
			t.Fatalf("no work_branch in the stop: %s", r.stdout)
		}
		// Abort git's rebase by hand, then point the work branch at the
		// original base: it is neither the branch tip nor a rebased result.
		h.mustGit(src, "rebase", "--abort")
		h.mustGit(src, "checkout", "-q", "--detach")
		h.mustGit(src, "branch", "-f", work, "feature/x~1")
		refusal(t, h, h.run("land", "feature/x", "--continue"), "rebase-state-missing")
		abortAndCheck(t, h)
	})
	t.Run("worktree-mismatch", func(t *testing.T) {
		h, _ := stopped(t)
		h.mustGit(h.repo, "worktree", "remove", "--force", h.wtPath("src"))
		refusal(t, h, h.run("land", "feature/x", "--continue"), "worktree-mismatch")
		abortAndCheck(t, h)
	})
	t.Run("operation-mismatch", func(t *testing.T) {
		h, _ := stopped(t)
		before := stopRefs(h)
		r := h.run("rebase", "feature/x", "--continue")
		expect(t, r, 5, "refused", "operation-mismatch")
		if after := stopRefs(h); after != before {
			t.Errorf("the stopped record changed:\nbefore %s\nafter  %s", before, after)
		}
		abortAndCheck(t, h)
	})
}

// Scenario 49: exit 75 is a retryable transient stop from a resolver and from
// a hook, and --continue later resumes; at the before hook it is the refusal
// before-hook-refused.
func TestTier2TransientExit75(t *testing.T) {
	t.Run("resolver", func(t *testing.T) {
		h := newHarness(t, Scenario{
			Shape:  ShapeConflict,
			Layout: srcOnBr,
			Config: resolverConfig("app.txt"),
			Hooks:  map[string]string{"resolver": `exit 75`},
		})
		r := h.run("land", "feature/x")
		expect(t, r, 4, "stopped", "transient")
		checkNow(t, h, r, Expected{Outcome: map[string]any{"retryable": true, "stage": "rebasing"}, Snapshot: Snapshot{Refs: startRefs()}, Stopped: true})
		resolveByHand(t, h, h.wtPath("src"))
		r = h.run("land", "feature/x", "--continue")
		done(t, h, r, Expected{
			Outcome: map[string]any{"outcome": "landed", "exit": 0},
			Snapshot: Snapshot{
				Refs:        map[string]string{"refs/heads/feature/x": ""},
				Worktrees:   cleanMain,
				NoWorktrees: []string{"src"},
			},
		})
	})
	t.Run("hook", func(t *testing.T) {
		h := newHarness(t, Scenario{
			Shape:  ShapeAhead,
			Layout: srcOnBr,
			Config: "[[repo]]\nroot = \"{repo}\"\n\n[repo.hooks.land]\nbefore_move = [{ run = [\"{hooks}/gate\"], timeout = \"60s\" }]\n",
			Hooks:  map[string]string{"gate": `[ -e "$PGL_MARKERS/fixed" ] || exit 75`},
		})
		r := h.run("land", "feature/x")
		expect(t, r, 4, "stopped", "transient")
		checkNow(t, h, r, Expected{
			Outcome:  map[string]any{"retryable": true, "stage": "before_move"},
			Snapshot: Snapshot{Refs: startRefs(), TargetTree: "@tree:main"},
			Stopped:  true,
		})
		writeFile(t, filepath.Join(h.marks, "fixed"), "")
		done(t, h, h.run("land", "feature/x", "--continue"), Expected{
			Outcome: map[string]any{"outcome": "landed", "exit": 0},
			Snapshot: Snapshot{
				Refs:        mainMoveTo,
				Worktrees:   cleanMain,
				NoWorktrees: []string{"src"},
				TargetTree:  "@tree:feature/x",
			},
		})
	})
	t.Run("before-hook", func(t *testing.T) {
		h := newHarness(t, Scenario{
			Shape:  ShapeAhead,
			Layout: srcOnBr,
			Config: "[[repo]]\nroot = \"{repo}\"\n\n[repo.hooks.land]\nbefore = [{ run = [\"{hooks}/gate\"], timeout = \"60s\" }]\n",
			Hooks:  map[string]string{"gate": `exit 75`},
		})
		r := h.run("land", "feature/x")
		expect(t, r, 5, "refused", "before-hook-refused")
		done(t, h, r, Expected{Snapshot: Snapshot{
			Refs: startRefs(),
			Worktrees: map[string]WorktreeState{
				"main": {Head: "branch:main", CheckStatus: true},
				"src":  {Head: "branch:feature/x", CheckStatus: true},
			},
			TargetTree: "@tree:main",
		}})
	})
}

// Scenario 50: the warnings undefined-merge-driver (an attribute names a
// driver with no definition), config-changed (a repo file changed between a
// stop and --continue) and work-in-destination (the work worktree is the main
// worktree and BRANCH is checked out elsewhere or nowhere).
func TestTier2Warnings(t *testing.T) {
	t.Run("undefined-merge-driver", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeConflict, Layout: srcOnBr})
		writeFile(t, filepath.Join(h.repo, ".git", "info", "attributes"), "app.txt merge=nosuchdriver\n")
		r := h.run("land", "feature/x")
		expect(t, r, 4, "stopped", "conflict")
		if !hasWarning(r, "undefined-merge-driver") {
			t.Errorf("no undefined-merge-driver warning on the conflict stop: %s", r.stdout)
		}
		resolveByHand(t, h, h.wtPath("src"))
		r = h.run("land", "feature/x", "--continue")
		expect(t, r, 0, "landed", "")
		h.noProblems()
	})
	t.Run("config-changed", func(t *testing.T) {
		h := newHarness(t, Scenario{
			Shape:  ShapeConflict,
			Layout: srcOnBr,
			Config: "[[repo]]\nroot = \"{repo}\"\ntrust_repo_config = true\n",
		})
		r := h.run("land", "feature/x")
		expect(t, r, 4, "stopped", "conflict")
		resolveByHand(t, h, h.wtPath("src"))
		// The configuration in effect changes while the operation is stopped.
		cfg := filepath.Join(h.dir, "home", ".config", "pg-git-lander", "config.toml")
		writeFile(t, cfg, h.expand("[[repo]]\nroot = \"{repo}\"\ntrust_repo_config = true\nmax_race_retries = 3\n"))
		r = h.run("land", "feature/x", "--continue")
		expect(t, r, 0, "landed", "")
		if !hasWarning(r, "config-changed") {
			t.Errorf("no config-changed warning: %s", r.stdout)
		}
		h.noProblems()
	})
	t.Run("work-in-destination", func(t *testing.T) {
		// BRANCH is checked out nowhere: the work worktree is the main one.
		h := newHarness(t, Scenario{Shape: ShapeAhead})
		r := h.run("land", "feature/x")
		done(t, h, r, Expected{
			Outcome:  map[string]any{"outcome": "landed", "exit": 0},
			Snapshot: Snapshot{Worktrees: cleanMain},
		})
		if !hasWarning(r, "work-in-destination") {
			t.Errorf("no work-in-destination warning: %s", r.stdout)
		}
	})
}
