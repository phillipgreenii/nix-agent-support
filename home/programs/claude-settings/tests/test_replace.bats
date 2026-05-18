#!/usr/bin/env bats
#
# Verify replace-managed-keys.sh:
#   - replaces enabledPlugins and extraKnownMarketplaces with the Nix-declared set
#   - writes a timestamped JSON file when any pre-existing entries are removed
#   - logs removed entries to stderr
#   - leaves other settings.json keys untouched

bats_require_minimum_version 1.5.0

SCRIPT="${BATS_TEST_DIRNAME}/../replace-managed-keys.sh"

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
