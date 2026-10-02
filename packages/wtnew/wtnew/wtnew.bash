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

# wtnew_link_precommit_config: guarantee a usable pre-commit config exists
# at DST ($2), sourced from SRC ($1) -- the canonical clone's
# .pre-commit-config.yaml. Prints exactly one of:
#   linked  - SRC is a symlink (this repo's normal case: a gitignored
#             nix-store symlink, absent from fresh worktrees --
#             phillipg-nix-repo-base ADR 0016). DST is symlinked to SRC's
#             *resolved* target, per this bead's own spec and this repo's
#             documented fix verbatim (CLAUDE.md "prek / pre-commit in
#             Fresh Worktrees"): `ln -s "$(readlink SRC)" DST`.
#   copied  - SRC is a plain committed file (a repo that doesn't manage the
#             config as a nix-store symlink). DST gets a literal copy so
#             such a repo still ends up with a working hook config.
#   none    - SRC does not exist. Nothing to link (matches
#             `pb drain isolate`'s linkPrecommitConfig, which also treats a
#             canonical clone with no config as a no-op).
#
# NOTE on a deliberate divergence from `pb drain isolate`: that Go
# implementation (linkPrecommitConfig) symlinks DST straight at SRC itself
# (a symlink-to-symlink) *instead of* resolving it first, specifically so a
# later `nix run .#install-pre-commit-hooks` in the canonical clone
# propagates to the worktree instead of pinning a stale hook generation.
# This function does NOT get that benefit -- it resolves the target, as
# this bead's spec and the CLAUDE.md fix both literally prescribe.
#
# RULED (operator, 2026-09-01, bead pg2-t9g3t): keep this literal
# resolve-through form; do NOT reconcile onto pb drain isolate's
# symlink-to-symlink form. The two tools are intentionally different --
# wtnew worktrees are for manual, short-lived work (re-running wtnew after
# a hook regen is an acceptable cost, and this form matches this repo's
# own documented CLAUDE.md fix verbatim), while pb drain isolate's
# worktrees are long-lived unattended drain isolation that must survive
# regens.
wtnew_link_precommit_config() {
  local src="$1" dst="$2"
  if [ -L "$src" ]; then
    ln -s "$(readlink "$src")" "$dst"
    printf 'linked'
    return
  fi
  if [ -f "$src" ]; then
    cp "$src" "$dst"
    printf 'copied'
    return
  fi
  printf 'none'
}

# wtnew_hooks_state: print the `state=` value of `pg-hooks status --porcelain`
# run inside DIR ($1) -- present|stale|missing|broken|unreachable|relocated|
# legacy -- or nothing when pg-hooks is not on PATH or prints no recognized
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
      present | stale | missing | broken | unreachable | relocated | legacy) printf '%s' "$v" ;;
      esac
      return 0
      ;;
    esac
  done <<<"$out"
}

# wtnew_should_link_precommit: succeed iff the pre-commit config link step
# applies, given STATE ($1) from wtnew_hooks_state. It applies ONLY to legacy
# repos -- and, so a machine that has not applied yet is no worse off, when
# pg-hooks is absent or reports nothing recognizable (STATE empty). Every
# other state means the repo has (or needs) a per-clone hook bundle and
# git runs its hooks from the shared common dir: nothing is written into the
# worktree (spec 7.1).
wtnew_should_link_precommit() {
  case "$1" in
  "" | legacy) return 0 ;;
  *) return 1 ;;
  esac
}

# wtnew_precommit_fact STATE [LINK_STATUS]: print the PRECOMMIT fact -- the
# bundle|stale|legacy|missing|broken vocabulary `integrate-branch-support
# --facts` defines (its precommit_state) -- from STATE ($1, wtnew_hooks_state's
# output) with the SAME mapping:
#   present -> bundle   stale|relocated -> stale   broken -> broken
#   legacy -> legacy    missing|unreachable -> missing
# With no STATE (pg-hooks absent or unrecognized) it falls back, like that
# tool, to the on-disk config: LINK_STATUS ($2, wtnew_link_precommit_config's
# output) linked|copied -> legacy, anything else -> missing. The mapping is
# duplicated rather than read back from integrate-branch-support because wtnew
# already holds the state (no second pg-hooks call, no dependence on which
# integrate-branch-support build is on PATH); keep the two in sync.
wtnew_precommit_fact() {
  case "$1" in
  present) printf 'bundle' ;;
  stale | relocated) printf 'stale' ;;
  broken) printf 'broken' ;;
  legacy) printf 'legacy' ;;
  missing | unreachable) printf 'missing' ;;
  *)
    case "${2:-}" in
    linked | copied) printf 'legacy' ;;
    *) printf 'missing' ;;
    esac
    ;;
  esac
}
