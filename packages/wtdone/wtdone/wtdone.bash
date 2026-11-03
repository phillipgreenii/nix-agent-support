# shellcheck shell=bash
# Core logic (canonical-root resolution, worktree-by-branch lookup and the
# remaining-worktrees report) as testable functions, split out of wtdone.sh.
# The lsof liveness guard and its allow-list live in wt-liveness-lib. No
# top-level code -- mkBashScript sources this file before the .sh body.

# canonical_root: print the absolute path to the MAIN working tree, i.e. the
# canonical clone, resolved even when wtdone is invoked from inside an
# existing linked worktree. Duplicated verbatim from
# integrate-branch-support.bash's function of the same name (wtnew's copy
# carries the identical note) -- this repo has no shared git-helpers library
# yet to depend on instead; keep the copies in sync if one is ever extracted.
# See integrate-branch-support.bash's own doc comment for the full rationale,
# including the --separate-git-dir caveat.
canonical_root() {
  local common_dir
  common_dir="$(git rev-parse --git-common-dir)"
  git -C "$(dirname "$common_dir")" rev-parse --show-toplevel
}

# wtdone_find_worktree <cc> <branch>: print the absolute path of the linked
# worktree in <cc> that has <branch> checked out, or nothing if no worktree
# has it checked out (already removed elsewhere, or never had one -- a plain
# local branch with no worktree). Deliberately resolved by ASKING GIT which
# worktree (if any) holds the branch, rather than assuming a directory-naming
# convention (e.g. wtnew's `.worktrees/<name>`): wtdone's four callers
# (cleanup-workforest, ff-merge-to-main's FF-4, wrap-up-session,
# drain-beads/pb-drain-isolate) each lay worktrees out differently -- a
# coordinated workforest set's members live at
# `<workspace_root>/.workforests/<branch>/<member>`, for instance, nowhere
# near `.worktrees/`. Querying `git worktree list --porcelain` works
# regardless of layout, and it is the SAME idiom this repo's own
# git-branch-maintenance.sh already uses (get_branch_worktree) for exactly
# this lookup.
wtdone_find_worktree() {
  local cc="$1" branch="$2"
  git -C "$cc" worktree list --porcelain | awk -v branch="$branch" '
    /^worktree / { path=$2 }
    /^branch refs\/heads\// && $0 == "branch refs/heads/"branch { print path; exit }
  '
}

# wtdone_remaining_worktrees <cc>: print `git worktree list`'s report for
# <cc>, for step 6's "remaining worktrees" line. Thin, named wrapper purely
# so the lib tests can assert on it directly instead of shelling `git`
# themselves.
wtdone_remaining_worktrees() {
  local cc="$1"
  git -C "$cc" worktree list
}
