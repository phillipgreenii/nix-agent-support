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

# A module requiring a dependency served by a file:// GOPROXY: the workspace
# needs a go.work.sum entry for it, which go appends when it is missing.
make_dep_fixture() {
  PROXY="$BATS_TEST_TMPDIR/proxy"
  local v="$PROXY/example.org/dep/@v" src="$BATS_TEST_TMPDIR/depsrc/example.org/dep@v1.0.0"
  mkdir -p "$v" "$src"
  printf 'module example.org/dep\n\ngo 1.21\n' >"$src/go.mod"
  printf 'package dep\n\nfunc V() int { return 9 }\n' >"$src/dep.go"
  cp "$src/go.mod" "$v/v1.0.0.mod"
  printf '{"Version":"v1.0.0","Time":"2020-01-01T00:00:00Z"}\n' >"$v/v1.0.0.info"
  (cd "$BATS_TEST_TMPDIR/depsrc" && zip -qrD "$v/v1.0.0.zip" example.org/dep@v1.0.0)
  printf 'module example.com/other\n\ngo 1.21\n\nrequire example.org/dep v1.0.0\n' >"$ROOT/packages/other/go.mod"
  printf 'package other\n\nimport "example.org/dep"\n\nfunc V() int { return dep.V() }\n' >"$ROOT/packages/other/other.go"
  export GOPROXY="file://$PROXY" GONOSUMDB='*' GONOSUMCHECK=1 GOFLAGS="-mod=mod -modcacherw" GOSUMDB=off
  (cd "$ROOT/packages/other" && GOWORK=off go mod tidy)
}

@test "go-work-vet fails and restores go.work.sum when the committed sums are incomplete" {
  make_dep_fixture
  rm -f "$ROOT/packages/other/go.sum"
  : >"$ROOT/go.work.sum"
  run bash "$VET" --root "$ROOT"
  [ "$status" -eq 1 ]
  [[ "$output" == *"go.work.sum is incomplete"* ]]
  [ ! -s "$ROOT/go.work.sum" ]
}

@test "go-work-vet passes when go.work.sum is complete and leaves it unchanged" {
  make_dep_fixture
  rm -f "$ROOT/packages/other/go.sum"
  # seed the sums the way an editor would, then the vet must leave them alone
  (cd "$ROOT/packages/other" && GOFLAGS="-modcacherw" GOWORK="$ROOT/go.work" go vet ./... >/dev/null 2>&1)
  cp "$ROOT/go.work.sum" "$BATS_TEST_TMPDIR/sum.before"
  run bash "$VET" --root "$ROOT"
  [ "$status" -eq 0 ]
  run cmp "$ROOT/go.work.sum" "$BATS_TEST_TMPDIR/sum.before"
  [ "$status" -eq 0 ]
}
