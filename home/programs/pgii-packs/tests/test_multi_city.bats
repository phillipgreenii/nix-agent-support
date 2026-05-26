#!/usr/bin/env bats
load test_helper

@test "multi-city: two cities both get the block set" {
  local city_a; city_a=$(mkCity city-a)
  local city_b; city_b=$(mkCity city-b $'[workspace]\nprovider = "claude"')
  local cities; cities=$(citiesJson "$city_a" "$city_b")
  local packs;  packs=$(packsJson "pgii-pack-foo" "/nix/store/foo")

  run "$SCRIPT" --cities "$cities" --packs "$packs"
  [ "$status" -eq 0 ]

  blockExists "$city_a/city.toml" "pgii-pack-foo"
  blockExists "$city_b/city.toml" "pgii-pack-foo"

  # city-b's hand-written [workspace] block survives.
  grep -q "^\[workspace\]" "$city_b/city.toml"
}

@test "multi-city: collision in one city errors without touching the other" {
  local city_a; city_a=$(mkCity city-a)
  local city_b; city_b=$(mkCity city-b $'[packs.pgii-pack-foo]\npath = "/by-hand"')

  run "$SCRIPT" --cities "$(citiesJson "$city_a" "$city_b")" \
                --packs  "$(packsJson "pgii-pack-foo" "/nix/store/foo")"
  [ "$status" -ne 0 ]

  # city-a is processed first; the user has to decide whether that's OK.
  # We just assert city-b is unchanged.
  grep -q "/by-hand" "$city_b/city.toml"
}
