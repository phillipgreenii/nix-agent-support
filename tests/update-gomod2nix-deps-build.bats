#!/usr/bin/env bats
# bats file_tags=type:unit
# Regression guard for the plain `go build ./...` assertion in
# packages/update-gomod2nix-deps.sh (bead pg2-c8821c, from pg2-lkhaon).
#
# Runs the REAL script and the REAL go tool (global rule V-3) against a
# hermetic throwaway repo under BATS_TEST_TMPDIR: one dependency-free module
# (no network, no module cache; GOPROXY=off), plus a minimal flake.lock holding
# the gomod2nix rev the script pins, so the script passes its pin check.
#
# `go mod tidy` runs first and self-heals a stale go.sum, so the assertion
# guards what tidy cannot repair: a module that still does not build. The
# failure case plants a package that fails to compile; it must exit non-zero
# BEFORE any nix step and name the module. The only stub is `nix`, used by the
# passing case to prove the script got past the build step (nix itself is out of
# scope and needs the network).

SCRIPT="$BATS_TEST_DIRNAME/../packages/update-gomod2nix-deps.sh"

setup() {
  if ! command -v go >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1; then
    echo "go and jq must be on PATH: these tests drive the real tools" >&2
    return 1
  fi
  ROOT="$BATS_TEST_TMPDIR/repo"
  PKG="$ROOT/packages/fixpkg"
  mkdir -p "$PKG"

  export GOFLAGS=""
  export GOPROXY=off
  export GOTOOLCHAIN=local
  export GOWORK=off
  export GOCACHE="$BATS_TEST_TMPDIR/gocache"
  export GOPATH="$BATS_TEST_TMPDIR/gopath"
  export GOENV=off

  rev="$(sed -n 's/^GOMOD2NIX_REV="\(.*\)"$/\1/p' "$SCRIPT")"
  cat >"$ROOT/flake.lock" <<EOF
{"nodes":{"root":{"inputs":{"gomod2nix":"gomod2nix"}},"gomod2nix":{"locked":{"rev":"$rev"}}},"root":"root","version":7}
EOF
  printf 'module example.com/fixpkg\n\ngo 1.21\n' >"$PKG/go.mod"
  printf 'package fixpkg\n\nfunc V() int { return 1 }\n' >"$PKG/fix.go"
}

@test "FAILS before any nix step when go build ./... fails, naming the module" {
  printf 'package fixpkg\n\nfunc W() int { return undefinedSymbol }\n' >"$PKG/broken.go"
  mkdir -p "$BATS_TEST_TMPDIR/shim"
  printf '#!/bin/sh\necho called >>"%s/nix-called"\nexit 0\n' "$BATS_TEST_TMPDIR" >"$BATS_TEST_TMPDIR/shim/nix"
  chmod +x "$BATS_TEST_TMPDIR/shim/nix"
  run env PATH="$BATS_TEST_TMPDIR/shim:$PATH" bash "$SCRIPT" "$PKG"
  [ "$status" -eq 1 ]
  [[ $output == *"go build ./... failed in module 'fixpkg'"* ]]
  [[ $output == *"undefinedSymbol"* ]]
  [ ! -e "$BATS_TEST_TMPDIR/nix-called" ]
}

@test "a buildable module passes the build step and proceeds to the nix step" {
  mkdir -p "$BATS_TEST_TMPDIR/shim"
  printf '#!/bin/sh\necho "$*" >>"%s/nix-called"\nexit 42\n' "$BATS_TEST_TMPDIR" >"$BATS_TEST_TMPDIR/shim/nix"
  chmod +x "$BATS_TEST_TMPDIR/shim/nix"
  run env PATH="$BATS_TEST_TMPDIR/shim:$PATH" bash "$SCRIPT" "$PKG"
  [ "$status" -eq 42 ]
  [[ $output == *"Verifying plain go build ./... (module fixpkg)"* ]]
  [[ $output != *"go build ./... failed"* ]]
  [ -e "$BATS_TEST_TMPDIR/nix-called" ]
}
