#!/usr/bin/env bats
# test_scope_split: activation.sh routes managed import blocks to TWO target
# files per city based on pack scope:
#   - city-scope packs ([imports.<name>])              → <city>/pack.toml
#   - rig-scope  packs ([defaults.rig.imports.<name>]) → <city>/city.toml
#
# gascity 1.2.x rejects [defaults.rig.imports] in pack.toml, so rig-scope
# imports must be relocated to city.toml. City-scope imports are unchanged.
load test_helper

# mkRigPack NAME → fake rig-scope pack (with .pack-meta.json scope="rig").
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

# mkCityPack NAME → fake city-scope pack (no meta file → "city" fallback).
mkCityPack() {
  local name="$1"
  local dir="$TMP/$name"
  mkdir -p "$dir"
  cat >"$dir/pack.toml" <<TOML
[pack]
name = "$name"
schema = 2
TOML
  echo "$dir"
}

@test "mixed scopes: city-scope → pack.toml, rig-scope → city.toml" {
  local city; city=$(mkCity gc)
  printf '%s\n' '[workspace]' 'name = "gc"' >"$city/city.toml"
  local rigpack;  rigpack=$(mkRigPack "pgii-workers")
  local citypack; citypack=$(mkCityPack "pgii-pr-support")

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "pgii-workers" "$rigpack" \
                                      "pgii-pr-support" "$citypack")"
  [ "$status" -eq 0 ]

  # rig-scope: in city.toml only.
  blockExists "$city/city.toml" "pgii-workers"
  grep -q "\[defaults\.rig\.imports\.pgii-workers\]" "$city/city.toml"
  run ! blockExists "$city/pack.toml" "pgii-workers"

  # city-scope: in pack.toml only.
  blockExists "$city/pack.toml" "pgii-pr-support"
  grep -q "\[imports\.pgii-pr-support\]" "$city/pack.toml"
  run ! blockExists "$city/city.toml" "pgii-pr-support"

  # Hand-written city.toml content survives untouched.
  grep -q "^\[workspace\]" "$city/city.toml"
}

@test "removal-on-disable: rig pack dropped from city.toml, city pack dropped from pack.toml" {
  local city; city=$(mkCity gc $'[pack]\nname = "gc"\nschema = 2')
  # Seed pack.toml with a managed city-scope block.
  cat >>"$city/pack.toml" <<EOF

# BEGIN pgii-pack:pgii-pr-support (managed)
[imports.pgii-pr-support]
source = "/nix/store/old-pr-support"
export = true
# END pgii-pack:pgii-pr-support (managed)
EOF
  # Hand-written city.toml plus a managed rig-scope block.
  cat >"$city/city.toml" <<EOF
[workspace]
name = "gc"

# BEGIN pgii-pack:pgii-workers (managed)
[defaults.rig.imports.pgii-workers]
source = "/nix/store/old-workers"
export = true
# END pgii-pack:pgii-workers (managed)
EOF

  # Re-run with NO packs enabled → both managed blocks must be stripped.
  run "$SCRIPT" --cities "$(citiesJson "$city")" --packs '{}'
  [ "$status" -eq 0 ]

  run ! blockExists "$city/pack.toml" "pgii-pr-support"
  run ! blockExists "$city/city.toml" "pgii-workers"

  # Hand-written content in both files survives.
  grep -q "^\[workspace\]" "$city/city.toml"
}

@test "no rig-scope packs: city.toml is not created" {
  local city; city=$(mkCity gc $'[pack]\nname = "gc"\nschema = 2')
  local citypack; citypack=$(mkCityPack "pgii-pr-support")

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "pgii-pr-support" "$citypack")"
  [ "$status" -eq 0 ]

  blockExists "$city/pack.toml" "pgii-pr-support"
  # No rig-scope pack → no city.toml should have been created.
  [ ! -f "$city/city.toml" ]
}

@test "empty packs + no city.toml: city.toml stays absent" {
  local city; city=$(mkCity gc $'[pack]\nname = "gc"\nschema = 2')

  run "$SCRIPT" --cities "$(citiesJson "$city")" --packs '{}'
  [ "$status" -eq 0 ]

  [ ! -f "$city/city.toml" ]
}

@test "city-scope hand-written collision in pack.toml still guards" {
  local city; city=$(mkCity gc $'[imports.pgii-pr-support]\nsource = "/by-hand"')
  local citypack; citypack=$(mkCityPack "pgii-pr-support")

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "pgii-pr-support" "$citypack")"
  [ "$status" -ne 0 ]
  [[ "$output" == *"Hand-written [imports.pgii-pr-support] exists"* ]]
  grep -q "/by-hand" "$city/pack.toml"
}

@test "rig-scope idempotency: second run with same packs is a no-op for both files" {
  local city; city=$(mkCity gc $'[workspace]\nname = "gc"')
  local rigpack;  rigpack=$(mkRigPack "pgii-workers")
  local citypack; citypack=$(mkCityPack "pgii-pr-support")

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "pgii-workers" "$rigpack" \
                                      "pgii-pr-support" "$citypack")"
  [ "$status" -eq 0 ]

  touch -t 202001010000 "$city/pack.toml" "$city/city.toml"
  local pb; pb=$(stat -c %Y "$city/pack.toml" 2>/dev/null || stat -f%m "$city/pack.toml")
  local cb; cb=$(stat -c %Y "$city/city.toml" 2>/dev/null || stat -f%m "$city/city.toml")

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "pgii-workers" "$rigpack" \
                                      "pgii-pr-support" "$citypack")"
  [ "$status" -eq 0 ]

  local pa; pa=$(stat -c %Y "$city/pack.toml" 2>/dev/null || stat -f%m "$city/pack.toml")
  local ca; ca=$(stat -c %Y "$city/city.toml" 2>/dev/null || stat -f%m "$city/city.toml")
  [ "$pb" -eq "$pa" ]
  [ "$cb" -eq "$ca" ]
}
