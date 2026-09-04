// Package scm declares the scm capability's provider interface — a small,
// capability-scoped Go interface (never named after a backend/system)
// [design: §3] that a Tier-2 scm backend's concrete provider implements. It
// matches this repo's existing small-per-capability-interface convention
// (e.g. pkg/provider/pr.Provider) rather than one interface spanning
// multiple systems [design: §3].
//
// Unlike pr/issue/ci, scm manages LOCAL git state and has no remote-sync
// concept, so its Provider has no analogue of pr.Provider's "categorize"/
// "feedback_set" remote-write ops [design: §4.7].
//
// This package sits alongside pkg/schema and pkg/scriptout as part of the
// module's shared surface importable across backend boundaries — see
// cmd/pg-connector's layout-convention check.
package scm

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Provider is the scm capability's provider interface: local git worktree
// management plus cwd->branch resolution [design: §4.7]. A concrete backend
// MAY additionally implement pkg/provider.AuthChecker, asserted via a
// type-check rather than folded into this interface [design: §4.6] — see
// NewDispatchTable in dispatch.go. The design's own scm backend
// (pg-connector-scm-git, a sibling packet) does NOT implement AuthChecker:
// local git plumbing has no remote credentials concept [design: §4.6,
// §4.7].
type Provider interface {
	// WorktreeAdd adds a local git worktree for branchOrRef — a branch or
	// ref, NEVER a PR number. A caller wanting "check out PR #482 for
	// review" composes pg-connector pr show 482 (to resolve the branch)
	// then this op, rather than this op resolving a PR number itself
	// [design: §4.7].
	WorktreeAdd(ctx context.Context, branchOrRef string) (*schema.WorktreeInfo, error)

	// WorktreeRemove removes the local git worktree at path. A well-formed
	// not_found response (path is not a known worktree) is a valid
	// negative answer, not a broken call [design: §4.5, §4.7].
	WorktreeRemove(ctx context.Context, path string) error

	// WorktreeList lists every local git worktree this backend manages.
	WorktreeList(ctx context.Context) ([]schema.WorktreeInfo, error)

	// BranchDetect resolves cwd to its repo and currently checked-out
	// branch [design: §4.7].
	BranchDetect(ctx context.Context, cwd string) (*schema.BranchInfo, error)
}
