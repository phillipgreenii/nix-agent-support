#!/usr/bin/env bats
# bats file_tags=type:unit

setup() {
  # SCRIPTS_DIR/TEST_SUPPORT: injected by nix check (raw src dir / base-flake
  # harness path), or computed relative to this test file for a local
  # `bats tests/` run. MUST honor an already-set env var -- the nix check
  # harness copies tests/* flat into a bare $TMPDIR (so recomputing
  # unconditionally from BATS_TEST_FILENAME would resolve to the wrong
  # directory there), and gfh_setup below scrubs every exported var not on
  # its allowlist -- both are captured into plain locals BEFORE it runs and
  # re-exported after (pg2-31f13).
  local scripts_dir_saved="${SCRIPTS_DIR:-}"
  local test_support_saved="${TEST_SUPPORT:-}"

  # GFH_LIB is the one required file (tc-4ehow); nix package checks inject
  # TEST_SUPPORT (the directory holding it) instead, so alias that.
  local gfh_lib="${GFH_LIB:-${TEST_SUPPORT:+$TEST_SUPPORT/git-fixture-harness.bash}}"
  if [[ -z $gfh_lib ]]; then
    echo "GFH_LIB is not set: commit via the run-unit-tests hook, run under the workspace .envrc (direnv, or direnv exec <workspace-root> ...), or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # shellcheck disable=SC1090,SC1091
  source "$gfh_lib"

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
  export GFH_LIB="$gfh_lib"

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

# ---------------------------------------------------------------------------
# Allow-list classification (bead pg2-qs7lp). All matching cases are driven by
# a fake `lsof` shim on PATH that emits `-F pcn` records, so they depend
# neither on a real process of a given name nor on macOS-only names (Python,
# .claude-wrapped) existing on disk -- the suite also runs on Linux.
# ---------------------------------------------------------------------------

# fake_lsof_records <pid> <name>...: install a fake `lsof` on PATH that records
# its argv to $FAKE_LSOF_ARGS and prints one `-F pcn` process record per
# <pid> <name> pair (the same field shape real lsof emits).
fake_lsof_records() {
  local fakebin="$GFH_ROOT/fakebin"
  mkdir -p "$fakebin"
  export FAKE_LSOF_ARGS="$GFH_ROOT/lsof-args"
  export FAKE_LSOF_OUT="$GFH_ROOT/lsof-out"
  : >"$FAKE_LSOF_OUT"
  while [[ $# -ge 2 ]]; do
    printf 'p%s\nc%s\nfcwd\nn/some/worktree\n' "$1" "$2" >>"$FAKE_LSOF_OUT"
    shift 2
  done
  cat >"$fakebin/lsof" <<'SHIM'
#!/usr/bin/env bash
printf '%s\n' "$*" >"$FAKE_LSOF_ARGS"
cat "$FAKE_LSOF_OUT"
SHIM
  chmod +x "$fakebin/lsof"
  PATH="$fakebin:$PATH"
}

# refute_blocks <name>: assert wtdone_command_blocks rejects <name>. A bare
# `! cmd` is NOT used because bash's errexit ignores a negated command, so it
# could never fail a test.
refute_blocks() {
  run wtdone_command_blocks "$1"
  [ "$status" -ne 0 ]
}

@test "wtdone_anchored_processes: asks lsof for full -F pcn records (+c 0) and returns its output unfiltered" {
  fake_lsof_records 111 node 222 claude
  run wtdone_anchored_processes "$TEST_DIR"
  [ "$status" -eq 0 ]
  [ "$output" = "$(cat "$FAKE_LSOF_OUT")" ]
  local args
  args="$(cat "$FAKE_LSOF_ARGS")"
  [[ $args == *"-d cwd"* ]]
  [[ $args == *"+D $TEST_DIR"* ]]
  [[ $args == *"+c 0"* ]]
  [[ $args == *"-F pcn"* ]]
}

@test "wtdone_anchored_processes: lsof's nonzero exit is discarded" {
  local fakebin="$GFH_ROOT/fakebin"
  mkdir -p "$fakebin"
  printf '#!/usr/bin/env bash\nexit 1\n' >"$fakebin/lsof"
  chmod +x "$fakebin/lsof"
  PATH="$fakebin:$PATH"
  run wtdone_anchored_processes "$TEST_DIR"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "wtdone_normalize_command: strips ONE leading dot and ONE trailing -wrapped, then lowercases" {
  [ "$(wtdone_normalize_command .claude-wrapped)" = claude ]
  [ "$(wtdone_normalize_command claude)" = claude ]
  [ "$(wtdone_normalize_command Python)" = python ]
  [ "$(wtdone_normalize_command ..foo-wrapped-wrapped)" = ".foo-wrapped" ]
  [ "$(wtdone_normalize_command "Google Chrome Helper (Renderer)")" = "google chrome helper (renderer)" ]
}

@test "wtdone_classify_anchored: every default allow-list entry blocks" {
  local name
  for name in claude git bash zsh sh python vim nvim emacs go nix; do
    fake_lsof_records 101 "$name"
    run bash -c 'source "$1"; lsof -F pcn | wtdone_classify_anchored' _ "$LIB"
    [ "$status" -eq 0 ]
    [ "$output" = "block 101 $name" ]
  done
}

@test "wtdone_classify_anchored: wrapped and cased names normalize and block; python* is a prefix match" {
  local name
  for name in .claude-wrapped claude Claude Python python3.1 python3.13 .git-wrapped; do
    fake_lsof_records 102 "$name"
    run bash -c 'source "$1"; lsof -F pcn | wtdone_classify_anchored' _ "$LIB"
    [ "$status" -eq 0 ]
    [ "$output" = "block 102 $name" ]
  done
}

@test "wtdone_classify_anchored: node, caffeinate and a name with spaces are ignored; non-prefix entries are exact" {
  local name
  for name in node caffeinate "Google Chrome Helper (Renderer)" gitk bash-completion pyth; do
    fake_lsof_records 103 "$name"
    run bash -c 'source "$1"; lsof -F pcn | wtdone_classify_anchored' _ "$LIB"
    [ "$status" -eq 0 ]
    [ "$output" = "ignore 103 $name" ]
  done
}

@test "wtdone_classify_anchored: one line per process, names with spaces stay whole, no COMMAND header" {
  fake_lsof_records 1 .claude-wrapped 2 "Google Chrome Helper (Renderer)" 3 node
  run bash -c 'source "$1"; lsof -F pcn | wtdone_classify_anchored' _ "$LIB"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 3 ]
  [ "${lines[0]}" = "block 1 .claude-wrapped" ]
  [ "${lines[1]}" = "ignore 2 Google Chrome Helper (Renderer)" ]
  [ "${lines[2]}" = "ignore 3 node" ]
  [[ $output != *COMMAND* ]]
}

@test "wtdone_classify_anchored: empty input yields no output" {
  run bash -c 'source "$1"; printf "" | wtdone_classify_anchored' _ "$LIB"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "WTDONE_BLOCKING_COMMANDS: unset uses the default list" {
  unset WTDONE_BLOCKING_COMMANDS
  wtdone_command_blocks claude
  wtdone_command_blocks python3.13
  refute_blocks node
}

@test "WTDONE_BLOCKING_COMMANDS: empty (and blank) uses the default list, never 'block nothing'" {
  export WTDONE_BLOCKING_COMMANDS=""
  wtdone_command_blocks claude
  wtdone_command_blocks bash
  refute_blocks node
  export WTDONE_BLOCKING_COMMANDS="   "
  wtdone_command_blocks claude
}

@test "WTDONE_BLOCKING_COMMANDS: a custom value REPLACES the default and accepts the same entry syntax" {
  export WTDONE_BLOCKING_COMMANDS="node ruby*"
  wtdone_command_blocks node
  wtdone_command_blocks Node
  wtdone_command_blocks ruby3.3
  # on the default list but absent from the custom value: no longer blocks
  refute_blocks claude
  refute_blocks bash
}
