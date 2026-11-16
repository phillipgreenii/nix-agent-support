#!/usr/bin/env bats
# bats file_tags=type:unit
# The skipped outcome: a registry command that exits 75 (EX_TEMPFAIL) is
# "skipped, retry later" in a dry run and under --apply: not a failure, run
# status 0, and its size is not added to the reclaimed total.
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

# install <dry-exit> <remove-exit>: one skip-capable item (4096 KiB) plus one
# plain item (1024 KiB) whose commands succeed.
install() {
  jq -n --arg path "$TEST_DIR/area" --arg dry "echo dry-out; exit $1" --arg rm "echo rm-out; exit $2" '[
    {id: "skippy", description: "skip-capable", path: $path, displayCommand: "true",
     sizeCommand: "echo 4096",
     variants: [{aggressiveness: 1, variantDescription: "v", dryRunCommand: $dry, removeCommand: $rm}]},
    {id: "plain", description: "plain", path: $path, displayCommand: "true",
     sizeCommand: "echo 1024",
     variants: [{aggressiveness: 1, variantDescription: "v", dryRunCommand: "echo ok", removeCommand: "true"}]}]' \
    >"$HOME/.config/pg-disk-reclaimer/registry.json"
}

@test "apply: exit 75 is skipped, status 0, size not in the reclaimed total" {
  install 0 75
  run --separate-stderr cmd_reclaim --aggressiveness 1 --apply
  [ "$status" -eq 0 ]
  [[ $stderr == *"remove command for 'skippy' skipped (exit 75)"* ]]
  [[ $stderr != *"exited non-zero"* ]]
  # 1024 KiB = 1.0M only; the 4096 KiB of the skipped item is absent
  [[ $output == *"total reclaimed: 1.0M (1 sized, 0 unknown)"* ]]
  [[ $output != *"failed"* ]]
}

@test "dry run: exit 75 is skipped, not failed, status 0, size not in the total" {
  install 75 0
  run --separate-stderr cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [[ $stderr == *"dry-run command for 'skippy' skipped (exit 75)"* ]]
  [[ $stderr != *"exited non-zero"* ]]
  [[ $output == *"total reclaimable: 1.0M (1 sized, 0 unknown)"* ]]
}

@test "exit 1 stays failed, in a dry run and under --apply" {
  install 1 1
  run --separate-stderr cmd_reclaim --aggressiveness 1
  [ "$status" -eq 1 ]
  [[ $stderr == *"dry-run command for 'skippy' exited non-zero"* ]]
  run --separate-stderr cmd_reclaim --aggressiveness 1 --apply
  [ "$status" -eq 1 ]
  [[ $stderr == *"remove command for 'skippy' exited non-zero"* ]]
}

@test "a run where only skipped items ran prints no total and exits 0" {
  jq -n --arg path "$TEST_DIR/area" '[
    {id: "only", description: "d", path: $path, displayCommand: "true", sizeCommand: "echo 4096",
     variants: [{aggressiveness: 1, variantDescription: "v", dryRunCommand: "exit 75", removeCommand: "exit 75"}]}]' \
    >"$HOME/.config/pg-disk-reclaimer/registry.json"
  run cmd_reclaim --aggressiveness 1 --apply
  [ "$status" -eq 0 ]
  [[ $output != *"total"* ]]
}
