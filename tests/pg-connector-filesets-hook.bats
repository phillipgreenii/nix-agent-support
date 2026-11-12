#!/usr/bin/env bats
# The pg-connector-filesets hook (flake.nix pg-connector-filesets-hook-pkg) against
# the REAL go and gen-filesets.sh in a throwaway git repo holding a miniature
# module. Env (set by the flake check): HOOK.

setup() {
  [[ -n "${HOOK-}" ]] || skip "HOOK comes from the flake check pg-connector-filesets-hook"
  work="$BATS_TEST_TMPDIR"
  export HOME="$work/home" GOCACHE="$work/gocache" GOPATH="$work/gopath" GOPROXY=off
  mkdir -p "$HOME"
  unset NIX_BUILD_TOP IN_NIX_BUILD GOFLAGS PG_CONNECTOR_FILESETS_MODULE
  repo="$work/repo"
  mod="$repo/packages/pg-connector"
  mkdir -p "$mod/cmd/pg-connector-x" "$mod/pkg/a" "$mod/pkg/b" "$mod/pkg/unrelated"
  git -C "$repo" init -q -b main
  printf 'module example.com/m\n\ngo 1.21\n' >"$mod/go.mod"
  printf 'package main\n\nimport _ "example.com/m/pkg/a"\n\nfunc main() {}\n' >"$mod/cmd/pg-connector-x/main.go"
  printf 'package a\n' >"$mod/pkg/a/a.go"
  printf 'package a\n\nimport "testing"\n\nfunc TestA(t *testing.T) {}\n' >"$mod/pkg/a/a_test.go"
  printf 'package b\n' >"$mod/pkg/b/b.go"
  printf 'package unrelated\n' >"$mod/pkg/unrelated/u.go"
  git -C "$repo" add -A
}

run_hook() {
  cd "$repo" || return 1
  run "$HOOK" "$@"
}

@test "fails when no fileset has been generated yet" {
  run_hook
  [ "$status" -eq 1 ]
  [[ "$output" == *"filesets/pg-connector-x.nix is missing"* ]]
}

@test "--fix generates a fileset with the import closure only, then the hook passes" {
  run_hook --fix
  [ "$status" -eq 0 ]
  f="$mod/filesets/pg-connector-x.nix"
  grep -q '"pkg/a/a.go"' "$f"
  grep -q '"cmd/pg-connector-x/main.go"' "$f"
  run grep -q 'a_test.go' "$f"
  [ "$status" -ne 0 ]
  run grep -q 'pkg/b' "$f"
  [ "$status" -ne 0 ]
  run grep -q 'pkg/unrelated' "$f"
  [ "$status" -ne 0 ]
  run_hook
  [ "$status" -eq 0 ]
}

@test "fails with a diff when a new in-module import is not yet listed" {
  run_hook --fix
  [ "$status" -eq 0 ]
  printf 'package a\n\nimport _ "example.com/m/pkg/b"\n' >"$mod/pkg/a/a.go"
  run_hook
  [ "$status" -eq 1 ]
  [[ "$output" == *'+  "pkg/b/b.go"'* ]]
  [[ "$output" == *"committed filesets are stale"* ]]
}

@test "an edit to a package the binary does not import leaves the fileset current" {
  run_hook --fix
  printf 'package unrelated\n\nvar X = 1\n' >"$mod/pkg/unrelated/u.go"
  printf 'package a\n\nimport "testing"\n\nfunc TestB(t *testing.T) {}\n' >"$mod/pkg/a/a_test.go"
  run_hook
  [ "$status" -eq 0 ]
}

@test "--fix after a new import makes the stale fileset current" {
  run_hook --fix
  printf 'package a\n\nimport _ "example.com/m/pkg/b"\n' >"$mod/pkg/a/a.go"
  run_hook --fix
  [ "$status" -eq 0 ]
  grep -q '"pkg/b/b.go"' "$mod/filesets/pg-connector-x.nix"
  run_hook
  [ "$status" -eq 0 ]
}

@test "skips inside the nix build sandbox even when stale" {
  NIX_BUILD_TOP=/build run_hook
  [ "$status" -eq 0 ]
  [[ "$output" == *"skipping"* ]]
}
