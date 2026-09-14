package pathspec

import (
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// ADR 0068 P3 (tc-mkpaz.3, docs/adr/0068-ceta-unified-path-access-
// resolution.md, "Spec sources" — Session/config spec bullet) folds the
// runtime-derived, session/config-scoped access grants — sandbox
// allowWrite, an INDEPENDENT allowRead grant (a deliberate widening; see
// below), the CETA_EXTRA_READWRITE_ROOTS/CETA_EXTRA_READONLY_ROOTS
// env-configured extra roots, and the project root/WORKSPACE_ROOT grant —
// into a pathspec.Kind, computed once from a *patheval.PathEvaluator's
// configuration via SessionKind, rather than staying separate sidecar
// checks threaded through patheval and the policies.
//
// # allowRead is an INDEPENDENT grant, not an override-only shim
//
// Operator ruling (Phillip, 2026-09-13, verbatim, recorded in the ADR):
// "allowRead converts to its own independent Read: Permitted spec grant,
// mirroring allowWrite. This is a deliberate WIDENING of read access beyond
// today's override-only behavior, not an incidental side effect of the
// port — implement it as a grant, not a preserved override-only shim."
// patheval.IsDenyRead's existing AllowRead override loop is UNTOUCHED by
// this package — it is a separate, pre-existing mechanism at the patheval
// layer that P5's PathAccessPolicy will still consult directly as a
// sidecar (see internal/patheval's AllowReadRoots doc comment); this Kind's
// grant is additive, not a replacement of that loop.
//
// # project root vs WORKSPACE_ROOT: a deliberate asymmetry, ported faithfully
//
// classify()'s <projectRoot>/** grant is gated by projectRootGrantsZone (a
// too-broad/fabricated root — $HOME or an ancestor of it — grants no zone,
// mirroring patheval's rootGrantsZone); its WORKSPACE_ROOT/** grant
// immediately below has NO such guard. This Kind ports that asymmetry
// exactly — projectRoot is skipped when pe.ProjectRootGrantsZone() is
// false; workspaceRoot is added unconditionally whenever it is set —
// rather than narrowing WORKSPACE_ROOT to match, which this packet's
// "keeps patheval's existing tests passing unchanged" contract does not
// authorize.
//
// # RemotePaths is OUT of scope
//
// The ADR's "Spec sources" bullet also lists "RemotePaths overrides" among
// the items folding into one Kind, but its later, more specific "What does
// not collapse" section explicitly keeps remote-scope paths out of the
// local Kind walk (keyed by remote host, not local filesystem structure —
// no local marker-walk applies to a path on a machine this process is not
// running on). This package treats that dedicated, more detailed section
// as controlling; PolicyContext.RemotePaths/remotePathGuard stay untouched
// and are NOT part of this Kind. Flagged in the decomposition report as an
// internal ADR inconsistency for the operator to confirm.
const sessionKindName = "session"

// SessionKind returns the pathspec.Kind declaring pe's session/config-scoped
// access grants — see this file's package-level doc comment. Delete stays
// Unknown for every root here: these are runtime access grants, not
// workspace deletability declarations (that remains workspace.go's temp/
// home/git/... kinds' job). Pass it inside an explicit []Kind to
// ResolveAccess/ResolveAccessWithSecrets/ClassifyWith — wiring it into
// DefaultKinds() is P5's job, not this packet's.
//
// A nil pe (mirroring ClassifyWith/NonSecretWithKinds' own nil-pe handling)
// yields a Kind with no roots at all, never a panic.
func SessionKind(pe *patheval.PathEvaluator) Kind {
	return Kind{
		Name:  sessionKindName,
		Roots: func() []RootDecl { return sessionRootDecls(pe) },
	}
}

// sessionRootDecls computes the RootDecl set SessionKind's Kind.Roots
// closure returns, per this file's package-level doc comment:
//
//   - every sandboxConfig.AllowWrite root: Write: Permitted (identical to
//     today's classify() behavior for allowWrite).
//   - every sandboxConfig.AllowRead root: Read: Permitted, INDEPENDENTLY —
//     not merely canceling a denyRead match.
//   - every CETA_EXTRA_READWRITE_ROOTS root: Write: Permitted, Read:
//     Permitted.
//   - every CETA_EXTRA_READONLY_ROOTS root: Read: Permitted only —
//     mirroring extraRootAccess's own read-write-before-read-only
//     precedence (irrelevant here since each entry only ever declares one
//     of the two shapes, but named for parity with classify()'s check
//     order).
//   - the project root, Read: Permitted + Write: Permitted, GATED by
//     ProjectRootGrantsZone().
//   - WORKSPACE_ROOT, Read: Permitted + Write: Permitted, UNGATED.
func sessionRootDecls(pe *patheval.PathEvaluator) []RootDecl {
	if pe == nil {
		return nil
	}
	var decls []RootDecl
	for _, root := range pe.AllowWriteRoots() {
		decls = append(decls, RootDecl{Root: root, Write: Permitted})
	}
	for _, root := range pe.AllowReadRoots() {
		decls = append(decls, RootDecl{Root: root, Read: Permitted})
	}
	for _, root := range pe.ExtraReadWriteRoots() {
		decls = append(decls, RootDecl{Root: root, Read: Permitted, Write: Permitted})
	}
	for _, root := range pe.ExtraReadOnlyRoots() {
		decls = append(decls, RootDecl{Root: root, Read: Permitted})
	}
	if pe.ProjectRootGrantsZone() {
		if root := pe.ProjectRoot(); root != "" {
			decls = append(decls, RootDecl{Root: root, Read: Permitted, Write: Permitted})
		}
	}
	if root := pe.WorkspaceRoot(); root != "" {
		decls = append(decls, RootDecl{Root: root, Read: Permitted, Write: Permitted})
	}
	return decls
}
