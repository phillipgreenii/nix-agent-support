// trustedexec_test.go — tc-o14i5.3.2 (Phase 2b, P4 executable identity).
//
// Exercises ResolveTrustedExecutable against the exact acceptance criteria
// its packet names: a same-basename-different-realpath argv0 is untrusted;
// an argv0 whose realpath matches a trusted-PATH entry's own resolution is
// trusted; a bare /nix/store/... argv0 is untrusted even when its realpath
// would otherwise match; and a repo-relative argv0 is never routed through
// this check at all (deterministically untrusted, not incidentally so).
//
// Every subtest pins HOME to its own t.TempDir() and writes (or omits) a
// userconfig config file there, so no subtest ever reads this machine's
// real ~/.config/claude-extended-tool-approver-engine/config.json.
package patheval

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTrustedExecPathConfig renders a userconfig config file at
// <home>/.config/claude-extended-tool-approver-engine/config.json naming
// trustedDirs as TrustedExecPath — hand-written JSON (not an import of
// package userconfig) so this test does not depend on that package's
// encoding beyond the wire contract ResolveTrustedExecutable itself relies
// on (userconfig.DefaultPath's location, "trustedExecPath" field name).
func writeTrustedExecPathConfig(t *testing.T, home string, trustedDirs []string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "claude-extended-tool-approver-engine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	quoted := make([]string, len(trustedDirs))
	for i, d := range trustedDirs {
		quoted[i] = `"` + d + `"`
	}
	body := `{"trustedExecPath":[` + joinComma(quoted) + `]}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

func joinComma(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

func TestResolveTrustedExecutable_TrustedPathRealpathMatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	realTarget := filepath.Join(t.TempDir(), "actual-git-binary")
	if err := os.WriteFile(realTarget, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write real target: %v", err)
	}

	trustedDir := t.TempDir()
	if err := os.Symlink(realTarget, filepath.Join(trustedDir, "git")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	writeTrustedExecPathConfig(t, home, []string{trustedDir})

	// argv0 is a DIFFERENT absolute path (not itself the trusted dir's own
	// entry) whose basename is "git" and which resolves, via its own
	// symlink, to the SAME real target — modeling e.g. a second profile
	// generation's symlink chain landing on the identical real binary.
	otherDir := t.TempDir()
	argv0 := filepath.Join(otherDir, "git")
	if err := os.Symlink(realTarget, argv0); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	realpath, trusted, err := ResolveTrustedExecutable(argv0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !trusted {
		t.Errorf("ResolveTrustedExecutable(%s) trusted = false, want true (realpath matches trusted PATH entry)", argv0)
	}
	wantReal := evalSymlinksWithFallback(realTarget)
	if realpath != wantReal {
		t.Errorf("realpath = %q, want %q", realpath, wantReal)
	}
}

func TestResolveTrustedExecutable_SameBasenameDifferentRealpath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	realGit := filepath.Join(t.TempDir(), "actual-git-binary")
	if err := os.WriteFile(realGit, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write real git: %v", err)
	}
	trustedDir := t.TempDir()
	if err := os.Symlink(realGit, filepath.Join(trustedDir, "git")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	writeTrustedExecPathConfig(t, home, []string{trustedDir})

	// impersonator: same basename "git", but resolves to a DIFFERENT real
	// file than the trusted PATH's own "git" entry.
	impersonator := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(impersonator, []byte("#!/bin/sh\necho pwned\n"), 0o755); err != nil {
		t.Fatalf("write impersonator: %v", err)
	}

	_, trusted, err := ResolveTrustedExecutable(impersonator)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if trusted {
		t.Errorf("ResolveTrustedExecutable(%s) trusted = true, want false (realpath differs from trusted PATH's git)", impersonator)
	}
}

func TestResolveTrustedExecutable_BareNixStoreUntrustedEvenIfResolves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Trust a /nix/store path directly (an artificial, non-existent-but-
	// reconstructable store path is fine — evalSymlinksWithFallback walks up
	// to an existing ancestor, e.g. /nix/store itself, and reconstructs).
	trustedStoreBin := "/nix/store/deadbeef00000000000000000000000-faketool-1.0/bin"
	writeTrustedExecPathConfig(t, home, []string{trustedStoreBin})

	// argv0 is the literal, would-otherwise-match entry itself: same
	// basename, same reconstructed realpath (both are the identical
	// nonexistent path, so they trivially "resolve" identically) — this
	// isolates the /nix/store carve-out from the realpath-comparison logic:
	// WITHOUT the carve-out this would incorrectly report trusted.
	argv0 := trustedStoreBin + "/git"

	realpath, trusted, err := ResolveTrustedExecutable(argv0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if trusted {
		t.Errorf("ResolveTrustedExecutable(%s) trusted = true, want false (bare /nix/store argv0 is never trusted, P4)", argv0)
	}
	if realpath == "" {
		t.Errorf("realpath = %q, want a non-empty reconstructed path (untrusted is orthogonal to whether it resolves)", realpath)
	}
}

func TestResolveTrustedExecutable_NoTrustedPathConfigured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// No config.json written at all: userconfig.LoadDefault fails closed to
	// an empty Config, so TrustedExecPath is empty and nothing can ever
	// match — the fail-closed default per P3.

	realBin := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(realBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write bin: %v", err)
	}

	_, trusted, err := ResolveTrustedExecutable(realBin)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if trusted {
		t.Errorf("ResolveTrustedExecutable(%s) trusted = true, want false (no trusted PATH configured at all)", realBin)
	}
}

func TestResolveTrustedExecutable_RepoRelativeNotRoutedThroughThisCheck(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Even if a config were present naming a trusted dir that happens to
	// resolve identically to how this process's own CWD would resolve a
	// relative path, a relative argv0 MUST still come back untrusted,
	// deterministically — this function never treats a relative argv0 as
	// in-scope at all (see its doc comment: callers route R2 in-checkout
	// exec elsewhere).
	writeTrustedExecPathConfig(t, home, []string{"."})

	for _, argv0 := range []string{"./scripts/foo", "bin/tool", "../sibling/tool"} {
		realpath, trusted, err := ResolveTrustedExecutable(argv0)
		if err != nil {
			t.Errorf("ResolveTrustedExecutable(%s): unexpected error: %v", argv0, err)
		}
		if trusted {
			t.Errorf("ResolveTrustedExecutable(%s) trusted = true, want false (relative argv0 is R2 in-checkout exec, not routed through P4 at all)", argv0)
		}
		if realpath != "" {
			t.Errorf("ResolveTrustedExecutable(%s) realpath = %q, want empty (relative argv0 is rejected before any resolution)", argv0, realpath)
		}
	}
}
