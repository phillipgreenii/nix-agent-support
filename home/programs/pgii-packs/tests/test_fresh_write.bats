#!/usr/bin/env bats
load test_helper

@test "fresh write: empty pack.toml gains managed block" {
  local city; city=$(mkCity gc)
  local cities; cities=$(citiesJson "$city")
  local packs;  packs=$(packsJson "pgii-pack-foo" "/nix/store/aaa-pgii-pack-foo")

  run "$SCRIPT" --cities "$cities" --packs "$packs"
  [ "$status" -eq 0 ]

  blockExists "$city/pack.toml" "pgii-pack-foo"
  [ "$(blockPath "$city/pack.toml" "pgii-pack-foo")" = "/nix/store/aaa-pgii-pack-foo" ]
}

@test "fresh write: non-existent pack.toml is created" {
  local city="$TMP/new-city"
  mkdir -p "$city/.gc"
  # No pack.toml created on disk.
  local cities; cities=$(citiesJson "$city")
  local packs;  packs=$(packsJson "pgii-pack-foo" "/nix/store/aaa")

  run "$SCRIPT" --cities "$cities" --packs "$packs"
  [ "$status" -eq 0 ]
  [ -f "$city/pack.toml" ]
  blockExists "$city/pack.toml" "pgii-pack-foo"
}
