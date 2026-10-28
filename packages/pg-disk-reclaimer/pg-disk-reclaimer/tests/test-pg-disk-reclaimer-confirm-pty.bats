#!/usr/bin/env bats
# The confirmation prompt, exercised for real (beads pg2-6zh4ni, pg2-kb5dby). The
# other suites stub pgdr_confirm, so none of them could notice that the real
# prompt was invisible: `read -p` writes to stderr and an earlier `2>/dev/null`
# threw it away, leaving the tool looking hung. These tests run the real
# pg-disk-reclaimer.sh on a real pseudo-terminal (tests/pty_drive.py) and assert
# what an operator would see. The script runs under `bash -euo pipefail`, the
# strict mode the nix wrapper adds, so an errexit-unsafe call shape fails here.
#
# A sandbox that cannot allocate a pty AND open /dev/tty skips the tests that
# need one. PGDR_REQUIRE_PTY=1 (set by the `pg-disk-reclaimer-pty-test` hook,
# which runs outside any sandbox) turns that skip into a failure, so the tier
# cannot go unrun unnoticed. The no-controlling-terminal test needs no pty.
bats_require_minimum_version 1.5.0
load 'hermetic'

# A prompt is a whole line that starts with the tool's own text and ends in the
# [y/N] marker: registry text containing "[y/N]" mid-line is not answered.
PROMPT_RE='^pg-disk-reclaimer: run --apply .*\[y/N\] $'

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  SCRIPT="$SCRIPTS_DIR/pg-disk-reclaimer.sh"
  DRIVE="$SCRIPTS_DIR/tests/pty_drive.py"
  RUN=(bash -euo pipefail "$SCRIPT")

  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  pgdr_test_isolate "$TEST_DIR"
  pgdr_test_assert_hermetic "$TEST_DIR"
  mkdir -p "$HOME/.config/pg-disk-reclaimer" "$TEST_DIR/area"
  MARKER_DIR="$TEST_DIR/markers"
  mkdir -p "$MARKER_DIR"

  # Three items: level 1 (ungated) and levels 4 and 5 (gated). Each remove
  # command drops a marker file, so a test can tell what really ran.
  jq -n --arg area "$TEST_DIR/area" --arg m "$MARKER_DIR" '
    [1, 4, 5] | map({
      id: "item-\(.)",
      description: "pty probe at level \(.)",
      path: $area,
      displayCommand: "echo probe",
      sizeCommand: "echo 1024",
      variants: [{
        aggressiveness: .,
        variantDescription: "probe",
        dryRunCommand: "echo dry-\(.)",
        removeCommand: "touch \($m)/ran-\(.)"
      }]
    })' >"$HOME/.config/pg-disk-reclaimer/registry.json"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# require_pty: skips the calling test when no pty with a /dev/tty can be had,
# unless PGDR_REQUIRE_PTY=1, which makes that a failure.
require_pty() {
  if python3 -I "$DRIVE" --probe; then
    return 0
  fi
  if [[ ${PGDR_REQUIRE_PTY:-0} == 1 ]]; then
    echo "PGDR_REQUIRE_PTY=1 but no pseudo-terminal with a /dev/tty can be had here" >&2
    return 1
  fi
  skip "no pseudo-terminal available in this environment"
}

# drive <answer> <cmd...>: runs the command on a pty, typing <answer> at the
# prompt line. The transcript's carriage returns are dropped so line anchors work.
drive() {
  local answer="$1"
  shift
  python3 -I "$DRIVE" --answer "$answer" --prompt "$PROMPT_RE" --timeout 30 -- "$@"
}

# clean: the transcript without carriage returns.
clean() {
  tr -d '\r' <<<"$output"
}

assert_none_ran() {
  [ ! -e "$MARKER_DIR/ran-1" ]
  [ ! -e "$MARKER_DIR/ran-4" ]
  [ ! -e "$MARKER_DIR/ran-5" ]
}

assert_all_ran() {
  [ -e "$MARKER_DIR/ran-1" ]
  [ -e "$MARKER_DIR/ran-4" ]
  [ -e "$MARKER_DIR/ran-5" ]
}

@test "the prompt is visible on the terminal, names each gated item and its size, appears once, and 'y' runs every item" {
  require_pty
  run drive y "${RUN[@]}" reclaim --aggressiveness 5 --apply
  [ "$status" -eq 0 ]
  [[ "$output" == *"[y/N]"* ]]
  [[ "$output" == *"including level 4+ item(s) (item-4: 1.0M, item-5: 1.0M)? This is the only confirmation."* ]]
  [[ "$output" == *"run --apply for 3 item(s)"* ]]
  [ "$(grep -o '\[y/N\]' <<<"$output" | wc -l)" -eq 1 ]
  assert_all_ran
}

@test "answering 'n' runs nothing, not even the ungated item, and exits 2" {
  require_pty
  run drive n "${RUN[@]}" reclaim --aggressiveness 5 --apply
  [ "$status" -eq 2 ]
  [[ "$output" == *"not confirmed; nothing was run"* ]]
  assert_none_ran
}

@test "the plan (each item's size) is printed on its own line before the prompt line" {
  require_pty
  run drive n "${RUN[@]}" reclaim --aggressiveness 5 --apply
  local text prompt_no id line_no
  text="$(clean)"
  prompt_no="$(grep -n '^pg-disk-reclaimer: run --apply ' <<<"$text" | head -1 | cut -d: -f1)"
  [ -n "$prompt_no" ]
  for id in item-1 item-4 item-5; do
    line_no="$(grep -n -x "$id: size: 1.0M" <<<"$text" | head -1 | cut -d: -f1)"
    [ -n "$line_no" ]
    [ "$line_no" -lt "$prompt_no" ]
  done
}

@test "a run that selects no gated item never prompts" {
  require_pty
  run drive y "${RUN[@]}" reclaim --aggressiveness 3 --apply
  [ "$status" -eq 0 ]
  [[ "$output" != *"[y/N]"* ]]
  [ -e "$MARKER_DIR/ran-1" ]
  [ ! -e "$MARKER_DIR/ran-4" ]
}

@test "a dry run at the highest level never prompts" {
  require_pty
  run drive y "${RUN[@]}" reclaim --aggressiveness 5
  [ "$status" -eq 0 ]
  [[ "$output" != *"[y/N]"* ]]
  [ ! -e "$MARKER_DIR/ran-5" ]
}

@test "with no controlling terminal the run says so, runs nothing and exits 3, even when an answer is piped in" {
  # Needs no pty: python's os.setsid gives the child NO controlling terminal.
  run bash -c 'yes | python3 -I -c "import os,sys; os.setsid(); os.execvp(sys.argv[1], sys.argv[1:])" "$@"' \
    _ "${RUN[@]}" reclaim --aggressiveness 5 --apply
  [ "$status" -eq 3 ]
  [[ "$output" == *"no terminal to confirm level 4+ item(s) (item-4,item-5); nothing was run"* ]]
  [[ "$output" != *"not confirmed"* ]]
  assert_none_ran
}

# --- the prompt goes to the terminal and the answer comes from it (item 8) ---

@test "the prompt is still visible and answerable when stderr is discarded (mutant: prompt to stderr)" {
  require_pty
  run drive y bash -c 'exec "$@" 2>/dev/null' _ "${RUN[@]}" reclaim --aggressiveness 5 --apply
  [ "$status" -eq 0 ]
  [[ "$output" == *"[y/N]"* ]]
  assert_all_ran
}

@test "the prompt is still visible and answerable when stdout and stderr are both discarded" {
  require_pty
  run drive y bash -c 'exec "$@" >/dev/null 2>&1' _ "${RUN[@]}" reclaim --aggressiveness 5 --apply
  [ "$status" -eq 0 ]
  [[ "$output" == *"[y/N]"* ]]
  [[ "$output" == *"item-4: 1.0M, item-5: 1.0M"* ]]
  assert_all_ran
}

@test "with stdout piped to another command the prompt still reaches the terminal and the run proceeds" {
  require_pty
  run drive y bash -c '"$@" | cat; exit "${PIPESTATUS[0]}"' _ "${RUN[@]}" reclaim --aggressiveness 5 --apply
  [ "$status" -eq 0 ]
  [[ "$output" == *"item-4: 1.0M, item-5: 1.0M"* ]]
  assert_all_ran
}

@test "an answer piped on stdin is ignored: the terminal's 'n' wins and nothing runs (mutant: read from stdin)" {
  require_pty
  run drive n bash -c 'printf "y\n" | "$@"' _ "${RUN[@]}" reclaim --aggressiveness 5 --apply
  [ "$status" -eq 2 ]
  [[ "$output" == *"not confirmed; nothing was run"* ]]
  assert_none_ran
}

# --- answer parsing, end to end (item 10) ---

@test "y, Y, yes, YES and Yes confirm" {
  require_pty
  local answer
  for answer in y Y yes YES Yes; do
    rm -f "$MARKER_DIR"/ran-*
    run drive "$answer" "${RUN[@]}" reclaim --aggressiveness 5 --apply
    if [ "$status" -ne 0 ] || [ ! -e "$MARKER_DIR/ran-5" ]; then
      echo "answer '$answer' was not accepted (status $status)" >&2
      return 1
    fi
  done
}

@test "an empty answer, n, no, yep, ye, yy, ' y' and 'yes please' decline with exit 2 and run nothing" {
  require_pty
  local answer
  for answer in "" n no yep ye yy " y" "yes please"; do
    rm -f "$MARKER_DIR"/ran-*
    run drive "$answer" "${RUN[@]}" reclaim --aggressiveness 5 --apply
    if [ "$status" -ne 2 ] || [ -e "$MARKER_DIR/ran-1" ] || [ -e "$MARKER_DIR/ran-5" ]; then
      echo "answer '$answer' was not declined (status $status)" >&2
      return 1
    fi
  done
}

@test "Ctrl-D at the prompt is a decline (exit 2) and the 'not confirmed' line starts its own line" {
  require_pty
  run python3 -I "$DRIVE" --eof --prompt "$PROMPT_RE" --timeout 30 -- "${RUN[@]}" reclaim --aggressiveness 5 --apply
  [ "$status" -eq 2 ]
  assert_none_ran
  [ "$(clean | grep -c '^pg-disk-reclaimer: not confirmed; nothing was run')" -eq 1 ]
}

# --- the driver itself (item 14) ---

@test "the driver answers only a whole prompt line, never [y/N] inside other text" {
  require_pty
  run python3 -I "$DRIVE" --prompt '^PROMPT: .*\[y/N\] $' --answer y --timeout 20 -- \
    bash -c 'printf "registry text [y/N] in the middle\n"; if read -t 2 -r x; then echo "got=$x"; else echo got=none; fi'
  [ "$status" -eq 0 ]
  [[ "$output" == *"got=none"* ]]
  run python3 -I "$DRIVE" --prompt '^PROMPT: .*\[y/N\] $' --answer y --timeout 20 -- \
    bash -c 'printf "PROMPT: go? [y/N] "; read -r x; echo "got=$x"'
  [ "$status" -eq 0 ]
  [[ "$output" == *"got=y"* ]]
}

@test "the driver exits 97 on a hang and kills the child's whole process group" {
  require_pty
  # The grandchild ignores SIGHUP (the signal a dying session leader sends its
  # foreground group), so only an explicit group kill ends it.
  local pidfile="$TEST_DIR/grandchild.pid"
  run timeout 30 python3 -I "$DRIVE" --timeout 2 -- \
    bash -c 'trap "" HUP; sleep 300 & echo $! >"$1"; wait' _ "$pidfile"
  [ "$status" -eq 97 ]
  [[ "$output" == *"timed out"* ]]
  local pid
  pid="$(cat "$pidfile")"
  [ -n "$pid" ]
  ! kill -0 "$pid" 2>/dev/null
}

@test "the driver exits 97, not hangs, when the child closes its terminal and keeps running" {
  require_pty
  run timeout 30 python3 -I "$DRIVE" --timeout 2 -- bash -c 'exec <&- >&- 2>&-; sleep 300'
  [ "$status" -eq 97 ]
}
