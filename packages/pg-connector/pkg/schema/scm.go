// scm.go: the scm entity/capability's shared JSON wire shapes, built by the
// "generic scm entity/capability" packet on top of the Tier-1 core's
// pkg/schema placeholder (see doc.go).
//
// Unlike pr/issue/ci, scm manages LOCAL git state (worktrees, cwd->branch
// resolution) and has no remote-sync concept [design: §4.7]. It is
// deliberately generic, not PR-aware: a caller wanting "check out PR #482
// for review" composes two calls (pg-connector pr show 482 to resolve the
// branch, then pg-connector scm worktree add <branch>) rather than one
// command doing both [design: §4.7]. Nothing in this file names a
// backend/system (e.g. "git") — the scm capability's schema is generic over
// whatever local-git-state backend eventually implements scm.Provider
// [design: §3].
package schema

// ScmSchemaVersion is the scm capability's own schema version, populated
// into the wire envelope's schemaVersion field by each of the scm
// capability's dispatch-table entries (pkg/provider/scm.NewDispatchTable) —
// independent of pkg/scriptout.ProtocolVersion and of the pr capability's
// own SchemaVersion [design: §4.2, §4.3 — "one integer per schema-bearing
// capability"]. Named ScmSchemaVersion rather than a second bare
// SchemaVersion because this package holds every capability's schema types
// together (§5.2's "public: shared JSON wire shapes" package) and a second
// top-level SchemaVersion constant in the same package would collide with
// pr.go's.
const ScmSchemaVersion = 1

// WorktreeInfo is the scm capability's shared JSON wire shape describing
// one local git worktree, returned by the worktree_add/worktree_list ops
// and carried by pkg/provider/scm.Provider's WorktreeAdd/WorktreeList
// [design: §4.7 (Produces)].
type WorktreeInfo struct {
	// Path is the worktree's local filesystem path.
	Path string `json:"path"`
	// Branch is the branch currently checked out in this worktree.
	Branch string `json:"branch"`
	// Ref is the ref (branch or otherwise) the worktree was added for —
	// worktree add takes a branch-or-ref, never a PR number
	// [design: §4.7].
	Ref string `json:"ref"`
}

// BranchInfo is the scm capability's shared JSON wire shape for a
// cwd->branch resolution, returned by the branch_detect op and carried by
// pkg/provider/scm.Provider's BranchDetect [design: §4.7 (Produces)].
type BranchInfo struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
}
