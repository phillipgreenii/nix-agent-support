#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Script-level tests for sync-projects (bead pg2-zr3jf). `pg-rescue`, `git` and
# `pn` are fakes on PATH, so nothing touches a real repository, a real
# workspace or a remote, and the push can never actually run.
#
#   pn workspace discover   prints the repos in $REPOS_FILE as name<TAB>url<TAB>path
#   pn workspace push       records "push" in $PN_LOG; exits $PN_PUSH_RC (default 0)
#   pg-rescue ...           records its argv, one arg per line, in $RESCUE_LOG;
#                           exits with the code mapped to its -C repo in $RC_FILE
#                           ("<repo path> <code>" lines; unmapped repos exit 0)
#   git                     records "git called" in $GIT_LOG: the script must not
#                           call git itself (pg-rescue runs it)

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    SYNC=("$SCRIPT_UNDER_TEST")
  else
    SYNC=(bash -euo pipefail "$SCRIPTS_DIR/sync-projects.sh")
  fi

  TEST_DIR="$(mktemp -d)"
  export HOME="$TEST_DIR/home"
  mkdir -p "$HOME"

  # Fakes live in their own directory, away from any repository.
  STUB_DIR="$TEST_DIR/bin"
  mkdir -p "$STUB_DIR"
  export PATH="$STUB_DIR:$PATH"
  export PN_LOG="$TEST_DIR/pn.log" RESCUE_LOG="$TEST_DIR/rescue.log" GIT_LOG="$TEST_DIR/git.log"
  export REPOS_FILE="$TEST_DIR/repos.tsv" RC_FILE="$TEST_DIR/rc.map"
  : >"$RC_FILE"
  unset PN_PUSH_RC PN_DISCOVER_RC

  REPO_A="$TEST_DIR/ws/repo-a"
  REPO_B="$TEST_DIR/ws/repo-b"
  REPO_C="$TEST_DIR/ws/repo-c"
  printf '%s\t%s\t%s\n' \
    repo-a "git@example.com:o/repo-a.git" "$REPO_A" \
    repo-b "git@example.com:o/repo-b.git" "$REPO_B" \
    repo-c "git@example.com:o/repo-c.git" "$REPO_C" >"$REPOS_FILE"

  cat >"$STUB_DIR/pn" <<'PN'
#!/bin/sh
case "$1 $2" in
"workspace discover")
  [ "${PN_DISCOVER_RC:-0}" = 0 ] || exit "$PN_DISCOVER_RC"
  cat "$REPOS_FILE"
  ;;
"workspace push")
  echo push >>"$PN_LOG"
  exit "${PN_PUSH_RC:-0}"
  ;;
*)
  echo "fake pn: unsupported: $*" >&2
  exit 64
  ;;
esac
PN
  cat >"$STUB_DIR/pg-rescue" <<'RESCUE'
#!/bin/sh
# Record the argv (one per line, a blank-line separator between calls), find
# the -C repo, and exit with its mapped code.
repo=""
prev=""
{
  for a in "$@"; do
    printf '%s\n' "$a"
    [ "$prev" = "-C" ] && repo="$a"
    prev="$a"
  done
  echo ---
} >>"$RESCUE_LOG"
rc=$(awk -v r="$repo" '$1 == r { print $2 }' "$RC_FILE")
exit "${rc:-0}"
RESCUE
  cat >"$STUB_DIR/git" <<'GIT'
#!/bin/sh
echo "git called: $*" >>"$GIT_LOG"
exit 0
GIT
  chmod +x "$STUB_DIR/pn" "$STUB_DIR/pg-rescue" "$STUB_DIR/git"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# The repos pg-rescue was run for, in order (the argument after each -C).
rescued_repos() {
  awk 'prev == "-C" { print } { prev = $0 }' "$RESCUE_LOG"
}

@test "all repos rebase cleanly, then the push runs" {
  run "${SYNC[@]}"
  [ "$status" -eq 0 ]
  [ "$(rescued_repos)" = "$REPO_A
$REPO_B
$REPO_C" ]
  [ "$(cat "$PN_LOG")" = push ]
}

@test "the push runs only after every repo has been rebased" {
  # A single shared log shows the interleaving.
  cat >"$STUB_DIR/pn" <<'PN'
#!/bin/sh
case "$1 $2" in
"workspace discover") cat "$REPOS_FILE" ;;
"workspace push") echo push >>"$RESCUE_LOG" ;;
esac
PN
  run "${SYNC[@]}"
  [ "$status" -eq 0 ]
  [ "$(tail -n 1 "$RESCUE_LOG")" = push ]
}

@test "argv passed to pg-rescue uses -C <repo>, the sync chain and the context" {
  printf '%s\t%s\t%s\n' only "git@example.com:o/only.git" "$REPO_A" >"$REPOS_FILE"
  run "${SYNC[@]}"
  [ "$status" -eq 0 ]
  expected="--chain
sync
-C
$REPO_A
--context
sync-projects: rebase onto origin
--
git
pull
--rebase
---"
  [ "$(cat "$RESCUE_LOG")" = "$expected" ]
}

@test "the script never calls git itself (no git -C)" {
  run "${SYNC[@]}"
  [ "$status" -eq 0 ]
  [ ! -e "$GIT_LOG" ]
}

@test "a repo that defers (75) stops the loop, exits 75 and pushes nothing" {
  printf '%s 75\n' "$REPO_B" >"$RC_FILE"
  run "${SYNC[@]}"
  [ "$status" -eq 75 ]
  # repo-c is never reached.
  [ "$(rescued_repos)" = "$REPO_A
$REPO_B" ]
  [ ! -e "$PN_LOG" ]
  [[ $output == *"stopped at $REPO_B"* ]]
}

@test "an unhandled repo stops the loop with that exit code and pushes nothing" {
  printf '%s 3\n' "$REPO_A" >"$RC_FILE"
  run "${SYNC[@]}"
  [ "$status" -eq 3 ]
  [ "$(rescued_repos)" = "$REPO_A" ]
  [ ! -e "$PN_LOG" ]
  [[ $output == *"stopped at $REPO_A"* ]]
  [[ $output == *"nothing was pushed"* ]]
}

@test "pn workspace push's exit code is the script's exit code" {
  export PN_PUSH_RC=7
  run "${SYNC[@]}"
  [ "$status" -eq 7 ]
  [ "$(cat "$PN_LOG")" = push ]
}

@test "a failing pn workspace discover aborts before any rebase or push" {
  export PN_DISCOVER_RC=5
  run "${SYNC[@]}"
  [ "$status" -eq 1 ]
  [[ $output == *"'pn workspace discover' failed"* ]]
  [ ! -e "$RESCUE_LOG" ]
  [ ! -e "$PN_LOG" ]
}

@test "an empty repo list is an error, not a bare push" {
  : >"$REPOS_FILE"
  run "${SYNC[@]}"
  [ "$status" -eq 1 ]
  [[ $output == *"listed no repos"* ]]
  [ ! -e "$PN_LOG" ]
}

@test "a repo path containing spaces is passed to -C intact" {
  printf '%s\t%s\t%s\n' spacey "git@example.com:o/s.git" "$TEST_DIR/my ws/repo" >"$REPOS_FILE"
  run "${SYNC[@]}"
  [ "$status" -eq 0 ]
  [ "$(rescued_repos)" = "$TEST_DIR/my ws/repo" ]
}

@test "--help prints usage and exits 0 without running anything" {
  run "${SYNC[@]}" --help
  [ "$status" -eq 0 ]
  [[ $output == *"Usage: sync-projects"* ]]
  [ ! -e "$RESCUE_LOG" ]
  [ ! -e "$PN_LOG" ]
}

@test "an unexpected argument is rejected" {
  run "${SYNC[@]}" --bogus
  [ "$status" -eq 1 ]
  [[ $output == *"unexpected argument: --bogus"* ]]
  [ ! -e "$RESCUE_LOG" ]
}
