#!/usr/bin/env bats
load test_helper

@test "multi-pack: three packs land in one invocation" {
  local city; city=$(mkCity gc)
  local packs
  packs=$(packsJson \
    "pgii-pack-foo" "/nix/store/foo" \
    "pgii-pack-bar" "/nix/store/bar" \
    "pgii-pack-baz" "/nix/store/baz")

  run "$SCRIPT" --cities "$(citiesJson "$city")" --packs "$packs"
  [ "$status" -eq 0 ]

  blockExists "$city/city.toml" "pgii-pack-foo"
  blockExists "$city/city.toml" "pgii-pack-bar"
  blockExists "$city/city.toml" "pgii-pack-baz"

  [ "$(blockPath "$city/city.toml" "pgii-pack-foo")" = "/nix/store/foo" ]
  [ "$(blockPath "$city/city.toml" "pgii-pack-bar")" = "/nix/store/bar" ]
  [ "$(blockPath "$city/city.toml" "pgii-pack-baz")" = "/nix/store/baz" ]
}

@test "multi-pack: two existing + one new + one disabled" {
  local seed
  seed=$(cat <<EOF
# BEGIN pgii-pack:pgii-pack-foo (managed)
[packs.pgii-pack-foo]
path = "/nix/store/OLD-foo"
# END pgii-pack:pgii-pack-foo (managed)

# BEGIN pgii-pack:pgii-pack-bar (managed)
[packs.pgii-pack-bar]
path = "/nix/store/bar"
# END pgii-pack:pgii-pack-bar (managed)

# BEGIN pgii-pack:pgii-pack-old (managed)
[packs.pgii-pack-old]
path = "/nix/store/old"
# END pgii-pack:pgii-pack-old (managed)
EOF
)
  local city; city=$(mkCity gc "$seed")
  local packs
  packs=$(packsJson \
    "pgii-pack-foo" "/nix/store/NEW-foo" \
    "pgii-pack-bar" "/nix/store/bar" \
    "pgii-pack-new" "/nix/store/new")

  run "$SCRIPT" --cities "$(citiesJson "$city")" --packs "$packs"
  [ "$status" -eq 0 ]

  [ "$(blockPath "$city/city.toml" "pgii-pack-foo")" = "/nix/store/NEW-foo" ]
  [ "$(blockPath "$city/city.toml" "pgii-pack-bar")" = "/nix/store/bar" ]
  blockExists "$city/city.toml" "pgii-pack-new"
  run ! blockExists "$city/city.toml" "pgii-pack-old"
}
