#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Verify claude-settings-replace-auto-mode.sh:
#   - owns the autoMode keys present in the desired JSON (["$defaults"] + desired)
#   - leaves absent keys, unrelated autoMode keys and other settings untouched
#   - saves dropped entries to a timestamped removal file and warns
#   - is idempotent (no removal file, no warning on an unchanged second run)
#   - preserves awkward characters byte-for-byte
#   - handles a missing file, {}, and a file without autoMode
#   - exits 2 and leaves settings.json byte-identical on invalid JSON

bats_require_minimum_version 1.5.0

load test_helper

# Resolve the script: prefer the packaged binary on PATH (Nix build sandbox
# via testBashScripts), fall back to a lib-sourcing wrapper around the sibling
# source script for direct dev-time runs (`bats tests/`).
SCRIPT="$(resolve_claude_settings_script claude-settings-replace-auto-mode)"

setup() {
  TMP="$(mktemp -d)"
  export TMP
  SETTINGS="$TMP/settings.json"
  DESIRED="$TMP/desired.json"
  REMOVED_DIR="$TMP/removed"
  export SETTINGS DESIRED REMOVED_DIR
}

teardown() {
  [ -n "$TMP" ] && rm -rf "$TMP"
}

# Count removal files (".claude-settings-removed-*.json") in REMOVED_DIR.
_removed_count() {
  if [ ! -d "$REMOVED_DIR" ]; then
    echo 0
    return 0
  fi
  find "$REMOVED_DIR" -maxdepth 1 -name '.claude-settings-removed-*.json' | wc -l | tr -d ' '
}

# Print the path of the single removal file (asserts exactly one exists).
_removed_file() {
  local files
  mapfile -t files < <(find "$REMOVED_DIR" -maxdepth 1 -name '.claude-settings-removed-*.json')
  [ "${#files[@]}" -eq 1 ] || { echo "expected 1 removal file, found ${#files[@]}" >&2; return 1; }
  printf '%s\n' "${files[0]}"
}

@test "writes \$defaults plus the desired entries for each owned key" {
  echo '{}' >"$SETTINGS"
  cat >"$DESIRED" <<'JSON'
{"environment":["env one","env two"],"allow":["allow one"],"soft_deny":["deny one"]}
JSON

  run "$SCRIPT" "$SETTINGS" "$DESIRED" "$REMOVED_DIR"

  [ "$status" -eq 0 ]
  [ "$(jq -c '.autoMode.environment' "$SETTINGS")" = '["$defaults","env one","env two"]' ]
  [ "$(jq -c '.autoMode.allow' "$SETTINGS")" = '["$defaults","allow one"]' ]
  [ "$(jq -c '.autoMode.soft_deny' "$SETTINGS")" = '["$defaults","deny one"]' ]
  [ "$(_removed_count)" -eq 0 ]
}

@test "an owned key with an empty desired list becomes exactly [\$defaults]" {
  cat >"$SETTINGS" <<'JSON'
{"autoMode":{"allow":["old entry"],"soft_deny":["$defaults","old deny"]}}
JSON
  echo '{"allow":[],"soft_deny":[]}' >"$DESIRED"

  run "$SCRIPT" "$SETTINGS" "$DESIRED" "$REMOVED_DIR"

  [ "$status" -eq 0 ]
  [ "$(jq -c '.autoMode.allow' "$SETTINGS")" = '["$defaults"]' ]
  [ "$(jq -c '.autoMode.soft_deny' "$SETTINGS")" = '["$defaults"]' ]
}

@test "an absent desired key leaves that autoMode key untouched" {
  cat >"$SETTINGS" <<'JSON'
{"autoMode":{"environment":["keep env"],"allow":["old allow"]}}
JSON
  echo '{"allow":["new allow"]}' >"$DESIRED"

  run "$SCRIPT" "$SETTINGS" "$DESIRED" "$REMOVED_DIR"

  [ "$status" -eq 0 ]
  [ "$(jq -c '.autoMode.environment' "$SETTINGS")" = '["keep env"]' ]
  [ "$(jq -c '.autoMode.allow' "$SETTINGS")" = '["$defaults","new allow"]' ]
  # The untouched key must not show up in the removal file either.
  [ "$(_removed_file | xargs jq -c '.autoMode | keys')" = '["allow"]' ]
}

@test "leaves unrelated autoMode keys and all other settings keys untouched" {
  cat >"$SETTINGS" <<'JSON'
{
  "model": "opus",
  "permissions": {"allow": ["Bash(ls:*)"]},
  "autoMode": {"classifyAllShell": true, "hard_deny": ["never this"], "allow": ["old"]}
}
JSON
  echo '{"allow":["new"]}' >"$DESIRED"

  run "$SCRIPT" "$SETTINGS" "$DESIRED" "$REMOVED_DIR"

  [ "$status" -eq 0 ]
  [ "$(jq -r '.model' "$SETTINGS")" = "opus" ]
  [ "$(jq -c '.permissions' "$SETTINGS")" = '{"allow":["Bash(ls:*)"]}' ]
  [ "$(jq -c '.autoMode.classifyAllShell' "$SETTINGS")" = "true" ]
  [ "$(jq -c '.autoMode.hard_deny' "$SETTINGS")" = '["never this"]' ]
  [ "$(jq -c '.autoMode.allow' "$SETTINGS")" = '["$defaults","new"]' ]
}

@test "saves dropped entries to .claude-settings-removed-<ts>-<pid>.json under autoMode.<key>" {
  cat >"$SETTINGS" <<'JSON'
{"autoMode":{"environment":["$defaults","keep","drop env"],"allow":["drop allow"],"soft_deny":["keep deny"]}}
JSON
  echo '{"environment":["keep"],"allow":[],"soft_deny":["keep deny"]}' >"$DESIRED"

  run "$SCRIPT" "$SETTINGS" "$DESIRED" "$REMOVED_DIR"

  [ "$status" -eq 0 ]
  [ "$(_removed_count)" -eq 1 ]
  removed_file="$(_removed_file)"
  [[ "$(basename "$removed_file")" =~ ^\.claude-settings-removed-[0-9]{8}T[0-9]{6}Z-[0-9]+\.json$ ]]
  [ "$(jq -c '.autoMode.environment' "$removed_file")" = '["drop env"]' ]
  [ "$(jq -c '.autoMode.allow' "$removed_file")" = '["drop allow"]' ]
  # soft_deny dropped nothing, so it has no key in the removal file.
  [ "$(jq -r '.autoMode.soft_deny // "absent"' "$removed_file")" = "absent" ]
  # The activation output warns about the removal.
  [[ "$output" == *"REMOVED"* ]]
}

@test "a second run with the same input writes no removal file and no warning" {
  cat >"$SETTINGS" <<'JSON'
{"autoMode":{"allow":["drop me"]}}
JSON
  echo '{"environment":["e"],"allow":["a"],"soft_deny":[]}' >"$DESIRED"

  run "$SCRIPT" "$SETTINGS" "$DESIRED" "$REMOVED_DIR"
  [ "$status" -eq 0 ]
  [ "$(_removed_count)" -eq 1 ]
  first="$(cat "$SETTINGS")"
  rm -f "$REMOVED_DIR"/.claude-settings-removed-*.json

  run "$SCRIPT" "$SETTINGS" "$DESIRED" "$REMOVED_DIR"
  [ "$status" -eq 0 ]
  [ "$(_removed_count)" -eq 0 ]
  [[ "$output" != *"REMOVED"* ]]
  [ "$(cat "$SETTINGS")" = "$first" ]
}

@test "an entry added ad hoc between activations is saved before being dropped" {
  echo '{}' >"$SETTINGS"
  echo '{"allow":["a"]}' >"$DESIRED"
  run "$SCRIPT" "$SETTINGS" "$DESIRED" "$REMOVED_DIR"
  [ "$status" -eq 0 ]

  jq '.autoMode.allow += ["added by hand"]' "$SETTINGS" >"$TMP/edited.json"
  mv "$TMP/edited.json" "$SETTINGS"

  run "$SCRIPT" "$SETTINGS" "$DESIRED" "$REMOVED_DIR"
  [ "$status" -eq 0 ]
  [ "$(_removed_count)" -eq 1 ]
  [ "$(_removed_file | xargs jq -c '.autoMode.allow')" = '["added by hand"]' ]
  [ "$(jq -c '.autoMode.allow' "$SETTINGS")" = '["$defaults","a"]' ]
}

@test "an existing \$defaults entry is never reported as removed" {
  cat >"$SETTINGS" <<'JSON'
{"autoMode":{"environment":["$defaults"],"allow":["$defaults"],"soft_deny":["$defaults"]}}
JSON
  echo '{"environment":["e"],"allow":["a"],"soft_deny":["s"]}' >"$DESIRED"

  run "$SCRIPT" "$SETTINGS" "$DESIRED" "$REMOVED_DIR"

  [ "$status" -eq 0 ]
  [ "$(_removed_count)" -eq 0 ]
  [[ "$output" != *"REMOVED"* ]]
  [ "$(jq -c '.autoMode.environment' "$SETTINGS")" = '["$defaults","e"]' ]
}

@test "preserves apostrophes, double quotes, dollar signs, backslashes and asterisks byte-for-byte" {
  echo '{}' >"$SETTINGS"
  # Written through jq so the desired JSON is itself valid; the entries hold
  # every awkward character, including the literal $defaults inside prose.
  jq -n --arg e "user's \"quoted\" \$HOME \$defaults back\\slash *.zr.org *.zipaws.com" \
    '{"environment":[$e],"allow":[$e],"soft_deny":[$e]}' >"$DESIRED"
  expected="user's \"quoted\" \$HOME \$defaults back\\slash *.zr.org *.zipaws.com"

  run "$SCRIPT" "$SETTINGS" "$DESIRED" "$REMOVED_DIR"

  [ "$status" -eq 0 ]
  for k in environment allow soft_deny; do
    [ "$(jq -r --arg k "$k" '.autoMode[$k][1]' "$SETTINGS")" = "$expected" ]
  done

  # A dropped awkward entry must also round-trip into the removal file.
  jq -n --arg e "$expected" '{"autoMode":{"allow":[$e]}}' >"$SETTINGS"
  echo '{"allow":[]}' >"$DESIRED"
  run "$SCRIPT" "$SETTINGS" "$DESIRED" "$REMOVED_DIR"
  [ "$status" -eq 0 ]
  [ "$(_removed_file | xargs jq -r '.autoMode.allow[0]')" = "$expected" ]
}

@test "creates settings.json containing only autoMode when the file is missing" {
  [ ! -e "$SETTINGS" ]
  echo '{"environment":["e"],"allow":[],"soft_deny":[]}' >"$DESIRED"

  run "$SCRIPT" "$SETTINGS" "$DESIRED" "$REMOVED_DIR"

  [ "$status" -eq 0 ]
  [ -f "$SETTINGS" ]
  [ "$(jq -c 'keys' "$SETTINGS")" = '["autoMode"]' ]
  [ "$(jq -c '.autoMode.environment' "$SETTINGS")" = '["$defaults","e"]' ]
  [ "$(_removed_count)" -eq 0 ]
}

@test "works when settings.json is {} or has no autoMode" {
  echo '{}' >"$SETTINGS"
  echo '{"allow":["a"]}' >"$DESIRED"
  run "$SCRIPT" "$SETTINGS" "$DESIRED" "$REMOVED_DIR"
  [ "$status" -eq 0 ]
  [ "$(jq -c '.autoMode.allow' "$SETTINGS")" = '["$defaults","a"]' ]

  echo '{"model":"opus"}' >"$SETTINGS"
  run "$SCRIPT" "$SETTINGS" "$DESIRED" "$REMOVED_DIR"
  [ "$status" -eq 0 ]
  [ "$(jq -r '.model' "$SETTINGS")" = "opus" ]
  [ "$(jq -c '.autoMode.allow' "$SETTINGS")" = '["$defaults","a"]' ]
  [ "$(_removed_count)" -eq 0 ]
}

@test "exits 2 and leaves settings.json byte-identical on invalid JSON" {
  printf '{"model": "opus", "autoMode": {"allow": ["x"' >"$SETTINGS"
  cp "$SETTINGS" "$TMP/before.json"
  echo '{"allow":["a"]}' >"$DESIRED"

  run "$SCRIPT" "$SETTINGS" "$DESIRED" "$REMOVED_DIR"
  [ "$status" -eq 2 ]
  cmp "$SETTINGS" "$TMP/before.json"

  # Invalid desired JSON: also exit 2 with a valid settings file untouched.
  echo '{"model":"opus"}' >"$SETTINGS"
  cp "$SETTINGS" "$TMP/before.json"
  printf '{"allow": [' >"$DESIRED"

  run "$SCRIPT" "$SETTINGS" "$DESIRED" "$REMOVED_DIR"
  [ "$status" -eq 2 ]
  cmp "$SETTINGS" "$TMP/before.json"
  [ "$(_removed_count)" -eq 0 ]
}

@test "a removal file is selectable with jq -e .autoMode and does not collide with managed-keys removal files" {
  mkdir -p "$REMOVED_DIR"
  # A removal file in the shape claude-settings-replace-managed-keys writes.
  echo '{"enabledPlugins":{"x@m":true}}' >"$REMOVED_DIR/.claude-settings-removed-20200101T000000Z-1.json"
  echo '{"autoMode":{"allow":["old"]}}' >"$SETTINGS"
  echo '{"allow":["new"]}' >"$DESIRED"

  run "$SCRIPT" "$SETTINGS" "$DESIRED" "$REMOVED_DIR"
  [ "$status" -eq 0 ]
  [ "$(_removed_count)" -eq 2 ]

  autoMode_files=0
  for f in "$REMOVED_DIR"/.claude-settings-removed-*.json; do
    if jq -e .autoMode "$f" >/dev/null 2>&1; then
      autoMode_files=$((autoMode_files + 1))
      [ "$(jq -c '.autoMode.allow' "$f")" = '["old"]' ]
    fi
  done
  [ "$autoMode_files" -eq 1 ]
  # The managed-keys file is left alone.
  [ "$(jq -c '.enabledPlugins' "$REMOVED_DIR/.claude-settings-removed-20200101T000000Z-1.json")" = '{"x@m":true}' ]
}
