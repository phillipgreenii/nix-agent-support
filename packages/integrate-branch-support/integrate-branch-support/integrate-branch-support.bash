# shellcheck shell=bash
# Core logic (primary-branch resolution, signal gathering, strategy
# selection) as testable functions, split out of integrate-branch-support.sh.
# No top-level code — mkBashScript sources this file before the .sh body.

# resolve_primary_branch: print the repo's primary branch name.
# Order: pgii-integrate-branch.primaryBranch git config -> origin/HEAD
# (stripped of the "origin/" prefix) -> "main".
resolve_primary_branch() {
  local b
  b="$(git config --get pgii-integrate-branch.primaryBranch 2>/dev/null || true)"
  [ -n "$b" ] && {
    printf '%s' "$b"
    return
  }
  b="$(git symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null || true)"
  [ -n "$b" ] && {
    printf '%s' "${b#origin/}"
    return
  }
  printf 'main'
}

# canonical_root: print the absolute path to the MAIN working tree, i.e. the
# canonical clone, resolved even when invoked from inside a linked worktree.
# git-common-dir is the shared ".git" dir for the whole worktree set; its
# parent directory is the main worktree (verified true both when already
# inside the main tree and from a linked one). For the rare repo laid out
# with `--separate-git-dir`, git-common-dir's parent is not the main
# worktree; `git worktree list --porcelain | head -1` (strip the "worktree "
# prefix) is the more-robust alternative for that layout.
canonical_root() {
  local common_dir
  common_dir="$(git rev-parse --git-common-dir)"
  git -C "$(dirname "$common_dir")" rev-parse --show-toplevel
}

# canonical_branch: print the branch checked out in the canonical clone.
canonical_branch() {
  git -C "$(canonical_root)" symbolic-ref --short -q HEAD || echo "(detached)"
}

# canonical_dirty: print "true"/"false" for whether the canonical clone has
# uncommitted changes (tracked or untracked).
canonical_dirty() {
  if [ -n "$(git -C "$(canonical_root)" status --porcelain)" ]; then
    echo true
  else
    echo false
  fi
}
