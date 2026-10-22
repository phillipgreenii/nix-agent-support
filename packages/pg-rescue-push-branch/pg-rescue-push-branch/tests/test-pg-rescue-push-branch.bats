#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Script-level tests for pg-rescue-push-branch (docket pg2-q6a96, design D5):
# the real git against a throwaway fixture (a bare remote, a clone as the
# "main working tree", a linked worktree holding the rebased PR branch).

setup() {
  # Save what the suite needs across gfh_setup (it rebuilds the environment
  # from an allowlist), then restore it; see the pg-rescue-flake-lock-conflict
  # suite for the same wiring.
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  if [[ -z ${GFH_LIB:-} ]]; then
    echo "GFH_LIB is not set: run under the workspace .envrc (direnv) or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # shellcheck disable=SC1090,SC1091
  source "$GFH_LIB"
  gfh_save_env SCRIPTS_DIR SCRIPT_UNDER_TEST TEST_SUPPORT GFH_LIB
  gfh_setup "pg-rescue-push-branch"
  gfh_restore_env

  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    PUSH=("$SCRIPT_UNDER_TEST")
  else
    PUSH=(bash -euo pipefail "$SCRIPTS_DIR/pg-rescue-push-branch.sh")
  fi

  TEST_DIR="$GFH_WORK"
  REMOTE="$TEST_DIR/remote.git"
  MAIN_WT="$TEST_DIR/main-wt"
  LINKED="$TEST_DIR/linked-wt"
  OTHER="$TEST_DIR/other-clone"

  # Remote with main and a pr branch (seed -> pr1).
  gfh_init_bare "$REMOTE"
  gfh_clone "$REMOTE" "$MAIN_WT" "pg-rescue-push-branch"
  echo seed >"$MAIN_WT/f"
  git -C "$MAIN_WT" add f
  git -C "$MAIN_WT" commit -q -m seed
  git -C "$MAIN_WT" push -q origin main
  git -C "$MAIN_WT" checkout -q -b pr
  echo pr1 >"$MAIN_WT/f"
  git -C "$MAIN_WT" commit -q -am pr1
  git -C "$MAIN_WT" push -q origin pr
  git -C "$MAIN_WT" checkout -q main

  OLD_SHA=$(git -C "$REMOTE" rev-parse refs/heads/pr)
  BASE_SHA=$(git -C "$REMOTE" rev-parse refs/heads/main)

  # The role's linked worktree: the PR branch rebased/amended locally.
  git -C "$MAIN_WT" worktree add -q -b pr-local "$LINKED" pr
  echo rebased >"$LINKED/f"
  git -C "$LINKED" commit -q -am rebased
  LOCAL_SHA=$(git -C "$LINKED" rev-parse HEAD)
}

teardown() {
  gfh_teardown
}

remote_ref() { git -C "$REMOTE" rev-parse --verify -q "refs/heads/$1" || echo none; }

run_push() { # ARGS... ; defaults supplied by the caller
  run "${PUSH[@]}" "$@"
}

assert_remote_unchanged() {
  [ "$(remote_ref pr)" = "$OLD_SHA" ]
  [ "$(remote_ref main)" = "$BASE_SHA" ]
}

@test "--help prints usage on stdout and exits 0" {
  run "${PUSH[@]}" --help
  [ "$status" -eq 0 ]
  [[ $output == *"Usage: pg-rescue-push-branch"* ]]
}

@test "a push from the linked worktree lands the rebased branch and leaves base alone" {
  run_push --worktree "$LINKED" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ "$(remote_ref pr)" = "$LOCAL_SHA" ]
  [ "$(remote_ref main)" = "$BASE_SHA" ]
}

@test "stdout stays empty on success" {
  out=$("${PUSH[@]}" --worktree "$LINKED" --branch pr --base main --expected-sha "$OLD_SHA" 2>/dev/null)
  [ -z "$out" ]
}

@test "a remote branch that moved since --expected-sha exits 3 and keeps the other writer's commit" {
  gfh_clone "$REMOTE" "$OTHER" "pg-rescue-push-branch"
  git -C "$OTHER" checkout -q pr
  echo theirs >"$OTHER/g"
  git -C "$OTHER" add g
  git -C "$OTHER" commit -q -m theirs
  git -C "$OTHER" push -q origin pr
  moved=$(remote_ref pr)
  [ "$moved" != "$OLD_SHA" ]

  run_push --worktree "$LINKED" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 3 ]
  [[ $output == *"lease rejected"* ]]
  [ "$(remote_ref pr)" = "$moved" ]
  [ "$(remote_ref main)" = "$BASE_SHA" ]
}

@test "a remote branch that does not exist exits 3 and creates nothing" {
  git -C "$LINKED" checkout -q -b ghost
  run_push --worktree "$LINKED" --branch ghost --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 3 ]
  [ "$(remote_ref ghost)" = none ]
  assert_remote_unchanged
}

@test "the pushed ref is the branch's, not the local branch name" {
  # The linked worktree's local branch is pr-local; the push targets pr.
  run_push --worktree "$LINKED" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 0 ]
  [ "$(remote_ref pr-local)" = none ]
}

@test "a push from the main working tree exits 2 and leaves the remote unchanged" {
  git -C "$MAIN_WT" checkout -q pr
  echo local >"$MAIN_WT/f"
  git -C "$MAIN_WT" commit -q -am local
  run_push --worktree "$MAIN_WT" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  [[ $output == *"main working tree"* ]]
  assert_remote_unchanged
}

@test "a directory that is not a git working tree exits 2" {
  mkdir -p "$TEST_DIR/plain"
  run_push --worktree "$TEST_DIR/plain" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  assert_remote_unchanged
}

@test "--branch equal to --base exits 2 and the remote base is untouched" {
  run_push --worktree "$LINKED" --branch main --base main --expected-sha "$BASE_SHA"
  [ "$status" -eq 2 ]
  assert_remote_unchanged
}

@test "an unsafe branch name exits 2 and the remote is untouched" {
  for bad in "-f" "--force" "a..b" "a b" "@{-1}" "x.lock" "/lead" "trail/" "HEAD" 'a~b' 'a^b' 'a:b' 'a?b' 'a*b' 'a[b' 'a\b'; do
    run_push --worktree "$LINKED" --branch "$bad" --base main --expected-sha "$OLD_SHA"
    [ "$status" -eq 2 ] || { echo "branch '$bad' exited $status: $output" >&2; return 1; }
  done
  assert_remote_unchanged
}

@test "a malformed --expected-sha exits 2 and the remote is untouched" {
  for bad in "" "abc" "${OLD_SHA:0:39}" "${OLD_SHA}0" "${OLD_SHA:0:39}g" "origin/pr" "HEAD"; do
    run_push --worktree "$LINKED" --branch pr --base main --expected-sha "$bad"
    [ "$status" -eq 2 ] || { echo "sha '$bad' exited $status: $output" >&2; return 1; }
  done
  assert_remote_unchanged
}

@test "an unknown option exits 2 and the remote is untouched" {
  run_push --worktree "$LINKED" --branch pr --base main --expected-sha "$OLD_SHA" --force
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --branch pr --base main --expected-sha "$OLD_SHA" --refspec HEAD:main
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --branch pr --base main --expected-sha "$OLD_SHA" extra
  [ "$status" -eq 2 ]
  assert_remote_unchanged
}

@test "a repeated option exits 2 and the remote is untouched" {
  run_push --worktree "$LINKED" --worktree "$LINKED" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --branch pr --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --branch pr --base main --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --branch pr --base main --expected-sha "$OLD_SHA" --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --branch pr --base main --expected-sha "$OLD_SHA" --remote origin --remote origin
  [ "$status" -eq 2 ]
  assert_remote_unchanged
}

@test "a missing option, or an option missing its value, exits 2" {
  run_push --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --branch pr --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --branch pr --base main
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --branch pr --base main --expected-sha
  [ "$status" -eq 2 ]
  assert_remote_unchanged
}

@test "a --remote starting with a dash exits 2" {
  run_push --worktree "$LINKED" --branch pr --base main --expected-sha "$OLD_SHA" --remote --force
  [ "$status" -eq 2 ]
  assert_remote_unchanged
}

@test "--remote NAME selects another remote" {
  git -C "$LINKED" remote add upstream "$REMOTE"
  run_push --worktree "$LINKED" --branch pr --base main --expected-sha "$OLD_SHA" --remote upstream
  [ "$status" -eq 0 ]
  [ "$(remote_ref pr)" = "$LOCAL_SHA" ]
}

@test "an unreachable remote is a plain failure (exit 1), not a lease rejection" {
  run_push --worktree "$LINKED" --branch pr --base main --expected-sha "$OLD_SHA" --remote nosuchremote
  [ "$status" -eq 1 ]
  [[ $output == *"push failed"* ]]
  assert_remote_unchanged
}
