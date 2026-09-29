package pathspec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWorktreeHardening exercises the two P9 hardening gaps Ground Truth
// flagged verbatim for pathspec/worktree.go's git invocations
// ("pathspec/worktree.go:129,227 execs git inside the hook (PATH-resolved,
// no timeout, repo config honoured)"), via trustedGitPath and
// gitProbeTimeout: a hung git process is killed at the configured timeout
// and reported WorktreeUnknown rather than hanging the hook or crashing, and
// a PATH-shadowed `git` binary (a fake script placed earlier on PATH than
// the real, operator-trusted git) is detected and NEVER EXECUTED — matching
// design item m's own marker-test wording ("PATH-shadowed git => no marker;
// timeout => WorktreeUnknown"), which K15's fuzz/adapter suite (out of this
// packet's scope) later exercises against this same behavior from the
// policy layer; this test pins the pathspec-level behavior directly.
func TestWorktreeHardening(t *testing.T) {
	t.Run("timeout kills a hung git and reports WorktreeUnknown", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		fakeDir := t.TempDir()
		fakeGit := filepath.Join(fakeDir, "git")
		if err := os.WriteFile(fakeGit, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
			t.Fatalf("write fake git: %v", err)
		}
		// Declared TRUSTED (the operator's own choice) so this subtest
		// isolates the TIMEOUT path from the PATH-shadow path below — a
		// trusted-but-hanging git must still be killed at the deadline.
		writeWorktreeTrustedExecPathConfig(t, home, []string{fakeDir})
		t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		orig := gitProbeTimeout
		gitProbeTimeout = 50 * time.Millisecond
		t.Cleanup(func() { gitProbeTimeout = orig })

		type result struct {
			state WorktreeState
			err   error
		}
		done := make(chan result, 1)
		go func() {
			state, err := realWorktreeState(t.TempDir())
			done <- result{state, err}
		}()
		select {
		case r := <-done:
			if r.state != WorktreeUnknown {
				t.Fatalf("got %s, want unknown", r.state)
			}
			if r.err == nil {
				t.Fatal("expected a non-nil error naming the timeout")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("realWorktreeState did not return within 5s of a 50ms timeout — it hung")
		}
	})

	t.Run("PATH-shadowed git is detected and never executed", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		realGitPath, lookErr := exec.LookPath("git")
		if lookErr != nil {
			t.Skipf("git not found on PATH: %v", lookErr)
		}
		// Declare ONLY the real git's directory trusted — the shadow
		// script below is a DIFFERENT, untrusted binary despite sharing
		// the basename "git".
		writeWorktreeTrustedExecPathConfig(t, home, []string{filepath.Dir(realGitPath)})

		fakeDir := t.TempDir()
		fakeGit := filepath.Join(fakeDir, "git")
		marker := filepath.Join(fakeDir, "executed.marker")
		script := "#!/bin/sh\ntouch '" + marker + "'\necho fake-git-ran\nexit 0\n"
		if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
			t.Fatalf("write fake git: %v", err)
		}
		t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		root := t.TempDir()
		state, err := realWorktreeState(root)
		if state != WorktreeUnknown {
			t.Fatalf("got %s, want unknown (a PATH-shadowed git must never be trusted)", state)
		}
		if err == nil {
			t.Fatal("expected a non-nil error naming the untrusted git")
		}
		if _, statErr := os.Stat(marker); statErr == nil {
			t.Fatal("the PATH-shadowed git script's marker file exists — realWorktreeState executed it, which P9 forbids")
		}

		if _, err := realGitTracked(root, "anything", false); err == nil {
			t.Fatal("realGitTracked: expected a non-nil error naming the untrusted git")
		}
		if _, statErr := os.Stat(marker); statErr == nil {
			t.Fatal("realGitTracked executed the PATH-shadowed git script")
		}
	})
}

// writeWorktreeTrustedExecPathConfig mirrors internal/patheval/
// trustedexec_test.go's writeTrustedExecPathConfig and worktree_test.go's
// own trustRealGit — kept a distinct name/signature in this file (a
// caller-chosen directory list, rather than "always the real git") since
// these subtests deliberately trust ONE directory while a DIFFERENT one
// shadows it on PATH.
func writeWorktreeTrustedExecPathConfig(t *testing.T, home string, trustedDirs []string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "claude-extended-tool-approver-engine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	quoted := make([]string, len(trustedDirs))
	for i, d := range trustedDirs {
		quoted[i] = `"` + filepath.ToSlash(d) + `"`
	}
	body := `{"trustedExecPath":[` + strings.Join(quoted, ",") + `]}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}
