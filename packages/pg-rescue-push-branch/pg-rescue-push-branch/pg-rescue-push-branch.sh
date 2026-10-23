# shellcheck shell=bash
# pg-rescue-push-branch - the single audited place from which the pg-rescue
# script roles push a rebased PR branch (docket pg2-q6a96, design D5; operator
# ruling Phillip, 2026-10-22): `--force-with-lease` only, written as the full
# ref and pinned to the item's head sha, only the PR branch, always origin,
# only from a linked worktree strictly inside a given root, never the base.
#
# Generic by design: names no employer and reads no configuration; every value
# arrives as an argument. stdout stays empty; one diagnostic line on stderr per
# refusal or failure.

show_help() {
  cat <<'HELP'
pg-rescue-push-branch: Push a rebased PR branch with a pinned force-with-lease

Usage: pg-rescue-push-branch --worktree DIR --worktree-root ROOT
                             --branch BRANCH --base BASE --expected-sha SHA

Runs, from DIR:

  git push --force-with-lease=refs/heads/BRANCH:SHA origin HEAD:refs/heads/BRANCH

The remote is always origin. The lease names the full ref (a tag with the
branch's name is never matched) and is the explicit form, so git compares the
remote's actual value for BRANCH with SHA at push time (no fetch). A remote
branch that moved, or does not exist, is a rejected lease. The helper never
fetches: origin/BASE is the remote-tracking ref the caller already fetched.

Refuses (exit 2, nothing pushed) when DIR or ROOT is not an existing
directory, when the real path of DIR is not strictly inside the real path of
ROOT, when DIR is not a linked git worktree (the main working tree is never
pushed from), when BRANCH equals BASE, when BRANCH is not a plain branch name,
when SHA is not a full 40-character hexadecimal object name, when a rebase is
in progress in DIR, when origin/BASE does not exist or is not an ancestor of
HEAD, or on a missing, repeated or unknown option (--remote is unknown). There
is no refspec, --force or other pass-through to git push. Every git call runs
as git -C DIR with GIT_DIR, GIT_WORK_TREE and the other repository-location
variables unset.

Options:
  --worktree DIR       The linked worktree to push from (required)
  --worktree-root ROOT Directory DIR must be strictly inside (required)
  --branch BRANCH      The PR branch to push (required)
  --base BASE          The base branch, which is never pushed (required)
  --expected-sha SHA   The remote branch's expected head, full 40 hex (required)
  -h, --help           Show this help message
  -v, --version        Show version information

Exit codes:
  0  pushed
  3  lease rejected (the remote branch moved or does not exist); not retried
  2  usage error or refusal
  1  any other failure

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

# refuse MSG: one diagnostic line, exit 2 (usage error or refusal).
refuse() {
  echo "pg-rescue-push-branch: $1" >&2
  exit 2
}

# An inherited repository-location environment must never redirect a git call
# of this helper to another repository.
unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE GIT_OBJECT_DIRECTORY \
  GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_NAMESPACE

worktree="" worktree_root="" branch="" base="" expected_sha=""
have_worktree=0 have_root=0 have_branch=0 have_base=0 have_sha=0

# need_value OPT COUNT: an option must carry a value.
need_value() { [[ $2 -ge 2 ]] || refuse "option $1 needs a value (see --help)"; }

while [[ $# -gt 0 ]]; do
  case $1 in
  -h | --help)
    show_help
    exit 0
    ;;
  --worktree)
    need_value "$1" $#
    [[ $have_worktree -eq 0 ]] || refuse "option --worktree given more than once"
    have_worktree=1 worktree=$2
    shift
    ;;
  --worktree-root)
    need_value "$1" $#
    [[ $have_root -eq 0 ]] || refuse "option --worktree-root given more than once"
    have_root=1 worktree_root=$2
    shift
    ;;
  --branch)
    need_value "$1" $#
    [[ $have_branch -eq 0 ]] || refuse "option --branch given more than once"
    have_branch=1 branch=$2
    shift
    ;;
  --base)
    need_value "$1" $#
    [[ $have_base -eq 0 ]] || refuse "option --base given more than once"
    have_base=1 base=$2
    shift
    ;;
  --expected-sha)
    need_value "$1" $#
    [[ $have_sha -eq 0 ]] || refuse "option --expected-sha given more than once"
    have_sha=1 expected_sha=$2
    shift
    ;;
  *) refuse "unexpected argument: $1 (see --help)" ;;
  esac
  shift
done

[[ $have_worktree -eq 1 && -n $worktree ]] || refuse "--worktree is required"
[[ $have_root -eq 1 && -n $worktree_root ]] || refuse "--worktree-root is required"
[[ $have_branch -eq 1 && -n $branch ]] || refuse "--branch is required"
[[ $have_base -eq 1 && -n $base ]] || refuse "--base is required"
[[ $have_sha -eq 1 ]] || refuse "--expected-sha is required"

[[ $expected_sha =~ ^[0-9a-fA-F]{40}$ ]] ||
  refuse "--expected-sha must be a full 40-character hexadecimal object name"

[[ $branch != -* ]] || refuse "--branch is not a plain branch name: $branch"
[[ $branch != "$base" ]] || refuse "--branch equals --base ($base); the base branch is never pushed"

[[ -d $worktree ]] || refuse "--worktree is not a directory: $worktree"
[[ -d $worktree_root ]] || refuse "--worktree-root is not a directory: $worktree_root"

# Containment: the real path of the worktree must be strictly inside the real
# path of the root (equal is a refusal).
wt_real=$(cd -- "$worktree" && pwd -P) || refuse "cannot resolve --worktree: $worktree"
root_real=$(cd -- "$worktree_root" && pwd -P) || refuse "cannot resolve --worktree-root: $worktree_root"
root_prefix=${root_real%/}/
[[ $wt_real == "$root_prefix"* && $wt_real != "$root_prefix" ]] ||
  refuse "--worktree ($wt_real) is not strictly inside --worktree-root ($root_real)"

# Only a LINKED worktree: its git dir differs from the common dir. The main
# working tree (git dir == common dir) is never pushed from.
git_dir=$(git -C "$worktree" rev-parse --absolute-git-dir 2>/dev/null) ||
  refuse "--worktree is not a git working tree: $worktree"
common_dir=$(git -C "$worktree" rev-parse --path-format=absolute --git-common-dir 2>/dev/null) ||
  refuse "cannot resolve the git common dir of $worktree"
[[ $git_dir != "$common_dir" ]] ||
  refuse "--worktree is the main working tree, not a linked worktree: $worktree"

# A rebase in progress means HEAD is a half-built state: never pushed.
for rebase_dir in rebase-merge rebase-apply; do
  rebase_path=$(git -C "$worktree" rev-parse --git-path "$rebase_dir" 2>/dev/null) ||
    refuse "cannot resolve the git dir of $worktree"
  [[ $rebase_path == /* ]] || rebase_path=$worktree/$rebase_path
  [[ ! -e $rebase_path ]] || refuse "a rebase is in progress in $worktree"
done

# A plain branch name: git must accept it AND `--branch` expansion (@{-1} and
# friends) must leave it unchanged.
normalized=$(git -C "$worktree" check-ref-format --branch "$branch" 2>/dev/null) ||
  refuse "--branch is not a valid branch name: $branch"
[[ $normalized == "$branch" ]] || refuse "--branch is not a plain branch name: $branch"

# Ancestry: HEAD must descend from (or equal) the remote-tracking base the
# caller fetched; this helper never fetches.
git -C "$worktree" rev-parse --verify -q "refs/remotes/origin/$base^{commit}" >/dev/null 2>&1 ||
  refuse "origin/$base does not exist in $worktree (the caller must fetch it first)"
git -C "$worktree" merge-base --is-ancestor "refs/remotes/origin/$base" HEAD ||
  refuse "HEAD does not descend from origin/$base"

# The only push this tool ever makes. LC_ALL=C so the lease-rejection text is
# stable; both streams are captured so stdout stays empty.
rc=0
out=$(LC_ALL=C git -C "$worktree" push \
  "--force-with-lease=refs/heads/$branch:$expected_sha" \
  origin "HEAD:refs/heads/$branch" 2>&1) || rc=$?

if [[ $rc -eq 0 ]]; then
  exit 0
fi

# git marks a rejected explicit lease as "[rejected] (stale info)"; that text
# is the match (tested against a real moved remote).
if grep -qF '(stale info)' <<<"$out"; then
  echo "pg-rescue-push-branch: lease rejected: origin/$branch moved or does not exist (expected $expected_sha); not retried" >&2
  exit 3
fi

detail=$(grep -v '^[[:space:]]*$' <<<"$out" | tail -n 1)
echo "pg-rescue-push-branch: push failed (git exit $rc): ${detail:-no output}" >&2
exit 1
