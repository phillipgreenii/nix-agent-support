// p9_marker_test.go - tc-o14i5.3.15 (Phase 2m, design item m): "P9 marker test
// (fsmonitor marker script + PATH-shadowed git => no marker; timeout =>
// WorktreeUnknown)".
//
// A MARKER is a file a script creates when (and only when) it is executed, so
// its presence is observable proof that the probe ran attacker-reachable code.
// Two different scripts are used, one per threat:
//
//   - the fsmonitor script: configured as the repository's own core.fsmonitor.
//     A hardened-for-identity probe still runs the real, TRUSTED git, which
//     honours repo-local config; the "normal" case therefore PROVES the marker
//     mechanism can fire (a positive control: without it, the two "no marker"
//     cases below would pass vacuously on a git that never invokes fsmonitor).
//   - the shadow `git`: a fake `git` earlier on PATH than the operator-trusted
//     one. P9 requires it to be detected and never executed, so neither its
//     marker nor the fsmonitor marker (git never started) may appear.
//
// The timeout case pins that a hung (but trusted) git is killed at the
// deadline and reported WorktreeUnknown rather than hanging the hook.
//
// Regression (tc-s6trn): the probes pass `-c core.fsmonitor=false`, so a
// repo-configured core.fsmonitor program must NOT run; an unhardened control
// run keeps that assertion from passing vacuously.
package pathspec

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func writeMarkerScript(t *testing.T, dir, name, marker, tail string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	body := "#!/bin/sh\n: > '" + marker + "'\n" + tail
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func markerPresent(marker string) bool {
	_, err := os.Stat(marker)
	return err == nil
}

func TestP9Marker(t *testing.T) {
	// literal /tmp, not $TMPDIR: see effectpolicy/golden_test.go's
	// tempDirUnderRealTmp for the nix-sandbox TMPDIR caveat. Nothing here
	// classifies these directories, but a script under a /nix/var/nix/builds
	// TMPDIR is needlessly exotic, so keep the scripts on /tmp.
	scripts, err := os.MkdirTemp("/tmp", "ceta-p9-marker-")
	if err != nil {
		t.Skipf("cannot create script dir under /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scripts) })

	t.Run("normal conditions: trusted git runs, fsmonitor marker does NOT fire (tc-s6trn regression)", func(t *testing.T) {
		root := gitTestRepo(t) // HOME pinned + the PATH-resolved git trusted
		// Positive control: an UNhardened status of the same repo fires the
		// marker, proving the no-marker assertion below is not vacuous.
		controlMarker := filepath.Join(scripts, "fsmonitor-control.marker")
		controlScript := writeMarkerScript(t, scripts, "fsmonitor-control.sh", controlMarker, "exit 0\n")
		git(t, root, "config", "core.fsmonitor", controlScript)
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Skipf("git not on PATH: %v", err)
		}
		ctl := exec.Command(realGit, "-C", root, "status", "--porcelain=v1")
		ctl.Env = hermeticGitEnviron()
		if out, err := ctl.CombinedOutput(); err != nil {
			t.Fatalf("control git status: %v\n%s", err, out)
		}
		if !markerPresent(controlMarker) {
			t.Skipf("git %v did not run core.fsmonitor for an unhardened `status` here; the positive control is unavailable on this git", gitVersion())
		}

		marker := filepath.Join(scripts, "fsmonitor-normal.marker")
		script := writeMarkerScript(t, scripts, "fsmonitor-normal.sh", marker, "exit 0\n")
		git(t, root, "config", "core.fsmonitor", script)

		state, err := realWorktreeState(root)
		if err != nil {
			t.Fatalf("realWorktreeState: %v", err)
		}
		if state == WorktreeUnknown {
			t.Fatalf("got unknown for a clean trusted-git repo")
		}
		if markerPresent(marker) {
			t.Fatal("the worktree probe executed the repo-configured core.fsmonitor program")
		}
		if _, err := realGitTracked(root, "README.md", false); err != nil {
			t.Fatalf("realGitTracked: %v", err)
		}
		if markerPresent(marker) {
			t.Fatal("realGitTracked executed the repo-configured core.fsmonitor program")
		}
	})

	t.Run("PATH-shadowed git: no marker, Unknown, shadow never executed", func(t *testing.T) {
		root := gitTestRepo(t)
		home := os.Getenv("HOME")
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Skipf("git not on PATH: %v", err)
		}
		writeWorktreeTrustedExecPathConfig(t, home, []string{filepath.Dir(realGit)})

		fsMarker := filepath.Join(scripts, "fsmonitor-shadow.marker")
		fsScript := writeMarkerScript(t, scripts, "fsmonitor-shadow.sh", fsMarker, "exit 0\n")
		git(t, root, "config", "core.fsmonitor", fsScript)

		shadowDir := t.TempDir()
		shadowMarker := filepath.Join(scripts, "shadow-git.marker")
		writeMarkerScript(t, shadowDir, "git", shadowMarker, "echo fake-git-ran\nexit 0\n")
		t.Setenv("PATH", shadowDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		state, err := realWorktreeState(root)
		if state != WorktreeUnknown || err == nil {
			t.Fatalf("got (%s, %v), want (unknown, non-nil error): a PATH-shadowed git must not be trusted", state, err)
		}
		if markerPresent(shadowMarker) {
			t.Fatal("the shadow git ran (its marker exists): P9 forbids executing an untrusted git")
		}
		if markerPresent(fsMarker) {
			t.Fatal("the fsmonitor marker exists although no trusted git ran")
		}
		if _, err := realGitTracked(root, "README.md", false); err == nil {
			t.Fatal("realGitTracked: want an error for the shadowed git")
		}
		if markerPresent(shadowMarker) || markerPresent(fsMarker) {
			t.Fatal("realGitTracked executed the shadow git or fsmonitor program")
		}
	})

	t.Run("timeout: a hung trusted git is killed and reported Unknown", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		hangDir := t.TempDir()
		hangMarker := filepath.Join(scripts, "hang-git.marker")
		writeMarkerScript(t, hangDir, "git", hangMarker, "sleep 30\n")
		// Declared trusted so the TIMEOUT path is isolated from the shadow path.
		writeWorktreeTrustedExecPathConfig(t, home, []string{hangDir})
		t.Setenv("PATH", hangDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		orig := gitProbeTimeout
		gitProbeTimeout = 100 * time.Millisecond
		t.Cleanup(func() { gitProbeTimeout = orig })

		type result struct {
			state WorktreeState
			err   error
		}
		done := make(chan result, 1)
		start := time.Now()
		go func() {
			state, err := realWorktreeState(t.TempDir())
			done <- result{state, err}
		}()
		select {
		case r := <-done:
			if r.state != WorktreeUnknown || r.err == nil {
				t.Fatalf("got (%s, %v), want (unknown, non-nil timeout error)", r.state, r.err)
			}
			if !markerPresent(hangMarker) {
				t.Fatal("the trusted hanging git never started, so the test did not exercise the timeout")
			}
			if d := time.Since(start); d > 10*time.Second {
				t.Fatalf("returned after %s; a 100ms timeout must bound the probe", d)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("realWorktreeState did not return within 15s of a 100ms timeout - it hung")
		}
	})
}

func gitVersion() string {
	p, err := exec.LookPath("git")
	if err != nil {
		return "(none)"
	}
	out, err := exec.Command(p, "--version").CombinedOutput()
	if err != nil {
		return "(unknown)"
	}
	return string(out)
}
