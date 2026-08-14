#!/usr/bin/env bats
#
# Verify claude-settings-install-plugin.sh:
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
#   - re-asserts the Nix-declared `enabledPlugins` value for the spec AFTER the
#     install/update pair, when the optional <settings_path> <declared_enabled>
#     arguments are supplied — `plugin install` enables at its target scope on
#     every successful invocation, overriding what replace-managed-keys wrote
#     earlier in the same activation (pg2-4q1qk)
#   - exits 0 (non-fatal) regardless of install/update outcome

bats_require_minimum_version 1.5.0

load test_helper

# Resolve the script: prefer the packaged binary on PATH (Nix build sandbox via
# testBashScripts), fall back to a lib-sourcing wrapper around the sibling
# source script for direct dev-time runs (`bats tests/`).
SCRIPT="$(resolve_claude_settings_script claude-settings-install-plugin)"

setup() {
  TMP="$(mktemp -d)"
  export TMP
  CACHE_ROOT="$TMP/cache"
  CLAUDE_BIN="$TMP/bin/claude"
  CALLS="$TMP/calls.log"
  SETTINGS="$TMP/settings.json"
  mkdir -p "$TMP/bin" "$CACHE_ROOT"
  : > "$CALLS"
  export CACHE_ROOT CLAUDE_BIN CALLS SETTINGS
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

# Write a mock claude that reproduces the MEASURED enablement side effect of
# the real CLI: `plugin install` sets .enabledPlugins["<spec>"] = true in the
# target scope's settings.json on every successful invocation, while `plugin
# update` never touches enablement (verified 2026-08-13 in an isolated
# CLAUDE_CONFIG_DIR against claude 2.1.220 and 2.1.228 — fresh install,
# already-installed same-version install, and a real version bump all enabled;
# no update state did). This mock is what makes the pg2-4q1qk regression tests
# exercise the real failure rather than a hypothetical one.
#   $1 = settings.json the mock writes (the "user scope" file)
#   $2 = cache version dir to CREATE on install, or "" for none. A new dir is
#        what distinguishes a GENUINE install (marketplace moved, content
#        actually pulled) from the already-installed short-circuit; both enable,
#        so both are covered.
_mock_claude_enabling() {
  local settings="$1" newver="${2:-}"
  cat > "$CLAUDE_BIN" <<EOF
#!/usr/bin/env bash
echo "\$@" >> "$CALLS"
case "\$2" in
  install)
    spec="\$3"
    newver="$newver"
    if [ -n "\$newver" ]; then
      mkdir -p "\$newver/.claude-plugin"
      printf '{"name":"%s"}' "\${spec%%@*}" > "\$newver/.claude-plugin/plugin.json"
    fi
    jq --arg s "\$spec" '.enabledPlugins[\$s] = true' "$settings" > "$settings.mocktmp" \\
      && mv -f "$settings.mocktmp" "$settings"
    exit 0
    ;;
  update) exit 0 ;;
esac
exit 0
EOF
  chmod +x "$CLAUDE_BIN"
}

# Read the enablement of $1 out of the settings file under test.
_enabled_of() {
  jq -r --arg s "$1" '.enabledPlugins[$s] | if . == null then "unset" else tostring end' "$SETTINGS"
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
  # The actual failure mode hit in production: unresolved git merge markers —
  # byte-for-byte what a real conflict leaves behind in plugin.json.
  #
  # Assembled rather than written literally so no line here BEGINS with a
  # marker: check-merge-conflict matches markers only at the START of a line,
  # and only while a merge/rebase is in progress, so a literal fixture makes
  # `prek run --all-files` fail on this test mid-rebase (bead pg2-dmktk).
  # printf -v, not a command substitution, so a trailing newline added here
  # later would not be silently stripped.
  local ours='<<<<<<< Updated upstream'
  local divider='======='
  local theirs='>>>>>>> Stashed changes'
  local manifest
  printf -v manifest '{"name":"beads",\n%s\n%s\n"version":"1.0.4",\n%s\n}' \
    "$ours" "$divider" "$theirs"
  _write_manifest "beads-marketplace" "beads" "1.0.4" "$manifest"

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

# ----------------------------------------------------------------------------
# pg2-4q1qk: the installer must not leave a Nix-declared-`false` plugin enabled
# ----------------------------------------------------------------------------

@test "regression: a REAL install that enables a declared-FALSE plugin is restored to false" {
  # THE regression that matters. The install is genuine, not a short-circuit:
  # the cache holds the pre-bump version and the mock creates a NEW version dir,
  # exactly as a marketplace HEAD advance does. This is the case an apply whose
  # installs all short-circuit can never verify.
  echo '{"enabledPlugins":{"jvm@ziprecruiter":false}}' > "$SETTINGS"
  _write_manifest "ziprecruiter" "jvm" "19b5ada4caa6" '{"name":"jvm"}'
  _mock_claude_enabling "$SETTINGS" "$CACHE_ROOT/ziprecruiter/jvm/c29658bd3aca"

  run "$SCRIPT" "$CLAUDE_BIN" "jvm@ziprecruiter" "$CACHE_ROOT" "$SETTINGS" "false"

  [ "$status" -eq 0 ]
  # The install ran, and it really was a NON-short-circuiting one.
  grep -Fxq "plugin install jvm@ziprecruiter --scope user" "$CALLS"
  [ -f "$CACHE_ROOT/ziprecruiter/jvm/c29658bd3aca/.claude-plugin/plugin.json" ]
  # The plugin stays INSTALLED but the Nix-declared `false` is what survives.
  [ "$(_enabled_of "jvm@ziprecruiter")" = "false" ]
  [[ "$output" == *"jvm@ziprecruiter enablement restored to false at user scope (was true)"* ]]
}

@test "regression: the SHORT-CIRCUIT install path also enables, and is also restored" {
  # The already-installed same-version install is NOT a safe path: it enables
  # too (measured). A witness plugin whose version never moved therefore proves
  # nothing about staying disabled, which is why the earlier pg-pr witness was
  # invalid. No new cache dir here — nothing is pulled — yet the key still flips.
  echo '{"enabledPlugins":{"slack@ziprecruiter":false}}' > "$SETTINGS"
  _write_manifest "ziprecruiter" "slack" "19b5ada4caa6" '{"name":"slack"}'
  _mock_claude_enabling "$SETTINGS" ""

  run "$SCRIPT" "$CLAUDE_BIN" "slack@ziprecruiter" "$CACHE_ROOT" "$SETTINGS" "false"

  [ "$status" -eq 0 ]
  # Only the pre-existing cached version — no new content was pulled.
  run bash -c 'ls "$CACHE_ROOT/ziprecruiter/slack" | tr "\n" " "'
  [ "$output" = "19b5ada4caa6 " ]
  [ "$(_enabled_of "slack@ziprecruiter")" = "false" ]
}

@test "declared TRUE is asserted too: a disabled key is restored to true" {
  # The restore asserts the DECLARED value, in both directions — it is not a
  # blanket disable. install fails / update succeeds here, so nothing in the CLI
  # path enables, and only the restore can produce `true`.
  echo '{"enabledPlugins":{"beads@beads-marketplace":false}}' > "$SETTINGS"
  _mock_claude 1 0 "already installed" ""

  run "$SCRIPT" "$CLAUDE_BIN" "beads@beads-marketplace" "$CACHE_ROOT" "$SETTINGS" "true"

  [ "$status" -eq 0 ]
  [ "$(_enabled_of "beads@beads-marketplace")" = "true" ]
  [[ "$output" == *"beads@beads-marketplace enablement restored to true at user scope (was false)"* ]]
}

@test "declared value is asserted even when install AND update both fail" {
  # The Nix declaration is authoritative regardless of how the CLI fared: a
  # partially-applied install must not leave the plugin enabled.
  echo '{"enabledPlugins":{"jvm@ziprecruiter":true}}' > "$SETTINGS"
  _mock_claude 1 1 "install boom" "update boom"

  run --separate-stderr "$SCRIPT" "$CLAUDE_BIN" "jvm@ziprecruiter" "$CACHE_ROOT" "$SETTINGS" "false"

  [ "$status" -eq 0 ]
  [[ "$stderr" == *"WARNING jvm@ziprecruiter install/update failed"* ]]
  [ "$(_enabled_of "jvm@ziprecruiter")" = "false" ]
}

@test "restore creates an absent key and touches nothing else; second run is a silent no-op" {
  # Idempotence plus blast radius: only .enabledPlugins[<spec>] may change.
  # extraKnownMarketplaces matters specifically — `marketplace add` writes it
  # into this same file, so the restore must not fight register-marketplace.
  cat > "$SETTINGS" <<'JSON'
{
  "model": "opus[1m]",
  "enabledPlugins": { "other@m": true },
  "extraKnownMarketplaces": {
    "ziprecruiter": { "source": { "source": "directory", "path": "/Volumes/ziprecruiter/pristine" } }
  }
}
JSON
  _mock_claude_enabling "$SETTINGS" ""

  run "$SCRIPT" "$CLAUDE_BIN" "findev@ziprecruiter" "$CACHE_ROOT" "$SETTINGS" "false"
  [ "$status" -eq 0 ]
  [ "$(_enabled_of "findev@ziprecruiter")" = "false" ]
  [[ "$output" == *"restored to false at user scope (was true)"* ]]

  # Untouched neighbours.
  [ "$(jq -r '.model' "$SETTINGS")" = "opus[1m]" ]
  [ "$(_enabled_of "other@m")" = "true" ]
  [ "$(jq -r '.extraKnownMarketplaces.ziprecruiter.source.path' "$SETTINGS")" = "/Volumes/ziprecruiter/pristine" ]

  # Second run: the install enables again, the restore corrects again, and the
  # file converges on the same content.
  local before after
  before="$(jq -S . "$SETTINGS")"
  run "$SCRIPT" "$CLAUDE_BIN" "findev@ziprecruiter" "$CACHE_ROOT" "$SETTINGS" "false"
  [ "$status" -eq 0 ]
  after="$(jq -S . "$SETTINGS")"
  [ "$before" = "$after" ]
  [ ! -f "$SETTINGS.tmp" ]
}

@test "already-declared value: restore stays silent (no spurious activation line)" {
  # A plugin whose install did not move enablement must produce no output — the
  # restore line is a signal that drift was corrected, so it must not cry wolf.
  echo '{"enabledPlugins":{"beads@beads-marketplace":true}}' > "$SETTINGS"
  _mock_claude_enabling "$SETTINGS" ""

  run "$SCRIPT" "$CLAUDE_BIN" "beads@beads-marketplace" "$CACHE_ROOT" "$SETTINGS" "true"

  [ "$status" -eq 0 ]
  [[ "$output" != *"enablement restored"* ]]
  [ "$(_enabled_of "beads@beads-marketplace")" = "true" ]
}

@test "3-arg form is unchanged: no settings file is written or required" {
  # Back-compat for a plugin with no enabledPlugins declaration: the script must
  # leave enablement entirely to Claude Code.
  echo '{"enabledPlugins":{"beads@beads-marketplace":false}}' > "$SETTINGS"
  _mock_claude_enabling "$SETTINGS" ""

  run "$SCRIPT" "$CLAUDE_BIN" "beads@beads-marketplace" "$CACHE_ROOT"

  [ "$status" -eq 0 ]
  # Only the mock (i.e. Claude Code itself) wrote enablement.
  [ "$(_enabled_of "beads@beads-marketplace")" = "true" ]
  [[ "$output" != *"enablement restored"* ]]
}

@test "missing settings file: non-fatal WARNING, no crash" {
  _mock_claude 0 0 "" ""

  run --separate-stderr "$SCRIPT" "$CLAUDE_BIN" "beads@beads-marketplace" "$CACHE_ROOT" \
    "$TMP/absent/settings.json" "false"

  [ "$status" -eq 0 ]
  [[ "$stderr" == *"enablement not restored: no settings file"* ]]
}

@test "a half-supplied pair (4 args) is a usage error, not a silent skip" {
  _mock_claude 0 0 "" ""

  run --separate-stderr "$SCRIPT" "$CLAUDE_BIN" "beads@beads-marketplace" "$CACHE_ROOT" "$SETTINGS"

  [ "$status" -eq 64 ]
  [[ "$stderr" == *"usage:"* ]]
  # The CLI was never invoked.
  [ ! -s "$CALLS" ]
}

@test "a non-boolean declared value is a usage error" {
  _mock_claude 0 0 "" ""

  run --separate-stderr "$SCRIPT" "$CLAUDE_BIN" "beads@beads-marketplace" "$CACHE_ROOT" \
    "$SETTINGS" "yes"

  [ "$status" -eq 64 ]
  [[ "$stderr" == *"must be 'true' or 'false'"* ]]
  [ ! -s "$CALLS" ]
}

@test "an EMPTY declared value is a usage error, not a degraded restore" {
  # Rejected up front rather than reaching jq as invalid JSON, where it would
  # surface as a non-fatal warning and hide the caller bug.
  _mock_claude 0 0 "" ""

  run --separate-stderr "$SCRIPT" "$CLAUDE_BIN" "beads@beads-marketplace" "$CACHE_ROOT" \
    "$SETTINGS" ""

  [ "$status" -eq 64 ]
  [[ "$stderr" == *"must be 'true' or 'false'"* ]]
  [ ! -s "$CALLS" ]
}
