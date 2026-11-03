#!/usr/bin/env bats
# bats file_tags=type:unit
# `auto`: the free-space-driven level selection (pure logic, nothing deleted)
# and the one-line run log. Registry commands only touch a temp root.
bats_require_minimum_version 1.5.0
load 'hermetic'

GB=1048576

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
  touch "$TEST_DIR/area/victim"
  jq -n --arg path "$TEST_DIR/area" --arg v "$TEST_DIR/area/victim" '[
    {id: "a", description: "auto item", path: $path, displayCommand: "true",
     sizeCommand: "echo 1",
     variants: [{aggressiveness: 1, variantDescription: "v",
                 dryRunCommand: "echo dry", removeCommand: ("rm -f " + $v)}]}]' \
    >"$HOME/.config/pg-disk-reclaimer/registry.json"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# level <free-gb>: selection with the default 60/45/30 thresholds.
level() {
  pgdr_auto_level_for_free_kb "$(($1 * GB))" 60 45 30
}

@test "free space at or above the first threshold selects nothing" {
  run level 60
  [ "$status" -eq 0 ]
  [ "$output" = none ]
  run level 500
  [ "$output" = none ]
}

@test "just below each threshold selects level 1, 2, 3" {
  run level 59
  [ "$output" = 1 ]
  run level 44
  [ "$output" = 2 ]
  run level 29
  [ "$output" = 3 ]
}

@test "a threshold is exclusive: exactly 45 GiB free is level 1, exactly 30 is level 2" {
  run level 45
  [ "$output" = 1 ]
  run level 30
  [ "$output" = 2 ]
}

@test "zero free space selects level 3, never 4 or 5" {
  run level 0
  [ "$output" = 3 ]
}

@test "custom thresholds are honoured" {
  run pgdr_auto_level_for_free_kb "$((20 * GB))" 100 50 25
  [ "$output" = 3 ]
  run pgdr_auto_level_for_free_kb "$((70 * GB))" 100 50 25
  [ "$output" = 1 ]
}

@test "increasing thresholds and non-numeric input are rejected" {
  run --separate-stderr pgdr_auto_level_for_free_kb 1 30 45 60
  [ "$status" -eq 1 ]
  run --separate-stderr pgdr_auto_level_for_free_kb 1 sixty 45 30
  [ "$status" -eq 1 ]
  run --separate-stderr pgdr_auto_level_for_free_kb abc 60 45 30
  [ "$status" -eq 1 ]
}

@test "auto without --apply is a dry run: removes nothing and logs the chosen level" {
  PGDR_AUTO_FREE_KB=$((50 * GB)) run --separate-stderr cmd_auto
  [ "$status" -eq 0 ]
  [ -e "$TEST_DIR/area/victim" ]
  [ "$(jq -r .level_chosen <<<"$output")" = 1 ]
  [ "$(jq -r .applied <<<"$output")" = false ]
  [ "$(jq -r .alert <<<"$output")" = false ]
  [ "$(jq -r .free_kb_before <<<"$output")" = "$((50 * GB))" ]
}

@test "auto --apply removes at the chosen level and logs free space before, after and bytes reclaimed" {
  # the removeCommand frees nothing real, so make the "after" reading differ by
  # reading a counter file instead of a fixed override
  export PGDR_AUTO_FREE_KB=$((50 * GB))
  pgdr_auto_free_kb() {
    if [[ -e "$TEST_DIR/area/victim" ]]; then echo "$((50 * GB))"; else echo "$((52 * GB))"; fi
  }
  run --separate-stderr cmd_auto --apply
  [ "$status" -eq 0 ]
  [ ! -e "$TEST_DIR/area/victim" ]
  [ "$(jq -r .free_kb_after <<<"$output")" = "$((52 * GB))" ]
  [ "$(jq -r .bytes_reclaimed <<<"$output")" = "$((2 * GB * 1024))" ]
  [ "$(jq -r .applied <<<"$output")" = true ]
}

@test "free space above every threshold does nothing even with --apply" {
  PGDR_AUTO_FREE_KB=$((100 * GB)) run --separate-stderr cmd_auto --apply
  [ "$status" -eq 0 ]
  [ -e "$TEST_DIR/area/victim" ]
  [ "$(jq -r .level_chosen <<<"$output")" = null ]
}

@test "below the lowest threshold raises an alert (ERROR line)" {
  PGDR_AUTO_FREE_KB=$((10 * GB)) run --separate-stderr cmd_auto
  [ "$status" -eq 0 ]
  [ "$(jq -r .level_chosen <<<"$output")" = 3 ]
  [ "$(jq -r .alert <<<"$output")" = true ]
  [ "$(jq -r .level <<<"$output")" = ERROR ]
}

@test "thresholds can be overridden on the command line" {
  PGDR_AUTO_FREE_KB=$((80 * GB)) run --separate-stderr cmd_auto --below1-gb 100 --below2-gb 90 --below3-gb 85
  [ "$status" -eq 0 ]
  [ "$(jq -r .level_chosen <<<"$output")" = 3 ]
  [ "$(jq -r .alert <<<"$output")" = true ]
}
