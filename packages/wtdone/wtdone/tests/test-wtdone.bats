#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Script-level (subprocess) tests for wtdone's entry point: arg parsing, the
# lsof liveness guard, the worktree-remove/branch--d/prune sequence, and the
# no-worktree-found degradation. Real `git` and `lsof` (must resolve on PATH
# -- testDeps entries under nix, ordinary system tools for a local `bats
# tests/` run).

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

  command -v lsof >/dev/null 2>&1 || skip "lsof not on PATH"

  # Hermetic-by-construction git fixture (GIT_CEILING_DIRECTORIES + env
  # allowlist reset + fresh HOME + hooks disabled): see pg2-31f13/pg2-gucfd.
  # This suite's own `add_worktree` helper creates a REAL linked worktree --
  # exactly the operation pg2-67h4y's write-up shows targeting the CANONICAL
  # clone when GIT_DIR leaks from a commit-hook environment.
  gfh_setup "wtdone"

  if [[ -z $scripts_dir_saved ]]; then
    scripts_dir_saved="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  export SCRIPTS_DIR="$scripts_dir_saved"

  # Re-export TEST_SUPPORT too (also scrubbed by gfh_setup above) -- a later
  # test resolving the harness path again would otherwise lose it.
  if [[ -n $test_support_saved ]]; then
    export TEST_SUPPORT="$test_support_saved"
  fi

  BIN="${SCRIPTS_DIR}/wtdone.sh"
  TEST_DIR="$GFH_REPO"
  cd "$TEST_DIR" || return 1
}

teardown() {
  # Best-effort: kill the liveness-guard fixture's anchor process, if a test
  # started one (ANCHOR_PID). Never fatal -- the process may have already
  # been killed inside the test itself.
  if [[ -n ${ANCHOR_PID:-} ]]; then
    kill "$ANCHOR_PID" 2>/dev/null || true
    wait "$ANCHOR_PID" 2>/dev/null || true
  fi
  # gfh_teardown removes GFH_ROOT, which contains TEST_DIR ($GFH_REPO) -- no
  # separate rm -rf "$TEST_DIR" needed.
  gfh_teardown
  if [ -n "${WT_DIR:-}" ]; then
    rm -rf "$WT_DIR"
  fi
}

# add_worktree <branch>: create a linked worktree on <branch>, checked out
# from the main tree ($TEST_DIR) into a *separate* temp dir at
# "$WT_DIR/wt". Deliberately NOT nested under $TEST_DIR -- see the identical
# helper in wtnew's own bats suite for the full rationale (nesting would
# falsely dirty the canonical clone with the worktree dir itself as
# untracked content).
add_worktree() {
  WT_DIR="$(mktemp -d)"
  git -C "$TEST_DIR" worktree add -q "$WT_DIR/wt" -b "$1" >/dev/null
}

@test "--help shows usage and exits 0" {
  run bash "$BIN" --help
  [ "$status" -eq 0 ]
  [[ "$output" =~ "Usage: wtdone" ]]
}

@test "no arguments: exits non-zero with a usage message on stderr" {
  run bash "$BIN"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "usage: wtdone" ]]
}

@test "an unknown option is rejected" {
  run bash "$BIN" --bogus
  [ "$status" -ne 0 ]
  [[ "$output" =~ "unknown option" ]]
}

@test "outside a git repository, no --cc given: exits non-zero" {
  local nogit_dir
  nogit_dir="$(mktemp -d)"
  cd "$nogit_dir" || return 1
  run bash "$BIN" some-branch
  cd "$TEST_DIR" || true
  rm -rf "$nogit_dir"
  [ "$status" -ne 0 ]
}

@test "--cc pointing at a non-git directory: exits non-zero" {
  local not_a_repo
  not_a_repo="$(mktemp -d)"
  run bash "$BIN" some-branch --cc "$not_a_repo"
  rm -rf "$not_a_repo"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "not a git repository" ]]
}

@test "a nonexistent branch (and no worktree): exits non-zero" {
  run bash "$BIN" does-not-exist
  [ "$status" -ne 0 ]
}

@test "landed worktree + branch: removes both, prints the landed sha and remaining worktrees, exits 0" {
  add_worktree feature
  echo second >"$WT_DIR/wt/file2.txt"
  git -C "$WT_DIR/wt" add file2.txt
  git -c user.email=t@t -c user.name=t -C "$WT_DIR/wt" commit -q -m second
  local sha
  sha="$(git -C "$TEST_DIR" rev-parse feature)"
  git -C "$TEST_DIR" merge -q --ff-only feature

  run bash "$BIN" feature
  [ "$status" -eq 0 ]
  [[ "$output" == *"landed sha: $sha"* ]]
  [[ "$output" == *"remaining worktrees:"* ]]
  [ ! -e "$WT_DIR/wt" ]
  run git -C "$TEST_DIR" rev-parse --verify --quiet refs/heads/feature
  [ "$status" -ne 0 ]
}

@test "branch with no worktree: skips the worktree steps, still deletes the (already-merged) branch" {
  git -C "$TEST_DIR" branch feature
  run bash "$BIN" feature
  [ "$status" -eq 0 ]
  [[ "$output" =~ "no worktree has" ]]
  run git -C "$TEST_DIR" rev-parse --verify --quiet refs/heads/feature
  [ "$status" -ne 0 ]
}

@test "refuse-unmerged: an unmerged branch is refused via plain -d (never -D), but the worktree is still removed" {
  add_worktree feature
  echo second >"$WT_DIR/wt/file2.txt"
  git -C "$WT_DIR/wt" add file2.txt
  git -c user.email=t@t -c user.name=t -C "$WT_DIR/wt" commit -q -m second
  # Deliberately NOT merged into TEST_DIR's main -- feature stays ahead.

  run bash "$BIN" feature
  [ "$status" -ne 0 ]
  [[ "$output" =~ "not fully merged" ]]
  # The worktree step runs BEFORE the branch step (bead pg2-hpurf's fixed
  # order), so it is already gone even though the branch delete refused.
  [ ! -e "$WT_DIR/wt" ]
  run git -C "$TEST_DIR" rev-parse --verify --quiet refs/heads/feature
  [ "$status" -eq 0 ]
}

@test "refuse-when-anchored: a live process cwd'd inside the worktree blocks removal, PID listed, nothing touched" {
  add_worktree feature
  git -C "$TEST_DIR" merge -q --ff-only feature

  bash -c 'cd "$1" && exec sleep 60' _ "$WT_DIR/wt" &
  ANCHOR_PID=$!

  # Bounded poll: give the child a moment to actually chdir+exec before
  # asserting lsof can see it (not an open-ended wait -- 10 attempts, 0.2s
  # apart, ~2s worst case).
  local _
  for _ in $(seq 1 10); do
    [[ -n "$(lsof -a -d cwd +D "$WT_DIR/wt" 2>/dev/null)" ]] && break
    sleep 0.2
  done

  run bash "$BIN" feature
  kill "$ANCHOR_PID" 2>/dev/null || true
  wait "$ANCHOR_PID" 2>/dev/null || true
  ANCHOR_PID=""

  [ "$status" -ne 0 ]
  [[ "$output" =~ "anchored" ]]
  [ -e "$WT_DIR/wt" ]
  run git -C "$TEST_DIR" rev-parse --verify --quiet refs/heads/feature
  [ "$status" -eq 0 ]
}

@test "a dirty (untracked) worktree is refused by git worktree remove itself -- never forced" {
  add_worktree feature
  echo dirty >"$WT_DIR/wt/untracked.txt"

  run bash "$BIN" feature
  [ "$status" -ne 0 ]
  [ -e "$WT_DIR/wt" ]
  run git -C "$TEST_DIR" rev-parse --verify --quiet refs/heads/feature
  [ "$status" -eq 0 ]
}

@test "--cc targets a canonical clone other than the current directory" {
  add_worktree feature
  git -C "$TEST_DIR" merge -q --ff-only feature

  # Run from somewhere that is neither TEST_DIR nor inside any git repo, to
  # prove --cc (not cwd resolution) drives the operation.
  cd "$WT_DIR" || return 1
  run bash "$BIN" feature --cc "$TEST_DIR"
  [ "$status" -eq 0 ]
  [ ! -e "$WT_DIR/wt" ]
  run git -C "$TEST_DIR" rev-parse --verify --quiet refs/heads/feature
  [ "$status" -ne 0 ]
}
