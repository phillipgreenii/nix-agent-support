#!/usr/bin/env bats
# bats file_tags=type:unit
# Regression guard for scripts/gen-go-work.sh (generator and drift check) and
# scripts/go-work-vet.sh (the cross-module vet), against a REAL `go` in a
# hermetic dependency-free fixture (global rule V-3).
#
#   packages/lib    example.com/lib
#   packages/app    consumes lib through `replace => ../lib`
#   packages/other  unrelated to lib

GEN="$BATS_TEST_DIRNAME/../scripts/gen-go-work.sh"
VET="$BATS_TEST_DIRNAME/../scripts/go-work-vet.sh"

setup() {
  command -v go >/dev/null 2>&1 || {
    echo "go not found on PATH: these tests drive the real go tool (no mock)" >&2
    return 1
  }
  ROOT="$BATS_TEST_TMPDIR/repo"
  mkdir -p "$ROOT/packages/lib" "$ROOT/packages/app" "$ROOT/packages/other"
  export GOFLAGS="" GOPROXY=off GOTOOLCHAIN=local GOCACHE="$BATS_TEST_TMPDIR/gocache" \
    GOPATH="$BATS_TEST_TMPDIR/gopath" GOENV=off
  unset GOWORK

  printf 'module example.com/lib\n\ngo 1.21\n' >"$ROOT/packages/lib/go.mod"
  printf 'package lib\n\nfunc V() int { return 1 }\n' >"$ROOT/packages/lib/lib.go"
  printf 'module example.com/app\n\ngo 1.21\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/lib => ../lib\n' >"$ROOT/packages/app/go.mod"
  printf 'package app\n\nimport "example.com/lib"\n\nfunc V() int { return lib.V() }\n' >"$ROOT/packages/app/app.go"
  printf 'module example.com/other\n\ngo 1.21\n' >"$ROOT/packages/other/go.mod"
  printf 'package other\n\nfunc V() int { return 3 }\n' >"$ROOT/packages/other/other.go"
  bash "$GEN" --root "$ROOT" --write
}

@test "the generator lists every module and the drift check passes on a fresh file" {
  run grep -c '^	./packages/' "$ROOT/go.work"
  [ "$output" = "3" ]
  run bash "$GEN" --root "$ROOT" --check
  [ "$status" -eq 0 ]
}

@test "the drift check fails when a module is added without regenerating" {
  mkdir "$ROOT/packages/extra"
  printf 'module example.com/extra\n\ngo 1.21\n' >"$ROOT/packages/extra/go.mod"
  run bash "$GEN" --root "$ROOT" --check
  [ "$status" -eq 1 ]
  [[ "$output" == *"drifted"* ]]
}

@test "go-work-vet passes over the whole workspace when given no files" {
  run bash "$VET" --root "$ROOT"
  [ "$status" -eq 0 ]
}

@test "go-work-vet fails on a cross-module type break and names the consumer" {
  # lib's exported V now returns a string: lib itself still vets, app does not.
  printf 'package lib\n\nfunc V() string { return "x" }\n' >"$ROOT/packages/lib/lib.go"
  run bash "$VET" --root "$ROOT" packages/lib/lib.go
  [ "$status" -eq 1 ]
  [[ "$output" == *"packages/app"* ]]
  [[ "$output" == *"FAIL"* ]]
}

@test "go-work-vet vets only the changed module and its consumers" {
  run bash "$VET" --root "$ROOT" packages/lib/lib.go
  [ "$status" -eq 0 ]
  [[ "$output" == *"packages/lib"* ]]
  [[ "$output" == *"packages/app"* ]]
  [[ "$output" != *"packages/other"* ]]
}

@test "go-work-vet ignores a changed test file" {
  run bash "$VET" --root "$ROOT" packages/lib/lib_test.go
  [ "$status" -eq 0 ]
  [[ "$output" == *"nothing to vet"* ]]
}
