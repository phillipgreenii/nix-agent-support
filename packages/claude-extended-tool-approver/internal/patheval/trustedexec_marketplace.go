package patheval

import (
	"os"
	"path/filepath"
	"strings"
)

// Marketplace plugin scripts as a trusted executable identity (pg2-yr90y;
// docs/adr/0099-ceta-marketplace-plugin-scripts-are-a-trusted-executable-identity.md,
// which amends ADR 0075's P4).
//
// Operator ruling (Phillip, 2026-10-04, epic pg2-cjfpy): every command form a
// plugin skill, command or agent instructs an agent to run MUST get a
// deterministic CETA approve. The plan-decompose decomposers call
// create-packet.sh, assemble-packet.sh and update-packet.sh by ABSOLUTE path
// under the installed marketplace tree, which P4 alone can never trust (the
// operator's TrustedExecPath names directories of binaries, not plugin script
// directories, and a bare /nix/store argv0 is unconditionally untrusted).
//
// The identity accepted here is deliberately narrow. argv0 is trusted only
// when ALL of these hold:
//
//  1. argv0 is already in canonical form (filepath.Clean(argv0) == argv0, so
//     no "..", ".", "//" or trailing slash) and is spelled
//     <HOME>/.local/share/pgii-marketplaces/<marketplace>/<plugin>/scripts/<file>.
//     HOME comes from os.UserHomeDir only (P7: never an inherited variable an
//     agent-controlled shell could export).
//  2. The marketplace entry <HOME>/.local/share/pgii-marketplaces/<marketplace>
//     resolves, through symlinks, to a DIRECT child of the Nix store whose name
//     carries "-claude-marketplace-" (the mkClaudeMarketplace derivation
//     name). The store is read-only and content-addressed, so an agent cannot
//     plant a script there; a real directory an agent created under HOME
//     fails this step because it does not resolve into the store.
//  3. The argv0's own symlink-resolved realpath EQUALS that store directory
//     joined with <plugin>/scripts/<file> exactly, so a symlink anywhere inside
//     the argv0 chain that leaves the marketplace derivation (a path that only
//     LOOKS like the prefix) is untrusted. EvalSymlinks is strict here: a
//     nonexistent argv0 is untrusted (unlike ResolveRealPath, which
//     reconstructs a nonexistent tail).
//
// Why code and not a TrustedExecPath config entry: TrustedExecPath is a flat
// list of directories compared per basename, so covering every marketplace
// plugin's scripts would need one operator entry per plugin (and per
// marketplace), would have to be kept in step with the plugin set by hand, and
// today no machine config writes that file at all (it is absent on this
// machine), so the fix would also depend on new Nix wiring plus an apply. A
// code rule keyed on the install layout covers every plugin's scripts/
// directory with no per-plugin list and the narrow surface above.

// marketplaceInstallRel is the HOME-relative directory the home-manager
// claude-marketplaces module installs each marketplace under.
const marketplaceInstallRel = ".local/share/pgii-marketplaces"

// marketplaceStoreNameMarker is the substring the mkClaudeMarketplace builder
// puts in the store name of a marketplace derivation
// (/nix/store/<hash>-claude-marketplace-<repo>).
const marketplaceStoreNameMarker = "-claude-marketplace-"

// marketplaceStoreRoot is the Nix store directory a marketplace entry must
// resolve into. A variable only so a test can point it at a temp directory
// (see SetMarketplaceStoreRootForTest); production never changes it.
var marketplaceStoreRoot = "/nix/store"

// SetMarketplaceStoreRootForTest redirects the store root the marketplace
// identity check requires, returning a function that restores the previous
// value. It exists so tests can build a hermetic fake "store" under a temp
// directory (the real /nix/store cannot be written to); nothing in production
// code calls it.
func SetMarketplaceStoreRootForTest(root string) (restore func()) {
	prev := marketplaceStoreRoot
	marketplaceStoreRoot = root
	return func() { marketplaceStoreRoot = prev }
}

// isMarketplacePluginScript reports whether argv0 is a plugin script served
// from the nix-store-backed pgii-marketplaces install (conditions 1-3 in the
// comment above) and returns its resolved realpath when it is.
func isMarketplacePluginScript(argv0 string) (realpath string, ok bool) {
	if !strings.HasPrefix(argv0, "/") || filepath.Clean(argv0) != argv0 {
		return "", false
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", false
	}
	installRoot := filepath.Join(home, marketplaceInstallRel)
	rel, found := strings.CutPrefix(argv0, installRoot+"/")
	if !found {
		return "", false
	}
	// <marketplace>/<plugin>/scripts/<file...>
	parts := strings.Split(rel, "/")
	if len(parts) < 4 || parts[2] != "scripts" {
		return "", false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return "", false
		}
	}

	storeRoot, err := filepath.EvalSymlinks(marketplaceStoreRoot)
	if err != nil {
		return "", false
	}
	storeDir, err := filepath.EvalSymlinks(filepath.Join(installRoot, parts[0]))
	if err != nil {
		return "", false
	}
	if filepath.Dir(storeDir) != filepath.Clean(storeRoot) || !strings.Contains(filepath.Base(storeDir), marketplaceStoreNameMarker) {
		return "", false
	}

	real, err := filepath.EvalSymlinks(argv0)
	if err != nil {
		return "", false
	}
	if real != filepath.Join(append([]string{storeDir}, parts[1:]...)...) {
		return "", false
	}
	return real, true
}
