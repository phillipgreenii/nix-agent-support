// exec_identity_fuzz_test.go - tc-o14i5.3.15 (Phase 2m, design item m).
//
// FuzzExecIdentity fuzzes P4 executable identity (ResolveTrustedExecutable,
// ADR 0075) against invariants that must hold for EVERY argv0:
//
//  1. It never panics and never reports an error (err is reserved), and two
//     calls agree (determinism).
//  2. A relative argv0 (anything not starting with "/") is untrusted with an
//     empty realpath: R2's in-checkout-exec handling owns that case.
//  3. A literal "/nix/store/..." argv0 is never trusted, however its realpath
//     compares with the trusted PATH.
//  4. trusted == true only arises when an INDEPENDENT recomputation agrees:
//     filepath.EvalSymlinks(D/<basename of argv0>) for the trusted directory D
//     equals the returned, non-empty realpath. (The oracle uses
//     filepath.EvalSymlinks directly, not ResolveRealPath, so a bug shared with
//     the code under test cannot hide itself.) Where D/<basename> does not
//     exist, ResolveRealPath's documented "a not-yet-existing path resolves to
//     itself" behaviour (evaluator.go) makes the name compare equal to itself;
//     the oracle then still requires the realpath to be a DIRECT child of D, so
//     trust can never arise from a path outside the trusted directory.
//
// The fixture lives under f.TempDir() with HOME pinned so the real ~/.config is
// never read; nothing here classifies the directory, so the nix-sandbox TMPDIR
// caveat does not apply. Seeds use the placeholders @T (trusted dir), @O (a
// second dir holding a symlink to the SAME binary) and @S (a dir holding a
// DIFFERENT binary named like the trusted one); the fuzz body expands them, so
// the checked-in corpus files under testdata/fuzz/FuzzExecIdentity stay
// machine-independent.
package patheval

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func FuzzExecIdentity(f *testing.F) {
	home := f.TempDir()
	f.Setenv("HOME", home)

	base := f.TempDir()
	realTarget := filepath.Join(base, "real-git")
	if err := os.WriteFile(realTarget, []byte("#!/bin/sh\n"), 0o755); err != nil {
		f.Fatalf("write real target: %v", err)
	}
	trusted := filepath.Join(base, "trusted")
	other := filepath.Join(base, "other")
	shadow := filepath.Join(base, "shadow")
	for _, d := range []string{trusted, other, shadow} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			f.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.Symlink(realTarget, filepath.Join(trusted, "git")); err != nil {
		f.Skipf("cannot create symlink: %v", err)
	}
	if err := os.Symlink(realTarget, filepath.Join(other, "git")); err != nil {
		f.Skipf("cannot create symlink: %v", err)
	}
	if err := os.WriteFile(filepath.Join(shadow, "git"), []byte("#!/bin/sh\necho evil\n"), 0o755); err != nil {
		f.Fatalf("write shadow git: %v", err)
	}
	realTrusted, err := filepath.EvalSymlinks(trusted)
	if err != nil {
		f.Fatalf("resolve trusted dir: %v", err)
	}
	cfgDir := filepath.Join(home, ".config", "claude-extended-tool-approver-engine")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		f.Fatalf("mkdir config dir: %v", err)
	}
	body := `{"trustedExecPath":["` + trusted + `"]}`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(body), 0o644); err != nil {
		f.Fatalf("write config.json: %v", err)
	}

	for _, seed := range []string{
		"@T/git",
		"@O/git",
		"@S/git",
		"@T/../shadow/git",
		"@S/../trusted/git",
		"@T//git",
		"@T/./git",
		"@T/git/",
		"@T/nonexistent",
		"git",
		"./git",
		"../git",
		"",
		"/",
		"//",
		"/nix/store/aaaa-git/bin/git",
		"/nix/store/",
		"/nix/store",
		"/usr/bin/../../@T/git",
		"/bin/true",
		"/opt/attacker-controlled/true",
		"~/git",
		"$HOME/git",
		"@T/git\x00x",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		argv0 := strings.NewReplacer("@T", trusted, "@O", other, "@S", shadow).Replace(raw)

		realpath, ok, err := ResolveTrustedExecutable(argv0)
		if err != nil {
			t.Fatalf("argv0 %q: unexpected error %v (err is reserved)", argv0, err)
		}
		realpath2, ok2, err2 := ResolveTrustedExecutable(argv0)
		if realpath != realpath2 || ok != ok2 || (err == nil) != (err2 == nil) {
			t.Fatalf("argv0 %q: not deterministic: (%q,%v) vs (%q,%v)", argv0, realpath, ok, realpath2, ok2)
		}

		if !strings.HasPrefix(argv0, "/") {
			if ok || realpath != "" {
				t.Fatalf("relative argv0 %q: got (%q, trusted=%v), want (\"\", false)", argv0, realpath, ok)
			}
			return
		}
		if strings.HasPrefix(argv0, "/nix/store/") && ok {
			t.Fatalf("literal store path %q reported trusted (realpath %q)", argv0, realpath)
		}
		if !ok {
			return
		}
		if realpath == "" {
			t.Fatalf("argv0 %q trusted with an empty realpath", argv0)
		}
		want, werr := filepath.EvalSymlinks(filepath.Join(trusted, path.Base(argv0)))
		switch {
		case werr == nil:
			if want != realpath {
				t.Fatalf("argv0 %q trusted with realpath %q but %s/%s resolves to %q", argv0, realpath, trusted, path.Base(argv0), want)
			}
		case os.IsNotExist(werr) || errors.Is(werr, syscall.EINVAL):
			if realpath != filepath.Join(realTrusted, path.Base(argv0)) {
				t.Fatalf("argv0 %q trusted (realpath %q) but it is not a direct child of the trusted dir %q", argv0, realpath, realTrusted)
			}
		default:
			t.Fatalf("argv0 %q trusted with realpath %q; oracle error %v", argv0, realpath, werr)
		}
	})
}
