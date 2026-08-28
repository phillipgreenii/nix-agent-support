// Package gitfacet resolves the git-dependent session-location facets reported
// by ccpool list --json (repo root, worktree, branch). Every facet fails SOFT:
// outside a git work tree (or when git is absent / errors) the corresponding
// field is nil, never an error, so a single bad cwd can't fail the whole list.
package gitfacet

import (
	"context"
	"path/filepath"

	"github.com/phillipgreenii/x/gitclient"
)

// Facets are the git-dependent location facets for a cwd. A nil field means the
// facet is unavailable (cwd is not inside a git work tree, or git failed).
type Facets struct {
	// RepoRoot is the MAIN repository root: for a normal checkout it equals
	// Worktree; for a linked worktree it is the directory containing the shared
	// .git (parent of --git-common-dir), NOT the linked worktree root.
	RepoRoot *string
	// Worktree is the working-tree root for cwd (git rev-parse --show-toplevel).
	Worktree *string
	// Branch is the checked-out branch; nil when detached (rev-parse reports
	// "HEAD") or unavailable.
	Branch *string
}

// Resolve returns the git facets for cwd. It never returns an error: any
// failed sub-query leaves that facet nil. A cwd outside a git work tree (or a
// missing git binary) yields an all-nil Facets. This is the app-side
// soft-fail policy; it is unchanged by the migration to x/gitclient below.
//
// Resolve takes no context because its only caller (ccpool list) has none to
// thread through the gitFn seam it plugs into (cmd/ccpool/list.go); a
// package-scoped context.Background() is used for the git calls this
// performs, matching the previous implementation's lack of any deadline.
func Resolve(cwd string) Facets {
	var f Facets
	ctx := context.Background()

	// gitclient.Discover walks up from cwd to the repository toplevel and
	// anchors a Client there -- exactly the "where am I" case this package
	// exists for (bead pg2-svfbb's design, "The client -- gitclient/client.go").
	// It also doubles as the cheap "are we in a repo at all?" probe:
	// ErrNotARepository (cwd outside a git work tree) or a missing git
	// binary both land here and leave every facet nil, matching the old raw
	// git() helper's soft-fail behavior.
	client, err := gitclient.Discover(ctx, cwd)
	if err != nil {
		return f
	}

	top, err := client.Toplevel(ctx)
	if err != nil {
		return f
	}
	f.Worktree = &top

	// Repo root = parent of the shared git common dir. For a normal checkout the
	// common dir is "<root>/.git", so its parent is the worktree root; for a
	// linked worktree it points at the MAIN repo's .git, so its parent is the
	// main repo root (which differs from the linked worktree).
	if commonDir, err := client.CommonDir(ctx); err == nil {
		root := filepath.Dir(commonDir)
		f.RepoRoot = &root
	}

	// Branch: nil on detached HEAD. The pre-migration implementation ran
	// `rev-parse --abbrev-ref HEAD`, which reports the literal string "HEAD"
	// on a detached checkout; gitclient's CurrentBranch (`branch
	// --show-current`) instead returns the typed sentinel
	// gitclient.ErrDetachedHEAD for the same case. Folding EVERY
	// CurrentBranch error (ErrDetachedHEAD included) into "leave Branch nil"
	// is the mapping: it preserves the exact observable behavior callers
	// already depend on without needing to special-case the sentinel.
	if branch, err := client.CurrentBranch(ctx); err == nil {
		f.Branch = &branch
	}

	return f
}
