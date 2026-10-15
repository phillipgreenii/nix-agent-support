// trustedexec_marketplace_test.go — pg2-yr90y: the marketplace plugin-script
// identity of ResolveTrustedExecutable (see trustedexec_marketplace.go).
//
// Every subtest builds a hermetic fake "nix store" and a fake HOME under
// t.TempDir(), points the store root at it with SetMarketplaceStoreRootForTest,
// and installs the marketplace the way the home-manager module does: a symlink
// <HOME>/.local/share/pgii-marketplaces/<marketplace> -> <store>/<hash>-claude-marketplace-<name>.
// The negative controls are real filesystem shapes (a planted real directory,
// a symlink that leaves the derivation, a ".." spelling), not mocks.
package patheval

import (
	"os"
	"path/filepath"
	"testing"
)

type marketplaceFixture struct {
	home      string
	storeRoot string
	storeDir  string // <store>/<hash>-claude-marketplace-fake
	installed string // <home>/.local/share/pgii-marketplaces/fake-marketplace-local
	script    string // the argv0 of the happy path
}

const fixtureMarketplace = "fake-marketplace-local"

func newMarketplaceFixture(t *testing.T) marketplaceFixture {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	storeRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(SetMarketplaceStoreRootForTest(storeRoot))

	storeDir := filepath.Join(storeRoot, "0123456789abcdfghijklmnpqrsvwxyz-claude-marketplace-fake")
	mustWrite(t, filepath.Join(storeDir, "plan-decompose", "scripts", "create-packet.sh"), "#!/bin/sh\n")
	mustWrite(t, filepath.Join(storeDir, "plan-decompose", "hooks", "hook.sh"), "#!/bin/sh\n")

	installRoot := filepath.Join(home, marketplaceInstallRel)
	if err := os.MkdirAll(installRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(installRoot, fixtureMarketplace)
	if err := os.Symlink(storeDir, installed); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	return marketplaceFixture{
		home: home, storeRoot: storeRoot, storeDir: storeDir, installed: installed,
		script: filepath.Join(installed, "plan-decompose", "scripts", "create-packet.sh"),
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestResolveTrustedExecutable_MarketplacePluginScriptTrusted(t *testing.T) {
	f := newMarketplaceFixture(t)
	realpath, trusted, err := ResolveTrustedExecutable(f.script)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !trusted {
		t.Fatalf("ResolveTrustedExecutable(%s) trusted = false, want true (marketplace plugin script)", f.script)
	}
	want := filepath.Join(f.storeDir, "plan-decompose", "scripts", "create-packet.sh")
	if realpath != want {
		t.Errorf("realpath = %q, want %q", realpath, want)
	}
}

func TestResolveTrustedExecutable_MarketplaceNegativeControls(t *testing.T) {
	cases := []struct {
		name  string
		argv0 func(t *testing.T, f marketplaceFixture) string
	}{
		{"same file spelled by its literal store path", func(t *testing.T, f marketplaceFixture) string {
			// The pre-existing literal /nix/store carve-out spelling, with the
			// fake store root standing in for /nix/store: not under the install
			// root, so it is not a marketplace-path spelling.
			return filepath.Join(f.storeDir, "plan-decompose", "scripts", "create-packet.sh")
		}},
		{"non-marketplace absolute path", func(t *testing.T, f marketplaceFixture) string {
			p := filepath.Join(t.TempDir(), "create-packet.sh")
			mustWrite(t, p, "#!/bin/sh\n")
			return p
		}},
		{"dot-dot spelling that looks like the marketplace prefix", func(t *testing.T, f marketplaceFixture) string {
			evil := filepath.Join(t.TempDir(), "evil.sh")
			mustWrite(t, evil, "#!/bin/sh\n")
			rel, err := filepath.Rel(filepath.Join(f.installed, "plan-decompose", "scripts"), evil)
			if err != nil {
				t.Fatal(err)
			}
			// <install>/<mkt>/plan-decompose/scripts/../../../../../tmp/.../evil.sh
			return f.installed + "/plan-decompose/scripts/" + rel
		}},
		{"dot-dot spelling that stays inside the derivation", func(t *testing.T, f marketplaceFixture) string {
			return f.installed + "/plan-decompose/hooks/../scripts/create-packet.sh"
		}},
		{"redundant dot-dot spelling of the very same script", func(t *testing.T, f marketplaceFixture) string {
			// Resolves to the trusted file, yet is rejected: only canonical
			// spellings are an identity, so a path that merely routes through
			// ".." never gets the benefit of the prefix.
			return f.installed + "/plan-decompose/scripts/../scripts/create-packet.sh"
		}},
		{"real directory planted under the install root", func(t *testing.T, f marketplaceFixture) string {
			p := filepath.Join(f.home, marketplaceInstallRel, "planted-marketplace-local", "plan-decompose", "scripts", "create-packet.sh")
			mustWrite(t, p, "#!/bin/sh\n")
			return p
		}},
		{"marketplace symlink pointing outside the store", func(t *testing.T, f marketplaceFixture) string {
			outside := t.TempDir()
			mustWrite(t, filepath.Join(outside, "plan-decompose", "scripts", "create-packet.sh"), "#!/bin/sh\n")
			link := filepath.Join(f.home, marketplaceInstallRel, "outside-marketplace-local")
			if err := os.Symlink(outside, link); err != nil {
				t.Skipf("cannot create symlink: %v", err)
			}
			return filepath.Join(link, "plan-decompose", "scripts", "create-packet.sh")
		}},
		{"marketplace symlink to a store entry that is not a marketplace derivation", func(t *testing.T, f marketplaceFixture) string {
			other := filepath.Join(f.storeRoot, "0123456789abcdfghijklmnpqrsvwxyz-some-other-package")
			mustWrite(t, filepath.Join(other, "plan-decompose", "scripts", "create-packet.sh"), "#!/bin/sh\n")
			link := filepath.Join(f.home, marketplaceInstallRel, "other-marketplace-local")
			if err := os.Symlink(other, link); err != nil {
				t.Skipf("cannot create symlink: %v", err)
			}
			return filepath.Join(link, "plan-decompose", "scripts", "create-packet.sh")
		}},
		{"marketplace symlink to a nested store path, not a direct child", func(t *testing.T, f marketplaceFixture) string {
			link := filepath.Join(f.home, marketplaceInstallRel, "nested-marketplace-local")
			if err := os.Symlink(filepath.Join(f.storeDir, "plan-decompose"), link); err != nil {
				t.Skipf("cannot create symlink: %v", err)
			}
			return filepath.Join(link, "scripts", "create-packet.sh")
		}},
		{"script symlinked out of the derivation", func(t *testing.T, f marketplaceFixture) string {
			target := filepath.Join(t.TempDir(), "elsewhere.sh")
			mustWrite(t, target, "#!/bin/sh\n")
			link := filepath.Join(f.storeDir, "plan-decompose", "scripts", "escape.sh")
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("cannot create symlink: %v", err)
			}
			return filepath.Join(f.installed, "plan-decompose", "scripts", "escape.sh")
		}},
		{"plugin directory symlinked out of the derivation", func(t *testing.T, f marketplaceFixture) string {
			outside := t.TempDir()
			mustWrite(t, filepath.Join(outside, "scripts", "create-packet.sh"), "#!/bin/sh\n")
			if err := os.Symlink(outside, filepath.Join(f.storeDir, "escaped-plugin")); err != nil {
				t.Skipf("cannot create symlink: %v", err)
			}
			return filepath.Join(f.installed, "escaped-plugin", "scripts", "create-packet.sh")
		}},
		{"a plugin directory other than scripts", func(t *testing.T, f marketplaceFixture) string {
			return filepath.Join(f.installed, "plan-decompose", "hooks", "hook.sh")
		}},
		{"scripts directory itself", func(t *testing.T, f marketplaceFixture) string {
			return filepath.Join(f.installed, "plan-decompose", "scripts")
		}},
		{"nonexistent script", func(t *testing.T, f marketplaceFixture) string {
			return filepath.Join(f.installed, "plan-decompose", "scripts", "no-such-script.sh")
		}},
		{"trailing slash", func(t *testing.T, f marketplaceFixture) string {
			return filepath.Join(f.installed, "plan-decompose", "scripts", "create-packet.sh") + "/"
		}},
		{"relative spelling", func(t *testing.T, f marketplaceFixture) string {
			return "plan-decompose/scripts/create-packet.sh"
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newMarketplaceFixture(t)
			argv0 := c.argv0(t, f)
			// Control: the happy path of the SAME fixture is trusted, so a
			// failure below is the shape's doing, not a broken fixture.
			if _, trusted, _ := ResolveTrustedExecutable(f.script); !trusted {
				t.Fatalf("fixture control: %s should be trusted", f.script)
			}
			if _, trusted, err := ResolveTrustedExecutable(argv0); err != nil || trusted {
				t.Errorf("ResolveTrustedExecutable(%s) = trusted %v, err %v; want untrusted", argv0, trusted, err)
			}
		})
	}
}

func TestResolveTrustedExecutable_MarketplaceHomeFromUserHomeDirOnly(t *testing.T) {
	f := newMarketplaceFixture(t)
	// A different HOME makes the very same spelling fall outside the install
	// root: trust follows os.UserHomeDir, never the path's own claim.
	t.Setenv("HOME", t.TempDir())
	if _, trusted, _ := ResolveTrustedExecutable(f.script); trusted {
		t.Errorf("ResolveTrustedExecutable(%s) trusted under a different HOME, want untrusted", f.script)
	}
}
