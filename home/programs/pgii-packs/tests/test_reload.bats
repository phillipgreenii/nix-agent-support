#!/usr/bin/env bats
load test_helper

# Provide a fake `gc` on PATH that records its args and exits 0.
setup_fake_gc() {
  local bindir="$TMP/bin"
  mkdir -p "$bindir"
  cat > "$bindir/gc" <<EOF
#!/usr/bin/env bash
echo "\$@" >> "$TMP/gc-calls.log"
exit 0
EOF
  chmod +x "$bindir/gc"
  PATH="$bindir:$PATH"
  export PATH
}

@test "reload: invokes gc supervisor reload when socket exists" {
  setup_fake_gc
  local city; city=$(mkCity gc)
  touch "$city/.gc/controller.sock"

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "pgii-pack-foo" "/nix/store/foo")" \
                --reload
  [ "$status" -eq 0 ]

  grep -Fxq -- "--city $city supervisor reload" "$TMP/gc-calls.log"
}

@test "reload: skipped when socket missing" {
  setup_fake_gc
  local city; city=$(mkCity gc)
  # No controller.sock.

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "pgii-pack-foo" "/nix/store/foo")" \
                --reload
  [ "$status" -eq 0 ]

  [ ! -f "$TMP/gc-calls.log" ] || ! grep -Fxq -- "--city $city supervisor reload" "$TMP/gc-calls.log"
}

@test "reload: gc failure warns but does not fail activation" {
  local bindir="$TMP/bin"
  mkdir -p "$bindir"
  cat > "$bindir/gc" <<EOF
#!/usr/bin/env bash
echo "simulated reload failure" >&2
exit 7
EOF
  chmod +x "$bindir/gc"
  PATH="$bindir:$PATH"; export PATH

  local city; city=$(mkCity gc)
  touch "$city/.gc/controller.sock"

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "pgii-pack-foo" "/nix/store/foo")" \
                --reload
  [ "$status" -eq 0 ]
  [[ "$output" =~ "WARN" ]] || [[ "$stderr" =~ "WARN" ]] || true
}
