package pathspec

import (
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/secretpath"
)

// ADR 0068 P4 (tc-mkpaz.4, docs/adr/0068-ceta-unified-path-access-
// resolution.md, "Secret handling folds in, with one named, deliberately
// non-generalized exception") folds internal/secretpath's lexical secret
// classification into this package's PathAccess model as spec-level
// entries, so a caller resolving a path's access no longer needs a
// separate secret-aware code path layered on top — see
// ResolveAccessWithSecrets.
//
// Two DIFFERENT treatments, matching secretpath.Kind's own two arms:
//
//  1. secretpath.WellKnownSecret (a specific credential store or file —
//     .ssh/.gnupg, the credential basenames, *.pem/*.key) folds in as an
//     ORDINARY spec entry at the SAME priority as an operator's
//     denyRead/denyWrite: Read, Write, and Delete are all Forbidden,
//     unconditionally — no project declaration can relax it. "Ordinary
//     spec entry" is aspirational, not literal, though:
//     secretpath.Classify matches by PATH COMPONENT/BASENAME anywhere in a
//     path, which is not a shape the root-identifying Kind candidate
//     mechanism (workspace.go's Markers/Home/Temp/Roots) can express (no
//     single ancestor root "identifies" a .ssh directory the way a marker
//     file identifies a git or go workspace). So this is implemented as a
//     RESOLVER-LEVEL pre-step, mirroring how secretRead/classifiedSecretRead
//     work TODAY in internal/effectpolicy/policy.go (just relocated here,
//     ahead of P5 deleting that duplication), rather than as a new Kind —
//     see this file's package-level freedom-boundary note in tc-mkpaz.4's
//     packet text for the two admissible shapes and why this one was
//     chosen. It still composes correctly with the ordinary Kind fold: a
//     WellKnownSecret match's Forbidden wins outright over any deeper
//     Kind's opinion, exactly like the "Protected always wins" /
//     "Forbidden wins outright" rule ResolveAccess itself already applies
//     (workspace.go's updateFacet) — implemented here by never even
//     consulting ResolveAccess for a WellKnownSecret path.
//
//  2. secretpath.GenericSecretsDir (the bare, role-describing "secrets"
//     path component) does NOT fold into ordinary depth-precedence. It
//     stays a distinct, NAMED, low-priority DEFAULT — Forbidden unless
//     vouched otherwise — overridable ONLY by a workspace's
//     tracked-and-not-ignored fact (gitKind's existing Secrecy facet,
//     consulted via NonSecretWith, unchanged from before this packet).
//     Two alternatives were considered and rejected during design (ADR
//     0068's own "Secret handling folds in..." paragraph):
//     - "Depth alone points the wrong way": the vouching git Kind is
//     usually SHALLOWER than the secrets/ directory it vouches for, so
//     ordinary innermost-wins precedence would let the "secrets"
//     component guess beat the shallower vouch.
//     - "Widening to 'any deeper spec's ordinary opinion overrides the
//     guess' would silently loosen protection": an UNTRACKED file
//     inside an otherwise ordinary git project's secrets/ directory
//     would be waved through by the git Kind's broad "this is project
//     content" opinion (gitKind's holdKeep — "needs consent", not a
//     veto) if the generic-secrets default merely participated in the
//     same fold as every other Kind.
//     So GenericSecretsDir handling here NEVER consults ResolveAccess's
//     ordinary Kind-fold result at all when unvouched: it is Forbidden by
//     itself, independent of what any Kind (including git) would otherwise
//     say about the same path. Only NonSecretWith's own single-vouch,
//     first-match-wins walk (already innermost-first, unchanged from
//     before this packet) can lift the default — and when it does, this
//     mechanism steps aside entirely (falls through to ordinary
//     ResolveAccess) rather than asserting Permitted itself.
const (
	wellKnownSecretReason   = "well-known credential store"
	genericSecretsDirReason = `bare "secrets" path component; no project declaration vouches for it as non-secret`
)

// forbidAllFacets returns a PathAccess whose Read, Write, and Delete are all
// Forbidden with the same reason — the shape both secretpath.Kind arms
// collapse to when this packet's spec-level handling applies unconditionally
// (WellKnownSecret always; GenericSecretsDir when unvouched).
func forbidAllFacets(reason string) PathAccess {
	v := Verdict{Result: Forbidden, Reason: reason}
	return PathAccess{Read: v, Write: v, Delete: v}
}

// ResolveAccessWithSecrets is ResolveAccess (workspace.go) with
// internal/secretpath's lexical secret classification folded in ahead of
// the ordinary Kind fold, per this file's package-level doc comment. It
// takes the same []Kind, abs shape ResolveAccess/ClassifyWith/
// NonSecretWithKinds already do, so P5's policy-layer collapse can call
// this in place of ResolveAccess uniformly, with no extra wiring of its
// own for the secret-path cases.
//
//   - abs matching secretpath.WellKnownSecret: Read/Write/Delete all
//     Forbidden, reason "well-known credential store" — ResolveAccess is
//     never even consulted, so no Kind's opinion (including a Permitted
//     one) can relax it.
//   - abs matching secretpath.GenericSecretsDir, UNVOUCHED (NonSecretWith
//     returns false): Read/Write/Delete all Forbidden, reason naming the
//     bare "secrets" component and the absence of a vouch — again without
//     consulting ResolveAccess, so an inner Kind's non-Forbidden opinion
//     (e.g. gitKind's holdKeep, "needs consent" on ordinary tracked
//     content) cannot wave it through.
//   - abs matching secretpath.GenericSecretsDir, VOUCHED (NonSecretWith
//     returns true): this mechanism has no opinion at all; the ordinary
//     ResolveAccess(kinds, abs) result is returned unchanged, exactly as
//     if abs were not a secrets-component match.
//   - abs matching neither (secretpath.NotSecret): ResolveAccess(kinds,
//     abs) unchanged — this function is a strict superset of ResolveAccess
//     for every non-secret path.
func ResolveAccessWithSecrets(kinds []Kind, abs string) PathAccess {
	switch secretpath.Classify(abs) {
	case secretpath.WellKnownSecret:
		return forbidAllFacets(wellKnownSecretReason)
	case secretpath.GenericSecretsDir:
		if vouched, _ := NonSecretWith(kinds, abs); vouched {
			return ResolveAccess(kinds, abs)
		}
		return forbidAllFacets(genericSecretsDirReason)
	default:
		return ResolveAccess(kinds, abs)
	}
}
