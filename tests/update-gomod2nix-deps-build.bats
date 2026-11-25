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

# fake_nix <toml-cache-entry...>: a `nix` stand-in that dispatches on argv.
# `run ... generate` records what the REAL go reports from the package dir
# (`go env GOWORK`, `go list -deps all`: the scope gomod2nix walks) and writes a
# gomod2nix-layout toml with the given cachePackages; `eval` answers the system
# and package-attr probes as for a library module (no package attr).
fake_nix() {
  mkdir -p "$BATS_TEST_TMPDIR/shim"
  {
    printf '#!%s\n' "$BASH"
    printf 'echo "$*" >>"%s/nix-called"\n' "$BATS_TEST_TMPDIR"
    printf 'case "$*" in\n'
    printf '*generate*)\n'
    printf '  go env GOWORK >"%s/gowork"\n' "$BATS_TEST_TMPDIR"
    printf '  go list -deps all >"%s/list-all"\n' "$BATS_TEST_TMPDIR"
    printf "  { echo 'schema = 3'; echo 'cachePackages = ['\n"
    local e
    for e in "$@"; do printf "    echo \"  '%s',\"\n" "$e"; done
    printf "    echo ']'; echo; echo '[mod]'; } >gomod2nix.toml ;;\n"
    printf '*currentSystem*) printf x86_64-linux ;;\n'
    printf '*hasAttr*) printf false ;;\n'
    printf '*) exit 99 ;;\n'
    printf 'esac\n'
  } >"$BATS_TEST_TMPDIR/shim/nix"
  chmod +x "$BATS_TEST_TMPDIR/shim/nix"
}

# A second workspace module the fixture module does NOT import, plus a root
# go.work that `use`s both: the 2026-11-18 layout that leaked sibling packages.
add_workspace_sibling() {
  mkdir -p "$ROOT/packages/sib"
  printf 'module example.com/sib\n\ngo 1.21\n' >"$ROOT/packages/sib/go.mod"
  printf 'package sib\n\nfunc S() int { return 2 }\n' >"$ROOT/packages/sib/sib.go"
  printf 'go 1.21\n\nuse (\n\t./packages/fixpkg\n\t./packages/sib\n)\n' >"$ROOT/go.work"
}

@test "gomod2nix runs with GOWORK=off even when a root go.work exists" {
  add_workspace_sibling
  fake_nix
  unset GOWORK
  run env PATH="$BATS_TEST_TMPDIR/shim:$PATH" bash "$SCRIPT" "$PKG"
  [ "$status" -eq 0 ]
  [ "$(cat "$BATS_TEST_TMPDIR/gowork")" = "off" ]
  grep -qx 'example.com/fixpkg' "$BATS_TEST_TMPDIR/list-all"
  ! grep -q 'example.com/sib' "$BATS_TEST_TMPDIR/list-all"
}

@test "a library module whose regenerated toml leaks a sibling fails before the attr probe" {
  add_workspace_sibling
  fake_nix 'example.com/sib'
  run env PATH="$BATS_TEST_TMPDIR/shim:$PATH" bash "$SCRIPT" "$PKG"
  [ "$status" -eq 3 ]
  [[ $output == *"fixpkg: cachePackages entry example.com/sib belongs to workspace module example.com/sib"* ]]
  ! grep -q 'hasAttr' "$BATS_TEST_TMPDIR/nix-called"
}

@test "a library module with an in-scope toml passes the scope check and succeeds" {
  add_workspace_sibling
  fake_nix
  run env PATH="$BATS_TEST_TMPDIR/shim:$PATH" bash "$SCRIPT" "$PKG"
  [ "$status" -eq 0 ]
  [[ $output == *"Checking gomod2nix.toml scope (module fixpkg)"* ]]
  [[ $output == *"OK: 1 module"* ]]
  [[ $output == *"No flake package attr 'fixpkg'"* ]]
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
