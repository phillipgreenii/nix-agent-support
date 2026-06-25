#!/usr/bin/env bats
#
# Verify install-plugin.sh:
#   - validates cache manifests; removes corrupt cached versions with warning
#   - installs the plugin via the supplied claude binary, then ALWAYS follows a
#     successful install with an (idempotent) update so content-digest version
#     bumps are pulled — 'install' short-circuits on an already-installed plugin
#     and never updates on its own (pg2-cxwj)
#   - on install failure falls back to update; surfaces stderr from both
#     commands only if both fail. On that GENUINE both-fail path it ALSO emits
#     diagnostic context (target scope, installed_plugins.json entries for the
#     spec, cached version dirs) — but the install-fails-then-update-SUCCEEDS
#     path stays quiet (pg2-oklb)
#   - a post-install update failure is non-fatal (warning only); the
#     already-installed copy is preserved
#   - warns about a STALE non-user-scope entry (e.g. a project entry for a dead
#     path) that shadows the user-scope enable; pruning is gated behind
#     CLAUDE_SETTINGS_PRUNE_STALE_SCOPE and NEVER skips the trailing update
#     (pg2-oklb / pg2-cxwj)
#   - exits 0 (non-fatal) regardless of install/update outcome

bats_require_minimum_version 1.5.0

SCRIPT="$(command -v claude-settings-install-plugin || true)"
if [ -z "$SCRIPT" ]; then
  SCRIPT="${BATS_TEST_DIRNAME}/../install-plugin.sh"
fi

setup() {
  TMP="$(mktemp -d)"
  export TMP
  CACHE_ROOT="$TMP/cache"
  CLAUDE_BIN="$TMP/bin/claude"
  CALLS="$TMP/calls.log"
  mkdir -p "$TMP/bin" "$CACHE_ROOT"
  : > "$CALLS"
  export CACHE_ROOT CLAUDE_BIN CALLS
}

teardown() {
  [ -n "$TMP" ] && rm -rf "$TMP"
}

# Write a mock claude binary that records calls and exits with scripted
# behavior. $1=install_exit, $2=update_exit, $3=install_stderr, $4=update_stderr
_mock_claude() {
  cat > "$CLAUDE_BIN" <<EOF
#!/usr/bin/env bash
echo "\$@" >> "$CALLS"
case "\$2" in
  install) echo "$3" >&2; exit $1 ;;
  update)  echo "$4" >&2; exit $2 ;;
esac
exit 0
EOF
  chmod +x "$CLAUDE_BIN"
}

# Helper: write a manifest at the right cache path.
# $1 = marketplace, $2 = plugin, $3 = version, $4 = content
_write_manifest() {
  local dir="$CACHE_ROOT/$1/$2/$3/.claude-plugin"
  mkdir -p "$dir"
  printf '%s' "$4" > "$dir/plugin.json"
}

# Helper: write an installed_plugins.json beside the cache dir (where the
# script derives it: dirname(cache_root)/installed_plugins.json).
# $1 = raw JSON document.
_write_installed_plugins() {
  printf '%s' "$1" > "$(dirname "$CACHE_ROOT")/installed_plugins.json"
}

# Path the script will derive for installed_plugins.json.
_installed_plugins_path() {
  echo "$(dirname "$CACHE_ROOT")/installed_plugins.json"
}

@test "install succeeds: also runs update so version bumps are pulled" {
  _mock_claude 0 0 "" ""

  run "$SCRIPT" "$CLAUDE_BIN" "beads@beads-marketplace" "$CACHE_ROOT"

  [ "$status" -eq 0 ]
  [[ "$output" == *"beads@beads-marketplace installed"* ]]
  [[ "$output" == *"beads@beads-marketplace updated"* ]]
  # 'install' short-circuits on an already-installed plugin without pulling a
  # newer marketplace version, so update MUST run too (pg2-cxwj).
  grep -Fxq "plugin install beads@beads-marketplace --scope user" "$CALLS"
  grep -Fxq "plugin update beads@beads-marketplace --scope user" "$CALLS"
}

@test "install succeeds, post-install update fails: non-fatal WARNING, install preserved" {
  _mock_claude 0 1 "" "update boom"

  run --separate-stderr "$SCRIPT" "$CLAUDE_BIN" "beads@beads-marketplace" "$CACHE_ROOT"

  [ "$status" -eq 0 ]   # non-fatal
  [[ "$output" == *"beads@beads-marketplace installed"* ]]
  [[ "$output" != *"updated"* ]]
  [[ "$stderr" == *"WARNING beads@beads-marketplace post-install update failed"* ]]
  [[ "$stderr" == *"update boom"* ]]
  # Both commands were attempted.
  grep -Fxq "plugin install beads@beads-marketplace --scope user" "$CALLS"
  grep -Fxq "plugin update beads@beads-marketplace --scope user" "$CALLS"
}

@test "install fails, update succeeds: echoes updated status on stdout, no warning" {
  _mock_claude 1 0 "already installed" ""

  run "$SCRIPT" "$CLAUDE_BIN" "beads@beads-marketplace" "$CACHE_ROOT"

  [ "$status" -eq 0 ]
  [[ "$output" == *"beads@beads-marketplace updated"* ]]
  # No WARNING since the fallback succeeded
  [[ "$output" != *"WARNING"* ]]
  grep -Fxq "plugin install beads@beads-marketplace --scope user" "$CALLS"
  grep -Fxq "plugin update beads@beads-marketplace --scope user" "$CALLS"
}

@test "install and update both fail: warning with stderr from both" {
  _mock_claude 1 1 "install boom" "update boom"

  run --separate-stderr "$SCRIPT" "$CLAUDE_BIN" "beads@beads-marketplace" "$CACHE_ROOT"

  [ "$status" -eq 0 ]   # non-fatal
  # stderr (captured separately) contains warning + both subcommand stderrs
  [[ "$stderr" == *"WARNING beads@beads-marketplace install/update failed"* ]]
  [[ "$stderr" == *"install boom"* ]]
  [[ "$stderr" == *"update boom"* ]]
  # stdout has no success line
  [[ "$output" != *"installed"* ]]
  [[ "$output" != *"updated"* ]]
}

@test "valid manifest in cache is preserved" {
  _mock_claude 0 0 "" ""
  _write_manifest "beads-marketplace" "beads" "1.0.4" '{"name":"beads","version":"1.0.4"}'

  run "$SCRIPT" "$CLAUDE_BIN" "beads@beads-marketplace" "$CACHE_ROOT"

  [ "$status" -eq 0 ]
  [ -f "$CACHE_ROOT/beads-marketplace/beads/1.0.4/.claude-plugin/plugin.json" ]
}

@test "corrupt manifest (parse error) is removed with WARNING on stderr" {
  _mock_claude 0 0 "" ""
  # The actual failure mode hit in production: unresolved git merge markers.
  _write_manifest "beads-marketplace" "beads" "1.0.4" \
'{"name":"beads",
<<<<<<< Updated upstream
=======
"version":"1.0.4",
>>>>>>> Stashed changes
}'

  run --separate-stderr "$SCRIPT" "$CLAUDE_BIN" "beads@beads-marketplace" "$CACHE_ROOT"

  [ "$status" -eq 0 ]
  [ ! -d "$CACHE_ROOT/beads-marketplace/beads/1.0.4" ]
  [[ "$stderr" == *"WARNING corrupt manifest"* ]]
  [[ "$stderr" == *"removing"* ]]
}

@test "manifest with name but no .version is preserved (version is optional)" {
  _mock_claude 0 0 "" ""
  # Plugins pinned by git ref (e.g. caveman) carry no semver in plugin.json;
  # a missing optional .version must NOT be treated as corrupt.
  _write_manifest "caveman" "caveman" "18e45320a0b1" '{"name":"caveman"}'

  run --separate-stderr "$SCRIPT" "$CLAUDE_BIN" "caveman@caveman" "$CACHE_ROOT"

  [ "$status" -eq 0 ]
  [ -f "$CACHE_ROOT/caveman/caveman/18e45320a0b1/.claude-plugin/plugin.json" ]
  [[ "$stderr" != *"WARNING corrupt manifest"* ]]
}

@test "manifest missing required .name is removed with WARNING" {
  _mock_claude 0 0 "" ""
  _write_manifest "beads-marketplace" "beads" "1.0.4" '{"version":"1.0.4"}'

  run --separate-stderr "$SCRIPT" "$CLAUDE_BIN" "beads@beads-marketplace" "$CACHE_ROOT"

  [ "$status" -eq 0 ]
  [ ! -d "$CACHE_ROOT/beads-marketplace/beads/1.0.4" ]
  [[ "$stderr" == *"WARNING corrupt manifest"* ]]
}

@test "no cache dir: install proceeds, no validation warning" {
  _mock_claude 0 0 "" ""
  # No cache populated for this plugin.

  run --separate-stderr "$SCRIPT" "$CLAUDE_BIN" "caveman@caveman" "$CACHE_ROOT"

  [ "$status" -eq 0 ]
  [[ "$output" == *"caveman@caveman installed"* ]]
  [[ "$stderr" != *"WARNING"* ]]
}

# ----------------------------------------------------------------------------
# pg2-oklb: failure context, stale wrong-scope detection, version-bump regression
# ----------------------------------------------------------------------------

@test "genuine failure (both fail) emits context: scope, installed entries, cached dirs" {
  _mock_claude 1 1 "Failed to clone repository:" ""
  # A user-scope entry recorded for the spec, plus two cached versions present
  # (the real pg2-oklb scenario: superpowers cache had both 5.1.0 and 6.0.3).
  _write_installed_plugins '{
    "version": 2,
    "plugins": {
      "superpowers@superpowers-marketplace": [
        { "scope": "user", "version": "6.0.3" }
      ]
    }
  }'
  _write_manifest "superpowers-marketplace" "superpowers" "5.1.0" '{"name":"superpowers"}'
  _write_manifest "superpowers-marketplace" "superpowers" "6.0.3" '{"name":"superpowers"}'

  run --separate-stderr "$SCRIPT" "$CLAUDE_BIN" "superpowers@superpowers-marketplace" "$CACHE_ROOT"

  [ "$status" -eq 0 ]   # non-fatal
  [[ "$stderr" == *"WARNING superpowers@superpowers-marketplace install/update failed"* ]]
  # Real CLI output is still surfaced...
  [[ "$stderr" == *"Failed to clone repository:"* ]]
  # ...plus the new diagnostic context.
  [[ "$stderr" == *"--- context ---"* ]]
  [[ "$stderr" == *"target scope: user"* ]]
  [[ "$stderr" == *"installed_plugins.json entries for superpowers@superpowers-marketplace"* ]]
  [[ "$stderr" == *"scope=user version=6.0.3"* ]]
  [[ "$stderr" == *"cached version dirs"* ]]
  [[ "$stderr" == *"5.1.0"* ]]
  [[ "$stderr" == *"6.0.3"* ]]
}

@test "install-fails-then-update-SUCCEEDS stays quiet: no context, no warning" {
  # Regression guard for the existing quiet fallback path: even with installed
  # entries and a cache present, a successful fallback update must NOT print a
  # warning or the failure context.
  _mock_claude 1 0 "already installed" ""
  _write_installed_plugins '{
    "version": 2,
    "plugins": { "beads@beads-marketplace": [ { "scope": "user", "version": "1.0.5" } ] }
  }'
  _write_manifest "beads-marketplace" "beads" "1.0.5" '{"name":"beads"}'

  run --separate-stderr "$SCRIPT" "$CLAUDE_BIN" "beads@beads-marketplace" "$CACHE_ROOT"

  [ "$status" -eq 0 ]
  [[ "$output" == *"beads@beads-marketplace updated"* ]]
  [[ "$output" != *"WARNING"* ]]
  [[ "$stderr" != *"WARNING"* ]]
  [[ "$stderr" != *"--- context ---"* ]]
}

@test "stale wrong-scope entry (dead project path) warns; default does NOT prune" {
  _mock_claude 0 0 "" ""
  local dead="$TMP/does-not-exist/slot-b"
  _write_installed_plugins "$(jq -n --arg p "$dead" '{
    version: 2,
    plugins: {
      "superpowers@superpowers-marketplace": [
        { scope: "user", version: "6.0.3" },
        { scope: "project", projectPath: $p, version: "0.3.1" }
      ]
    }
  }')"

  run --separate-stderr "$SCRIPT" "$CLAUDE_BIN" "superpowers@superpowers-marketplace" "$CACHE_ROOT"

  [ "$status" -eq 0 ]
  [[ "$stderr" == *"WARNING superpowers@superpowers-marketplace has a stale project-scope entry"* ]]
  [[ "$stderr" == *"$dead"* ]]
  [[ "$stderr" == *"shadows the user-scope enable"* ]]
  # Default (no opt-in): the stale entry is NOT removed.
  [[ "$stderr" != *"pruned stale"* ]]
  run jq '.plugins["superpowers@superpowers-marketplace"] | length' "$(_installed_plugins_path)"
  [ "$output" -eq 2 ]
}

@test "stale wrong-scope entry: opt-in prune removes only the stale entry" {
  _mock_claude 0 0 "" ""
  local dead="$TMP/does-not-exist/slot-b"
  _write_installed_plugins "$(jq -n --arg p "$dead" '{
    version: 2,
    plugins: {
      "superpowers@superpowers-marketplace": [
        { scope: "user", version: "6.0.3" },
        { scope: "project", projectPath: $p, version: "0.3.1" }
      ]
    }
  }')"

  CLAUDE_SETTINGS_PRUNE_STALE_SCOPE=1 run --separate-stderr \
    "$SCRIPT" "$CLAUDE_BIN" "superpowers@superpowers-marketplace" "$CACHE_ROOT"

  [ "$status" -eq 0 ]
  [[ "$stderr" == *"pruned stale project-scope entry"* ]]
  # Only the user-scope entry survives.
  run jq -r '.plugins["superpowers@superpowers-marketplace"] | [.[].scope] | join(",")' \
    "$(_installed_plugins_path)"
  [ "$output" = "user" ]
}

@test "LIVE non-user-scope entry (existing path) is NOT flagged stale" {
  _mock_claude 0 0 "" ""
  local live="$TMP/live-project"
  mkdir -p "$live"
  _write_installed_plugins "$(jq -n --arg p "$live" '{
    version: 2,
    plugins: {
      "superpowers@superpowers-marketplace": [
        { scope: "project", projectPath: $p, version: "0.3.1" }
      ]
    }
  }')"

  run --separate-stderr "$SCRIPT" "$CLAUDE_BIN" "superpowers@superpowers-marketplace" "$CACHE_ROOT"

  [ "$status" -eq 0 ]
  [[ "$stderr" != *"stale"* ]]
}

@test "regression: a cached AND enabled plugin still runs update (no skip-if-cached)" {
  # pg2-cxwj invariant: 'plugin install' never pulls a newer marketplace
  # version, so the trailing update is what applies a content-digest bump. Even
  # though the plugin is already cached (5.1.0) AND recorded as enabled at user
  # scope, install must STILL be followed by update — a skip-if-cached shortcut
  # would silently pin the stale 5.1.0 and block the 6.0.3 bump.
  _mock_claude 0 0 "" ""
  _write_manifest "superpowers-marketplace" "superpowers" "5.1.0" '{"name":"superpowers"}'
  _write_installed_plugins '{
    "version": 2,
    "plugins": {
      "superpowers@superpowers-marketplace": [
        { "scope": "user", "version": "5.1.0" }
      ]
    }
  }'

  run "$SCRIPT" "$CLAUDE_BIN" "superpowers@superpowers-marketplace" "$CACHE_ROOT"

  [ "$status" -eq 0 ]
  [[ "$output" == *"superpowers@superpowers-marketplace installed"* ]]
  [[ "$output" == *"superpowers@superpowers-marketplace updated"* ]]
  # The update MUST have been attempted despite the cached + enabled state.
  grep -Fxq "plugin install superpowers@superpowers-marketplace --scope user" "$CALLS"
  grep -Fxq "plugin update superpowers@superpowers-marketplace --scope user" "$CALLS"
}
