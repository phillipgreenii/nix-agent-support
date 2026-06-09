#!/usr/bin/env bats
load test_helper

@test "hand-written collision: errors when [imports.X] exists without sentinel" {
  local seed
  seed=$(cat <<EOF
[pack]
name = "gc"
schema = 2

[imports.pgii-pack-foo]
source = "/Users/phillipg/somewhere-by-hand"
EOF
)
  local city; city=$(mkCity gc "$seed")
  local cities; cities=$(citiesJson "$city")
  local packs;  packs=$(packsJson "pgii-pack-foo" "/nix/store/aaa")

  run "$SCRIPT" --cities "$cities" --packs "$packs"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "Hand-written [imports.pgii-pack-foo] exists" ]]

  # File untouched.
  grep -q "somewhere-by-hand" "$city/pack.toml"
}

@test "city-scope un-sentineled block from a nix-store build of this pack is adopted, not rejected" {
  # Symmetric to the rig-scope case: if gascity strips our sentinels from
  # pack.toml, the bare [imports.<name>] still points at a /nix/store build of
  # this pack. Adopt and re-wrap it instead of failing the activation.
  local seed
  seed=$(cat <<EOF
[pack]
name = "gc"
schema = 2

[imports.pgii-pack-foo]
source = "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-pgii-pack-foo-0.1.0"
export = true
EOF
)
  local city; city=$(mkCity gc "$seed")

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "pgii-pack-foo" "/nix/store/ccccccccccccccccccccccccccccccccc-pgii-pack-foo-0.1.0")"
  [ "$status" -eq 0 ]
  blockExists "$city/pack.toml" "pgii-pack-foo"
  [ "$(blockPath "$city/pack.toml" "pgii-pack-foo")" = "/nix/store/ccccccccccccccccccccccccccccccccc-pgii-pack-foo-0.1.0" ]
  # No duplicate: the stale bare table is gone, exactly one declaration remains.
  [ "$(grep -c '^\[imports\.pgii-pack-foo\]$' "$city/pack.toml")" -eq 1 ]
  # Hand-written [pack] header survives.
  grep -q "^\[pack\]" "$city/pack.toml"
}

@test "hand-written collision: managed block does NOT trigger collision" {
  local seed
  seed=$(cat <<EOF
# BEGIN pgii-pack:pgii-pack-foo (managed)
[imports.pgii-pack-foo]
source = "/nix/store/OLD"
export = true
# END pgii-pack:pgii-pack-foo (managed)
EOF
)
  local city; city=$(mkCity gc "$seed")

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "pgii-pack-foo" "/nix/store/NEW")"
  [ "$status" -eq 0 ]
  [ "$(blockPath "$city/pack.toml" "pgii-pack-foo")" = "/nix/store/NEW" ]
}
