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
//
// # NON-SECRET declarations (tc-lc8f item 3z, resume bead tc-lc8f)
//
// Slice 3x's goTestSchema started emitting a PathRead effect for every `go
// test` package operand (registry_breadth.go), so `go test
// ./internal/rules/secrets/...` newly reached NoReadOfSecretPath, whose
// secretRead calls secretpath.IsSecret and Forbids the bare `secrets` path
// COMPONENT (secretpath.GenericSecretsDir) — a regression against the
// 2026-09-07 ruling "go test ... are fine" (agreement_integration_test.go /
// golden_test.go's go-family comments).
//
// Production's internal/rules/secrets ALREADY has a fix for the same shape
// (its package doc's decision 3, operator ruling on pg2-fhb9q/pg2-pmk9q):
// the bare `secrets` component is skipped for READS inside a git repository,
// decided inside the secret-detection RULE itself via
// patheval.InGitRepo. This package's fix is DELIBERATELY A DIFFERENT
// mechanism, per an explicit operator ruling that rejected copying that
// approach into the spike.
//
// Operator ruling 1 (Phillip, 2026-09-07, verbatim, recorded on beads
// tc-lc8f and tc-vn5z): "i dont want a in-git-repo relaxation rule. i would
// like of the definition of a git repo project contains information about
// nonsecrets. ie, can we bake it into a gnereal spexification of projexts."
//
// That instruction inverts WHERE the non-secret information lives: not a
// special case inside the secret-detection policy ("if we're inside a git
// repo, relax"), but a declaration owned by the PROJECT SPECIFICATION this
// package already is (the same Kind mechanism workspace.go uses for
// deletability) — general to every kind, not git-specific by construction,
// even though git is the only kind that declares an opinion in this slice.
// Kind.Secrecy is that declaration; NonSecret/NonSecretWith resolve it using
// the SAME innermost-workspace-wins candidate collection Resolve already
// uses for deletability (workspace.go), so a project's notion of "this path
// is not secret" is answered by the identical machinery as "this path is
// disposable" — one project specification, two questions.
//
// Operator ruling 2 (the design choice the operator confirmed from three
// options put to them): "Tracked-by-git means non-secret" — the git
// workspace Kind declares that a path committed to the repository (in the
// index) and NOT gitignored is non-secret; secrets are never committed, so
// an ignored file such as `.env` keeps ordinary secret matching. See
// workspace.go's gitKind.Secrecy and gitTrackedProbe (the hermetic,
// injectable `git ls-files` probe, mirroring worktree.go's
// ProbeWorktreeState/SetWorktreeStateProbe pattern from slice 3t) for the
// mechanism. Other project kinds MAY add their own Secrecy declarations
// later (glob rules, say) — not implemented in this slice.
//
// A well-known secret basename or directory (secretpath.WellKnownSecret —
// `.ssh`/`.gnupg`, the credential basenames, `*.pem`/`*.key`) is NEVER
// relaxed by any project declaration, tracked or not: only the bare,
// role-describing `secrets` component (secretpath.GenericSecretsDir) is a
// question this package answers at all. internal/effectpolicy's
// NoReadOfSecretPath/secretRead is the only caller and enforces that split
// — see its own doc comment.
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

// NonSecret reports whether path (cwd-relative, `~`- and env-expanded,
// exactly as Classify accepts) is declared NON-SECRET by pe and the default
// workspace kinds, with a short reason naming what decided — see this
// package's doc comment ("# NON-SECRET declarations") for the operator
// rulings and provenance behind why this question is answered here rather
// than inside a secret-detection policy.
//
// A false result means "no project declaration vouches for this path" —
// NOT "this path is secret". Whether path is secret at all is
// secretpath.IsSecret/Classify's question; NonSecret only ever narrows a
// GenericSecretsDir match, and only internal/effectpolicy's secretRead
// calls it, only for that one Kind of match (see secretpath.Kind's doc for
// why a WellKnownSecret match is never a question this package is asked).
func NonSecret(pe *patheval.PathEvaluator, path string) (bool, string) {
	return NonSecretWithKinds(pe, DefaultKinds(), path)
}

// NonSecretWithKinds is NonSecret with an explicit kind set (mirrors
// ClassifyWith).
func NonSecretWithKinds(pe *patheval.PathEvaluator, kinds []Kind, path string) (bool, string) {
	if pe == nil {
		return false, "no path evaluator"
	}
	abs := pe.ResolvePath(path)
	if abs == "" {
		return false, "path does not resolve"
	}
	return NonSecretWith(kinds, abs)
}
