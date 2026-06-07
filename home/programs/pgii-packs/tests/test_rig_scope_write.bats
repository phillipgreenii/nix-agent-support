#!/usr/bin/env bats
# test_rig_scope_write: when a pack's .pack-meta.json declares scope="rig",
# activation.sh writes [defaults.rig.imports.<name>] into the city's
# city.toml (NOT pack.toml). gascity 1.2.x rejects [defaults.rig.imports]
# in pack.toml ("belongs in city.toml, not pack.toml"); rig-scope imports
# must live in city.toml.
#
# Companion to test_fresh_write.bats (which covers city-scope writes via the
# .pack-meta.json-absent fallback path into pack.toml).
load test_helper

# Create a fake rig-scope pack in $TMP/<name> with .pack-meta.json.
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

@test "rig-scope pack writes [defaults.rig.imports.<name>] block into city.toml" {
  local city; city=$(mkCity gc)
  # Hand-written city.toml (the typical case: a city with rigs already has one).
  printf '%s\n' '[workspace]' 'name = "gc"' >"$city/city.toml"
  local pack; pack=$(mkRigPack "test-rig-pack")

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "test-rig-pack" "$pack")"
  [ "$status" -eq 0 ]

  # Lands in city.toml, NOT pack.toml.
  blockExists "$city/city.toml" "test-rig-pack"
  grep -q "\[defaults\.rig\.imports\.test-rig-pack\]" "$city/city.toml"
  grep -q "source = \"$pack\"" "$city/city.toml"
  run ! blockExists "$city/pack.toml" "test-rig-pack"
  run ! grep -q "\[defaults\.rig\.imports\.test-rig-pack\]" "$city/pack.toml"

  # Hand-written city.toml content survives.
  grep -q "^\[workspace\]" "$city/city.toml"
}

@test "rig-scope hand-written collision in city.toml is detected" {
  local city; city=$(mkCity gc $'[workspace]\nname = "gc"')
  # Hand-written rig import in city.toml, outside any managed sentinel.
  cat >>"$city/city.toml" <<EOF

[defaults.rig.imports.test-rig-pack]
source = "/already-managed-elsewhere"
EOF
  local pack; pack=$(mkRigPack "test-rig-pack")

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "test-rig-pack" "$pack")"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "Hand-written" ]]

  # File must be untouched.
  grep -q "already-managed-elsewhere" "$city/city.toml"
}

@test "rig-scope managed block in city.toml does NOT trigger collision" {
  local pack; pack=$(mkRigPack "test-rig-pack")
  local city; city=$(mkCity gc $'[workspace]\nname = "gc"')
  cat >>"$city/city.toml" <<EOF

# BEGIN pgii-pack:test-rig-pack (managed)
[defaults.rig.imports.test-rig-pack]
source = "/nix/store/OLD"
export = true
# END pgii-pack:test-rig-pack (managed)
EOF

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "test-rig-pack" "$pack")"
  [ "$status" -eq 0 ]
  [ "$(blockPath "$city/city.toml" "test-rig-pack")" = "$pack" ]
}

@test "rig-scope rebuild with same path is a no-op (city.toml mtime unchanged)" {
  local pack; pack=$(mkRigPack "test-rig-pack")
  local city; city=$(mkCity gc $'[workspace]\nname = "gc"')

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "test-rig-pack" "$pack")"
  [ "$status" -eq 0 ]
  [ -f "$city/city.toml" ]

  # Freeze mtime to a known past time, then re-run.
  touch -t 202001010000 "$city/city.toml"
  local before; before=$(stat -c %Y "$city/city.toml" 2>/dev/null || stat -f%m "$city/city.toml")

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "test-rig-pack" "$pack")"
  [ "$status" -eq 0 ]

  local after; after=$(stat -c %Y "$city/city.toml" 2>/dev/null || stat -f%m "$city/city.toml")
  [ "$before" -eq "$after" ]
}

@test "scope change from city to rig moves the block from pack.toml to city.toml" {
  # Build a city-scope pack (no meta file → falls back to "city").
  local PACK_CITY="$TMP/scope-shifter"
  mkdir -p "$PACK_CITY"
  cat >"$PACK_CITY/pack.toml" <<TOML
[pack]
name = "scope-shifter"
schema = 2
TOML

  local city; city=$(mkCity gc $'[workspace]\nname = "gc"')

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "scope-shifter" "$PACK_CITY")"
  [ "$status" -eq 0 ]

  grep -q "\[imports\.scope-shifter\]" "$city/pack.toml" \
    || { echo "first activation should have written [imports.scope-shifter] to pack.toml"; cat "$city/pack.toml"; false; }

  # Now add a meta file marking it rig-scope, re-run activation.
  cat >"$PACK_CITY/.pack-meta.json" <<JSON
{ "name": "scope-shifter", "version": "0.1.0", "scope": "rig" }
JSON

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "scope-shifter" "$PACK_CITY")"
  [ "$status" -eq 0 ]

  # Now in city.toml as a rig import...
  grep -q "\[defaults\.rig\.imports\.scope-shifter\]" "$city/city.toml" \
    || { echo "rig-scope rewrite to city.toml did not happen"; cat "$city/city.toml"; false; }
  # ...and the stale city-scope block is gone from pack.toml.
  ! blockExists "$city/pack.toml" "scope-shifter" \
    || { echo "stale city-scope block remained in pack.toml after scope change"; cat "$city/pack.toml"; false; }
}
