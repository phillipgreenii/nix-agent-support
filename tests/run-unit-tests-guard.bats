#!/usr/bin/env bats
# bats file_tags=type:unit
# Regression guard for scripts/run-unit-tests-guard.sh, the wrapper the
# `run-unit-tests` hook runs the unit tests through. Real git in a throwaway
# repo under BATS_TEST_TMPDIR (global rule V-3); it never touches the real
# clone because every git call here is scoped by -C/GIT_DIR to the fixture.
#
# The incident: under a git hook GIT_DIR/GIT_WORK_TREE point at the ambient
# repo, and a test that runs a bare `git init` in some directory then writes
# core.worktree into the ambient repo's config.

GUARD="$BATS_TEST_DIRNAME/../scripts/run-unit-tests-guard.sh"

# Fixture hygiene: a commit-time hook exports GIT_DIR/GIT_WORK_TREE/GIT_INDEX_FILE,
# and a bare `git -C <dir> init` then re-initialises THAT repo (writing
# core.worktree into its config) instead of creating <dir>/.git. Drop every
# inherited GIT_* before the fixture is built. Individual tests re-export the
# variables they mean to leak.
scrub_git_env() {
  local v
  for v in $(compgen -e | grep '^GIT_' || true); do
    unset "$v"
  done
}

setup() {
  scrub_git_env
  AMBIENT="$BATS_TEST_TMPDIR/ambient"
  mkdir -p "$AMBIENT" "$BATS_TEST_TMPDIR/victim"
  git -C "$AMBIENT" init -q -b main
}

# Run the guard from inside AMBIENT the way a hook does: GIT_* exported.
run_hooklike() {
  run env -C "$AMBIENT" GIT_DIR="$AMBIENT/.git" GIT_WORK_TREE="$AMBIENT" "$GUARD" "$@"
}

@test "a bare git init under leaked GIT_DIR no longer writes core.worktree into the ambient repo" {
  # Without the guard this very command sets core.worktree=<victim> in AMBIENT/.git/config.
  run env -C "$BATS_TEST_TMPDIR/victim" GIT_DIR="$AMBIENT/.git" GIT_WORK_TREE="$BATS_TEST_TMPDIR/victim" git init -q
  [ "$status" -eq 0 ]
  [ -n "$(git -C "$AMBIENT" config --local --get core.worktree)" ] # the leak is real
  git -C "$AMBIENT" config --local --unset core.worktree

  run_hooklike bash -c 'cd "$1" && git init -q' _ "$BATS_TEST_TMPDIR/victim"
  [ "$status" -eq 0 ]
  [[ "$output" == *"removing inherited GIT_DIR"* ]]
  run git -C "$AMBIENT" config --local --get core.worktree
  [ "$status" -eq 1 ]
  [ -d "$BATS_TEST_TMPDIR/victim/.git" ] # the init went to its own directory
}

@test "the command sees no GIT_* variable" {
  run_hooklike bash -c 'env | grep "^GIT_" || echo none'
  [ "$status" -eq 0 ]
  [[ "$output" == *none* ]]
}

@test "a test that writes the ambient config by path fails the run even if it passes" {
  run_hooklike git config --file "$AMBIENT/.git/config" core.worktree /somewhere
  [ "$status" -eq 97 ]
  [[ "$output" == *"changed the ambient repo's"* ]]
  [[ "$output" == *"/somewhere"* ]]
}

@test "the command's own exit status is passed through when the config is untouched" {
  run_hooklike bash -c 'exit 7'
  [ "$status" -eq 7 ]
}

@test "outside a git repo the command still runs scrubbed" {
  run env -C "$BATS_TEST_TMPDIR/victim" GIT_CEILING_DIRECTORIES="$BATS_TEST_TMPDIR" "$GUARD" true
  [ "$status" -eq 0 ]
}

@test "this suite's own fixture setup is hermetic when the hook leaks GIT_DIR and GIT_WORK_TREE" {
  # Regression for the commit-time leak: run this file's fast test under a leaked
  # GIT_DIR/GIT_WORK_TREE that name a sentinel repo; setup must not touch it.
  command -v bats >/dev/null || skip "bats not on PATH"
  local sentinel="$BATS_TEST_TMPDIR/sentinel" other="$BATS_TEST_TMPDIR/other"
  mkdir -p "$sentinel" "$other"
  git -C "$sentinel" init -q
  run env -C "$other" GIT_DIR="$sentinel/.git" GIT_WORK_TREE="$other" \
    bats --filter "own exit status is passed through" "$BATS_TEST_DIRNAME/run-unit-tests-guard.bats"
  [ "$status" -eq 0 ]
  run git -C "$sentinel" config --local --get core.worktree
  [ "$status" -eq 1 ]
}
