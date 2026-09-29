package patheval

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/userconfig"
)

// ResolveTrustedExecutable implements ADR 0075's P4 ("executable identity"):
// an absolute argv0 (leaf.Executable containing "/") matches its basename's
// command spec only if its realpath equals the realpath of
// <dir>/<basename(argv0)> for some dir in the trusted PATH the operator
// declared in the new engine's user-level config
// (userconfig.Config.TrustedExecPath, P17) — never a hardcoded guess at
// where a Nix profile or /run/current-system/sw/bin live.
//
// # The /nix/store carve-out
//
// A bare "/nix/store/..." argv0 is UNCONDITIONALLY untrusted, even when its
// realpath happens to equal a trusted PATH entry's own resolution. A
// genuine PATH-search invocation of an operator-trusted binary is never
// itself a literal /nix/store path — the shell finds it via a profile
// symlink (or /run/current-system/sw/bin), and EvalSymlinks only resolves
// THAT symlink down to its /nix/store target for the COMPARISON side, never
// for argv0 itself. An argv0 that already spells out /nix/store directly is
// exactly the identity-spoofing case P4 exists to catch (any locally-built
// or fetched store path is world-readable and world-executable, regardless
// of whether the operator's PATH was ever configured to trust it), so it is
// rejected before the realpath comparison even runs.
//
// # Relative argv0 is out of scope (R2), not "unknown"
//
// Callers MUST route a relative argv0 (one not starting with "/", e.g.
// "./scripts/foo" or "bin/tool") to R2's in-checkout-exec handling instead
// of calling this function at all — see effectgraph/build.go's call site,
// which only calls this for a leaf.Executable starting with "/". This
// function still defends itself against a relative argv0 by returning
// (untrusted, no error) deterministically, rather than resolving it against
// this PROCESS's own incidental working directory (which has nothing to do
// with the shell leaf's actual CWD, since this function intentionally takes
// no cwd parameter).
//
// realpath is argv0's own resolved real path (ResolveRealPath), returned
// even when untrusted so a caller can cite it in a Detail/Reason string.
// err is reserved for a future config source that can fail with a real I/O
// error (userconfig.LoadDefault currently never errors — it fails closed to
// an empty Config instead) — always nil today.
func ResolveTrustedExecutable(argv0 string) (realpath string, trusted bool, err error) {
	if !strings.HasPrefix(argv0, "/") {
		return "", false, nil
	}

	realpath = ResolveRealPath(argv0)
	if realpath == "" {
		return "", false, nil
	}

	if strings.HasPrefix(argv0, "/nix/store/") {
		return realpath, false, nil
	}

	base := path.Base(argv0)
	cfg := userconfig.LoadDefault()
	for _, dir := range cfg.TrustedExecPath {
		if dir == "" {
			continue
		}
		candidateReal := ResolveRealPath(filepath.Join(dir, base))
		if candidateReal != "" && candidateReal == realpath {
			return realpath, true, nil
		}
	}
	return realpath, false, nil
}
