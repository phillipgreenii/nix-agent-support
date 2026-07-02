#!/usr/bin/env bats
#
# Verify claude-settings-register-marketplace.sh:
#   - registers a directory marketplace via `claude plugin marketplace add <path>`
#   - on `add` failure (e.g. already registered), falls back to
#     `marketplace update <name>`
#   - exits 0 (non-fatal) regardless of outcome; warns on stderr only when both
#     add and update fail
#
# This is the registration that must run BEFORE the per-plugin install loop so
# a freshly nix-provided directory marketplace is scanned into the registry and
# `claude plugin install <plugin>@<mkt>` does not fail "Plugin not found".

bats_require_minimum_version 1.5.0

load test_helper

# Resolve the script: prefer the packaged binary on PATH (Nix build sandbox via
# testBashScripts), fall back to a lib-sourcing wrapper around the sibling
# source script for direct dev-time runs (`bats tests/`).
SCRIPT="$(resolve_claude_settings_script claude-settings-register-marketplace)"

setup() {
  TMP="$(mktemp -d)"
  export TMP
  CLAUDE_BIN="$TMP/bin/claude"
  CALLS="$TMP/calls.log"
  mkdir -p "$TMP/bin"
  : > "$CALLS"
  export CLAUDE_BIN CALLS
}

teardown() {
  [ -n "$TMP" ] && rm -rf "$TMP"
}

# Write a mock claude binary that records the marketplace subcommand it is given
# and exits with scripted behavior.
#   $1 = exit code for `marketplace add`
#   $2 = exit code for `marketplace update`
_mock_claude() {
  cat > "$CLAUDE_BIN" <<EOF
#!/usr/bin/env bash
echo "\$@" >> "$CALLS"
# args: plugin marketplace <add|update> <arg>
case "\$3" in
  add)    exit $1 ;;
  update) exit $2 ;;
esac
exit 0
EOF
  chmod +x "$CLAUDE_BIN"
}

@test "directory marketplace: marketplace add is called and succeeds" {
  _mock_claude 0 0

  run "$SCRIPT" "$CLAUDE_BIN" "pgii-local-plugins" "/path/to/mkt"

  [ "$status" -eq 0 ]
  [[ "$output" == *"marketplace pgii-local-plugins registered"* ]]
  # `add <path>` was issued; `update` was NOT needed.
  grep -Fxq "plugin marketplace add /path/to/mkt" "$CALLS"
  ! grep -Fq "marketplace update" "$CALLS"
}

@test "add fails, update succeeds: falls back to marketplace update <name>" {
  _mock_claude 1 0

  run "$SCRIPT" "$CLAUDE_BIN" "pgii-local-plugins" "/path/to/mkt"

  [ "$status" -eq 0 ]
  [[ "$output" == *"marketplace pgii-local-plugins refreshed"* ]]
  [[ "$output" != *"WARNING"* ]]
  grep -Fxq "plugin marketplace add /path/to/mkt" "$CALLS"
  grep -Fxq "plugin marketplace update pgii-local-plugins" "$CALLS"
}

@test "add and update both fail: WARNING on stderr, still exits 0 (non-fatal)" {
  _mock_claude 1 1

  run --separate-stderr "$SCRIPT" "$CLAUDE_BIN" "pgii-local-plugins" "/path/to/mkt"

  [ "$status" -eq 0 ] # non-fatal, matching the activation's || true style
  [[ "$stderr" == *"WARNING marketplace pgii-local-plugins add/update skipped"* ]]
  [[ "$output" != *"registered"* ]]
  [[ "$output" != *"refreshed"* ]]
}

@test "claude binary entirely unavailable: non-fatal, exits 0" {
  # No mock written → CLAUDE_BIN is not executable; both add and update fail.
  run --separate-stderr "$SCRIPT" "$CLAUDE_BIN" "pgii-local-plugins" "/path/to/mkt"

  [ "$status" -eq 0 ]
  [[ "$stderr" == *"WARNING marketplace pgii-local-plugins add/update skipped"* ]]
}

@test "wrong arg count: usage error, exit 64" {
  run "$SCRIPT" "$CLAUDE_BIN" "only-two-args"

  [ "$status" -eq 64 ]
  [[ "$output" == *"usage:"* ]]
}
