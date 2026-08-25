#!/usr/bin/env bats
# Unit tests for pg-disk-reclaimer's core subcommand functions
# (pg-disk-reclaimer.bash). cmd_list/cmd_validate/cmd_reclaim remain stubs
# here (bead pg2-txxyj.1) -- real bodies land with tasks pg2-txxyj.4/.5/.6.
# The registry loading + schema validation engine (pgdr_default_registry_path
# / pgdr_validate_registry / pgdr_read_registry) is exercised below against
# fixtures under tests/fixtures/ (bead pg2-txxyj.2).

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  source "$SCRIPTS_DIR/pg-disk-reclaimer.bash"

  FIXTURES_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/fixtures" && pwd)"

  # Standard test isolation (bash-scripting skill): isolate HOME, and start
  # with XDG_CONFIG_HOME unset so default-path tests aren't at the mercy of
  # whatever the outer environment happens to have set.
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  export REAL_HOME="${HOME:-}"
  export HOME="$TEST_DIR/home"
  mkdir -p "$HOME"
  unset XDG_CONFIG_HOME
}

teardown() {
  rm -rf "$TEST_DIR"
}

@test "cmd_list is defined and fails (not implemented yet)" {
  run cmd_list
  [ "$status" -eq 1 ]
  [[ "$output" =~ "not implemented yet" ]]
}

@test "cmd_validate is defined and fails (not implemented yet)" {
  run cmd_validate
  [ "$status" -eq 1 ]
  [[ "$output" =~ "not implemented yet" ]]
}

@test "cmd_reclaim is defined and fails (not implemented yet)" {
  run cmd_reclaim
  [ "$status" -eq 1 ]
  [[ "$output" =~ "not implemented yet" ]]
}

@test "pgdr_default_registry_path honors XDG_CONFIG_HOME" {
  export XDG_CONFIG_HOME="$TEST_DIR/xdg-config"
  run pgdr_default_registry_path
  [ "$status" -eq 0 ]
  [ "$output" = "$TEST_DIR/xdg-config/pg-disk-reclaimer/registry.json" ]
}

@test "pgdr_default_registry_path falls back to \$HOME/.config" {
  run pgdr_default_registry_path
  [ "$status" -eq 0 ]
  [ "$output" = "$HOME/.config/pg-disk-reclaimer/registry.json" ]
}

@test "pgdr_validate_registry accepts a fully valid registry" {
  run pgdr_validate_registry "$FIXTURES_DIR/valid.json"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "pgdr_validate_registry accepts an item with an empty variants array" {
  run pgdr_validate_registry "$FIXTURES_DIR/empty-variants.json"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "pgdr_validate_registry rejects malformed JSON" {
  # Malformed JSON is generated into TEST_DIR at test time rather than kept
  # as a committed tests/fixtures/*.json file: this repo's treefmt/prettier
  # pre-commit hook parses and reformats every committed *.json file, and it
  # silently REPAIRS a trailing-comma-style syntax error (dropping the comma)
  # instead of erroring -- so a committed "malformed" fixture would stop
  # being malformed the moment it was formatted.
  local malformed="$TEST_DIR/malformed.json"
  cat >"$malformed" <<'JSON'
[
  {
    "id": "broken",
    "description": "broken json",
    "path": "/tmp/broken",
    "displayCommand": "echo broken",
    "variants": [],
  }
]
JSON
  run pgdr_validate_registry "$malformed"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "not valid JSON" ]]
}

@test "pgdr_validate_registry rejects an item missing a required field" {
  run pgdr_validate_registry "$FIXTURES_DIR/missing-field.json"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "missing a non-empty id/description/path/displayCommand" ]]
}

@test "pgdr_validate_registry rejects duplicate ids across items" {
  run pgdr_validate_registry "$FIXTURES_DIR/duplicate-id.json"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "duplicate id" ]]
}

@test "pgdr_validate_registry rejects duplicate aggressiveness within one item's variants" {
  run pgdr_validate_registry "$FIXTURES_DIR/duplicate-aggressiveness.json"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "duplicate variant aggressiveness" ]]
}

@test "pgdr_read_registry fails loudly when the registry file is missing" {
  run pgdr_read_registry "$TEST_DIR/does-not-exist.json"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "registry file not found" ]]
}

@test "pgdr_read_registry fails loudly on a malformed registry" {
  local malformed="$TEST_DIR/malformed.json"
  cat >"$malformed" <<'JSON'
[
  {
    "id": "broken",
    "description": "broken json",
    "path": "/tmp/broken",
    "displayCommand": "echo broken",
    "variants": [],
  }
]
JSON
  run pgdr_read_registry "$malformed"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "not valid JSON" ]]
}

@test "pgdr_read_registry prints the parsed registry on success" {
  run pgdr_read_registry "$FIXTURES_DIR/valid.json"
  [ "$status" -eq 0 ]
  [[ "$output" =~ "npm-cache" ]]
}

@test "pgdr_read_registry defaults to the XDG registry path when none is given" {
  mkdir -p "$HOME/.config/pg-disk-reclaimer"
  cp "$FIXTURES_DIR/valid.json" "$HOME/.config/pg-disk-reclaimer/registry.json"
  run pgdr_read_registry
  [ "$status" -eq 0 ]
  [[ "$output" =~ "npm-cache" ]]
}
