#!/usr/bin/env bats
# Unit tests for the bash completion script's registry-driven item-id
# helpers (completions/pg-disk-reclaimer.bash, beads pg2-txxyj.7/pg2-d2lo7).
# _pg_disk_reclaimer_item_ids and _pg_disk_reclaimer_complete_item_ids are
# plain bash+jq and are tested directly here without loading the
# bash-completion framework (_init_completion et al.) -- this suite never
# calls the top-level _pg_disk_reclaimer completion function.

load 'hermetic'

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi

  # Standard test isolation (tests/hermetic.bash): every location the code reads
  # from the environment points under TEST_DIR, XDG_CONFIG_HOME starts unset so
  # default-path tests aren't at the mercy of the outer environment, and the test
  # fails here, before anything runs, if that did not hold.
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  pgdr_test_isolate "$TEST_DIR"
  pgdr_test_assert_hermetic "$TEST_DIR"

  source "$SCRIPTS_DIR/completions/pg-disk-reclaimer.bash"

  FIXTURES_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/fixtures" && pwd)"
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

@test "_pg_disk_reclaimer_complete_item_ids offers every id with no prefix filter" {
  mkdir -p "$HOME/.config/pg-disk-reclaimer"
  cp "$FIXTURES_DIR/valid.json" "$HOME/.config/pg-disk-reclaimer/registry.json"
  _pg_disk_reclaimer_complete_item_ids ""
  [ "${#COMPREPLY[@]}" -eq 2 ]
  [ "${COMPREPLY[0]}" = "npm-cache" ]
  [ "${COMPREPLY[1]}" = "brew-prefix-info" ]
}

@test "_pg_disk_reclaimer_complete_item_ids filters to ids matching the given prefix" {
  mkdir -p "$HOME/.config/pg-disk-reclaimer"
  cp "$FIXTURES_DIR/valid.json" "$HOME/.config/pg-disk-reclaimer/registry.json"
  _pg_disk_reclaimer_complete_item_ids "npm"
  [ "${#COMPREPLY[@]}" -eq 1 ]
  [ "${COMPREPLY[0]}" = "npm-cache" ]
}

@test "_pg_disk_reclaimer_complete_item_ids treats a command-substitution-shaped id as a literal candidate, never executing it (regression for pg2-d2lo7)" {
  # pg2-d2lo7: the original implementation piped ids through
  # `compgen -W "$ids" -- "$cur"`, and compgen -W EXPANDS each word in its
  # list -- including command substitution -- so an id like
  # "$(touch ...)" executed the moment a user tab-completed `reclaim`.
  # This test proves the fix: the crafted id comes back as an inert
  # string, and nothing on disk gets created.
  local marker="$TEST_DIR/pwned-marker"
  mkdir -p "$HOME/.config/pg-disk-reclaimer"
  cat >"$HOME/.config/pg-disk-reclaimer/registry.json" <<JSON
[{"id": "\$(touch $marker)", "description": "x", "path": "/tmp/x", "displayCommand": "echo x", "variants": []}]
JSON
  _pg_disk_reclaimer_complete_item_ids ""
  [ "${#COMPREPLY[@]}" -eq 1 ]
  [ "${COMPREPLY[0]}" = '$(touch '"$marker"')' ]
  [ ! -e "$marker" ]
}

# --- go-cache subcommands -----------------------------------------------------

# complete_words <words...>: runs the bash completion with a minimal stand-in
# for bash-completion's _init_completion.
complete_words() {
  # shellcheck disable=SC2034 # cur, prev and words are read by _pg_disk_reclaimer
  _init_completion() {
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD - 1]}"
    words=("${COMP_WORDS[@]}")
    return 0
  }
  COMP_WORDS=("$@")
  COMP_CWORD=$(($# - 1))
  COMPREPLY=()
  _pg_disk_reclaimer
}

@test "the subcommand word list completes go-cache-trim and go-cache-wipe" {
  complete_words pg-disk-reclaimer go-
  [[ " ${COMPREPLY[*]} " == *" go-cache-trim "* ]]
  [[ " ${COMPREPLY[*]} " == *" go-cache-wipe "* ]]
}

@test "go-cache-trim completes its flags" {
  complete_words pg-disk-reclaimer go-cache-trim --
  [[ " ${COMPREPLY[*]} " == *" --cache "* ]]
  [[ " ${COMPREPLY[*]} " == *" --older-than-days "* ]]
  [[ " ${COMPREPLY[*]} " == *" --dry-run "* ]]
}

@test "the zsh completion lists both go-cache subcommands and their flags" {
  local f="$SCRIPTS_DIR/completions/_pg-disk-reclaimer"
  grep -q 'go-cache-trim go-cache-wipe' "$f"
  grep -q -- '--older-than-days' "$f"
  grep -q 'go-cache-wipe)' "$f"
}
