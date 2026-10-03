# shellcheck shell=bash
# Core logic (canonical-root resolution, default-branch naming, pre-commit
# link-for-legacy-repos, base-ref resolution) as testable functions, split out
# of wtnew.sh. No top-level code -- mkBashScript sources this file before
# the .sh body.

# canonical_root: print the absolute path to the MAIN working tree, i.e. the
# canonical clone, resolved even when wtnew is invoked from inside an
# existing linked worktree. Duplicated verbatim from
# integrate-branch-support.bash's function of the same name -- this repo has
# no shared git-helpers library yet to depend on instead; keep the two in
# sync if one is ever extracted. See that file's doc comment for the full
# rationale, including the --separate-git-dir caveat.
canonical_root() {
  local common_dir
  common_dir="$(git rev-parse --git-common-dir)"
  git -C "$(dirname "$common_dir")" rev-parse --show-toplevel
}

# wtnew_default_branch: print the default branch name for a new worktree
# named NAME ($1), used whenever --branch is not given.
#
# Deliberately the PLAIN name, not "drain/<name>": drain/<id> is the
# managed-worktree convention owned by `pb drain isolate`
# (packages/pb/internal/drain/isolate.go) for the automated /drain-beads
# flow. wtnew exists to fill the gap for MANUAL (non-drain) worktree
# creation -- defaulting to the same prefix would make a manually created
# worktree indistinguishable from one the drain queue is actively managing.
# Pass `--branch drain/<name>` explicitly to opt into that convention
# anyway (e.g. to hand a manually started worktree off to the drain flow).
wtnew_default_branch() {
  printf '%s' "$1"
}

# wtnew_resolve_base: print the default --base ref, used whenever --base is
# not given. Resolved by asking integrate-branch-support (run from ROOT,
# $1) for its `primary_branch` field, reusing ITS primary-branch resolution
# (pgii-integrate-branch.primaryBranch git config -> origin/HEAD -> "main")
# rather than re-implementing the same three-step fallback a second time --
# the two tools can then never drift apart on what a repo's primary branch
# is.
wtnew_resolve_base() {
  local root="$1"
  (cd "$root" && integrate-branch-support) | jq -r '.primary_branch'
}

# wtnew_hooks_state: print the `state=` value of `pg-hooks status --porcelain`
# run inside DIR ($1) -- present|stale|missing|broken|unreachable|relocated
# -- or nothing when pg-hooks is not on PATH or prints no recognized
# state. The exit code is ignored on purpose: status encodes the state in its
# exit code too (14 stale, 13 missing, ...), so non-zero is expected; callers
# parse the porcelain, never prose (spec 5.3).
wtnew_hooks_state() {
  local dir="$1" out line v
  command -v pg-hooks >/dev/null 2>&1 || return 0
  out="$(cd "$dir" && pg-hooks status --porcelain 2>/dev/null)" || true
  while IFS= read -r line; do
    case "$line" in
    state=*)
      v="${line#state=}"
      case "$v" in
      present | stale | missing | broken | unreachable | relocated) printf '%s' "$v" ;;
      esac
      return 0
      ;;
    esac
  done <<<"$out"
}

# wtnew_precommit_fact STATE: print the PRECOMMIT fact -- the
# bundle|stale|missing|broken vocabulary `integrate-branch-support --facts`
# defines (its precommit_state) -- from STATE ($1, wtnew_hooks_state's output)
# with the SAME mapping:
#   present -> bundle   stale|relocated -> stale   broken -> broken
#   missing|unreachable -> missing
# Anything else (no STATE because pg-hooks is absent, an unrecognized state, or
# a retired one such as an old pg-hooks's `legacy`) is missing. The mapping is
# duplicated rather than read back from integrate-branch-support because wtnew
# already holds the state (no second pg-hooks call, no dependence on which
# integrate-branch-support build is on PATH); keep the two in sync.
wtnew_precommit_fact() {
  case "$1" in
  present) printf 'bundle' ;;
  stale | relocated) printf 'stale' ;;
  broken) printf 'broken' ;;
  *) printf 'missing' ;;
  esac
}
