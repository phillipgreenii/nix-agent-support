# shellcheck shell=bash
# wtnew - create a fresh git worktree for MANUAL (non-drain) work: adds the
# worktree on a new branch, reports the clone's hook-bundle state on stderr
# (git runs a bundle's hooks from the shared common dir, so nothing is written
# into the worktree), and prints the same integration-facts JSON
# block `integrate-branch-support` prints, computed from inside the new
# worktree.
#
# nix build already sources wtnew.bash ahead of this body (mkBashScript's
# hasSupportBash injection); this guard only fires for a raw `bash
# wtnew.sh` run (e.g. local bats), where nothing has sourced it yet.
if ! declare -F canonical_root >/dev/null 2>&1; then
  source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/wtnew.bash"
fi

die() {
  echo "wtnew: $1" >&2
  exit 1
}

show_help() {
  cat <<'HELP'
wtnew: Create a fresh git worktree for manual (non-drain) work

Usage: wtnew <bead-or-name> [OPTIONS]

Creates .worktrees/<bead-or-name> off a base ref on a new branch (writing no
hook config into it: git runs the clone's hook bundle from the shared common
dir), reports the hook-bundle state from `pg-hooks status --porcelain`, and
prints the same integration-facts JSON block `integrate-branch-support` prints (computed from
inside the new worktree). On stderr it also reports `PRECOMMIT=<value>`, the
facts-block vocabulary: bundle|stale|missing|broken.

Arguments:
  <bead-or-name>    Directory name under .worktrees/, and (unless --branch
                     is given) the new branch name too. A bead id or any
                     short name; must not contain '/'.

Options:
  --base <ref>      Base ref to branch from. Default: this repo's resolved
                     primary branch (pgii-integrate-branch.primaryBranch git
                     config, else origin/HEAD, else "main") -- the same
                     resolution integrate-branch-support itself uses.
  --branch <name>   Branch name. Default: <bead-or-name>, PLAIN -- NOT
                     drain/<bead-or-name>. The drain/ prefix is reserved for
                     the automated /drain-beads flow (`pb drain isolate`);
                     pass --branch drain/<name> to opt into that naming
                     manually.
  -h, --help        Show this help message
  -v, --version     Show version information

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

NAME=""
BASE=""
BRANCH=""

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --base)
    [[ -n ${2:-} ]] || die "--base requires a value"
    BASE="$2"
    shift 2
    ;;
  --branch)
    [[ -n ${2:-} ]] || die "--branch requires a value"
    BRANCH="$2"
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

[[ -n $NAME ]] || die "usage: wtnew <bead-or-name> [--base <ref>] [--branch <name>]"
[[ $NAME != */* ]] || die "<bead-or-name> must not contain '/': $NAME"

if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  die "not inside a git repository"
fi

root="$(canonical_root)"
branch="${BRANCH:-$(wtnew_default_branch "$NAME")}"

if [[ -n $BASE ]]; then
  base="$BASE"
else
  base="$(wtnew_resolve_base "$root")"
fi

wt="$root/.worktrees/$NAME"

# git worktree add's own progress chatter is informational, not part of
# this tool's data output -- routed to stderr so stdout stays exactly the
# facts-block JSON printed at the end (matching integrate-branch-support's
# own discipline: stdout carries only that one JSON object). Checked
# explicitly rather than relying on the nix-build-injected `set -e` (a raw
# local `bash wtnew.sh` run, e.g. under bats, has no strict mode).
if ! git -C "$root" worktree add "$wt" -b "$branch" "$base" >&2; then
  die "git worktree add failed"
fi

hooks_state="$(wtnew_hooks_state "$wt")"
echo "wtnew: PRECOMMIT=$(wtnew_precommit_fact "$hooks_state")" >&2

(cd "$wt" && integrate-branch-support)
