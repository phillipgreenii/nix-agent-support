// Package deletable is the DELETABLE path class of the CETA effect-graph
// spike (design bead tc-z806, child tc-z806.1): a third access class layered
// OVER patheval's read-only / read-write zones, answering "may this path be
// removed without asking?" for a path the zone model already says is
// writable.
//
// Operator ruling (Phillip, 2026-09-07, recorded verbatim on tc-z806): "rm
// would be rejected for paths which aren't at least writable. for writable it
// should abstain (by default) and if deletable, then it can approve (by
// default). in both cases, there could be other rules which change the
// default. paths which are 'deletable' would be files in temp directories.
// also, i would expect that files listed in .gitignore would be deletable,
// by default. obviously, things like .env which would be in the .gitignore
// should not be deletable, but there should be other rules above this default
// rule which would protect them."
//
// # Shape: a layer over patheval, not a fifth PathAccess value
//
// tc-z806.1 offered two admissible shapes. This package is shape (b) — keep
// patheval's four zones and add a predicate layered over them — for two
// reasons. First, patheval.PathAccess is an ORDERED enum that production
// rules compare directly (`access == PathReadWrite` in patheval's own
// symlink-escape branch, the CanRead/CanWrite predicates every rule calls);
// a new value above PathReadWrite would ripple through every one of those
// sites and through String()'s output, whereas the bead requires the
// existing patheval tests to pass unchanged. Second, and decisive: the
// temp-root source MUST reuse internal/temproot.Roots (the one list of
// temp landing zones, pg2-yoqsr) rather than grow a second copy, and
// temproot imports patheval, so patheval cannot import it back. The layer
// therefore lives in its own package that imports both; patheval gains only
// the small GitRoot export the gitignore source needs.
//
// # Precedence: innermost workspace wins
//
// Sources are consulted in the parent design's order (tc-z806's model, item
// 5): the INNERMOST workspace a path belongs to decides. Today there are two
// sources. A path inside a git working tree is judged by that repository's
// ignore rules — an ignored path is deletable, an un-ignored one is NOT, even
// when the repository itself sits under a temp root (a `git init` fixture in
// $TMPDIR is a workspace; its tracked files are not disposable just because
// of where it was created). Only a path OUTSIDE any repository falls through
// to the temp-root source. The workspace-declarations bead (tc-z806.3) adds
// further sources on top of this ordering; it does not change it.
//
// # Protections win
//
// Deletable IMPLIES writable: Classify returns Deletable only when
// patheval's zone can be written. It does NOT itself consult the sandbox
// deny lists or secretpath — the DeleteAccess policy (internal/effectpolicy)
// checks IsDenyWrite / IsDenyRead / secretpath / the reject and read-only
// zones BEFORE it ever asks this package, so a gitignored `.env` or a
// `~/.ssh` key is Forbidden before deletability is a question. Classify is a
// pure classification; it never decides.
package deletable

import (
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/temproot"
)

// Class is the deletability of a path.
type Class int

const (
	// NotWritable: patheval's zone does not allow writing (or the path could
	// not be resolved), so deletability does not arise.
	NotWritable Class = iota
	// Writable: the zone allows writing but no source marks the path
	// disposable — a delete needs consent (Abstain by default).
	Writable
	// Deletable: writable AND a source marks the path disposable — a delete
	// may be approved by default.
	Deletable
)

// String returns the deterministic class name.
func (c Class) String() string {
	switch c {
	case NotWritable:
		return "not-writable"
	case Writable:
		return "writable"
	case Deletable:
		return "deletable"
	default:
		return "class-invalid"
	}
}

// Classify reports the deletability of path (cwd-relative, `~`- and
// env-expanded, exactly as patheval.Evaluate would take it) under pe, with a
// short reason naming the source that decided. It never returns Deletable
// for a path pe cannot write.
func Classify(pe *patheval.PathEvaluator, path string) (Class, string) {
	if pe == nil {
		return NotWritable, "no path evaluator"
	}
	access := pe.Evaluate(path)
	if !access.CanWrite() {
		return NotWritable, "zone " + access.String()
	}
	abs := pe.ResolvePath(path)
	if abs == "" {
		return NotWritable, "path does not resolve"
	}
	if root, ok := patheval.GitRoot(abs); ok {
		if Ignored(root, abs) {
			return Deletable, "gitignored in " + root
		}
		return Writable, "not gitignored in " + root
	}
	if temproot.Under(abs) {
		return Deletable, "under a temp root"
	}
	return Writable, "no deletability source matched"
}
