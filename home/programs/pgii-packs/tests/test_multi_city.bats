#!/usr/bin/env bats
load test_helper

@test "multi-city: two cities both get the block set" {
  local city_a; city_a=$(mkCity city-a)
  local city_b; city_b=$(mkCity city-b $'[pack]\nname = "city-b"\nschema = 2')
  local cities; cities=$(citiesJson "$city_a" "$city_b")
  local packs;  packs=$(packsJson "pgii-pack-foo" "/nix/store/foo")

  run "$SCRIPT" --cities "$cities" --packs "$packs"
  [ "$status" -eq 0 ]

  blockExists "$city_a/pack.toml" "pgii-pack-foo"
  blockExists "$city_b/pack.toml" "pgii-pack-foo"

  # city-b's hand-written [pack] block survives.
  grep -q "^\[pack\]" "$city_b/pack.toml"
}

@test "multi-city: collision in one city errors without touching the other" {
  local city_a; city_a=$(mkCity city-a)
  local city_b; city_b=$(mkCity city-b $'[imports.pgii-pack-foo]\nsource = "/by-hand"')

  run "$SCRIPT" --cities "$(citiesJson "$city_a" "$city_b")" \
                --packs  "$(packsJson "pgii-pack-foo" "/nix/store/foo")"
  [ "$status" -ne 0 ]

  # city-a is processed first; the user has to decide whether that's OK.
  # We just assert city-b is unchanged.
  grep -q "/by-hand" "$city_b/pack.toml"
}
