#!/usr/bin/env bats
# Unit tests for the bash completion script's registry-driven item-id
# helper (completions/pg-disk-reclaimer.bash, bead pg2-txxyj.7).
# _pg_disk_reclaimer_item_ids is plain bash+jq and is tested directly here
# without loading the bash-completion framework (_init_completion et al.)
# -- this suite never calls the top-level _pg_disk_reclaimer completion
# function, only the id-extraction helper it delegates to.

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  source "$SCRIPTS_DIR/completions/pg-disk-reclaimer.bash"

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

@test "_pg_disk_reclaimer_item_ids prints nothing when the registry file is missing" {
  run _pg_disk_reclaimer_item_ids
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "_pg_disk_reclaimer_item_ids prints every item id from the default (XDG) registry path" {
  mkdir -p "$HOME/.config/pg-disk-reclaimer"
  cp "$FIXTURES_DIR/valid.json" "$HOME/.config/pg-disk-reclaimer/registry.json"
  run _pg_disk_reclaimer_item_ids
  [ "$status" -eq 0 ]
  [ "$output" = "$(printf 'npm-cache\nbrew-prefix-info')" ]
}

@test "_pg_disk_reclaimer_item_ids honors XDG_CONFIG_HOME" {
  export XDG_CONFIG_HOME="$TEST_DIR/xdg-config"
  mkdir -p "$XDG_CONFIG_HOME/pg-disk-reclaimer"
  cp "$FIXTURES_DIR/valid.json" "$XDG_CONFIG_HOME/pg-disk-reclaimer/registry.json"
  run _pg_disk_reclaimer_item_ids
  [ "$status" -eq 0 ]
  [ "$output" = "$(printf 'npm-cache\nbrew-prefix-info')" ]
}

@test "_pg_disk_reclaimer_item_ids prints nothing on malformed JSON rather than erroring the completion" {
  mkdir -p "$HOME/.config/pg-disk-reclaimer"
  cat >"$HOME/.config/pg-disk-reclaimer/registry.json" <<'JSON'
not valid json
JSON
  run _pg_disk_reclaimer_item_ids
  [ -z "$output" ]
}
