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
