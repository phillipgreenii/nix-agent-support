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
# Resolution order: (1) the CANONICAL clone's checked-out branch's upstream
# remote (git -C <canonical_root> rev-parse --abbrev-ref
# --symbolic-full-name @{upstream}, taking the remote portion before the
# first "/"); (2) if exactly one git remote exists, use it; (3) otherwise
# empty. Zero remotes is simply "no remote" (not ambiguous); two or more
# remotes with no upstream configured IS ambiguous (which remote would it
# be?) and reports so on line 2. Every git query is anchored to
# canonical_root so it is correct from a flake subdirectory and from inside
# a linked worktree (see the anchoring note in the body).
detect_remote() {
  local root upstream remotes count reason=""
  # Anchor every git query to the canonical clone, NOT the current working
  # directory. The tool may run from a flake SUBDIRECTORY (e.g. homelab's
  # nix/) inside a LINKED WORKTREE whose feature branch has no upstream. A
  # cwd-relative '@{upstream}' then resolves nothing and the fallback below
  # counts the repo's remotes -- with 2+ remotes that is reported as
  # "ambiguous", so remote comes back null even though the repo genuinely
  # pushes to a remote (homelab has origin+bitbucket and main tracks
  # origin/main). The canonical clone's checked-out branch is the one
  # integration lands/pushes, so ITS upstream is the remote we want; this
  # matches canonical_branch/canonical_dirty, which already anchor here.
  # git remote's output is repo-wide (shared config) so anchoring it is a
  # no-op for correctness, but kept consistent. See canonical_root.
  root="$(canonical_root)"
  upstream="$(git -C "$root" rev-parse --abbrev-ref --symbolic-full-name '@{upstream}' 2>/dev/null || true)"
  if [ -n "$upstream" ]; then
    printf '%s\n\n' "${upstream%%/*}"
    return
  fi
  remotes="$(git -C "$root" remote 2>/dev/null || true)"
  count=0
  if [ -n "$remotes" ]; then
    # Pure-bash line count via a read loop, not `grep -c`/`wc -l`: grep and
    # wc are external commands that may be absent from a scrubbed PATH, so a
    # bash builtin avoids that coreutils dependency. (This is the external-
    # tool concern, distinct from the bash-version `mapfile` rationale
    # documented at the .sh call site.)
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

# resolve_strategy: print exactly two lines — (1) the resolved strategy
# (empty for "cannot infer", i.e. JSON null); (2) a human-readable reason.
# Two lines for the same subshell-survival reason as detect_remote (this
# runs inside a `$(...)`/`< <(...)` caller).
#
# Implements the resolution order (spec §4.2/§4.3). The tool is advisory
# ONLY — it never emits ask/halt/none, and it never overrides a declared
# strategy even when that strategy looks infeasible; flagging the conflict
# in the reason is as far as the tool goes (§4.4: the agent decides):
#   1. declared `pgii-integrate-branch.strategy` wins outright. A declared
#      `pull-request` with no resolvable remote is kept as-is but flagged
#      infeasible in the reason.
#   2. else an ambiguous remote (detect_remote's line-2 reason) means the
#      tool cannot even resolve what "no remote" vs. "has a remote" means,
#      so it cannot infer either — reported as-is.
#   3. else no remote at all rules out `pull-request` outright ->
#      `ff-merge-to-main`.
#   4. else an open PR or an open merge-request bead -> `pull-request`.
#   5. else the tool cannot infer -> null (empty line 1).
resolve_strategy() {
  local declared="$1" remote="$2" remote_reason="$3" open_pr_json="$4" mr_bead="$5"

  if [ -n "$declared" ]; then
    if [ "$declared" = "pull-request" ] && [ -z "$remote" ]; then
      printf '%s\ndeclared strategy is '"'"'pull-request'"'"' but no remote could be resolved -- pull request is infeasible; the agent decides how to proceed\n' "$declared"
      return
    fi
    printf '%s\ndeclared strategy: %s (pgii-integrate-branch.strategy)\n' "$declared" "$declared"
    return
  fi

  if [ -n "$remote_reason" ]; then
    printf '\n%s\n' "$remote_reason"
    return
  fi

  if [ -z "$remote" ]; then
    printf 'ff-merge-to-main\nno remote configured -- a pull request is impossible\n'
    return
  fi

  if [ -n "$open_pr_json" ] && [ "$open_pr_json" != "null" ]; then
    printf 'pull-request\nan open pull request was found for the current branch\n'
    return
  fi

  if [ -n "$mr_bead" ]; then
    printf 'pull-request\nan open merge-request bead was found: %s\n' "$mr_bead"
    return
  fi

  printf '\nremote present, no open PR or merge-request bead, and no strategy declared -- cannot infer a strategy\n'
}
