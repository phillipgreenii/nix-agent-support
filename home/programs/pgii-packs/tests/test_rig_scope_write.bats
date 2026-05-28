#!/usr/bin/env bats
# test_rig_scope_write: when a pack's .pack-meta.json declares scope="rig",
# activation.sh writes [defaults.rig.imports.<name>] instead of
# [imports.<name>] inside the managed-block sentinels.
#
# Companion to test_fresh_write.bats (which covers city-scope writes via the
# .pack-meta.json-absent fallback path).
load test_helper

# Create a fake rig-scope pack in $TMP/pack-rig with .pack-meta.json.
mkRigPack() {
  local name="$1"
  local dir="$TMP/$name"
  mkdir -p "$dir"
  cat >"$dir/pack.toml" <<TOML
[pack]
name = "$name"
schema = 2
TOML
  cat >"$dir/.pack-meta.json" <<JSON
{ "name": "$name", "version": "0.1.0", "scope": "rig" }
JSON
  echo "$dir"
}

@test "rig-scope pack writes [defaults.rig.imports.<name>] block" {
  local city; city=$(mkCity gc)
  local pack; pack=$(mkRigPack "test-rig-pack")

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "test-rig-pack" "$pack")"
  [ "$status" -eq 0 ]

  blockExists "$city/pack.toml" "test-rig-pack"
  grep -q "\[defaults\.rig\.imports\.test-rig-pack\]" "$city/pack.toml"
  grep -q "source = \"$pack\"" "$city/pack.toml"
  ! grep -q "\[imports\.test-rig-pack\]" "$city/pack.toml"
}

@test "rig-scope hand-written collision is detected" {
  local seed
  seed=$(cat <<EOF
[pack]
name = "gc"
schema = 2

[defaults.rig.imports.test-rig-pack]
source = "/already-managed-elsewhere"
EOF
)
  local city; city=$(mkCity gc "$seed")
  local pack; pack=$(mkRigPack "test-rig-pack")

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "test-rig-pack" "$pack")"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "Hand-written" ]]

  # File must be untouched.
  grep -q "already-managed-elsewhere" "$city/pack.toml"
}

@test "rig-scope managed block does NOT trigger collision" {
  local pack; pack=$(mkRigPack "test-rig-pack")
  local seed
  seed=$(cat <<EOF
# BEGIN pgii-pack:test-rig-pack (managed)
[defaults.rig.imports.test-rig-pack]
source = "/nix/store/OLD"
export = true
# END pgii-pack:test-rig-pack (managed)
EOF
)
  local city; city=$(mkCity gc "$seed")

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "test-rig-pack" "$pack")"
  [ "$status" -eq 0 ]
  [ "$(blockPath "$city/pack.toml" "test-rig-pack")" = "$pack" ]
}

@test "rig-scope rebuild with same path is a no-op (mtime unchanged)" {
  local pack; pack=$(mkRigPack "test-rig-pack")
  local city; city=$(mkCity gc)

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "test-rig-pack" "$pack")"
  [ "$status" -eq 0 ]

  # Freeze mtime to a known past time, then re-run.
  touch -t 202001010000 "$city/pack.toml"
  local before; before=$(stat -c %Y "$city/pack.toml" 2>/dev/null || stat -f%m "$city/pack.toml")

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "test-rig-pack" "$pack")"
  [ "$status" -eq 0 ]

  local after; after=$(stat -c %Y "$city/pack.toml" 2>/dev/null || stat -f%m "$city/pack.toml")
  [ "$before" -eq "$after" ]
}

@test "scope change from city to rig triggers rewrite with correct shape" {
  # Build a city-scope pack (no meta file → falls back to "city").
  local PACK_CITY="$TMP/scope-shifter"
  mkdir -p "$PACK_CITY"
  cat >"$PACK_CITY/pack.toml" <<TOML
[pack]
name = "scope-shifter"
schema = 2
TOML

  local city; city=$(mkCity gc)

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "scope-shifter" "$PACK_CITY")"
  [ "$status" -eq 0 ]

  grep -q "\[imports\.scope-shifter\]" "$city/pack.toml" \
    || { echo "first activation should have written [imports.scope-shifter]"; cat "$city/pack.toml"; false; }

  # Now add a meta file marking it rig-scope, re-run activation.
  cat >"$PACK_CITY/.pack-meta.json" <<JSON
{ "name": "scope-shifter", "version": "0.1.0", "scope": "rig" }
JSON

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "scope-shifter" "$PACK_CITY")"
  [ "$status" -eq 0 ]

  grep -q "\[defaults\.rig\.imports\.scope-shifter\]" "$city/pack.toml" \
    || { echo "rig-scope rewrite did not happen"; cat "$city/pack.toml"; false; }
  ! grep -q "\[imports\.scope-shifter\]" "$city/pack.toml" \
    || { echo "stale city-scope block remained after scope change"; cat "$city/pack.toml"; false; }
}
