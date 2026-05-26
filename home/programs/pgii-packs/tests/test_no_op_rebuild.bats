#!/usr/bin/env bats
load test_helper

@test "no-op rebuild: file mtime unchanged when block path matches" {
  local seed
  seed=$(cat <<EOF
# BEGIN pgii-pack:pgii-pack-foo (managed)
[packs.pgii-pack-foo]
path = "/nix/store/abc-pgii-pack-foo"
# END pgii-pack:pgii-pack-foo (managed)
EOF
)
  local city; city=$(mkCity gc "$seed")
  local cities; cities=$(citiesJson "$city")
  local packs;  packs=$(packsJson "pgii-pack-foo" "/nix/store/abc-pgii-pack-foo")

  # Freeze mtime to a known past time, then re-run.
  touch -t 202001010000 "$city/city.toml"
  local before; before=$(stat -c %Y "$city/city.toml" 2>/dev/null || stat -f%m "$city/city.toml")

  run "$SCRIPT" --cities "$cities" --packs "$packs"
  [ "$status" -eq 0 ]

  local after; after=$(stat -c %Y "$city/city.toml" 2>/dev/null || stat -f%m "$city/city.toml")
  [ "$before" -eq "$after" ]
}
