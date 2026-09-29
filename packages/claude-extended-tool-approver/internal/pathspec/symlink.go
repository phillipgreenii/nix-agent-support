package pathspec

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// COMPONENT-WISE SYMLINK RESOLUTION (Phase 2h(1) Objective, docket
// tc-o14i5.3, "harden path resolution in pathspec ... resolve symlinks
// component-wise, re-checking each resolved segment against a
// `..`-traversal escape"). internal/patheval is this docket's read-only
// reference (every Phase 2 packet's own Files section says so; see
// osspec.go's header) — the fix below therefore lives entirely in this
// package as an INDEPENDENT resolution entry point, not an edit to
// patheval.PathEvaluator.ResolvePath.
//
// # The bug this closes
//
// patheval.PathEvaluator.resolvePath computes its result via cleanPath:
// env/`~`-expand, filepath.Join the cwd on, THEN filepath.Clean — and Clean
// is a purely LEXICAL, symlink-UNAWARE string operation. When a raw request
// path names a symlink component immediately followed by `..` (e.g.
// "scratch/back/../.git", where "scratch/back" is a symlink pointing
// elsewhere), Clean cancels "back/.." against each other AS STRINGS before
// symlink resolution ever runs — so the eventual filepath.EvalSymlinks call
// never even sees the symlink component, and the result is the WRONG
// (pre-resolution, lexical) location, not the real one a shell or kernel
// would actually reach.
//
// Verified empirically (2026-09-29): filepath.EvalSymlinks("scratch/back/..")
// on a real symlink `scratch/back` correctly returns the symlink target's
// PARENT directory (genuine physical resolution — Go's own implementation
// walks the RAW string component-by-component, resolving each symlink as
// encountered and interpreting a subsequent `..` against the REAL,
// already-resolved directory). But
// filepath.EvalSymlinks(filepath.Clean("scratch/back/..")) — exactly
// patheval.resolvePath's own call order — returns "scratch" itself, because
// Clean already discarded the symlink hop before EvalSymlinks ran.
//
// # The fix
//
// ResolveComponentwise recovers cwd/home via pe.CleanPath(".")/
// pe.CleanPath("~") — each SAFE to lexically clean on its own, since
// neither is expected to carry an embedded symlink-adjacent `..` — then
// builds the candidate's absolute form by PLAIN STRING CONCATENATION
// (never filepath.Join/Clean, both of which lexically collapse `..`), and
// hands that RAW, un-Cleaned string directly to filepath.EvalSymlinks. This
// is what closes "a symlink pointing outside a zone followed by `..` must
// not silently re-enter it" — the escape is checked against the path's REAL
// (post-resolution) location, never the pre-resolution literal path.
//
// # Scope of this packet's wiring
//
// This package's own Classify/ClassifyWith/NonSecretWithKinds (pathspec.go)
// call ResolveComponentwise instead of pe.ResolvePath, so every zone/
// workspace-declaration classification this package performs is hardened.
// internal/effectpolicy's own resolvePathAccessAbs (policy.go) still calls
// patheval.PathEvaluator.ResolvePath DIRECTLY for the `abs` it hands to
// judgeRead/judgeWrite/judgeDelete — policy.go is outside this packet's own
// Files section (Modify: internal/pathspec/* only), so rewiring that call
// site to this hardened resolver is a later packet's job, matching this
// packet's own Contract ("K5, K6, K8, K13 Consume this packet's resolved
// paths/zone classes").
//
// # Known limitation
//
// Only the bare `~` / `~/...` tilde forms are expanded here, not `~user`
// (matching this package's established "fail-safe, prefer silence over a
// guess" convention — see verbs.go's own "Known limitations" note): a
// `~user/...` path falls through to patheval's own ResolvePath instead,
// which is lexically-clean but at least correct for any path with no
// symlink-adjacent `..` (the shape this file exists to fix).

// ResolveComponentwise resolves path exactly as
// patheval.PathEvaluator.ResolvePath would (env/`~`-expand, cwd-relative
// join, symlink resolution), but WITHOUT the intermediate lexical
// filepath.Clean that can hide a symlink-then-`..` escape (see this file's
// package doc comment). Returns "" exactly where ResolvePath would (an
// unexpanded variable, an unknown `~user`, a broken symlink, or an
// unresolvable ancestor).
func ResolveComponentwise(pe *patheval.PathEvaluator, path string) string {
	if pe == nil || path == "" {
		return ""
	}
	expanded := os.ExpandEnv(path)
	if strings.Contains(expanded, "$") {
		return "" // an unexpanded variable — same fail-closed answer ResolvePath gives
	}
	switch {
	case expanded == "~" || strings.HasPrefix(expanded, "~/"):
		home := pe.CleanPath("~")
		if home == "" {
			return ""
		}
		expanded = home + expanded[1:]
	case strings.HasPrefix(expanded, "~"):
		// `~user/...` — not reconstructed here; see this file's package doc
		// comment ("Known limitation").
		return pe.ResolvePath(path)
	}
	if !filepath.IsAbs(expanded) {
		cwd := pe.CleanPath(".")
		if cwd == "" {
			return ""
		}
		expanded = cwd + string(filepath.Separator) + expanded
	}
	return evalSymlinksRaw(expanded)
}

// evalSymlinksRaw mirrors patheval/evaluator.go's own
// evalSymlinksWithFallback algorithm (read-only reference — duplicated here
// rather than exported from there, the same "deliberate, accepted
// duplication" pattern osspec.go's header already establishes for this
// docket), applied to a RAW, un-Clean-ed absolute path: filepath.EvalSymlinks
// first; on failure (the path does not yet exist — e.g. a Write's
// not-yet-created target), walk up by RAW path segments (never
// filepath.Dir, which also Cleans) to the nearest existing ancestor, resolve
// THAT, and reattach the removed suffix.
func evalSymlinksRaw(abs string) string {
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(r)
	} else if info, lerr := os.Lstat(abs); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "" // the path itself is a broken symlink
	}
	sep := string(filepath.Separator)
	segments := strings.Split(strings.TrimPrefix(abs, sep), sep)
	for i := len(segments) - 1; i > 0; i-- {
		ancestorRaw := sep + strings.Join(segments[:i], sep)
		if r, err := filepath.EvalSymlinks(ancestorRaw); err == nil {
			suffix := strings.Join(segments[i:], sep)
			return filepath.Clean(filepath.Join(r, suffix))
		}
	}
	return ""
}
