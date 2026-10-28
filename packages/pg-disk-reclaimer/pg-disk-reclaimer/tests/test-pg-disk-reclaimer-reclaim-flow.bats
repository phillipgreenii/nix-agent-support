#!/usr/bin/env bats
# bats file_tags=type:unit
# cmd_reclaim's run flow, pinned case by case against the REAL functions (bead
# pg2-kb5dby, follow-up to pg2-6zh4ni): what the confirmation prompt shows, when
# sizing happens, what a registry command can and cannot touch, and the exit
# statuses of a decline or no terminal under the shipped `set -euo pipefail`
# wrapper. Only pgdr_confirm is stubbed here (the real one needs a terminal and
# is driven on a pty in test-pg-disk-reclaimer-confirm-pty.bats). Each test names
# the hand-applied mutant it catches.
bats_require_minimum_version 1.5.0
load 'hermetic'

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  pgdr_test_isolate "$TEST_DIR"
  pgdr_test_assert_hermetic "$TEST_DIR"
  source "$SCRIPTS_DIR/pg-disk-reclaimer.bash"
  mkdir -p "$TEST_DIR/area" "$HOME/.config/pg-disk-reclaimer"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# item <id> <level> <sizeCommand> <dryRunCommand> <removeCommand> [path] [extra-jq-merge]:
# prints one registry item as JSON. An empty sizeCommand omits the field.
item() {
  jq -n --arg id "$1" --argjson level "$2" --arg sc "$3" --arg dry "$4" \
    --arg rm "$5" --arg path "${6:-$TEST_DIR/area}" '
    {
      id: $id,
      description: ("flow item " + $id),
      path: $path,
      displayCommand: "true",
      variants: [{
        aggressiveness: $level,
        variantDescription: "flow",
        dryRunCommand: $dry,
        removeCommand: $rm
      }]
    } + (if $sc == "" then {} else {sizeCommand: $sc} end)'
}

install_registry() {
  jq -s '.' >"$HOME/.config/pg-disk-reclaimer/registry.json" < <(printf '%s\n' "$@")
}

# with_stdin <cmd...>: runs the command with data waiting on its stdin, so a
# command that reads without its own </dev/null would get it.
with_stdin() {
  printf 'x\n' | "$@"
}

# line_no <exact line>: the 1-based line of $output equal to the argument (empty
# when absent), anchored so a substring elsewhere cannot match.
line_no() {
  grep -n -x -F -- "$1" <<<"$output" | head -1 | cut -d: -f1
}

# --- stdin is /dev/null for every command kind (item 7) -------------------

@test "a sizeCommand that reads stdin gets EOF, not the caller's data (mutant: no </dev/null in pgdr_size_kb)" {
  install_registry "$(item a 1 'if read -r g; then echo 99999; else echo 1024; fi' 'echo dry-a' 'echo rm-a')"
  run with_stdin cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ $output == *"a: size: 1.0M"* ]]
}

@test "a heldSizeCommand that reads stdin gets EOF (mutant: no </dev/null for the held size)" {
  install_registry "$(item a 1 'echo 1024' 'echo dry-a' 'echo rm-a' | jq '.heldSizeCommand = "if read -r g; then echo 77777; else echo 2048; fi"')"
  run with_stdin cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ $output == *"a: held, not removable: 2.0M"* ]]
}

@test "a dryRunCommand that reads stdin gets EOF (mutant: no </dev/null at the dry-run site)" {
  install_registry "$(item a 1 'echo 1' 'read -r x && echo dry-got-$x || echo dry-eof' 'echo rm-a')"
  run with_stdin cmd_reclaim --aggressiveness 1
  [[ $output == *"dry-eof"* ]]
  [[ $output != *"-got-"* ]]
}

@test "a removeCommand that reads stdin gets EOF (mutant: no </dev/null at the remove site)" {
  install_registry "$(item a 1 'echo 1' 'echo dry-a' 'read -r x && echo remove-got-$x || echo remove-eof')"
  run with_stdin cmd_reclaim --aggressiveness 1 --apply
  [[ $output == *"remove-eof"* ]]
  [[ $output != *"-got-"* ]]
}

@test "a displayCommand that reads stdin gets EOF, in pgdr_display_output directly (mutant: no </dev/null there)" {
  run with_stdin pgdr_display_output 'read -r x && echo display-got-$x || echo display-eof'
  [[ $output == *"display-eof"* ]]
  [[ $output != *"-got-"* ]]
}

@test "a first item whose sizeCommand drains stdin does not make the second item vanish, dry or --apply" {
  install_registry \
    "$(item a 1 'cat >/dev/null; echo 1024' 'echo dry-a' 'echo rm-a')" \
    "$(item b 1 'echo 2048' 'echo dry-b' 'echo rm-b')"
  run with_stdin cmd_reclaim --aggressiveness 1
  [[ $output == *"a: size: 1.0M"* ]]
  [[ $output == *"b: size: 2.0M"* ]]
  run with_stdin cmd_reclaim --aggressiveness 1 --apply
  [[ $output == *"a: size: 1.0M"* ]]
  [[ $output == *"b: size: 2.0M"* ]]
  [[ $output == *"rm-b"* ]]
}

# --- a registry command cannot corrupt the run (item 4) -------------------

@test "a removeCommand with a for loop, a cd and assignments to the loop's own variable names leaves every later item running (mutant: in-process eval)" {
  install_registry \
    "$(item r1 1 'echo 1' 'echo dry-1' 'for i in x y z; do :; done; n_items=0; apply=0; r_id=(); cd /')" \
    "$(item r2 1 'echo 1' 'echo dry-2' 'pwd >"$TEST_DIR/r2-pwd"; touch "$TEST_DIR/r2-ran"')" \
    "$(item r3 1 'echo 1' 'echo dry-3' 'touch "$TEST_DIR/r3-ran"')"
  local here="$PWD"
  run cmd_reclaim --aggressiveness 1 --apply
  [ "$status" -eq 0 ]
  [ -e "$TEST_DIR/r2-ran" ]
  [ -e "$TEST_DIR/r3-ran" ]
  [ "$(cat "$TEST_DIR/r2-pwd")" = "$here" ]
  [[ $output == *"total reclaimed: 3K (3 sized, 0 unknown)"* ]]
}

@test "a dryRunCommand with a for loop and a cd also leaves later items running" {
  install_registry \
    "$(item r1 1 'echo 1' 'for i in x y z; do :; done; n_items=0; cd /; echo dry-1' 'echo rm-1')" \
    "$(item r2 1 'echo 1' 'echo dry-2' 'echo rm-2')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ $output == *"dry-1"* ]]
  [[ $output == *"dry-2"* ]]
}

# --- sizing is lazy on a dry run, eager under --apply (item 2) ------------

@test "a dry run sizes an item only after the previous item's output was printed (mutant: size every item up front)" {
  install_registry \
    "$(item a 1 'echo 1' 'touch "$TEST_DIR/a-dry-ran"; echo dry-a' 'echo rm-a')" \
    "$(item b 1 'if [ -e "$TEST_DIR/a-dry-ran" ]; then echo 2048; else echo 1; fi' 'echo dry-b' 'echo rm-b')"
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ $output == *"b: size: 2.0M"* ]]
}

@test "--apply sizes every item before the first removeCommand runs" {
  install_registry \
    "$(item a 1 'echo 1' 'echo dry-a' 'touch "$TEST_DIR/a-removed"')" \
    "$(item b 1 'if [ -e "$TEST_DIR/a-removed" ]; then echo 1; else echo 2048; fi' 'echo dry-b' 'echo rm-b')"
  run cmd_reclaim --aggressiveness 1 --apply
  [ "$status" -eq 0 ]
  [[ $output == *"b: size: 2.0M"* ]]
}

@test "a dry run with --verbose prints a missing path's note in registry order, not ahead of every item (mutant: note in pass 1)" {
  install_registry \
    "$(item a 1 'echo 1' 'echo dry-a' 'echo rm-a')" \
    "$(item gone 1 'echo 1' 'echo dry-gone' 'echo rm-gone' "$TEST_DIR/does-not-exist")" \
    "$(item b 1 'echo 1' 'echo dry-b' 'echo rm-b')"
  run cmd_reclaim --aggressiveness 1 --verbose
  [ "$status" -eq 0 ]
  local a gone b
  a=$(grep -n '^dry-a$' <<<"$output" | cut -d: -f1)
  gone=$(grep -n "^pg-disk-reclaimer: 'gone' path .* does not exist" <<<"$output" | cut -d: -f1)
  b=$(grep -n '^dry-b$' <<<"$output" | cut -d: -f1)
  [ -n "$a" ] && [ -n "$gone" ] && [ -n "$b" ]
  [ "$a" -lt "$gone" ]
  [ "$gone" -lt "$b" ]
  [[ $output != *"dry-gone"* ]]
}

# --- the prompt names the plan (items 1 and 13) ---------------------------

PROMPT_HEAD="PROMPT:pg-disk-reclaimer: run --apply for"

@test "the prompt carries the item count and each gated item's id and size, and every plan line precedes it" {
  install_registry \
    "$(item lo 1 'echo 1024' 'echo dry' 'echo rm-lo')" \
    "$(item hi4 4 'echo 2048' 'echo dry' 'echo rm-hi4')" \
    "$(item hi5 5 'echo 3072' 'echo dry' 'echo rm-hi5')"
  pgdr_confirm() {
    echo "PROMPT:$1"
    return 0
  }
  run cmd_reclaim --aggressiveness 5 --apply
  [ "$status" -eq 0 ]
  local prompt_no
  prompt_no=$(line_no "$PROMPT_HEAD 3 item(s), including level 4+ item(s) (hi4: 2.0M, hi5: 3.0M)? This is the only confirmation. [y/N] ")
  [ -n "$prompt_no" ]
  [ "$(grep -c '^PROMPT:' <<<"$output")" -eq 1 ]
  local id
  for id in lo hi4 hi5; do
    [ "$(grep -n -x -E "$id: size: [0-9.]+M" <<<"$output" | head -1 | cut -d: -f1)" -lt "$prompt_no" ]
  done
  [ "$(line_no 'rm-lo')" -gt "$prompt_no" ]
  [ "$(line_no 'rm-hi5')" -gt "$prompt_no" ]
}

@test "a gated item whose path does not exist neither prompts nor counts" {
  install_registry \
    "$(item lo 1 'echo 1024' 'echo dry' 'echo rm-lo')" \
    "$(item gone5 5 'echo 1024' 'echo dry' 'echo rm-gone5' "$TEST_DIR/does-not-exist")"
  pgdr_confirm() {
    echo "PROMPT:$1"
    return 0
  }
  run cmd_reclaim --aggressiveness 5 --apply
  [ "$status" -eq 0 ]
  [[ $output != *"PROMPT:"* ]]
  [[ $output == *"rm-lo"* ]]
  [[ $output != *"gone5"* ]]
}

@test "a missing gated item does not count toward the item total in the prompt of a run that still prompts" {
  install_registry \
    "$(item lo 1 'echo 1024' 'echo dry' 'echo rm-lo')" \
    "$(item hi4 4 'echo 1024' 'echo dry' 'echo rm-hi4')" \
    "$(item gone5 5 'echo 1024' 'echo dry' 'echo rm-gone5' "$TEST_DIR/does-not-exist")"
  pgdr_confirm() {
    echo "PROMPT:$1"
    return 0
  }
  run cmd_reclaim --aggressiveness 5 --apply
  [ "$status" -eq 0 ]
  [ -n "$(line_no "$PROMPT_HEAD 2 item(s), including level 4+ item(s) (hi4: 1.0M)? This is the only confirmation. [y/N] ")" ]
}

@test "a gated item whose sizing fails still prompts, and the prompt says the size is unknown" {
  install_registry \
    "$(item hi4 4 'echo not-a-number' 'echo dry' 'echo rm-hi4')"
  pgdr_confirm() {
    echo "PROMPT:$1"
    return 0
  }
  run cmd_reclaim --aggressiveness 5 --apply
  [ "$status" -eq 0 ]
  [ -n "$(line_no "$PROMPT_HEAD 1 item(s), including level 4+ item(s) (hi4: unknown (size command gave no number))? This is the only confirmation. [y/N] ")" ]
  [[ $output == *"rm-hi4"* ]]
}

# --- a bad explicit id never turns a decline or no terminal into status 1 (item 13)

@test "a bad explicit id plus a decline exits 2 and runs nothing" {
  install_registry \
    "$(item lo 1 'echo 1' 'echo dry' 'echo rm-lo')" \
    "$(item hi4 4 'echo 1' 'echo dry' 'echo rm-hi4')"
  pgdr_confirm() { return 1; }
  run cmd_reclaim --aggressiveness 5 --apply lo hi4 no-such-id
  [ "$status" -eq 2 ]
  [[ $output == *"not confirmed; nothing was run"* ]]
  [[ $output != *"rm-"* ]]
}

@test "a bad explicit id plus no terminal exits 3 and runs nothing" {
  install_registry \
    "$(item lo 1 'echo 1' 'echo dry' 'echo rm-lo')" \
    "$(item hi4 4 'echo 1' 'echo dry' 'echo rm-hi4')"
  pgdr_confirm() { return 2; }
  run cmd_reclaim --aggressiveness 5 --apply lo hi4 no-such-id
  [ "$status" -eq 3 ]
  [[ $output == *"no terminal to confirm level 4+ item(s) (hi4); nothing was run"* ]]
  [[ $output != *"rm-"* ]]
}

# --- the call shape survives the shipped strict-mode wrapper (item 9) -----

# strict_reclaim <pgdr_confirm-status>: cmd_reclaim as the LAST command of a
# `bash -euo pipefail` script, the way the nix wrapper runs it, with pgdr_confirm
# returning the given status. No `||` or `if` around the call: either would turn
# errexit off inside the function and hide the defect this guards.
strict_reclaim() {
  bash -euo pipefail -c '
    source "$1"
    stub_status="$2"
    pgdr_confirm() { return "$stub_status"; }
    cmd_reclaim --aggressiveness 5 --apply
  ' _ "$SCRIPTS_DIR/pg-disk-reclaimer.bash" "$1"
}

@test "under set -euo pipefail a decline exits 2, not 1 (mutant: pgdr_confirm ...; confirm_status=\$?)" {
  install_registry "$(item hi4 4 'echo 1' 'echo dry' 'echo rm-hi4')"
  run strict_reclaim 1
  [ "$status" -eq 2 ]
  [[ $output == *"not confirmed"* ]]
}

@test "under set -euo pipefail no terminal exits 3, not 1" {
  install_registry "$(item hi4 4 'echo 1' 'echo dry' 'echo rm-hi4')"
  run strict_reclaim 2
  [ "$status" -eq 3 ]
  [[ $output == *"no terminal to confirm"* ]]
}

@test "under set -euo pipefail a confirmation runs the items and exits 0" {
  install_registry "$(item hi4 4 'echo 1' 'echo dry' 'echo rm-hi4')"
  run strict_reclaim 0
  [ "$status" -eq 0 ]
  [[ $output == *"rm-hi4"* ]]
}

# --- answer parsing (item 10) ---------------------------------------------

@test "pgdr_reply_is_yes accepts exactly y, Y, yes in any case" {
  local reply
  for reply in y Y yes YES Yes yEs; do
    pgdr_reply_is_yes "$reply" || {
      echo "accepted-set miss: '$reply'" >&2
      return 1
    }
  done
}

@test "pgdr_reply_is_yes rejects empty, n, no, yep, ye, yy, a leading space and trailing text" {
  local reply
  for reply in "" n N no NO yep ye yy " y" "y " "yes please" yes. 1 true; do
    if pgdr_reply_is_yes "$reply"; then
      echo "rejected-set hit: '$reply'" >&2
      return 1
    fi
  done
}
