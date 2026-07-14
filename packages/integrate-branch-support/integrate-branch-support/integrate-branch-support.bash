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
