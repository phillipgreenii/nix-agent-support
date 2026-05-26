#!/usr/bin/env bats
load test_helper

@test "replace existing: store path changes, single block remains" {
  local seed
  seed=$(cat <<EOF
[workspace]
provider = "claude"

# BEGIN pgii-pack:pgii-pack-foo (managed)
[packs.pgii-pack-foo]
path = "/nix/store/OLD-pgii-pack-foo"
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
  count=$(grep -cF "# BEGIN pgii-pack:pgii-pack-foo (managed)" "$city/city.toml")
  [ "$count" -eq 1 ]

  # And it points at the new path.
  [ "$(blockPath "$city/city.toml" "pgii-pack-foo")" = "/nix/store/NEW-pgii-pack-foo" ]

  # Pre-existing [workspace] block survives.
  grep -q "^\[workspace\]" "$city/city.toml"
}
