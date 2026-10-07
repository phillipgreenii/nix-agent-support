#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Script-level (subprocess) tests for wtdone's entry point: arg parsing, the
# lsof liveness guard, the worktree-remove/branch--d/prune sequence, and the
# no-worktree-found degradation. Real `git` and `lsof` (must resolve on PATH
# -- testDeps entries under nix, ordinary system tools for a local `bats
# tests/` run).

setup() {
  # SCRIPTS_DIR/TEST_SUPPORT: injected by nix check (raw src dir / base-flake
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
    source "$(git -C "$BATS_TEST_DIRNAME" rev-parse --path-format=absolute --git-common-dir)/../../phillipg-nix-repo-base/lib/scripts/git-fixture-harness.bash"
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

# start_anchor <dir>: start an ANCHOR_PID `bash` whose cwd is <dir> and which
# runs `sleep` as a CHILD (deliberately not exec'd, so bash itself keeps the
# cwd and is on the allow-list while the child sleep is anchored too and is
# not). The TERM trap also stops the child, so teardown leaves nothing behind.
# Bounded poll until lsof sees BOTH processes (10 attempts, 0.2s apart).
start_anchor() {
  bash -c 'trap "kill \$child 2>/dev/null; exit 0" TERM; cd "$1" || exit 1; sleep 60 & child=$!; wait' _ "$1" 3>&- &
  ANCHOR_PID=$!
  local _ n
  for _ in $(seq 1 10); do
    n="$(lsof -a -d cwd +D "$1" -F p 2>/dev/null | grep -c '^p' || true)"
    [[ $n -ge 2 ]] && break
    sleep 0.2
  done
}

# anchored_child_name <dir> <not-pid>: the command name lsof reports for the
# anchored process in <dir> that is NOT <not-pid> (the sleep child). Looked up
# rather than hard-coded because the name is the kernel COMM name: `sleep` on
# Linux, but a multi-call coreutils binary can show up under another name on
# macOS.
anchored_child_name() {
  local line pid="" name=""
  while IFS= read -r line; do
    case "$line" in
    p*) pid="${line#p}" ;;
    c*)
      name="${line#c}"
      if [[ $pid != "$2" ]]; then
        echo "$pid $name"
        return 0
      fi
      ;;
    *) ;;
    esac
  done < <(lsof -a -d cwd +D "$1" +c 0 -F pc 2>/dev/null)
  return 1
}

# fake_lsof_records <pid> <name>...: put a fake `lsof` on PATH printing one
# `-F pcn` record per <pid> <name> pair (see the lib suite's identical helper).
fake_lsof_records() {
  local fakebin="$GFH_ROOT/fakebin"
  mkdir -p "$fakebin"
  export FAKE_LSOF_OUT="$GFH_ROOT/lsof-out"
  : >"$FAKE_LSOF_OUT"
  while [[ $# -ge 2 ]]; do
    printf 'p%s\nc%s\nfcwd\nn/some/worktree\n' "$1" "$2" >>"$FAKE_LSOF_OUT"
    shift 2
  done
  printf '#!/usr/bin/env bash\ncat "$FAKE_LSOF_OUT"\n' >"$fakebin/lsof"
  chmod +x "$fakebin/lsof"
  PATH="$fakebin:$PATH"
}

@test "refuse-when-anchored: an allow-listed process (bash) cwd'd inside the worktree blocks removal, PID listed, ignored child reported, nothing touched" {
  add_worktree feature
  git -C "$TEST_DIR" merge -q --ff-only feature

  start_anchor "$WT_DIR/wt"
  local child child_pid child_name
  child="$(anchored_child_name "$WT_DIR/wt" "$ANCHOR_PID")"
  child_pid="${child%% *}"
  child_name="${child#* }"

  run bash "$BIN" feature
  kill "$ANCHOR_PID" 2>/dev/null || true
  wait "$ANCHOR_PID" 2>/dev/null || true
  local anchor_pid_val="$ANCHOR_PID"
  ANCHOR_PID=""

  [ "$status" -eq 1 ]
  [[ "$output" =~ "anchored" ]]
  # the blocking row names the bash holder; no header row is ever printed
  [[ "$output" == *"(pid $anchor_pid_val)"* ]]
  [[ "$output" != *COMMAND* ]]
  # the child sleep is anchored too but off the list: reported, not blocking
  [[ "$output" == *"wtdone: ignoring anchored process $child_name (pid $child_pid): not in the blocking list"* ]]
  # the bash holder itself is never reported as ignored
  local saved_output="$output"
  run grep "ignoring anchored process .*(pid $anchor_pid_val)" <<<"$saved_output"
  [ "$status" -ne 0 ]
  [ -e "$WT_DIR/wt" ]
  run git -C "$TEST_DIR" rev-parse --verify --quiet refs/heads/feature
  [ "$status" -eq 0 ]
}

@test "all-ignored: a real anchored process that is off the allow-list does not block; one ignoring line, worktree removed" {
  add_worktree feature
  git -C "$TEST_DIR" merge -q --ff-only feature

  # exec: the anchored process IS the sleep (ANCHOR_PID), not a bash.
  bash -c 'cd "$1" && exec sleep 60' _ "$WT_DIR/wt" 3>&- &
  ANCHOR_PID=$!
  local _
  for _ in $(seq 1 10); do
    [[ -n "$(lsof -a -d cwd +D "$WT_DIR/wt" 2>/dev/null)" ]] && break
    sleep 0.2
  done
  local child_name
  child_name="$(anchored_child_name "$WT_DIR/wt" "none" | cut -d' ' -f2-)"

  run bash "$BIN" feature
  [ "$status" -eq 0 ]
  [[ "$output" == *"wtdone: ignoring anchored process $child_name (pid $ANCHOR_PID): not in the blocking list"* ]]
  [ "$(grep -c 'ignoring anchored process' <<<"$output")" -eq 1 ]
  [[ "$output" != *COMMAND* ]]
  [ ! -e "$WT_DIR/wt" ]
  run git -C "$TEST_DIR" rev-parse --verify --quiet refs/heads/feature
  [ "$status" -ne 0 ]
}

@test "the caller's own shell cwd'd inside the worktree (a shell is on the allow-list) still refuses" {
  add_worktree feature
  git -C "$TEST_DIR" merge -q --ff-only feature

  run bash -c 'cd "$1" && bash "$2" feature --cc "$3"' _ "$WT_DIR/wt" "$BIN" "$TEST_DIR"
  [ "$status" -eq 1 ]
  [[ "$output" =~ "anchored" ]]
  [ -e "$WT_DIR/wt" ]
  run git -C "$TEST_DIR" rev-parse --verify --quiet refs/heads/feature
  [ "$status" -eq 0 ]
}

@test "nothing anchored: unchanged behavior, exit 0, no ignoring line" {
  add_worktree feature
  git -C "$TEST_DIR" merge -q --ff-only feature
  run bash "$BIN" feature
  [ "$status" -eq 0 ]
  [[ "$output" != *"ignoring anchored process"* ]]
  [ ! -e "$WT_DIR/wt" ]
}

@test "fake lsof, all off the list: exit 0, worktree removed, one ignoring line per process (names with spaces whole), no COMMAND header" {
  add_worktree feature
  git -C "$TEST_DIR" merge -q --ff-only feature
  fake_lsof_records 11 node 12 caffeinate 13 "Google Chrome Helper (Renderer)"

  run bash "$BIN" feature
  [ "$status" -eq 0 ]
  [ "$(grep -c 'ignoring anchored process' <<<"$output")" -eq 3 ]
  [[ "$output" == *"wtdone: ignoring anchored process node (pid 11): not in the blocking list"* ]]
  [[ "$output" == *"wtdone: ignoring anchored process caffeinate (pid 12): not in the blocking list"* ]]
  [[ "$output" == *"wtdone: ignoring anchored process Google Chrome Helper (Renderer) (pid 13): not in the blocking list"* ]]
  [[ "$output" != *COMMAND* ]]
  [ ! -e "$WT_DIR/wt" ]
}

@test "fake lsof, mixed: a nix-wrapped claude blocks (exit 1, only blocking rows), ignored rows still reported, nothing removed" {
  add_worktree feature
  git -C "$TEST_DIR" merge -q --ff-only feature
  fake_lsof_records 21 node 22 .claude-wrapped

  run bash "$BIN" feature
  [ "$status" -eq 1 ]
  [[ "$output" =~ "anchored" ]]
  [[ "$output" == *"wtdone: ignoring anchored process node (pid 21): not in the blocking list"* ]]
  [[ "$output" == *".claude-wrapped (pid 22)"* ]]
  # blocking rows only: node appears solely in its ignoring line
  [ "$(grep -c 'node' <<<"$output")" -eq 1 ]
  [[ "$output" != *COMMAND* ]]
  [ -e "$WT_DIR/wt" ]
  run git -C "$TEST_DIR" rev-parse --verify --quiet refs/heads/feature
  [ "$status" -eq 0 ]
}

@test "WTDONE_BLOCKING_COMMANDS: a custom value replaces the default list (node now blocks, claude no longer does)" {
  add_worktree feature
  git -C "$TEST_DIR" merge -q --ff-only feature
  fake_lsof_records 31 node 32 claude

  run env WTDONE_BLOCKING_COMMANDS="node" bash "$BIN" feature
  [ "$status" -eq 1 ]
  [[ "$output" == *"node (pid 31)"* ]]
  [[ "$output" == *"wtdone: ignoring anchored process claude (pid 32): not in the blocking list"* ]]
  [ -e "$WT_DIR/wt" ]
}

@test "WTDONE_BLOCKING_COMMANDS: empty means the default list, not 'block nothing'" {
  add_worktree feature
  git -C "$TEST_DIR" merge -q --ff-only feature
  fake_lsof_records 41 claude

  run env WTDONE_BLOCKING_COMMANDS="" bash "$BIN" feature
  [ "$status" -eq 1 ]
  [[ "$output" =~ "anchored" ]]
  [ -e "$WT_DIR/wt" ]
}

@test "--help documents the allow-list, its default entries, the env override and the accepted limits" {
  run bash "$BIN" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"WTDONE_BLOCKING_COMMANDS"* ]]
  [[ "$output" == *"claude git bash zsh sh python* vim nvim"* ]]
  [[ "$output" == *"only processes whose name is on the list block"* ]]
  [[ "$output" == *"ignored"* ]]
  [[ "$output" == *"shebang"* ]]
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
