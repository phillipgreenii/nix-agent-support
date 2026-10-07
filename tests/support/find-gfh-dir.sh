#!/usr/bin/env bash
# find-gfh-dir.sh -- print the directory holding git-fixture-harness.bash.
#
# Used by bats tests as a LOCAL-RUN fallback only; nix check derivations export
# GFH_LIB / TEST_SUPPORT and never reach this script.
#
# The harness lives in the nix-repo-base checkout, a sibling of this repo in a
# pn-workspace. The sibling's directory name is not stable across machines
# (nix-repo-base, or the older phillipg-nix-repo-base), and the checkout may sit
# in a linked worktree, so nothing here depends on the caller's cwd or on a fixed
# relative depth.
#
# Search order (first hit wins):
#   1. $GFH_LIB (a file) or $TEST_SUPPORT (a dir), if set and valid
#   2. $PN_WORKSPACE_ROOT/<name>
#   3. <parent of this repo's canonical clone>/<name>   (via git-common-dir)
# for <name> in nix-repo-base, phillipg-nix-repo-base.
#
# Usage:   find-gfh-dir.sh
# Output:  absolute directory path on stdout
# Exit:    0 found; 1 not found (every tried path listed on stderr)

set -euo pipefail

readonly HARNESS="git-fixture-harness.bash"
readonly REL="lib/scripts"
readonly NAMES=(nix-repo-base phillipg-nix-repo-base)

tried=()

# probe <dir>: succeed (and print) when <dir> holds the harness.
probe() {
  tried+=("$1")
  if [[ -f $1/$HARNESS ]]; then
    (cd "$1" && pwd -P)
    return 0
  fi
  return 1
}

if [[ -n ${GFH_LIB:-} && -f ${GFH_LIB} ]]; then
  dirname "$GFH_LIB"
  exit 0
fi
if [[ -n ${TEST_SUPPORT:-} ]] && probe "$TEST_SUPPORT"; then
  exit 0
fi

roots=()
if [[ -n ${PN_WORKSPACE_ROOT:-} ]]; then
  roots+=("$PN_WORKSPACE_ROOT")
fi
# The canonical clone's .git is the common dir for every linked worktree, so its
# parent's parent is the workspace root regardless of where the test runs from.
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
if common="$(env -u GIT_DIR -u GIT_COMMON_DIR -u GIT_WORK_TREE \
  git -C "$script_dir" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)"; then
  roots+=("$(dirname "$(dirname "$common")")")
fi

for root in "${roots[@]}"; do
  for name in "${NAMES[@]}"; do
    if probe "$root/$name/$REL"; then
      exit 0
    fi
  done
done

{
  echo "find-gfh-dir.sh: cannot locate $HARNESS. Tried:"
  printf '  %s\n' "${tried[@]}"
  echo "Set GFH_LIB (path to the file) or PN_WORKSPACE_ROOT (workspace containing nix-repo-base)."
} >&2
exit 1
