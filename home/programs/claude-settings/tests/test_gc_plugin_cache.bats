#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Verify claude-settings-gc-plugin-cache.sh (pg2-x3a3t):
#   - primary rule: a version reachable from a LOCAL nix-built marketplace's
#     plugin.json (its `.version`, "+" -> "-") is kept; every sibling version
#     directory for that <marketplace>/<plugin> becomes a removal candidate
#   - fallback rule: when no local marketplace manifest names a live version
#     (non-local marketplace, or an orphaned plugin), the newest 2 version
#     directories BY MTIME are kept and the rest removed
#   - installed_plugins.json `installPath` entries are NEVER removed,
#     regardless of which rule would otherwise apply
#   - a directory holding a live `.in_use` lock is skipped and the skip is
#     reported (stderr), never silently dropped
#   - --dry-run lists candidates and a reclaimed-byte total, and removes
#     nothing
#   - usage errors (bad option, wrong arg count) exit 64

bats_require_minimum_version 1.5.0

load test_helper

SCRIPT="$(resolve_claude_settings_script claude-settings-gc-plugin-cache)"

setup() {
  TMP="$(mktemp -d)"
  export TMP
  CACHE_ROOT="$TMP/cache"
  MARKETPLACES_ROOT="$TMP/marketplaces"
  mkdir -p "$CACHE_ROOT" "$MARKETPLACES_ROOT"
  export CACHE_ROOT MARKETPLACES_ROOT
}

teardown() {
  [ -n "$TMP" ] && rm -rf "$TMP"
}

# Create a cache version directory with a little content (so it has a
# nonzero, but otherwise irrelevant, size) and an explicit mtime.
#   $1 = marketplace, $2 = plugin, $3 = version, $4 = mtime (touch -t form,
#        e.g. "202601010000.00"), $5 = content (optional)
_mk_version_dir() {
  local dir="$CACHE_ROOT/$1/$2/$3"
  mkdir -p "$dir"
  printf '%s' "${5:-x}" >"$dir/payload"
  touch -t "$4" "$dir" "$dir/payload"
}

# Register a plugin under a LOCAL marketplace, matching the layout GC reads:
# <marketplaces_root>/<marketplace>/<plugin>/.claude-plugin/plugin.json {version}
#   $1 = marketplace, $2 = plugin, $3 = version (nix content-hash form, "+")
_mk_local_manifest() {
  local dir="$MARKETPLACES_ROOT/$1/$2/.claude-plugin"
  mkdir -p "$dir"
  printf '{"name":"%s","version":"%s"}' "$2" "$3" >"$dir/plugin.json"
}

# $1 = raw installed_plugins.json content, written beside the cache dir
# (dirname(cache_root)/installed_plugins.json — the script's own convention).
_write_installed_plugins() {
  printf '%s' "$1" >"$(dirname "$CACHE_ROOT")/installed_plugins.json"
}

@test "primary rule: the version matching the local marketplace manifest is kept, siblings removed" {
  _mk_local_manifest "pgii-local" "pb" "1.0.0+990632f6"
  _mk_version_dir "pgii-local" "pb" "1.0.0-990632f6" "202608010000.00"
  _mk_version_dir "pgii-local" "pb" "1.0.0-oldhash01" "202601010000.00"

  run "$SCRIPT" "$CACHE_ROOT" "$MARKETPLACES_ROOT"

  [ "$status" -eq 0 ]
  [ -d "$CACHE_ROOT/pgii-local/pb/1.0.0-990632f6" ]
  [ ! -d "$CACHE_ROOT/pgii-local/pb/1.0.0-oldhash01" ]
  [[ "$output" == *"removed pgii-local/pb/1.0.0-oldhash01"* ]]
}

@test "fallback rule: no local manifest — newest 2 by mtime kept, older removed" {
  _mk_version_dir "superpowers-marketplace" "superpowers" "4.0.0" "202601010000.00"
  _mk_version_dir "superpowers-marketplace" "superpowers" "5.1.0" "202606250000.00"
  _mk_version_dir "superpowers-marketplace" "superpowers" "6.0.3" "202608120000.00"
  _mk_version_dir "superpowers-marketplace" "superpowers" "6.3.0" "202608190000.00"

  run "$SCRIPT" "$CACHE_ROOT" "$MARKETPLACES_ROOT"

  [ "$status" -eq 0 ]
  # Newest 2 survive.
  [ -d "$CACHE_ROOT/superpowers-marketplace/superpowers/6.3.0" ]
  [ -d "$CACHE_ROOT/superpowers-marketplace/superpowers/6.0.3" ]
  # Older 2 are gone.
  [ ! -d "$CACHE_ROOT/superpowers-marketplace/superpowers/5.1.0" ]
  [ ! -d "$CACHE_ROOT/superpowers-marketplace/superpowers/4.0.0" ]
  [[ "$output" == *"removed superpowers-marketplace/superpowers/5.1.0"* ]]
  [[ "$output" == *"removed superpowers-marketplace/superpowers/4.0.0"* ]]
}

@test "fallback rule: orphaned plugin (was local, no longer in the manifest) also gets newest-2, not wiped" {
  # No _mk_local_manifest call: the plugin dir under MARKETPLACES_ROOT is
  # simply absent, exactly as if it had been dropped from the build.
  _mk_version_dir "pgii-local" "retired-plugin" "1.0.0-aaa11111" "202601010000.00"
  _mk_version_dir "pgii-local" "retired-plugin" "1.0.0-bbb22222" "202602010000.00"
  _mk_version_dir "pgii-local" "retired-plugin" "1.0.0-ccc33333" "202603010000.00"

  run "$SCRIPT" "$CACHE_ROOT" "$MARKETPLACES_ROOT"

  [ "$status" -eq 0 ]
  # Newest 2 survive, even with no manifest to consult — never wiped to zero.
  [ -d "$CACHE_ROOT/pgii-local/retired-plugin/1.0.0-ccc33333" ]
  [ -d "$CACHE_ROOT/pgii-local/retired-plugin/1.0.0-bbb22222" ]
  [ ! -d "$CACHE_ROOT/pgii-local/retired-plugin/1.0.0-aaa11111" ]
}

@test "installPath in installed_plugins.json is never removed, even though it is the oldest fallback candidate" {
  _mk_version_dir "beads-marketplace" "beads" "1.0.2" "202601010000.00"
  _mk_version_dir "beads-marketplace" "beads" "1.0.4" "202602010000.00"
  _mk_version_dir "beads-marketplace" "beads" "1.0.5" "202603010000.00"
  _mk_version_dir "beads-marketplace" "beads" "1.1.0" "202604010000.00"
  # The OLDEST version (1.0.2) is what installed_plugins.json records as
  # currently installed — an inconsistent-looking but real scenario (e.g. a
  # scope mismatch) that must still win over the mtime fallback.
  _write_installed_plugins "$(
    jq -n --arg p "$CACHE_ROOT/beads-marketplace/beads/1.0.2" '{
      version: 2,
      plugins: { "beads@beads-marketplace": [ { scope: "user", installPath: $p, version: "1.0.2" } ] }
    }'
  )"

  run "$SCRIPT" "$CACHE_ROOT" "$MARKETPLACES_ROOT"

  [ "$status" -eq 0 ]
  # Protected despite being older than the newest-2 cutoff.
  [ -d "$CACHE_ROOT/beads-marketplace/beads/1.0.2" ]
  # Newest 2 also survive (both rules can keep independent directories).
  [ -d "$CACHE_ROOT/beads-marketplace/beads/1.1.0" ]
  [ -d "$CACHE_ROOT/beads-marketplace/beads/1.0.5" ]
  # The remaining, unprotected, non-newest-2 version is removed.
  [ ! -d "$CACHE_ROOT/beads-marketplace/beads/1.0.4" ]
}

@test "a directory holding a live .in_use lock is skipped and the skip is reported, never silently dropped" {
  _mk_version_dir "superpowers-marketplace" "superpowers" "4.0.0" "202601010000.00"
  _mk_version_dir "superpowers-marketplace" "superpowers" "5.1.0" "202606250000.00"
  _mk_version_dir "superpowers-marketplace" "superpowers" "6.0.3" "202608120000.00"
  _mk_version_dir "superpowers-marketplace" "superpowers" "6.3.0" "202608190000.00"
  # 4.0.0 would otherwise be removed by the mtime fallback; a concurrent
  # session holds it. Re-pin the directory's own mtime after creating the
  # lock file — creating a new dirent bumps the containing directory's mtime
  # to "now", which would otherwise make 4.0.0 look newest and pass the
  # newest-2 cut on its own, defeating the point of this test (the lock, not
  # mtime, must be what protects it).
  touch "$CACHE_ROOT/superpowers-marketplace/superpowers/4.0.0/.in_use"
  touch -t "202601010000.00" "$CACHE_ROOT/superpowers-marketplace/superpowers/4.0.0"

  run --separate-stderr "$SCRIPT" "$CACHE_ROOT" "$MARKETPLACES_ROOT"

  [ "$status" -eq 0 ]
  [ -d "$CACHE_ROOT/superpowers-marketplace/superpowers/4.0.0" ]
  [[ "$stderr" == *"WARNING skipping superpowers-marketplace/superpowers/4.0.0: held by a live .in_use lock"* ]]
  [[ "$stderr" == *"WARNING plugin cache GC: skipped 1 version dir(s) held by a live .in_use lock"* ]]
}

@test "--dry-run lists candidates and a reclaimed-byte total, and removes nothing" {
  _mk_local_manifest "pgii-local" "pb" "1.0.0+990632f6"
  _mk_version_dir "pgii-local" "pb" "1.0.0-990632f6" "202608010000.00"
  _mk_version_dir "pgii-local" "pb" "1.0.0-oldhash01" "202601010000.00"

  run "$SCRIPT" --dry-run "$CACHE_ROOT" "$MARKETPLACES_ROOT"

  [ "$status" -eq 0 ]
  [[ "$output" == *"would remove pgii-local/pb/1.0.0-oldhash01"* ]]
  [[ "$output" == *"dry-run: would reclaim "*" bytes across 1 version dir(s)"* ]]
  # Nothing was actually touched.
  [ -d "$CACHE_ROOT/pgii-local/pb/1.0.0-990632f6" ]
  [ -d "$CACHE_ROOT/pgii-local/pb/1.0.0-oldhash01" ]
}

@test "single cached version: nothing to remove under either rule" {
  _mk_local_manifest "pgii-local" "pb" "1.0.0+990632f6"
  _mk_version_dir "pgii-local" "pb" "1.0.0-990632f6" "202608010000.00"

  run "$SCRIPT" "$CACHE_ROOT" "$MARKETPLACES_ROOT"

  [ "$status" -eq 0 ]
  [ -d "$CACHE_ROOT/pgii-local/pb/1.0.0-990632f6" ]
  [[ "$output" != *"removed"* ]]
}

@test "no cache_root directory: exits 0, does nothing" {
  rm -rf "$CACHE_ROOT"

  run "$SCRIPT" "$CACHE_ROOT" "$MARKETPLACES_ROOT"

  [ "$status" -eq 0 ]
}

@test "no marketplaces_root directory: falls back to newest-2 for everything, no crash" {
  rm -rf "$MARKETPLACES_ROOT"
  _mk_version_dir "beads-marketplace" "beads" "1.0.2" "202601010000.00"
  _mk_version_dir "beads-marketplace" "beads" "1.1.0" "202602010000.00"
  _mk_version_dir "beads-marketplace" "beads" "1.2.1" "202603010000.00"

  run "$SCRIPT" "$CACHE_ROOT" "$MARKETPLACES_ROOT"

  [ "$status" -eq 0 ]
  [ -d "$CACHE_ROOT/beads-marketplace/beads/1.2.1" ]
  [ -d "$CACHE_ROOT/beads-marketplace/beads/1.1.0" ]
  [ ! -d "$CACHE_ROOT/beads-marketplace/beads/1.0.2" ]
}

@test "wrong arg count: usage error, exit 64" {
  run "$SCRIPT" "$CACHE_ROOT"

  [ "$status" -eq 64 ]
  [[ "$output" == *"usage:"* ]]
}

@test "unknown option: usage error, exit 64" {
  run "$SCRIPT" --bogus "$CACHE_ROOT" "$MARKETPLACES_ROOT"

  [ "$status" -eq 64 ]
  [[ "$output" == *"usage:"* ]]
}
