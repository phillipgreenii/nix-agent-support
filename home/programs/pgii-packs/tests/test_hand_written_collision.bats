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
