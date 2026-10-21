#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Verify claude-settings-prune-allow.sh:
#   - deletes exact-string entries from .permissions.allow only
#   - keeps the order of the remaining entries and every other key
#   - reports removed=<n> not-found=<m> plus one line per removed entry
#   - backs the settings file up (pre-allow-prune) only when something is removed
#   - is idempotent, honours --dry-run, and exits 2 on invalid JSON

bats_require_minimum_version 1.5.0

load test_helper

# Resolve the script: prefer the packaged binary on PATH (Nix build sandbox
# via testBashScripts), fall back to a lib-sourcing wrapper around the sibling
# source script for direct dev-time runs (`bats tests/`).
SCRIPT="$(resolve_claude_settings_script claude-settings-prune-allow)"

setup() {
  TMP="$(mktemp -d)"
  export TMP
  SETTINGS="$TMP/settings.json"
  REMOVAL="$TMP/removal.json"
  export SETTINGS REMOVAL
}

teardown() {
  [ -n "$TMP" ] && rm -rf "$TMP"
}

# Number of backup files next to the settings file.
_backup_count() {
  find "$TMP" -maxdepth 1 -name 'settings.json.pre-allow-prune.*' | wc -l | tr -d ' '
}

@test "removes exact-string entries and keeps the order of the rest" {
  echo '{"permissions":{"allow":["a","b","c","d","e"]}}' >"$SETTINGS"
  echo '["b","d"]' >"$REMOVAL"
  run "$SCRIPT" "$SETTINGS" "$REMOVAL"
  [ "$status" -eq 0 ]
  [[ "$output" == *"removed=2 not-found=0"* ]]
  [[ "$output" == *"b"* ]]
  [[ "$output" == *"d"* ]]
  [ "$(jq -c '.permissions.allow' "$SETTINGS")" = '["a","c","e"]' ]
}

@test "entries not present are counted as not-found and do not fail" {
  echo '{"permissions":{"allow":["a","b"]}}' >"$SETTINGS"
  echo '["b","zzz","yyy"]' >"$REMOVAL"
  run "$SCRIPT" "$SETTINGS" "$REMOVAL"
  [ "$status" -eq 0 ]
  [[ "$output" == *"removed=1 not-found=2"* ]]
  [ "$(jq -c '.permissions.allow' "$SETTINGS")" = '["a"]' ]
}

@test "a second run removes nothing" {
  echo '{"permissions":{"allow":["a","b","c"]}}' >"$SETTINGS"
  echo '["b"]' >"$REMOVAL"
  run "$SCRIPT" "$SETTINGS" "$REMOVAL"
  [ "$status" -eq 0 ]
  [[ "$output" == *"removed=1 "* ]]
  after_first="$(cat "$SETTINGS")"
  run "$SCRIPT" "$SETTINGS" "$REMOVAL"
  [ "$status" -eq 0 ]
  [[ "$output" == *"removed=0 not-found=1"* ]]
  [ "$(cat "$SETTINGS")" = "$after_first" ]
}

@test "--dry-run changes nothing and creates no backup" {
  echo '{"permissions":{"allow":["a","b","c"]}}' >"$SETTINGS"
  echo '["b"]' >"$REMOVAL"
  before="$(cat "$SETTINGS")"
  run "$SCRIPT" "$SETTINGS" "$REMOVAL" --dry-run
  [ "$status" -eq 0 ]
  [[ "$output" == *"removed=1 not-found=0"* ]]
  [ "$(cat "$SETTINGS")" = "$before" ]
  [ "$(_backup_count)" -eq 0 ]
}

@test "writes a pre-allow-prune backup only when something was removed" {
  echo '{"permissions":{"allow":["a","b"]}}' >"$SETTINGS"
  echo '["nothere"]' >"$REMOVAL"
  run "$SCRIPT" "$SETTINGS" "$REMOVAL"
  [ "$status" -eq 0 ]
  [ "$(_backup_count)" -eq 0 ]

  before="$(cat "$SETTINGS")"
  echo '["a"]' >"$REMOVAL"
  run "$SCRIPT" "$SETTINGS" "$REMOVAL"
  [ "$status" -eq 0 ]
  [ "$(_backup_count)" -eq 1 ]
  backup="$(find "$TMP" -maxdepth 1 -name 'settings.json.pre-allow-prune.*')"
  [ "$(cat "$backup")" = "$before" ]
}

@test "matches whole strings only, never substrings" {
  echo '{"permissions":{"allow":["Bash(git:*)","Bash(git status)","git"]}}' >"$SETTINGS"
  echo '["git","Bash(git"]' >"$REMOVAL"
  run "$SCRIPT" "$SETTINGS" "$REMOVAL"
  [ "$status" -eq 0 ]
  [[ "$output" == *"removed=1 not-found=1"* ]]
  [ "$(jq -c '.permissions.allow' "$SETTINGS")" = '["Bash(git:*)","Bash(git status)"]' ]
}

@test "preserves all other settings keys and other permissions keys" {
  cat >"$SETTINGS" <<'JSON'
{
  "model": "opus",
  "env": {"A": "1"},
  "permissions": {
    "allow": ["a", "b"],
    "deny": ["x"],
    "defaultMode": "plan"
  },
  "autoMode": {"allow": ["b"]}
}
JSON
  echo '["b"]' >"$REMOVAL"
  run "$SCRIPT" "$SETTINGS" "$REMOVAL"
  [ "$status" -eq 0 ]
  [ "$(jq -c '.permissions.allow' "$SETTINGS")" = '["a"]' ]
  [ "$(jq -c '.permissions.deny' "$SETTINGS")" = '["x"]' ]
  [ "$(jq -r '.permissions.defaultMode' "$SETTINGS")" = 'plan' ]
  [ "$(jq -r '.model' "$SETTINGS")" = 'opus' ]
  [ "$(jq -c '.env' "$SETTINGS")" = '{"A":"1"}' ]
  [ "$(jq -c '.autoMode' "$SETTINGS")" = '{"allow":["b"]}' ]
}

@test "handles entries containing quotes, backslashes, dollar signs and pipes" {
  cat >"$SETTINGS" <<'JSON'
{"permissions":{"allow":["Bash(echo \"hi\")","Bash(a\\b)","Bash(echo $HOME)","Bash(cat x | grep y)","keep"]}}
JSON
  cat >"$REMOVAL" <<'JSON'
["Bash(echo \"hi\")","Bash(a\\b)","Bash(echo $HOME)","Bash(cat x | grep y)"]
JSON
  run "$SCRIPT" "$SETTINGS" "$REMOVAL"
  [ "$status" -eq 0 ]
  [[ "$output" == *"removed=4 not-found=0"* ]]
  [[ "$output" == *'Bash(echo $HOME)'* ]]
  [[ "$output" == *'Bash(cat x | grep y)'* ]]
  [ "$(jq -c '.permissions.allow' "$SETTINGS")" = '["keep"]' ]
}

@test "exits 2 and leaves the file byte-identical on invalid JSON" {
  printf '{"permissions":{"allow":["a",' >"$SETTINGS"
  echo '["a"]' >"$REMOVAL"
  before="$(cksum <"$SETTINGS")"
  run "$SCRIPT" "$SETTINGS" "$REMOVAL"
  [ "$status" -eq 2 ]
  [ "$(cksum <"$SETTINGS")" = "$before" ]
  [ "$(_backup_count)" -eq 0 ]

  # Invalid JSON in the removal file as well.
  echo '{"permissions":{"allow":["a"]}}' >"$SETTINGS"
  printf '["a"' >"$REMOVAL"
  before="$(cksum <"$SETTINGS")"
  run "$SCRIPT" "$SETTINGS" "$REMOVAL"
  [ "$status" -eq 2 ]
  [ "$(cksum <"$SETTINGS")" = "$before" ]
  [ "$(_backup_count)" -eq 0 ]
}
