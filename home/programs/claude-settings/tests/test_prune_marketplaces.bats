#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Verify claude-settings-prune-marketplaces.sh:
#   - detects a DIRECTORY-source known_marketplaces.json entry as stale only
#     when its target path no longer exists on disk AND its name is not in
#     the declared-names set passed by the caller
#   - warns always on stderr; prunes the entry from known_marketplaces.json
#     only when CLAUDE_SETTINGS_PRUNE_STALE_MARKETPLACES is set (mirrors
#     claude-settings-install-plugin's CLAUDE_SETTINGS_PRUNE_STALE_SCOPE
#     opt-in style)
#   - a LIVE entry (path exists) is never flagged, regardless of declaration
#   - a DECLARED entry is never flagged, regardless of path existence
#   - GITHUB-source entries are never candidates (matches
#     claude-settings-register-marketplace.sh's own directory-only scope)
#   - exits 0 (non-fatal) regardless of outcome; exit 64 on a caller usage
#     error
#
# This is the counterpart to claude-settings-register-marketplace.sh: that
# script REGISTERS a nix-declared directory marketplace before the
# per-plugin install loop; this one UNREGISTERS one removed from nix
# declarations, run right after (pg2-rjfti).

bats_require_minimum_version 1.5.0

load test_helper

# Resolve the script: prefer the packaged binary on PATH (Nix build sandbox via
# testBashScripts), fall back to a lib-sourcing wrapper around the sibling
# source script for direct dev-time runs (`bats tests/`).
SCRIPT="$(resolve_claude_settings_script claude-settings-prune-marketplaces)"

setup() {
  TMP="$(mktemp -d)"
  export TMP
  KNOWN="$TMP/known_marketplaces.json"
  export KNOWN
}

teardown() {
  [ -n "$TMP" ] && rm -rf "$TMP"
}

# Write known_marketplaces.json. $1 = raw JSON document.
_write_known() {
  printf '%s' "$1" >"$KNOWN"
}

@test "dead directory-source entry, not declared: warns; default does NOT prune" {
  local dead="$TMP/does-not-exist/mkt"
  _write_known "$(jq -n --arg p "$dead" '{
    "stale-mkt": { source: { source: "directory", path: $p } }
  }')"

  run --separate-stderr "$SCRIPT" "$KNOWN" '[]'

  [ "$status" -eq 0 ]
  [[ "$stderr" == *"WARNING marketplace stale-mkt is stale"* ]]
  [[ "$stderr" == *"$dead"* ]]
  # Default (no opt-in): the stale entry is NOT removed.
  [[ "$stderr" != *"pruned"* ]]
  run jq 'has("stale-mkt")' "$KNOWN"
  [ "$output" = "true" ]
}

@test "dead directory-source entry, not declared: opt-in prune removes only it" {
  local dead="$TMP/does-not-exist/mkt"
  _write_known "$(jq -n --arg p "$dead" '{
    "stale-mkt": { source: { source: "directory", path: $p } },
    "live-mkt": { source: { source: "directory", path: "/tmp" } }
  }')"

  CLAUDE_SETTINGS_PRUNE_STALE_MARKETPLACES=1 run --separate-stderr \
    "$SCRIPT" "$KNOWN" '["live-mkt"]'

  [ "$status" -eq 0 ]
  [[ "$stderr" == *"pruned stale marketplace stale-mkt"* ]]
  run jq 'has("stale-mkt")' "$KNOWN"
  [ "$output" = "false" ]
  # The still-live, still-declared sibling entry is untouched.
  run jq 'has("live-mkt")' "$KNOWN"
  [ "$output" = "true" ]
}

@test "entry still declared survives even with a dead path" {
  local dead="$TMP/does-not-exist/mkt"
  _write_known "$(jq -n --arg p "$dead" '{
    "still-declared": { source: { source: "directory", path: $p } }
  }')"

  CLAUDE_SETTINGS_PRUNE_STALE_MARKETPLACES=1 run --separate-stderr \
    "$SCRIPT" "$KNOWN" '["still-declared"]'

  [ "$status" -eq 0 ]
  [[ "$stderr" != *"stale"* ]]
  run jq 'has("still-declared")' "$KNOWN"
  [ "$output" = "true" ]
}

@test "entry with a LIVE path is never flagged, declared or not" {
  local live="$TMP/live-dir"
  mkdir -p "$live"
  _write_known "$(jq -n --arg p "$live" '{
    "live-mkt": { source: { source: "directory", path: $p } }
  }')"

  CLAUDE_SETTINGS_PRUNE_STALE_MARKETPLACES=1 run --separate-stderr \
    "$SCRIPT" "$KNOWN" '[]'

  [ "$status" -eq 0 ]
  [[ "$stderr" != *"stale"* ]]
  run jq 'has("live-mkt")' "$KNOWN"
  [ "$output" = "true" ]
}

@test "github-source entry with a dead installLocation is never a candidate" {
  local dead="$TMP/does-not-exist/marketplaces/gh-mkt"
  _write_known "$(jq -n --arg p "$dead" '{
    "gh-mkt": { source: { source: "github", repo: "x/y" }, installLocation: $p }
  }')"

  CLAUDE_SETTINGS_PRUNE_STALE_MARKETPLACES=1 run --separate-stderr \
    "$SCRIPT" "$KNOWN" '[]'

  [ "$status" -eq 0 ]
  [[ "$stderr" != *"stale"* ]]
  run jq 'has("gh-mkt")' "$KNOWN"
  [ "$output" = "true" ]
}

@test "no known_marketplaces.json file: non-fatal no-op" {
  run --separate-stderr "$SCRIPT" "$TMP/absent.json" '[]'

  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
}

@test "multiple stale entries: each is pruned independently, live entry kept" {
  _write_known '{
    "stale-a": { "source": { "source": "directory", "path": "/nonexistent/a" } },
    "stale-b": { "source": { "source": "directory", "path": "/nonexistent/b" } },
    "kept": { "source": { "source": "directory", "path": "/tmp" } }
  }'

  CLAUDE_SETTINGS_PRUNE_STALE_MARKETPLACES=1 run --separate-stderr \
    "$SCRIPT" "$KNOWN" '[]'

  [ "$status" -eq 0 ]
  [[ "$stderr" == *"pruned stale marketplace stale-a"* ]]
  [[ "$stderr" == *"pruned stale marketplace stale-b"* ]]
  run jq -r 'keys | sort | join(",")' "$KNOWN"
  [ "$output" = "kept" ]
}

@test "wrong arg count: usage error, exit 64" {
  run "$SCRIPT" "$KNOWN"

  [ "$status" -eq 64 ]
  [[ "$output" == *"usage:"* ]]
}

@test "declared-names arg that is not a JSON array: usage error, exit 64" {
  _write_known '{}'

  run --separate-stderr "$SCRIPT" "$KNOWN" 'not-json'

  [ "$status" -eq 64 ]
  [[ "$stderr" == *"must be a JSON array"* ]]
}
