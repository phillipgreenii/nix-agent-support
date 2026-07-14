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

# detect_remote: print exactly two lines — (1) the resolved remote name,
# empty when there is none or it can't be resolved unambiguously; (2) an
# "ambiguous" reason note, empty unless line 1 is empty *because* of
# ambiguity (never because of a genuine absence of any remote). Two
# lines (rather than a bare name via a global side-effect) because this
# runs inside `$(...)` command substitutions, which fork a subshell — a
# global set inside the function would not survive back to the caller.
# Resolution order: (1) the current branch's upstream remote
# (git rev-parse --abbrev-ref --symbolic-full-name @{upstream}, taking the
# remote portion before the first "/"); (2) if exactly one git remote
# exists, use it; (3) otherwise empty. Zero remotes is simply "no remote"
# (not ambiguous); two or more remotes with no upstream configured IS
# ambiguous (which remote would it be?) and reports so on line 2.
detect_remote() {
  local upstream remotes count reason=""
  upstream="$(git rev-parse --abbrev-ref --symbolic-full-name '@{upstream}' 2>/dev/null || true)"
  if [ -n "$upstream" ]; then
    printf '%s\n\n' "${upstream%%/*}"
    return
  fi
  remotes="$(git remote 2>/dev/null || true)"
  count=0
  if [ -n "$remotes" ]; then
    # Pure-bash line count (no grep/wc dependency): this must also work
    # under whatever `bash` a scrubbed PATH resolves to (e.g. macOS's
    # ancient system /bin/bash), not just a modern one.
    while IFS= read -r _line; do
      [ -n "$_line" ] && count=$((count + 1))
    done <<<"$remotes"
  fi
  if [ "$count" -eq 1 ]; then
    printf '%s\n\n' "$remotes"
    return
  fi
  if [ "$count" -gt 1 ]; then
    reason="ambiguous remote: multiple remotes (${remotes//$'\n'/, }) and no upstream set for the current branch"
  fi
  printf '\n%s\n' "$reason"
}

# detect_open_pr: print a JSON object ({number,state,url}) describing the
# OPEN pull request for the current branch, or nothing when there is no
# open PR, `gh` is absent, or the call fails for any reason. `gh` is an
# optional source — this must never fail the tool (spec §4.2 graceful
# degradation). A merged/closed PR is treated the same as "no PR" (the
# work is already integrated).
detect_open_pr() {
  local json state
  command -v gh >/dev/null 2>&1 || return 0
  json="$(gh pr view --json number,state,url 2>/dev/null)" || return 0
  state="$(printf '%s' "$json" | jq -r '.state // empty' 2>/dev/null)" || return 0
  [ "$state" = "OPEN" ] || return 0
  printf '%s' "$json"
}

# detect_mr_bead: print the id of an open merge-request-type bead, or
# nothing when beads (`bd`) is absent, unreachable (e.g. no beads
# database for this repo), there is none, or the call errors for any
# reason. `bd` is an optional source — this must never fail the tool
# (spec §4.2 graceful degradation).
detect_mr_bead() {
  local json
  command -v bd >/dev/null 2>&1 || return 0
  json="$(bd list --type=merge-request --json 2>/dev/null)" || return 0
  printf '%s' "$json" | jq -r '.data[0].id // empty' 2>/dev/null || true
}
