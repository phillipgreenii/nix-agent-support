#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Wiring tests for the caffeinate shim as the home-manager module installs it:
# `bin/caffeinate` in the per-user profile is a symlink that resolves to
# pg-caffeinate-shim, a PATH assembled the way a pool session's is (the
# per-user profile bin before /usr/bin) resolves `caffeinate` to it, and a real
# invocation through the symlink writes and then removes a shim lease in a
# hermetic state directory (so the symlink works without the shim reading
# argv[0]).
#
# The profile under test, in order: $SHIM_PROFILE (a directory with a bin/ that
# holds the module's `caffeinate` symlink; the nix check passes the profile it
# builds from the module's home.packages), then a stand-in profile that this
# file builds around a `go build` of cmd/pg-caffeinate-shim (a local
# `bats tests/`). The lease test needs macOS process inspection and is skipped
# elsewhere.

bats_require_minimum_version 1.5.0

PKG_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"

setup_file() {
  if [[ -n ${SHIM_PROFILE:-} ]]; then
    PROFILE="$SHIM_PROFILE"
  else
    command -v go >/dev/null 2>&1 || skip "no SHIM_PROFILE, and go is not on PATH"
    PROFILE="$BATS_FILE_TMPDIR/profile"
    mkdir -p "$PROFILE/bin"
    (cd "$PKG_DIR" && go build -o "$PROFILE/bin/pg-caffeinate-shim" ./cmd/pg-caffeinate-shim)
    ln -s pg-caffeinate-shim "$PROFILE/bin/caffeinate"
  fi
  export PROFILE

  # A stand-in for /usr/bin: the real caffeinate must never run from a test.
  FAKE_USR_BIN="$BATS_FILE_TMPDIR/usr-bin"
  mkdir -p "$FAKE_USR_BIN"
  printf '#!/bin/sh\necho real-caffeinate\n' >"$FAKE_USR_BIN/caffeinate"
  chmod +x "$FAKE_USR_BIN/caffeinate"
  export FAKE_USR_BIN
}

setup() {
  export HOME="$BATS_TEST_TMPDIR/home"
  export XDG_STATE_HOME="$BATS_TEST_TMPDIR/state"
  mkdir -p "$HOME"
}

# The shim's lease files under the hermetic state directory.
leases() {
  compgen -G "$XDG_STATE_HOME/pg-keep-awake/leases/*.json" || true
}

@test "the profile's caffeinate is a symlink that resolves to pg-caffeinate-shim" {
  [ -L "$PROFILE/bin/caffeinate" ]
  resolved="$(readlink -f "$PROFILE/bin/caffeinate")"
  [ "$(basename "$resolved")" = pg-caffeinate-shim ]
  [ -x "$resolved" ]
}

@test "a PATH assembled as the pool does resolves caffeinate to the shim" {
  run env PATH="$PROFILE/bin:$FAKE_USR_BIN" "$BASH" -c 'command -v caffeinate'
  [ "$status" -eq 0 ]
  [ "$output" = "$PROFILE/bin/caffeinate" ]
}

@test "without the per-user profile on PATH, caffeinate is not the shim (control)" {
  run env PATH="$FAKE_USR_BIN:$PROFILE/bin" "$BASH" -c 'command -v caffeinate'
  [ "$status" -eq 0 ]
  [ "$output" = "$FAKE_USR_BIN/caffeinate" ]
  run env PATH="$FAKE_USR_BIN" "$BASH" -c 'command -v caffeinate'
  [ "$output" = "$FAKE_USR_BIN/caffeinate" ]
}

@test "caffeinate -t through the symlink writes a shim lease and removes it on exit" {
  [[ "$(uname -s)" == Darwin ]] || skip "the shim needs macOS process inspection"
  PATH="$PROFILE/bin:$FAKE_USR_BIN" caffeinate -t 3 &
  pid=$!
  seen=""
  for _ in $(seq 1 100); do
    seen="$(leases)"
    [[ -n $seen ]] && break
    sleep 0.1
  done
  [ -n "$seen" ] || {
    kill "$pid" 2>/dev/null || true
    echo "no lease appeared under $XDG_STATE_HOME"
    return 1
  }
  [ "$(printf '%s\n' "$seen" | wc -l)" -eq 1 ]
  grep -Eq '"kind": *"shim"' "$seen"
  wait "$pid"
  [ -z "$(leases)" ]
}

@test "the shim does not depend on argv[0]: a differently named link behaves the same" {
  [[ "$(uname -s)" == Darwin ]] || skip "the shim needs macOS process inspection"
  other="$BATS_TEST_TMPDIR/not-caffeinate"
  ln -s "$(readlink -f "$PROFILE/bin/caffeinate")" "$other"
  "$other" -t 1
  [ -z "$(leases)" ]
}
