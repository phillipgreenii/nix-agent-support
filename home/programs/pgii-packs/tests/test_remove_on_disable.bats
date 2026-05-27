#!/usr/bin/env bats
load test_helper

@test "remove on disable: managed block for non-arg pack is dropped" {
  local seed
  seed=$(cat <<EOF
[pack]
name = "gc"
schema = 2

# BEGIN pgii-pack:pgii-pack-foo (managed)
[imports.pgii-pack-foo]
source = "/nix/store/aaa-pgii-pack-foo"
export = true
# END pgii-pack:pgii-pack-foo (managed)

# BEGIN pgii-pack:pgii-pack-bar (managed)
[imports.pgii-pack-bar]
source = "/nix/store/bbb-pgii-pack-bar"
export = true
# END pgii-pack:pgii-pack-bar (managed)
EOF
)
  local city; city=$(mkCity gc "$seed")
  local cities; cities=$(citiesJson "$city")
  # Only "foo" is enabled this rebuild; "bar" should be removed.
  local packs;  packs=$(packsJson "pgii-pack-foo" "/nix/store/aaa-pgii-pack-foo")

  run "$SCRIPT" --cities "$cities" --packs "$packs"
  [ "$status" -eq 0 ]

  blockExists "$city/pack.toml" "pgii-pack-foo"
  run ! blockExists "$city/pack.toml" "pgii-pack-bar"

  # Hand-written content survives.
  grep -q "^\[pack\]" "$city/pack.toml"
}

@test "remove on disable: empty --packs removes all managed blocks" {
  local seed
  seed=$(cat <<EOF
# BEGIN pgii-pack:pgii-pack-foo (managed)
[imports.pgii-pack-foo]
source = "/nix/store/aaa"
export = true
# END pgii-pack:pgii-pack-foo (managed)
EOF
)
  local city; city=$(mkCity gc "$seed")

  run "$SCRIPT" --cities "$(citiesJson "$city")" --packs '{}'
  [ "$status" -eq 0 ]
  run ! blockExists "$city/pack.toml" "pgii-pack-foo"
}
