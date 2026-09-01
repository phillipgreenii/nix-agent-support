# shellcheck shell=bash
# wtdone - guarded worktree teardown (bead pg2-hpurf): the paired
# counterpart to wtnew. Given a bead id or branch name, refuses to touch
# anything if a live process is still anchored inside the associated
# worktree (lsof), then stops its fsmonitor daemon, removes the worktree,
# deletes the branch with a PLAIN `git branch -d` (never `-D` -- an unmerged
# branch is refused, never force-discarded), prunes worktree admin, and
# prints the landed sha plus the canonical clone's remaining worktrees.
#
# nix build already sources wtdone.bash ahead of this body (mkBashScript's
# hasSupportBash injection); this guard only fires for a raw `bash
# wtdone.sh` run (e.g. local bats), where nothing has sourced it yet.
if ! declare -F canonical_root >/dev/null 2>&1; then
  source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/wtdone.bash"
fi

die() {
  echo "wtdone: $1" >&2
  exit 1
}

show_help() {
  cat <<'HELP'
wtdone: Guarded worktree teardown -- the paired counterpart to wtnew

Usage: wtdone <bead-or-branch> [OPTIONS]

Given a bead id or branch name, performs, in order:
  1. Liveness guard: refuse (and list the offending PIDs/commands) if any
     live process has its working directory anchored inside the associated
     worktree.
  2. Best-effort `git fsmonitor--daemon stop` on that worktree.
  3. `git worktree remove` (refuses a dirty/untracked worktree -- git's own
     behavior; never forced).
  4. `git branch -d` (plain -- refuses an unmerged branch; never `-D`).
  5. `git worktree prune`.
  6. Prints the landed sha and the canonical clone's remaining worktrees.

If no worktree has <bead-or-branch> checked out (already removed elsewhere,
or a plain local branch that never had one), steps 1-3 are skipped and only
the branch is deleted.

Arguments:
  <bead-or-branch>  The branch name to tear down (a bead id, when this
                     workspace's convention is bead-id-as-branch-name).

Options:
  --cc <canonical-dir>  The canonical clone to operate against. Default: the
                         canonical clone resolved from the current working
                         directory (same resolution as wtnew/
                         integrate-branch-support). A session tearing down
                         its OWN worktree MUST `cd` to the canonical clone
                         first and rely on this default, or pass --cc
                         explicitly -- the liveness guard cannot protect the
                         caller from itself if the probe excludes the
                         caller's own shell.
  -h, --help            Show this help message
  -v, --version         Show version information

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

NAME=""
CC=""

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --cc)
    [[ -n ${2:-} ]] || die "--cc requires a value"
    CC="$2"
    shift 2
    ;;
  --)
    shift
    break
    ;;
  -*)
    die "unknown option: $1"
    ;;
  *)
    [[ -z $NAME ]] || die "unexpected extra argument: $1"
    NAME="$1"
    shift
    ;;
  esac
done

if [[ $# -gt 0 ]]; then
  [[ -z $NAME ]] || die "unexpected extra argument: $1"
  NAME="$1"
  shift
fi
[[ $# -eq 0 ]] || die "unexpected extra argument: $1"

[[ -n $NAME ]] || die "usage: wtdone <bead-or-branch> [--cc <canonical-dir>]"

if [[ -n $CC ]]; then
  git -C "$CC" rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "--cc is not a git repository: $CC"
  cc="$CC"
else
  git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "not inside a git repository (pass --cc explicitly, or cd into a repo first)"
  cc="$(canonical_root)"
fi

branch="$NAME"

wt="$(wtdone_find_worktree "$cc" "$branch")"

if [[ -n $wt ]]; then
  # Step 1: liveness guard. Refuse before anything else is touched.
  anchored="$(wtdone_anchored_processes "$wt")"
  if [[ -n $anchored ]]; then
    echo "wtdone: refusing to remove '$wt' -- live process(es) are anchored inside it:" >&2
    echo "$anchored" >&2
    exit 1
  fi

  # Step 2: best-effort fsmonitor stop (may be absent -- fsmonitor off, or
  # never started).
  git -C "$wt" fsmonitor--daemon stop >/dev/null 2>&1 || true
else
  echo "wtdone: no worktree has '$branch' checked out in '$cc' -- skipping the worktree steps" >&2
fi

# Captured before the branch is deleted below -- this is the sha the branch
# pointed at, i.e. what actually landed.
landed_sha="$(git -C "$cc" rev-parse --verify -q "refs/heads/$branch" 2>/dev/null || true)"

if [[ -n $wt ]]; then
  # Step 3: never forced -- a dirty/untracked worktree is refused, matching
  # this repo's existing FF-4 cleanup step and pnwf's default (non-forced)
  # cleanup path.
  rm_out="$(git -C "$cc" worktree remove "$wt" 2>&1)" || die "git worktree remove failed: $rm_out"
fi

# Step 4: plain -d only. An unmerged branch is refused here, verbatim --
# never escalated to -D.
br_out="$(git -C "$cc" branch -d "$branch" 2>&1)" || die "git branch -d failed (branch not fully merged, or not found): $br_out"

# Step 5.
git -C "$cc" worktree prune

# Step 6.
echo "wtdone: landed sha: ${landed_sha:-unknown}"
echo "wtdone: remaining worktrees:"
wtdone_remaining_worktrees "$cc"
