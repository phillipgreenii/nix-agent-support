#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Script-level (subprocess) tests for wtnew's entry point: arg parsing,
# worktree/branch creation, the pre-commit symlink guarantee, and the
# facts-block output. Runs the REAL `integrate-branch-support` (must
# resolve on PATH -- a testDeps entry under nix, and already installed in
# this workspace's dev shell for a local `bats tests/` run) so the facts
# block assertions prove the two tools' output is genuinely identical, not
# just a stub shaped to match.

setup() {
  # SCRIPTS_DIR/TEST_SUPPORT: injected by nix check (raw src dir / vendored
  # harness path), or computed relative to this test file for a local
  # `bats tests/` run. MUST honor an already-set env var -- gfh_setup below
  # scrubs every exported var not on its allowlist, so both are captured
  # into plain locals BEFORE it runs and re-exported after (pg2-31f13).
  local scripts_dir_saved="${SCRIPTS_DIR:-}"
  local test_support_saved="${TEST_SUPPORT:-}"

  if [[ -n $test_support_saved ]]; then
    # shellcheck disable=SC1091
    source "$test_support_saved/git-fixture-harness.bash"
  else
    # shellcheck disable=SC1091
    source "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/git-fixture-harness.bash"
  fi

  command -v integrate-branch-support >/dev/null 2>&1 || skip "integrate-branch-support not on PATH"

  # Hermetic-by-construction git fixture (GIT_CEILING_DIRECTORIES + env
  # allowlist reset + fresh HOME + hooks disabled): see pg2-31f13/pg2-gucfd.
  # This suite's own `add_worktree` helper creates a REAL linked worktree --
  # exactly the operation pg2-67h4y's write-up shows targeting the CANONICAL
  # clone when GIT_DIR leaks from a commit-hook environment.
  gfh_setup "wtnew"

  if [[ -z $scripts_dir_saved ]]; then
    scripts_dir_saved="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  export SCRIPTS_DIR="$scripts_dir_saved"

  # Re-export TEST_SUPPORT too (also scrubbed by gfh_setup above) -- a later
  # test resolving the harness path again would otherwise lose it.
  if [[ -n $test_support_saved ]]; then
    export TEST_SUPPORT="$test_support_saved"
  fi

  BIN="${SCRIPTS_DIR}/wtnew.sh"
  TEST_DIR="$GFH_REPO"
  cd "$TEST_DIR" || return 1
}

teardown() {
  # gfh_teardown removes GFH_ROOT, which contains TEST_DIR ($GFH_REPO) -- no
  # separate rm -rf "$TEST_DIR" needed.
  gfh_teardown
  if [ -n "${WT_DIR:-}" ]; then
    rm -rf "$WT_DIR"
  fi
}

# add_worktree <branch>: create a linked worktree on <branch>, checked out
# from the main tree ($TEST_DIR) into a *separate* temp dir, then cd into
# it. Deliberately NOT nested under $TEST_DIR -- see the identical helper
# in integrate-branch-support's own bats suite for the full rationale
# (nesting would falsely dirty the canonical clone with the worktree dir
# itself as untracked content).
add_worktree() {
  WT_DIR="$(mktemp -d)"
  git -C "$TEST_DIR" worktree add -q "$WT_DIR/wt" -b "$1" >/dev/null
  cd "$WT_DIR/wt" || return 1
}

@test "--help shows usage and exits 0" {
  run bash "$BIN" --help
  [ "$status" -eq 0 ]
  [[ "$output" =~ "Usage: wtnew" ]]
}

@test "no arguments: exits non-zero with a usage message on stderr" {
  run bash "$BIN"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "usage: wtnew" ]]
}

@test "an unknown option is rejected" {
  run bash "$BIN" --bogus
  [ "$status" -ne 0 ]
  [[ "$output" =~ "unknown option" ]]
}

@test "a name containing '/' is rejected" {
  run bash "$BIN" "foo/bar"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "must not contain" ]]
}

@test "outside a git repository: exits non-zero" {
  local nogit_dir
  nogit_dir="$(mktemp -d)"
  cd "$nogit_dir" || return 1
  run bash "$BIN" some-name
  cd "$TEST_DIR" || true
  rm -rf "$nogit_dir"
  [ "$status" -ne 0 ]
}

@test "default run: creates .worktrees/<name> on a PLAIN branch (no drain/ prefix)" {
  run bash "$BIN" pg2-abcde
  [ "$status" -eq 0 ]
  [ -d "$TEST_DIR/.worktrees/pg2-abcde" ]
  [ "$(git -C "$TEST_DIR/.worktrees/pg2-abcde" symbolic-ref --short HEAD)" = "pg2-abcde" ]
}

@test "default run: base defaults to the resolved primary branch (main, no origin/config set)" {
  run bash "$BIN" pg2-abcde
  [ "$status" -eq 0 ]
  # The new branch's tip must equal main's tip (branched from main).
  [ "$(git -C "$TEST_DIR/.worktrees/pg2-abcde" rev-parse HEAD)" = "$(git -C "$TEST_DIR" rev-parse main)" ]
}

@test "default run: prints the same facts-block shape integrate-branch-support prints" {
  # NOTE: captured via command substitution (stdout only), not bats' `run`
  # -- `run` merges stdout+stderr into $output, and wtnew.sh deliberately
  # writes its own progress/diagnostic notes to stderr (git worktree add's
  # chatter, the pre-commit-config link status) so stdout carries ONLY the
  # facts-block JSON, exactly like integrate-branch-support itself. Mixing
  # stderr back in here would break the JSON parse.
  stdout="$(bash "$BIN" pg2-abcde 2>/dev/null)"
  status=$?
  [ "$status" -eq 0 ]
  echo "$stdout" | jq -e 'has("strategy") and has("reason") and has("primary_branch") and has("canonical") and has("remote") and has("open_pr") and has("mr_bead")'
  echo "$stdout" | jq -e '.primary_branch == "main"'
}

@test "facts block matches running integrate-branch-support directly inside the new worktree" {
  # Same stdout-only capture rationale as the previous test.
  stdout="$(bash "$BIN" pg2-abcde 2>/dev/null)"
  status=$?
  [ "$status" -eq 0 ]
  direct="$(cd "$TEST_DIR/.worktrees/pg2-abcde" && integrate-branch-support)"
  [ "$stdout" = "$direct" ]
}

@test "--branch overrides the default branch name" {
  run bash "$BIN" pg2-abcde --branch custom-branch
  [ "$status" -eq 0 ]
  [ "$(git -C "$TEST_DIR/.worktrees/pg2-abcde" symbolic-ref --short HEAD)" = "custom-branch" ]
}

@test "--base overrides the default base ref" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m second
  git checkout -q -b other-base
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m third
  git checkout -q main
  run bash "$BIN" pg2-abcde --base other-base
  [ "$status" -eq 0 ]
  [ "$(git -C "$TEST_DIR/.worktrees/pg2-abcde" rev-parse HEAD)" = "$(git -C "$TEST_DIR" rev-parse other-base)" ]
}

@test "pre-commit config: canonical's symlink is relinked to its resolved target, not to the canonical path itself" {
  mkdir -p "$TEST_DIR/store-target"
  touch "$TEST_DIR/store-target/config.yaml"
  ln -s "$TEST_DIR/store-target/config.yaml" "$TEST_DIR/.pre-commit-config.yaml"
  run bash "$BIN" pg2-abcde
  [ "$status" -eq 0 ]
  [ -L "$TEST_DIR/.worktrees/pg2-abcde/.pre-commit-config.yaml" ]
  [ "$(readlink "$TEST_DIR/.worktrees/pg2-abcde/.pre-commit-config.yaml")" = "$TEST_DIR/store-target/config.yaml" ]
}

@test "pre-commit config: a plain committed file is copied, not symlinked" {
  echo "repos: []" >"$TEST_DIR/.pre-commit-config.yaml"
  run bash "$BIN" pg2-abcde
  [ "$status" -eq 0 ]
  [ ! -L "$TEST_DIR/.worktrees/pg2-abcde/.pre-commit-config.yaml" ]
  diff "$TEST_DIR/.pre-commit-config.yaml" "$TEST_DIR/.worktrees/pg2-abcde/.pre-commit-config.yaml"
}

@test "pre-commit config: absent in canonical -> nothing created in the worktree, tool still succeeds" {
  run bash "$BIN" pg2-abcde
  [ "$status" -eq 0 ]
  [ ! -e "$TEST_DIR/.worktrees/pg2-abcde/.pre-commit-config.yaml" ]
}

@test "run from inside an existing linked worktree: the new worktree lands under the CANONICAL clone's .worktrees/, not nested in the current one" {
  add_worktree existing-feature
  run bash "$BIN" pg2-abcde
  [ "$status" -eq 0 ]
  [ -d "$TEST_DIR/.worktrees/pg2-abcde" ]
  [ ! -d "$WT_DIR/wt/.worktrees" ]
}
