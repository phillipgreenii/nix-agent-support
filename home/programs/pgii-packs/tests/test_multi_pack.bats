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

  blockExists "$city/pack.toml" "pgii-pack-foo"
  blockExists "$city/pack.toml" "pgii-pack-bar"
  blockExists "$city/pack.toml" "pgii-pack-baz"

  [ "$(blockPath "$city/pack.toml" "pgii-pack-foo")" = "/nix/store/foo" ]
  [ "$(blockPath "$city/pack.toml" "pgii-pack-bar")" = "/nix/store/bar" ]
  [ "$(blockPath "$city/pack.toml" "pgii-pack-baz")" = "/nix/store/baz" ]
}

@test "multi-pack: two existing + one new + one disabled" {
  local seed
  seed=$(cat <<EOF
# BEGIN pgii-pack:pgii-pack-foo (managed)
[imports.pgii-pack-foo]
source = "/nix/store/OLD-foo"
export = true
# END pgii-pack:pgii-pack-foo (managed)

# BEGIN pgii-pack:pgii-pack-bar (managed)
[imports.pgii-pack-bar]
source = "/nix/store/bar"
export = true
# END pgii-pack:pgii-pack-bar (managed)

# BEGIN pgii-pack:pgii-pack-old (managed)
[imports.pgii-pack-old]
source = "/nix/store/old"
export = true
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

  [ "$(blockPath "$city/pack.toml" "pgii-pack-foo")" = "/nix/store/NEW-foo" ]
  [ "$(blockPath "$city/pack.toml" "pgii-pack-bar")" = "/nix/store/bar" ]
  blockExists "$city/pack.toml" "pgii-pack-new"
  run ! blockExists "$city/pack.toml" "pgii-pack-old"
}
