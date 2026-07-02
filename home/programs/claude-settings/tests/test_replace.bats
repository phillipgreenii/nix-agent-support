#!/usr/bin/env bats
#
# Verify claude-settings-replace-managed-keys.sh:
#   - replaces enabledPlugins and extraKnownMarketplaces with the Nix-declared set
#   - writes a timestamped JSON file when any pre-existing entries are removed
#   - logs removed entries to stderr
#   - leaves other settings.json keys untouched

bats_require_minimum_version 1.5.0

load test_helper

# Resolve the script: prefer the packaged binary on PATH (Nix build sandbox
# via testBashScripts), fall back to a lib-sourcing wrapper around the sibling
# source script for direct dev-time runs (`bats tests/`).
SCRIPT="$(resolve_claude_settings_script claude-settings-replace-managed-keys)"

setup() {
  TMP="$(mktemp -d)"
  export TMP
  SETTINGS="$TMP/settings.json"
  REMOVED_DIR="$TMP"
  export SETTINGS REMOVED_DIR
}

teardown() {
  [ -n "$TMP" ] && rm -rf "$TMP"
}

# Count removal files (".claude-settings-removed-*.json") in REMOVED_DIR.
_removed_count() {
  find "$REMOVED_DIR" -maxdepth 1 -name '.claude-settings-removed-*.json' | wc -l | tr -d ' '
}

# Read the single removal file's contents (asserts exactly one exists).
_removed_file_content() {
  local files
  mapfile -t files < <(find "$REMOVED_DIR" -maxdepth 1 -name '.claude-settings-removed-*.json')
  [ "${#files[@]}" -eq 1 ] || { echo "expected 1 removal file, found ${#files[@]}" >&2; return 1; }
  cat "${files[0]}"
}

@test "creates both managed keys when settings.json starts empty" {
  echo '{}' > "$SETTINGS"

  run "$SCRIPT" "$SETTINGS" \
    '{"foo@m":true}' \
    '{"m":{"source":{"source":"github","repo":"x/y"}}}' \
    "$REMOVED_DIR"

  [ "$status" -eq 0 ]
  [ "$(jq -r '.enabledPlugins["foo@m"]' "$SETTINGS")" = "true" ]
  [ "$(jq -r '.extraKnownMarketplaces.m.source.repo' "$SETTINGS")" = "x/y" ]
  [ "$(_removed_count)" -eq 0 ]
}

@test "removes extra enabledPlugins entry and writes removal file" {
  cat > "$SETTINGS" <<'JSON'
{
  "enabledPlugins": {
    "keep@m": true,
    "drop@m": true
  }
}
JSON

  run "$SCRIPT" "$SETTINGS" \
    '{"keep@m":true}' \
    '{}' \
    "$REMOVED_DIR"

  [ "$status" -eq 0 ]
  [ "$(jq -r '.enabledPlugins["drop@m"] // "absent"' "$SETTINGS")" = "absent" ]
  [ "$(jq -r '.enabledPlugins["keep@m"]' "$SETTINGS")" = "true" ]
  [ "$(_removed_count)" -eq 1 ]

  removed="$(_removed_file_content)"
  [ "$(echo "$removed" | jq -r '.enabledPlugins["drop@m"]')" = "true" ]
}

@test "removes extra extraKnownMarketplaces entry and writes removal file" {
  cat > "$SETTINGS" <<'JSON'
{
  "extraKnownMarketplaces": {
    "keep-mkt": { "source": { "source": "github", "repo": "x/keep" } },
    "drop-mkt": { "source": { "source": "github", "repo": "x/drop" } }
  }
}
JSON

  run "$SCRIPT" "$SETTINGS" \
    '{}' \
    '{"keep-mkt":{"source":{"source":"github","repo":"x/keep"}}}' \
    "$REMOVED_DIR"

  [ "$status" -eq 0 ]
  [ "$(jq -r '.extraKnownMarketplaces["drop-mkt"] // "absent"' "$SETTINGS")" = "absent" ]
  [ "$(jq -r '.extraKnownMarketplaces["keep-mkt"].source.repo' "$SETTINGS")" = "x/keep" ]
  [ "$(_removed_count)" -eq 1 ]

  removed="$(_removed_file_content)"
  [ "$(echo "$removed" | jq -r '.extraKnownMarketplaces["drop-mkt"].source.repo')" = "x/drop" ]
}

@test "no removal file when settings already match Nix set" {
  cat > "$SETTINGS" <<'JSON'
{
  "enabledPlugins": { "foo@m": true },
  "extraKnownMarketplaces": {
    "m": { "source": { "source": "github", "repo": "x/y" } }
  }
}
JSON

  run "$SCRIPT" "$SETTINGS" \
    '{"foo@m":true}' \
    '{"m":{"source":{"source":"github","repo":"x/y"}}}' \
    "$REMOVED_DIR"

  [ "$status" -eq 0 ]
  [ "$(_removed_count)" -eq 0 ]
  [ "$(jq -r '.enabledPlugins["foo@m"]' "$SETTINGS")" = "true" ]
  [ "$(jq -r '.extraKnownMarketplaces.m.source.repo' "$SETTINGS")" = "x/y" ]
}

@test "single removal file captures both enabledPlugins and extraKnownMarketplaces removals" {
  cat > "$SETTINGS" <<'JSON'
{
  "enabledPlugins": { "drop@m": true },
  "extraKnownMarketplaces": {
    "drop-mkt": { "source": { "source": "github", "repo": "x/drop" } }
  }
}
JSON

  run "$SCRIPT" "$SETTINGS" \
    '{}' \
    '{}' \
    "$REMOVED_DIR"

  [ "$status" -eq 0 ]
  [ "$(_removed_count)" -eq 1 ]

  removed="$(_removed_file_content)"
  [ "$(echo "$removed" | jq -r '.enabledPlugins["drop@m"]')" = "true" ]
  [ "$(echo "$removed" | jq -r '.extraKnownMarketplaces["drop-mkt"].source.repo')" = "x/drop" ]
}

@test "second run after removal produces no new removal file" {
  cat > "$SETTINGS" <<'JSON'
{
  "enabledPlugins": { "keep@m": true, "drop@m": true }
}
JSON

  run "$SCRIPT" "$SETTINGS" '{"keep@m":true}' '{}' "$REMOVED_DIR"
  [ "$status" -eq 0 ]
  [ "$(_removed_count)" -eq 1 ]

  # Second run: nothing left to remove
  run "$SCRIPT" "$SETTINGS" '{"keep@m":true}' '{}' "$REMOVED_DIR"
  [ "$status" -eq 0 ]
  [ "$(_removed_count)" -eq 1 ]  # still just the first one
}

@test "leaves env, permissions, and scalar keys untouched" {
  cat > "$SETTINGS" <<'JSON'
{
  "model": "opus[1m]",
  "includeCoAuthoredBy": false,
  "env": { "CLAUDE_CODE_NO_FLICKER": "1" },
  "permissions": { "allow": ["Read(//tmp/**)"] },
  "enabledPlugins": { "drop@m": true }
}
JSON

  run "$SCRIPT" "$SETTINGS" '{}' '{}' "$REMOVED_DIR"

  [ "$status" -eq 0 ]
  [ "$(jq -r '.model' "$SETTINGS")" = "opus[1m]" ]
  [ "$(jq -r '.includeCoAuthoredBy' "$SETTINGS")" = "false" ]
  [ "$(jq -r '.env.CLAUDE_CODE_NO_FLICKER' "$SETTINGS")" = "1" ]
  [ "$(jq -r '.permissions.allow[0]' "$SETTINGS")" = "Read(//tmp/**)" ]
}
