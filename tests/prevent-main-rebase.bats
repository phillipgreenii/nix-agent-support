#!/usr/bin/env bats
# bats file_tags=type:integration
# Regression guard for the `prevent-main-rebase` pre-rebase hook's safe-case
# auto-allow (pg2-i5t0k, operator-approved design 2026-09-14 via
# /pb:unblock-human-beads). Drives the real `prevent-main-rebase-hook` binary
# built from flake.nix's shared `prevent-main-rebase-hook-script` (put on
# PATH by checks.<system>.test-prevent-main-rebase-hook), so this suite
# exercises the actual production script, never a re-implementation of it.
#
# Original behaviour (pg2-m146l): refuse a direct `git rebase` of the
# primary branch unconditionally -- bypass only with explicit operator
# authorization. New behaviour (pg2-i5t0k): auto-allow ONLY when rebasing
# "$primary" directly onto a freshly-fetched origin/"$primary", where every
# commit that would actually be rewritten (the range
# origin/"$primary".."$branch") is structurally unpublished (not yet an
# ancestor of origin/"$primary") and none of them is a merge commit.
# Everything else -- a different/stale upstream, nothing unpublished, or a
# merge commit in the range -- still falls through to the original blanket
# refusal, unchanged.
#
# type:integration (not type:unit): the hook binary this suite drives is
# built by a nix derivation (checks.<system>.test-prevent-main-rebase-hook),
# not resolvable on PATH against a plain working-tree checkout, so it MUST
# stay outside the commit-time `run-unit-tests` hook's `--labels unit` scope
# (see flake.nix's tests/pg-pr-marker.bats for the identical rationale).

if [[ -n ${GFH_LIB:-} ]]; then
  # shellcheck disable=SC1090
  source "$GFH_LIB"
else
  # shellcheck disable=SC1090
  source "$(cd "$BATS_TEST_DIRNAME/support" && pwd)/git-fixture-harness.bash"
fi

setup() {
  gfh_setup "prevent-main-rebase"

  # A real "origin" remote the hook can `git fetch`. A plain (non-bare)
  # clone of GFH_REPO is enough for a fetch-only fixture -- GFH_REPO already
  # has one commit on main from gfh_setup.
  ORIGIN="$GFH_WORK/origin"
  git clone -q "$GFH_REPO" "$ORIGIN"

  cd "$GFH_REPO" || return 1
  git remote add origin "$ORIGIN"
  git fetch -q origin
  git config pgii-integrate-branch.primaryBranch main
}

teardown() {
  gfh_teardown
}

@test "prevent-main-rebase: allows rebasing main onto a freshly-fetched origin/main when only unpublished, non-merge commits would be rewritten" {
  printf 'more\n' >>file.txt
  git commit -qam "unpublished change"

  PRE_COMMIT_PRE_REBASE_UPSTREAM="origin/main" PRE_COMMIT_PRE_REBASE_BRANCH="main" \
    run prevent-main-rebase-hook
  [ "$status" -eq 0 ]
}

@test "prevent-main-rebase: refuses when nothing is unpublished -- fail-safe on an empty range (would rewrite already-published history)" {
  # HEAD is already exactly origin/main -- no commit would actually be
  # rewritten by this rebase, so the hook must fail safe rather than treat
  # an empty range as a harmless no-op.
  PRE_COMMIT_PRE_REBASE_UPSTREAM="origin/main" PRE_COMMIT_PRE_REBASE_BRANCH="main" \
    run prevent-main-rebase-hook
  [ "$status" -eq 1 ]
  [[ "$output" == *"refusing to rebase 'main' directly"* ]]
}

@test "prevent-main-rebase: refuses when the unpublished range contains a merge commit" {
  git checkout -qb side
  printf 'side\n' >side.txt
  git add side.txt
  git commit -qm "side change"
  git checkout -q main
  git merge -q --no-ff side -m "merge side"

  PRE_COMMIT_PRE_REBASE_UPSTREAM="origin/main" PRE_COMMIT_PRE_REBASE_BRANCH="main" \
    run prevent-main-rebase-hook
  [ "$status" -eq 1 ]
}

@test "prevent-main-rebase: refuses rebasing main onto anything other than fresh origin/main" {
  printf 'more\n' >>file.txt
  git commit -qam "unpublished change"

  # "HEAD" resolves to main's own tip, not origin/main -- the general,
  # not-the-safe-case shape the original blanket refusal must still cover.
  PRE_COMMIT_PRE_REBASE_UPSTREAM="HEAD" PRE_COMMIT_PRE_REBASE_BRANCH="main" \
    run prevent-main-rebase-hook
  [ "$status" -eq 1 ]
}

@test "prevent-main-rebase: still allows rebasing a non-primary (feature) branch, unaffected by the new safe-case logic" {
  git checkout -qb feature

  PRE_COMMIT_PRE_REBASE_UPSTREAM="main" PRE_COMMIT_PRE_REBASE_BRANCH="feature" \
    run prevent-main-rebase-hook
  [ "$status" -eq 0 ]
}
