#!/usr/bin/env bats
# bats file_tags=type:unit
# Regression guard for scripts/check-go-local-replace-build.sh, the body of the
# `go-local-replace-build` hook (bead pg2-cahkj).
#
# Deterministic logic against a REAL `go` (global rule V-3: a mock go would not
# prove the cross-module failure mode), in a hermetic throwaway fixture under
# BATS_TEST_TMPDIR. The fixture has no third-party dependency, so it needs no
# network and no module cache: three modules under packages/ joined by local
# replaces, where `dep` stands in for a third-party module whose version was
# "bumped" in one module and not the other.
#
#   packages/dep  example.com/dep (any version resolves, it is a local replace)
#   packages/lib  requires dep v1.1.0 (the "bumped" module)
#   packages/app  requires lib via `replace => ../lib`; requires dep v1.0.0 when
#                 NOT synced (go build then fails: "updates to go.mod needed"),
#                 v1.1.0 when synced.

SCRIPT="$BATS_TEST_DIRNAME/../scripts/check-go-local-replace-build.sh"

setup() {
  if ! command -v go >/dev/null 2>&1; then
    echo "go not found on PATH: these tests drive the real go tool (no mock)" >&2
    return 1
  fi
  ROOT="$BATS_TEST_TMPDIR/repo"
  mkdir -p "$ROOT/packages/dep" "$ROOT/packages/lib" "$ROOT/packages/app" "$ROOT/packages/other"

  # Hermetic go environment: nothing from the caller's config or network.
  export GOFLAGS=""
  export GOPROXY=off
  export GOTOOLCHAIN=local
  export GOWORK=off
  export GOCACHE="$BATS_TEST_TMPDIR/gocache"
  export GOPATH="$BATS_TEST_TMPDIR/gopath"
  export GOENV=off

  printf 'module example.com/dep\n\ngo 1.21\n' >"$ROOT/packages/dep/go.mod"
  printf 'package dep\n\nfunc V() int { return 1 }\n' >"$ROOT/packages/dep/dep.go"

  cat >"$ROOT/packages/lib/go.mod" <<'EOF'
module example.com/lib

go 1.21

require example.com/dep v1.1.0

replace example.com/dep => ../dep
EOF
  printf 'package lib\n\nimport "example.com/dep"\n\nfunc V() int { return dep.V() }\n' >"$ROOT/packages/lib/lib.go"

  write_app_go_mod v1.1.0
  printf 'package app\n\nimport "example.com/lib"\n\nfunc V() int { return lib.V() }\n' >"$ROOT/packages/app/app.go"

  # A module that only MENTIONS the replace in a comment and is deliberately
  # unbuildable: it must never be built on account of a lib change.
  cat >"$ROOT/packages/other/go.mod" <<'EOF'
module example.com/other

// a note about "local replace => ../lib" that is prose, not a replace

go 1.21
EOF
  printf 'package other\n\nfunc broken( {\n' >"$ROOT/packages/other/other.go"
}

# write_app_go_mod <dep-version>: app's go.mod with its own dep requirement.
write_app_go_mod() {
  cat >"$ROOT/packages/app/go.mod" <<EOF
module example.com/app

go 1.21

require (
	example.com/dep $1
	example.com/lib v0.0.0
)

replace example.com/dep => ../dep

replace example.com/lib => ../lib
EOF
}

@test "fixture sanity: synced modules build with the real go tool" {
  run env -C "$ROOT/packages/app" go build ./...
  [ "$status" -eq 0 ]
}

@test "fixture sanity: the unsynced bump really breaks the dependent build" {
  write_app_go_mod v1.0.0
  run env -C "$ROOT/packages/app" go build ./...
  [ "$status" -ne 0 ]
  [[ $output == *"updates to go.mod needed"* ]]
}

@test "passes when the changed module's dependents are in sync" {
  run bash "$SCRIPT" --root "$ROOT" packages/lib/go.mod
  [ "$status" -eq 0 ]
  [[ $output == *"go build ./... in packages/app"* ]]
  [[ $output == *"OK: 1 dependent"* ]]
}

@test "FAILS on a planted violation: a bump synced into lib only" {
  write_app_go_mod v1.0.0
  run bash "$SCRIPT" --root "$ROOT" packages/lib/go.mod
  [ "$status" -eq 1 ]
  [[ $output == *"updates to go.mod needed"* ]]
  [[ $output == *"- packages/app"* ]]
}

@test "go.sum alone also triggers the check" {
  write_app_go_mod v1.0.0
  run bash "$SCRIPT" --root "$ROOT" packages/lib/go.sum
  [ "$status" -eq 1 ]
  [[ $output == *"- packages/app"* ]]
}

@test "reports EVERY broken dependent, not just the first" {
  mkdir -p "$ROOT/packages/app2"
  sed 's|example.com/app|example.com/app2|' "$ROOT/packages/app/go.mod" >"$ROOT/packages/app2/go.mod"
  cp "$ROOT/packages/app/app.go" "$ROOT/packages/app2/app.go"
  sed -i.bak 's|example.com/dep v1.1.0|example.com/dep v1.0.0|' "$ROOT/packages/app/go.mod" "$ROOT/packages/app2/go.mod"
  run bash "$SCRIPT" --root "$ROOT" packages/lib/go.mod
  [ "$status" -eq 1 ]
  [[ $output == *"- packages/app"$'\n'* ]]
  [[ $output == *"- packages/app2"* ]]
}

# A `go` shim that records any invocation and fails, to prove a diff costs no
# go process. (The positive tests above use the real go; this shim only asserts
# ABSENCE of a call.)
shim_go() {
  mkdir -p "$BATS_TEST_TMPDIR/shim"
  printf '#!/bin/sh\necho called >>"%s/go-called"\nexit 99\n' "$BATS_TEST_TMPDIR" >"$BATS_TEST_TMPDIR/shim/go"
  chmod +x "$BATS_TEST_TMPDIR/shim/go"
  PATH="$BATS_TEST_TMPDIR/shim:$PATH"
}

@test "is cheap on non-triggering diffs: no go process when nothing triggers" {
  write_app_go_mod v1.0.0
  shim_go
  # A source-file change in lib, an unrelated file, and no files at all.
  run bash "$SCRIPT" --root "$ROOT" packages/lib/lib.go README.md
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  run bash "$SCRIPT" --root "$ROOT"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ ! -e "$BATS_TEST_TMPDIR/go-called" ]
}

@test "a changed module that nobody replaces builds nothing (even a broken dependent set)" {
  write_app_go_mod v1.0.0
  shim_go
  run bash "$SCRIPT" --root "$ROOT" packages/app/go.mod
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ ! -e "$BATS_TEST_TMPDIR/go-called" ]
}

@test "a comment mentioning '=> ../lib' is not a replace: other is never built" {
  run bash "$SCRIPT" --root "$ROOT" packages/lib/go.mod
  [ "$status" -eq 0 ]
  [[ $output != *"packages/other"* ]]
}

@test "replace block form is recognised" {
  cat >"$ROOT/packages/app/go.mod" <<'EOF'
module example.com/app

go 1.21

require (
	example.com/dep v1.0.0
	example.com/lib v0.0.0
)

replace (
	example.com/dep => ../dep
	example.com/lib => ../lib
)
EOF
  run bash "$SCRIPT" --root "$ROOT" packages/lib/go.mod
  [ "$status" -eq 1 ]
  [[ $output == *"- packages/app"* ]]
}

@test "a missing go is a loud failure only when a build is needed" {
  # A PATH holding only the tools the script itself uses, and no go.
  mkdir -p "$BATS_TEST_TMPDIR/nogo"
  for t in sed sort basename dirname grep nice; do
    ln -s "$(command -v "$t")" "$BATS_TEST_TMPDIR/nogo/$t"
  done
  run env PATH="$BATS_TEST_TMPDIR/nogo" "$BASH" "$SCRIPT" --root "$ROOT" packages/lib/go.mod
  [ "$status" -eq 1 ]
  [[ $output == *"go not found on PATH"* ]]
  run env PATH="$BATS_TEST_TMPDIR/nogo" "$BASH" "$SCRIPT" --root "$ROOT" packages/lib/lib.go
  [ "$status" -eq 0 ]
}
