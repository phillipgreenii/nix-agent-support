# shellcheck shell=bash
# Core logic (canonical-root resolution, default-branch naming, pre-commit
# symlink guarantee, base-ref resolution) as testable functions, split out
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
# this bead's spec and the CLAUDE.md fix both literally prescribe. Whether
# to reconcile the two tools onto the symlink-to-symlink form is flagged
# for a human decision rather than decided here.
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
