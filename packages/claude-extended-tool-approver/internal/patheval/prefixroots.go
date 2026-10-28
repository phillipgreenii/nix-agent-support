package patheval

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ExtraReadOnlyPrefixRootsEnv names the env var holding the named-prefix read-only
// roots: ":"-separated entries, each `<parent>/<prefix>*` where <parent> is either an
// absolute directory or the literal token `$TMPDIR`. The home-manager option
// `extraReadOnlyPrefixRoots` renders it. Bead pg2-d00wo0, operator ruling 2026-10-27 on
// pg2-ibih79 (allow the agent scratch dirs p2-decomp-*, pdec-*, pd-* under ~/.cache and
// pd-* under $TMPDIR, but NOT ~/.cache or $TMPDIR as a whole). ADR 0112.
const ExtraReadOnlyPrefixRootsEnv = "CETA_EXTRA_READONLY_PREFIX_ROOTS"

// tmpdirToken is the parent spelling that means "this process's own $TMPDIR",
// resolved when the evaluator is built (per-user, per-boot /var/folders/.../T/ is not
// knowable at nix build time).
const tmpdirToken = "$TMPDIR"

var (
	// prefixPattern is the SHAPE RULE for a prefix: an alphanumeric first character,
	// a safe name charset, and a mandatory trailing dash. The dash is what keeps
	// `pd-*` from matching a sibling `pd` or `pdx-foo`.
	prefixPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*-$`)
	// childSuffixPattern is the safe charset for what follows the prefix in a child
	// directory name (non-empty: the bare prefix directory is not a child).
	childSuffixPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	// parentForbiddenChars are never allowed in a configured parent: glob and shell
	// metacharacters, so the only wildcard in an entry is the single trailing `*`.
	parentForbiddenChars = "*?[]{}$\\~"
)

// PrefixRoot grants read access to the DIRECT children of Parent whose name is Prefix
// followed by a safe suffix, and everything beneath them. It never grants Parent
// itself, a sibling whose name merely resembles Prefix, or anything reached through a
// symlink that leaves such a child.
type PrefixRoot struct {
	// Parent is the symlink-resolved directory the children live directly under.
	Parent string
	// Prefix is the child-name prefix, always ending in "-".
	Prefix string
}

// ChildOf returns the matched child directory (Parent/<name>) when path is that
// directory or lies beneath it, else "". path MUST already be cleaned and
// symlink-resolved (Evaluate hands classify such a path): a `..` cannot survive
// cleaning, and a symlink that escapes a child resolves to its real target, which is
// then judged on its own and does not match. The matched name must be Prefix + a
// non-empty suffix of the safe charset, so `pd` (no suffix), `pdx-foo` (different
// prefix) and `pd-a b` (unsafe name) are all refused.
func (r PrefixRoot) ChildOf(path string) string {
	if r.Parent == "" || r.Prefix == "" || !strings.HasPrefix(path, r.Parent+"/") {
		return ""
	}
	rest := path[len(r.Parent)+1:]
	name := rest
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		name = rest[:i]
	}
	if !strings.HasPrefix(name, r.Prefix) || !childSuffixPattern.MatchString(name[len(r.Prefix):]) {
		return ""
	}
	return r.Parent + "/" + name
}

// parsePrefixRoot parses one `<parent>/<prefix>*` entry. ok is false for anything that
// does not satisfy the shape rule or whose parent is too broad (at or above $HOME) or
// secret-adjacent (is, contains or sits under ~/.claude, whose transcripts can hold
// secrets, ruling pg2-r9kv7), so a bad entry grants nothing rather than something wide.
func parsePrefixRoot(entry, home string) (PrefixRoot, bool) {
	i := strings.LastIndexByte(entry, '/')
	if i < 0 {
		return PrefixRoot{}, false
	}
	parent, last := entry[:i], entry[i+1:]
	if !strings.HasSuffix(last, "*") || strings.Count(entry, "*") != 1 {
		return PrefixRoot{}, false
	}
	prefix := strings.TrimSuffix(last, "*")
	if !prefixPattern.MatchString(prefix) {
		return PrefixRoot{}, false
	}

	var resolved string
	if parent == tmpdirToken {
		resolved = unattendedTmpdir()
	} else {
		if !filepath.IsAbs(parent) || parent == "/" || strings.ContainsAny(parent, parentForbiddenChars) {
			return PrefixRoot{}, false
		}
		for _, seg := range strings.Split(parent, "/") {
			if seg == "." || seg == ".." {
				return PrefixRoot{}, false
			}
		}
		if filepath.Clean(parent) != parent {
			return PrefixRoot{}, false
		}
		resolved = resolveRefPath(parent)
	}
	if resolved == "" {
		return PrefixRoot{}, false
	}
	if home != "" {
		claude := filepath.Join(home, ".claude")
		if coversPath(resolved, home) || coversPath(resolved, claude) || coversPath(claude, resolved) {
			return PrefixRoot{}, false
		}
	}
	return PrefixRoot{Parent: resolved, Prefix: prefix}, true
}

// resolveExtraPrefixRoots reads ExtraReadOnlyPrefixRootsEnv and returns every entry
// that passes parsePrefixRoot. home is the symlink-resolved home directory.
func resolveExtraPrefixRoots(home string) []PrefixRoot {
	raw := os.Getenv(ExtraReadOnlyPrefixRootsEnv)
	if raw == "" {
		return nil
	}
	var roots []PrefixRoot
	for _, entry := range strings.Split(raw, ":") {
		if entry == "" {
			continue
		}
		if r, ok := parsePrefixRoot(entry, home); ok {
			roots = append(roots, r)
		}
	}
	return roots
}

// ExtraReadOnlyPrefixRoots exposes the configured named-prefix read-only roots (see
// ExtraReadOnlyRoots' doc comment for why the session spec needs them).
func (pe *PathEvaluator) ExtraReadOnlyPrefixRoots() []PrefixRoot {
	return pe.extraReadOnlyPrefix
}
