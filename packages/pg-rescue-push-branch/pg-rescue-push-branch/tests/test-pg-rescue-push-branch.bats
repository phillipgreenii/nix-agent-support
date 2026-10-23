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
  ROOT="$TEST_DIR/wt-root"
  LINKED="$ROOT/linked-wt"
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

  mkdir -p "$ROOT"
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
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ "$(remote_ref pr)" = "$LOCAL_SHA" ]
  [ "$(remote_ref main)" = "$BASE_SHA" ]
}

@test "stdout stays empty on success" {
  out=$("${PUSH[@]}" --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA" 2>/dev/null)
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

  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 3 ]
  [[ $output == *"lease rejected"* ]]
  [ "$(remote_ref pr)" = "$moved" ]
  [ "$(remote_ref main)" = "$BASE_SHA" ]
}

@test "a remote branch that does not exist exits 3 and creates nothing" {
  git -C "$LINKED" checkout -q -b ghost
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch ghost --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 3 ]
  [ "$(remote_ref ghost)" = none ]
  assert_remote_unchanged
}

@test "the pushed ref is the branch's, not the local branch name" {
  # The linked worktree's local branch is pr-local; the push targets pr.
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 0 ]
  [ "$(remote_ref pr-local)" = none ]
}

@test "a push from the main working tree exits 2 and leaves the remote unchanged" {
  git -C "$MAIN_WT" checkout -q pr
  echo local >"$MAIN_WT/f"
  git -C "$MAIN_WT" commit -q -am local
  run_push --worktree "$MAIN_WT" --worktree-root "$TEST_DIR" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  [[ $output == *"main working tree"* ]]
  assert_remote_unchanged
}

@test "a directory that is not a git working tree exits 2" {
  mkdir -p "$ROOT/plain"
  run_push --worktree "$ROOT/plain" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  assert_remote_unchanged
}

@test "--branch equal to --base exits 2 and the remote base is untouched" {
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch main --base main --expected-sha "$BASE_SHA"
  [ "$status" -eq 2 ]
  assert_remote_unchanged
}

@test "an unsafe branch name exits 2 and the remote is untouched" {
  for bad in "-f" "--force" "a..b" "a b" "@{-1}" "x.lock" "/lead" "trail/" "HEAD" 'a~b' 'a^b' 'a:b' 'a?b' 'a*b' 'a[b' 'a\b'; do
    run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch "$bad" --base main --expected-sha "$OLD_SHA"
    [ "$status" -eq 2 ] || { echo "branch '$bad' exited $status: $output" >&2; return 1; }
  done
  assert_remote_unchanged
}

@test "a malformed --expected-sha exits 2 and the remote is untouched" {
  for bad in "" "abc" "${OLD_SHA:0:39}" "${OLD_SHA}0" "${OLD_SHA:0:39}g" "origin/pr" "HEAD"; do
    run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$bad"
    [ "$status" -eq 2 ] || { echo "sha '$bad' exited $status: $output" >&2; return 1; }
  done
  assert_remote_unchanged
}

@test "an unknown option exits 2 and the remote is untouched" {
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA" --force
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA" --refspec HEAD:main
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA" extra
  [ "$status" -eq 2 ]
  assert_remote_unchanged
}

@test "a repeated option exits 2 and the remote is untouched" {
  run_push --worktree "$LINKED" --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA" --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  assert_remote_unchanged
}

@test "a missing option, or an option missing its value, exits 2" {
  run_push --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha
  [ "$status" -eq 2 ]
  assert_remote_unchanged
}

@test "--remote is an unknown option (exit 2), however it is spelled" {
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA" --remote origin
  [ "$status" -eq 2 ]
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA" --remote --force
  [ "$status" -eq 2 ]
  assert_remote_unchanged
}

@test "the lease names the full ref: a tag with the branch's name does not make a stale lease succeed" {
  # A tag named like the branch, created by pushing an object to refs/tags/pr.
  git -C "$MAIN_WT" push -q origin "$LOCAL_SHA:refs/tags/pr"
  [ "$(git -C "$REMOTE" rev-parse refs/tags/pr)" = "$LOCAL_SHA" ]
  # A stale lease (the remote branch is not at this sha) is still rejected.
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$LOCAL_SHA"
  [ "$status" -eq 3 ]
  [ "$(remote_ref pr)" = "$OLD_SHA" ]
  [ "$(git -C "$REMOTE" rev-parse refs/tags/pr)" = "$LOCAL_SHA" ]
}

@test "with a same-named tag on the remote, a correct lease still pushes the branch" {
  git -C "$MAIN_WT" push -q origin "$OLD_SHA:refs/tags/pr"
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 0 ]
  [ "$(remote_ref pr)" = "$LOCAL_SHA" ]
  [ "$(git -C "$REMOTE" rev-parse refs/tags/pr)" = "$OLD_SHA" ]
}

@test "a missing --worktree-root exits 2 and the remote is untouched" {
  run_push --worktree "$LINKED" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  [[ $output == *"--worktree-root"* ]]
  assert_remote_unchanged
}

@test "a --worktree-root that is not a directory exits 2" {
  run_push --worktree "$LINKED" --worktree-root "$TEST_DIR/no-such-root" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  assert_remote_unchanged
}

@test "a --worktree that is not a directory exits 2" {
  run_push --worktree "$ROOT/no-such-wt" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  assert_remote_unchanged
}

@test "a worktree outside --worktree-root exits 2 and nothing is pushed" {
  mkdir -p "$TEST_DIR/elsewhere"
  run_push --worktree "$LINKED" --worktree-root "$TEST_DIR/elsewhere" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  [[ $output == *"not strictly inside"* ]]
  assert_remote_unchanged
}

@test "a sibling directory sharing the root's name prefix is outside the root" {
  mkdir -p "$TEST_DIR/wt-root-sibling"
  run_push --worktree "$LINKED" --worktree-root "$TEST_DIR/wt-root-sibling" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  assert_remote_unchanged
}

@test "a worktree equal to --worktree-root exits 2 and nothing is pushed" {
  run_push --worktree "$LINKED" --worktree-root "$LINKED" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  assert_remote_unchanged
}

@test "containment compares real paths: a symlink out of the root is outside it" {
  mkdir -p "$TEST_DIR/outside"
  git -C "$MAIN_WT" worktree add -q -b pr-out "$TEST_DIR/outside/wt" pr
  ln -s "$TEST_DIR/outside/wt" "$ROOT/link-to-outside"
  run_push --worktree "$ROOT/link-to-outside" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  assert_remote_unchanged
}

@test "a root given with a trailing slash and a symlinked spelling still contains the worktree" {
  ln -s "$ROOT" "$TEST_DIR/root-link"
  run_push --worktree "$LINKED" --worktree-root "$TEST_DIR/root-link/" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 0 ]
  [ "$(remote_ref pr)" = "$LOCAL_SHA" ]
}

@test "a worktree in the middle of a rebase exits 2 and nothing is pushed" {
  # Two diverging commits on the same line force a conflict, which stops the rebase.
  git -C "$MAIN_WT" checkout -q main
  echo base2 >"$MAIN_WT/f"
  git -C "$MAIN_WT" commit -q -am base2
  git -C "$MAIN_WT" push -q origin main
  git -C "$LINKED" fetch -q origin
  run git -C "$LINKED" rebase origin/main
  [ "$status" -ne 0 ]
  [ -d "$(git -C "$LINKED" rev-parse --absolute-git-dir)/rebase-merge" ]
  BASE_SHA=$(remote_ref main)
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  [[ $output == *"rebase is in progress"* ]]
  assert_remote_unchanged
}

@test "a worktree whose HEAD lacks origin/<base> exits 2 and nothing is pushed" {
  # The base moves on and is fetched; the worktree's HEAD does not contain it.
  git -C "$MAIN_WT" checkout -q main
  echo base2 >"$MAIN_WT/g"
  git -C "$MAIN_WT" add g
  git -C "$MAIN_WT" commit -q -m base2
  git -C "$MAIN_WT" push -q origin main
  BASE_SHA=$(remote_ref main)
  git -C "$LINKED" fetch -q origin
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  [[ $output == *"does not descend"* ]]
  assert_remote_unchanged
}

@test "a HEAD equal to origin/<base> passes the ancestry check" {
  git -C "$LINKED" reset -q --hard origin/main
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 0 ]
  [ "$(remote_ref pr)" = "$BASE_SHA" ]
}

@test "a missing origin remote-tracking ref for the base exits 2 and nothing is pushed" {
  git -C "$LINKED" update-ref -d refs/remotes/origin/main
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 2 ]
  [[ $output == *"origin/main does not exist"* ]]
  assert_remote_unchanged
}

@test "the helper never fetches: a base that moved on the remote but was not fetched is not seen" {
  # Move the remote base from ANOTHER clone, so this repository's
  # remote-tracking ref stays where it was.
  gfh_clone "$REMOTE" "$OTHER" "pg-rescue-push-branch"
  echo base2 >"$OTHER/g"
  git -C "$OTHER" add g
  git -C "$OTHER" commit -q -m base2
  git -C "$OTHER" push -q origin main
  BASE_SHA=$(remote_ref main)
  before=$(git -C "$LINKED" rev-parse refs/remotes/origin/main)
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 0 ]
  [ "$(git -C "$LINKED" rev-parse refs/remotes/origin/main)" = "$before" ]
  [ "$(remote_ref main)" = "$BASE_SHA" ]
}

@test "an uppercase 40-hex --expected-sha is accepted" {
  upper=$(tr 'a-f' 'A-F' <<<"$OLD_SHA")
  run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$upper"
  [ "$status" -eq 0 ]
  [ "$(remote_ref pr)" = "$LOCAL_SHA" ]
}

@test "an exported GIT_DIR or GIT_WORK_TREE does not redirect the push" {
  # Another repository with its own refs, compared before and after.
  other_repo="$TEST_DIR/decoy"
  git init -q "$other_repo"
  git -C "$other_repo" config user.email decoy@example.invalid
  git -C "$other_repo" config user.name decoy
  echo decoy >"$other_repo/d"
  git -C "$other_repo" add d
  git -C "$other_repo" commit -q -m decoy
  before=$(git -C "$other_repo" for-each-ref --format='%(refname) %(objectname)'; git -C "$other_repo" rev-parse HEAD)

  GIT_DIR="$other_repo/.git" GIT_WORK_TREE="$other_repo" GIT_COMMON_DIR="$other_repo/.git" \
    GIT_INDEX_FILE="$other_repo/.git/index" GIT_NAMESPACE=decoy \
    run_push --worktree "$LINKED" --worktree-root "$ROOT" --branch pr --base main --expected-sha "$OLD_SHA"
  [ "$status" -eq 0 ]
  [ "$(remote_ref pr)" = "$LOCAL_SHA" ]
  [ "$(remote_ref main)" = "$BASE_SHA" ]
  after=$(git -C "$other_repo" for-each-ref --format='%(refname) %(objectname)'; git -C "$other_repo" rev-parse HEAD)
  [ "$before" = "$after" ]
  [ -z "$(git -C "$other_repo" status --porcelain)" ]
}
