#!/usr/bin/env bats
# The confirmation prompt, exercised for real (bead pg2-6zh4ni). The other
# suites stub pgdr_confirm, so none of them could notice that the real prompt was
# invisible: `read -p` writes to stderr and an earlier `2>/dev/null` threw it
# away, leaving the tool looking hung. These tests run the real
# pg-disk-reclaimer.sh on a real pseudo-terminal (tests/pty_drive.py) and assert
# what an operator would see.
bats_require_minimum_version 1.5.0

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  SCRIPT="$SCRIPTS_DIR/pg-disk-reclaimer.sh"
  DRIVE="$SCRIPTS_DIR/tests/pty_drive.py"

  # A sandbox that cannot allocate a pseudo-terminal cannot run these tests;
  # PGDR_REQUIRE_PTY=1 (set where a pty is guaranteed) turns that skip into a failure.
  if ! python3 -I -c 'import pty; pty.openpty()' 2>/dev/null; then
    if [[ ${PGDR_REQUIRE_PTY:-0} == 1 ]]; then
      echo "PGDR_REQUIRE_PTY=1 but no pseudo-terminal can be allocated" >&2
      return 1
    fi
    skip "no pseudo-terminal available in this environment"
  fi

  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  export HOME="$TEST_DIR/home"
  mkdir -p "$HOME/.config/pg-disk-reclaimer" "$TEST_DIR/area"
  unset XDG_CONFIG_HOME
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

drive() {
  local answer="$1"
  shift
  python3 -I "$DRIVE" --answer "$answer" --wait-for "[y/N]" --timeout 30 -- "$@"
}

@test "the prompt is visible on the terminal, appears once for two gated items, and 'y' runs every item" {
  run drive y bash "$SCRIPT" reclaim --aggressiveness 5 --apply
  [ "$status" -eq 0 ]
  [[ "$output" == *"[y/N]"* ]]
  [[ "$output" == *"item-4,item-5"* ]]
  [ "$(grep -o '\[y/N\]' <<<"$output" | wc -l)" -eq 1 ]
  [ -e "$MARKER_DIR/ran-1" ]
  [ -e "$MARKER_DIR/ran-4" ]
  [ -e "$MARKER_DIR/ran-5" ]
}

@test "answering 'n' runs nothing, not even the ungated item, and exits 2" {
  run drive n bash "$SCRIPT" reclaim --aggressiveness 5 --apply
  [ "$status" -eq 2 ]
  [[ "$output" == *"not confirmed; nothing was run"* ]]
  [ ! -e "$MARKER_DIR/ran-1" ]
  [ ! -e "$MARKER_DIR/ran-4" ]
  [ ! -e "$MARKER_DIR/ran-5" ]
}

@test "the plan (each item's size) is printed before the prompt" {
  run drive n bash "$SCRIPT" reclaim --aggressiveness 5 --apply
  [[ "$output" == *"item-5: size: 1.0M"*"[y/N]"* ]]
}

@test "a run that selects no gated item never prompts" {
  run drive y bash "$SCRIPT" reclaim --aggressiveness 3 --apply
  [ "$status" -eq 0 ]
  [[ "$output" != *"[y/N]"* ]]
  [ -e "$MARKER_DIR/ran-1" ]
  [ ! -e "$MARKER_DIR/ran-4" ]
}

@test "a dry run at the highest level never prompts" {
  run drive y bash "$SCRIPT" reclaim --aggressiveness 5
  [ "$status" -eq 0 ]
  [[ "$output" != *"[y/N]"* ]]
  [ ! -e "$MARKER_DIR/ran-5" ]
}

@test "with no controlling terminal the run says so, runs nothing and exits 3, even when an answer is piped in" {
  # setsid-less detach: python's os.setsid in a child gives it NO controlling terminal.
  run bash -c "yes | python3 -I -c 'import os,sys; os.setsid(); os.execvp(sys.argv[1], sys.argv[1:])' bash \"$SCRIPT\" reclaim --aggressiveness 5 --apply"
  [ "$status" -eq 3 ]
  [[ "$output" == *"no terminal to confirm level 4+ item(s) (item-4,item-5); nothing was run"* ]]
  [ ! -e "$MARKER_DIR/ran-1" ]
  [ ! -e "$MARKER_DIR/ran-4" ]
}
