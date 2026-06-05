#!/usr/bin/env bats
#
# Verify install-plugin.sh:
#   - validates cache manifests; removes corrupt cached versions with warning
#   - installs the plugin via the supplied claude binary; on install failure
#     falls back to update; surfaces stderr from both commands only if both
#     fail
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

@test "install succeeds: echoes installed status on stdout" {
  _mock_claude 0 0 "" ""

  run "$SCRIPT" "$CLAUDE_BIN" "beads@beads-marketplace" "$CACHE_ROOT"

  [ "$status" -eq 0 ]
  [[ "$output" == *"beads@beads-marketplace installed"* ]]
  # Confirm install was called, update was not
  grep -Fxq "plugin install beads@beads-marketplace --scope user" "$CALLS"
  ! grep -Fq "update" "$CALLS"
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
