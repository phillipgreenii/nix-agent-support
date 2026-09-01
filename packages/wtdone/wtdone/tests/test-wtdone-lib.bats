#!/usr/bin/env bats
# bats file_tags=type:unit

setup() {
  # SCRIPTS_DIR/TEST_SUPPORT: injected by nix check (raw src dir / vendored
  # harness path), or computed relative to this test file for a local
  # `bats tests/` run. MUST honor an already-set env var -- the nix check
  # harness copies tests/* flat into a bare $TMPDIR (so recomputing
  # unconditionally from BATS_TEST_FILENAME would resolve to the wrong
  # directory there), and gfh_setup below scrubs every exported var not on
  # its allowlist -- both are captured into plain locals BEFORE it runs and
  # re-exported after (pg2-31f13).
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

  if [[ -z $scripts_dir_saved ]]; then
    scripts_dir_saved="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  LIB="${scripts_dir_saved}/wtdone.bash"
  # shellcheck disable=SC1090  # runtime-computed path, by design
  source "$LIB"

  # Hermetic-by-construction git fixture (GIT_CEILING_DIRECTORIES + env
  # allowlist reset + fresh HOME + hooks disabled): see pg2-31f13/pg2-gucfd.
  gfh_setup "wtdone-lib"

  export SCRIPTS_DIR="$scripts_dir_saved"

  if [[ -n $test_support_saved ]]; then
    export TEST_SUPPORT="$test_support_saved"
  fi

  TEST_DIR="$GFH_REPO"
  cd "$TEST_DIR" || return 1
}

teardown() {
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

add_worktree() {
  WT_DIR="$(mktemp -d)"
  git -C "$TEST_DIR" worktree add -q "$WT_DIR/wt" -b "$1" >/dev/null
}

@test "canonical_root: resolves the main worktree from the main worktree itself" {
  run canonical_root
  [ "$status" -eq 0 ]
  # macOS: $TEST_DIR under /tmp resolves to /private/tmp; compare resolved
  # forms both ways.
  [ "$(cd "$output" && pwd -P)" = "$(cd "$TEST_DIR" && pwd -P)" ]
}

@test "canonical_root: resolves the main worktree from inside a linked worktree" {
  add_worktree feat
  cd "$WT_DIR/wt" || return 1
  run canonical_root
  [ "$status" -eq 0 ]
  [ "$(cd "$output" && pwd -P)" = "$(cd "$TEST_DIR" && pwd -P)" ]
}

@test "wtdone_find_worktree: resolves the worktree path for a branch checked out in a linked worktree" {
  add_worktree feature
  run wtdone_find_worktree "$TEST_DIR" feature
  [ "$status" -eq 0 ]
  [ "$(cd "$output" && pwd -P)" = "$(cd "$WT_DIR/wt" && pwd -P)" ]
}

@test "wtdone_find_worktree: prints nothing for a plain local branch with no worktree" {
  git -C "$TEST_DIR" branch feature
  run wtdone_find_worktree "$TEST_DIR" feature
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "wtdone_find_worktree: prints nothing for a branch name that does not exist at all" {
  run wtdone_find_worktree "$TEST_DIR" does-not-exist
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "wtdone_anchored_processes: empty when nothing has its cwd inside the directory" {
  mkdir -p "$TEST_DIR/plain-dir"
  run wtdone_anchored_processes "$TEST_DIR/plain-dir"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "wtdone_anchored_processes: reports a live process whose cwd is inside the directory" {
  mkdir -p "$TEST_DIR/anchored-dir"
  bash -c 'cd "$1" && exec sleep 60' _ "$TEST_DIR/anchored-dir" &
  ANCHOR_PID=$!
  local anchor_pid_val="$ANCHOR_PID"

  local _ output_str=""
  for _ in $(seq 1 10); do
    output_str="$(wtdone_anchored_processes "$TEST_DIR/anchored-dir")"
    [[ -n $output_str ]] && break
    sleep 0.2
  done

  kill "$ANCHOR_PID" 2>/dev/null || true
  wait "$ANCHOR_PID" 2>/dev/null || true
  ANCHOR_PID=""

  [[ -n $output_str ]]
  [[ "$output_str" == *"$anchor_pid_val"* ]]
}

@test "wtdone_remaining_worktrees: matches 'git worktree list' for the given canonical dir" {
  add_worktree feature
  run wtdone_remaining_worktrees "$TEST_DIR"
  [ "$status" -eq 0 ]
  [ "$output" = "$(git -C "$TEST_DIR" worktree list)" ]
}
