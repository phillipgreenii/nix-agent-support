#!/usr/bin/env bats
load test_helper

@test "replace existing: store path changes, single block remains" {
  local seed
  seed=$(cat <<EOF
[pack]
name = "gc"
schema = 2

# BEGIN pgii-pack:pgii-pack-foo (managed)
[imports.pgii-pack-foo]
source = "/nix/store/OLD-pgii-pack-foo"
export = true
# END pgii-pack:pgii-pack-foo (managed)
EOF
)
  local city; city=$(mkCity gc "$seed")
  local cities; cities=$(citiesJson "$city")
  local packs;  packs=$(packsJson "pgii-pack-foo" "/nix/store/NEW-pgii-pack-foo")

  run "$SCRIPT" --cities "$cities" --packs "$packs"
  [ "$status" -eq 0 ]

  # Exactly one managed block for this pack.
  local count
  count=$(grep -cF "# BEGIN pgii-pack:pgii-pack-foo (managed)" "$city/pack.toml")
  [ "$count" -eq 1 ]

  # And it points at the new source path.
  [ "$(blockPath "$city/pack.toml" "pgii-pack-foo")" = "/nix/store/NEW-pgii-pack-foo" ]

  # Pre-existing [pack] block survives.
  grep -q "^\[pack\]" "$city/pack.toml"
}
