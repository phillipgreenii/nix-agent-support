#!/usr/bin/env bats
# bats file_tags=type:unit
# Regression guard for scripts/refresh-go-work-sum.sh, the update-locks.sh step
# that refreshes go.work.sum after the GOWORK=off Go module steps, against a REAL
# `go` and a file:// module proxy in a hermetic fixture (global rule V-3).
#
#   packages/a  imports example.org/dep (go.sum complete for v1.0.0)
#   packages/b  carries example.org/dep as an indirect requirement and no zip hash
#
# Bumping b's requirement to v1.1.0 (as `go get -u` does under GOWORK=off) makes
# the workspace select v1.1.0 for a, whose zip hash is in no module go.sum, so
# only go.work.sum can hold it.

REFRESH="$BATS_TEST_DIRNAME/../scripts/refresh-go-work-sum.sh"

setup() {
  command -v go >/dev/null 2>&1 || {
    echo "go not found on PATH: these tests drive the real go tool (no mock)" >&2
    return 1
  }
  command -v zip >/dev/null 2>&1 || {
    echo "zip not found on PATH: the fixture module proxy needs it" >&2
    return 1
  }
  ROOT="$BATS_TEST_TMPDIR/repo"
  PROXY="$BATS_TEST_TMPDIR/proxy"
  mkdir -p "$ROOT/packages/a" "$ROOT/packages/b"
  export GOPROXY="file://$PROXY" GONOSUMDB='*' GOSUMDB=off GOTOOLCHAIN=local GOENV=off \
    GOFLAGS="-modcacherw" GOCACHE="$BATS_TEST_TMPDIR/gocache" GOPATH="$BATS_TEST_TMPDIR/gopath"
  unset GOWORK

  local v src
  mkdir -p "$PROXY/example.org/dep/@v"
  for v in v1.0.0 v1.1.0; do
    src="$BATS_TEST_TMPDIR/depsrc/example.org/dep@$v"
    mkdir -p "$src"
    printf 'module example.org/dep\n\ngo 1.21\n' >"$src/go.mod"
    printf 'package dep\n\nfunc V() int { return 9 }\n' >"$src/dep.go"
    cp "$src/go.mod" "$PROXY/example.org/dep/@v/$v.mod"
    printf '{"Version":"%s","Time":"2020-01-01T00:00:00Z"}\n' "$v" >"$PROXY/example.org/dep/@v/$v.info"
    (cd "$BATS_TEST_TMPDIR/depsrc" && zip -qrD "$PROXY/example.org/dep/@v/$v.zip" "example.org/dep@$v")
  done
  printf 'v1.0.0\nv1.1.0\n' >"$PROXY/example.org/dep/@v/list"

  printf 'module example.com/a\n\ngo 1.21\n\nrequire example.org/dep v1.0.0\n' >"$ROOT/packages/a/go.mod"
  printf 'package a\n\nimport "example.org/dep"\n\nfunc V() int { return dep.V() }\n' >"$ROOT/packages/a/a.go"
  printf 'module example.com/b\n\ngo 1.21\n\nrequire example.org/dep v1.0.0 // indirect\n' >"$ROOT/packages/b/go.mod"
  printf 'package b\n\nfunc V() int { return 2 }\n' >"$ROOT/packages/b/b.go"
  printf 'go 1.21\n\nuse (\n\t./packages/a\n\t./packages/b\n)\n' >"$ROOT/go.work"
  (cd "$ROOT/packages/a" && GOWORK=off go mod tidy)
  # b: only the go.mod hash, as a module that does not import dep records it
  grep 'dep v1.0.0/go.mod' "$ROOT/packages/a/go.sum" >"$ROOT/packages/b/go.sum"
}

# The bump update-locks.sh performs, with GOWORK=off: b moves to v1.1.0.
bump_b() {
  (cd "$ROOT/packages/b" && GOWORK=off go mod edit -require=example.org/dep@v1.1.0)
  printf '%s\n' "example.org/dep v1.1.0/go.mod $(GOWORK=off go mod download -json example.org/dep@v1.1.0 | sed -n 's/.*"GoModSum": "\(.*\)".*/\1/p')" >"$ROOT/packages/b/go.sum"
}

module_files_digest() {
  (cd "$ROOT" && cat packages/*/go.mod packages/*/go.sum | cksum)
}

ws_list() {
  (cd "$ROOT" && GOWORK="$ROOT/go.work" go list -deps -test all >/dev/null)
}

@test "after a bump, the refresh leaves workspace-mode go list nothing to add to go.work.sum" {
  bump_b
  digest="$(module_files_digest)"
  run bash "$REFRESH" --root "$ROOT"
  [ "$status" -eq 0 ]
  [ -s "$ROOT/go.work.sum" ]
  grep -q '^example.org/dep v1.1.0 h1:' "$ROOT/go.work.sum"
  cp "$ROOT/go.work.sum" "$BATS_TEST_TMPDIR/sum.before"
  ws_list
  run cmp "$ROOT/go.work.sum" "$BATS_TEST_TMPDIR/sum.before"
  [ "$status" -eq 0 ]
  # no module go.mod or go.sum was touched
  [ "$(module_files_digest)" = "$digest" ]
}

@test "without the refresh, the bump leaves go.work.sum stale (a workspace-mode go list adds to it)" {
  bump_b
  [ ! -s "$ROOT/go.work.sum" ] || ! grep -q 'dep v1.1.0 h1:' "$ROOT/go.work.sum"
  ws_list
  grep -q '^example.org/dep v1.1.0 h1:' "$ROOT/go.work.sum"
}

@test "the refresh fails loudly when there is no go.work" {
  rm "$ROOT/go.work"
  run bash "$REFRESH" --root "$ROOT"
  [ "$status" -eq 1 ]
  [[ "$output" == *"go.work is missing"* ]]
}
