// Package deletable is the DELETABLE path class of the CETA effect-graph
// spike (design bead tc-z806, children tc-z806.1 and tc-z806.3): a third
// access class layered OVER patheval's read-only / read-write zones,
// answering "may this path be removed without asking?".
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
// patheval's four zones and add a classification layered over them — for
// two reasons. First, patheval.PathAccess is an ORDERED enum that production
// rules compare directly (`access == PathReadWrite` in patheval's own
// symlink-escape branch, the CanRead/CanWrite predicates every rule calls);
// a new value above PathReadWrite would ripple through every one of those
// sites and through String()'s output, whereas the bead requires the
// existing patheval tests to pass unchanged. Second, and decisive: the
// temp-root source MUST reuse internal/temproot.Roots (the one list of
// temp landing zones, pg2-yoqsr) rather than grow a second copy, and
// temproot imports patheval, so patheval cannot import it back. The layer
// therefore lives in its own package that imports both; patheval gains only
// the small GitRoot export the git kind needs.
//
// # Sources are WORKSPACE DECLARATIONS
//
// Deletability comes from workspace kinds (workspace.go: temp, home, git,
// go, gradle, pn), each declaring how to identify its root and how to
// categorise paths under it. Resolve picks the deciding kind: a Protected
// opinion from ANY kind wins; otherwise the innermost kind with an opinion.
// A path inside a git working tree is therefore judged by that repository's
// ignore rules (ignored => Deletable, else Keep) even when the repository
// sits under a temp root — a `git init` fixture in $TMPDIR is a workspace;
// its tracked files are not disposable just because of where it was
// created. Only a path OUTSIDE every inner kind falls through to home or
// temp.
//
// # Protections win
//
// Classify never returns Deletable for a path patheval zones reject or
// read-only, and returns Protected for a path any kind protects. It does NOT
// itself consult the sandbox deny lists or secretpath — the DeleteAccess
// policy (internal/effectpolicy) checks IsDenyWrite / IsDenyRead /
// secretpath BEFORE it ever asks this package, so a gitignored `.env` or a
// `~/.ssh` key is Forbidden before deletability is a question. Classify is
// a pure classification; it never decides.
package deletable

import (
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// Class is the deletability of a path.
type Class int

const (
	// NotWritable: patheval's zone forbids writing (reject or read-only),
	// or the path could not be resolved, or no declaration vouches for a
	// zone-unknown path. Deletability does not arise.
	NotWritable Class = iota
	// Writable: the zone allows writing but no declaration marks the path
	// disposable (or a kind explicitly says Keep) — a delete needs consent
	// (Abstain by default).
	Writable
	// Deletable: a declaration marks the path disposable — a delete may be
	// approved by default. Deletable implies writable by declaration: a
	// zone-unknown path under a declared cache or temp root is Deletable.
	Deletable
	// Protected: a declaration says the path must not be removed by a
	// plain delete (repository metadata, worktree administration). Reject
	// by default.
	Protected
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
	case Protected:
		return "protected"
	default:
		return "class-invalid"
	}
}

// Classify reports the deletability of path (cwd-relative, `~`- and
// env-expanded, exactly as patheval.Evaluate would take it) under pe and the
// default workspace kinds, with a short reason naming what decided.
func Classify(pe *patheval.PathEvaluator, path string) (Class, string) {
	return ClassifyWith(pe, DefaultKinds(), path)
}

// ClassifyWith is Classify with an explicit kind set.
func ClassifyWith(pe *patheval.PathEvaluator, kinds []Kind, path string) (Class, string) {
	if pe == nil {
		return NotWritable, "no path evaluator"
	}
	access := pe.Evaluate(path)
	if access == patheval.PathReject || access == patheval.PathReadOnly {
		return NotWritable, "zone " + access.String()
	}
	abs := pe.ResolvePath(path)
	if abs == "" {
		return NotWritable, "path does not resolve"
	}
	res := Resolve(kinds, abs)
	switch res.Category {
	case CatProtected:
		return Protected, "protected by " + res.Kind + " workspace at " + res.Root
	case CatDeletable:
		return Deletable, "deletable per " + res.Kind + " workspace at " + res.Root
	case CatKeep:
		if access.CanWrite() {
			return Writable, "kept by " + res.Kind + " workspace at " + res.Root
		}
		return NotWritable, "zone " + access.String() + "; kept by " + res.Kind + " workspace at " + res.Root
	}
	if access.CanWrite() {
		return Writable, "no workspace declaration matched"
	}
	return NotWritable, "zone " + access.String() + "; no workspace declaration matched"
}
