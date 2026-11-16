#!/usr/bin/env bats
# bats file_tags=type:unit
# go-cache-trim and go-cache-wipe, driven through $SCRIPT_UNDER_TEST (the
# assembled artifact, so the pinned tool paths are the real ones) against a real
# temp cache whose ages are set with touch -t.
bats_require_minimum_version 1.5.0
load 'hermetic'

setup() {
  [[ -n ${SCRIPT_UNDER_TEST:-} ]] || skip "SCRIPT_UNDER_TEST is set by the nix check"
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  pgdr_test_isolate "$TEST_DIR"
  pgdr_test_assert_hermetic "$TEST_DIR"
  CACHE="$TEST_DIR/go-build"
  mkdir -p "$CACHE"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# age <hours> <path>: sets the mtime of path to that many hours ago (UTC).
age() {
  TZ=UTC touch -t "$(jq -nr --argjson h "$1" '(now - $h * 3600) | strftime("%Y%m%d%H%M.%S")')" "$2"
}

# entry <name> <age-hours>: a file entry in shard ab.
entry() {
  mkdir -p "$CACHE/ab"
  : >"$CACHE/ab/$1"
  age "$2" "$CACHE/ab/$1"
}

mkreadme() {
  printf 'This directory holds cached build artifacts from the Go build system.\n' >"$CACHE/README"
}

trim() {
  run --separate-stderr "$SCRIPT_UNDER_TEST" go-cache-trim --cache "$CACHE" --older-than-days 3 "$@"
}

@test "trim removes old -a and -d files and keeps everything else" {
  mkreadme
  entry old1-a 100
  entry old2-d 100
  entry fresh-a 1
  entry fresh-d 1
  entry edge-a 72.5 # inside N days + 1 h of slack
  entry old-x 100   # not an -a/-d entry
  : >"$CACHE/trim.txt"
  : >"$CACHE/testexpire.txt"
  age 200 "$CACHE/trim.txt"
  age 200 "$CACHE/testexpire.txt"
  age 200 "$CACHE/README"
  mkdir -p "$CACHE/fuzz" "$CACHE/zz"
  : >"$CACHE/fuzz/old-a"
  : >"$CACHE/zz/old-a"
  age 200 "$CACHE/fuzz/old-a"
  age 200 "$CACHE/zz/old-a"
  trim
  [ "$status" -eq 0 ]
  [ ! -e "$CACHE/ab/old1-a" ]
  [ ! -e "$CACHE/ab/old2-d" ]
  [ -e "$CACHE/ab/fresh-a" ]
  [ -e "$CACHE/ab/fresh-d" ]
  [ -e "$CACHE/ab/edge-a" ]
  [ -e "$CACHE/ab/old-x" ]
  [ -e "$CACHE/README" ] && [ -e "$CACHE/trim.txt" ] && [ -e "$CACHE/testexpire.txt" ]
  [ -e "$CACHE/fuzz/old-a" ]
  [ -e "$CACHE/zz/old-a" ]
}

@test "a -d directory with a fresh own mtime holding an old inner file survives whole" {
  mkreadme
  mkdir -p "$CACHE/ab/exe-d"
  : >"$CACHE/ab/exe-d/inner"
  age 200 "$CACHE/ab/exe-d/inner"
  age 1 "$CACHE/ab/exe-d"
  trim
  [ "$status" -eq 0 ]
  [ -e "$CACHE/ab/exe-d/inner" ]
}

@test "an old -d directory goes whole" {
  mkreadme
  mkdir -p "$CACHE/ab/exe-d"
  : >"$CACHE/ab/exe-d/inner"
  age 100 "$CACHE/ab/exe-d"
  trim
  [ "$status" -eq 0 ]
  [ ! -e "$CACHE/ab/exe-d" ]
}

@test "dry run lists the entries and removes nothing" {
  mkreadme
  entry old-a 100
  trim --dry-run
  [ "$status" -eq 0 ]
  [[ $output == *"would remove $CACHE/ab/old-a"* ]]
  [ -e "$CACHE/ab/old-a" ]
}

@test "a README with no shard directories is nothing to do, exit 0" {
  mkreadme
  trim
  [ "$status" -eq 0 ]
}

@test "a directory without the README is refused, exit 2, and nothing is touched" {
  entry old-a 100
  trim
  [ "$status" -eq 2 ]
  [ -e "$CACHE/ab/old-a" ]
}

@test "missing --older-than-days is a usage error, exit 2" {
  mkreadme
  run --separate-stderr "$SCRIPT_UNDER_TEST" go-cache-trim --cache "$CACHE"
  [ "$status" -eq 2 ]
}

@test "PATH negative control: failing find, date and jq first on PATH do not matter" {
  mkreadme
  entry old-a 100
  entry fresh-a 1
  local fake="$TEST_DIR/fakebin" t
  mkdir -p "$fake"
  for t in find date jq rm mv mkdir stat; do
    printf '#!/bin/sh\necho "fake %s used" >&2\nexit 97\n' "$t" >"$fake/$t"
    chmod +x "$fake/$t"
  done
  PATH="$fake:$PATH" run --separate-stderr "$SCRIPT_UNDER_TEST" go-cache-trim --cache "$CACHE" --older-than-days 3
  [ "$status" -eq 0 ]
  [[ $stderr != *"fake "* ]]
  [ ! -e "$CACHE/ab/old-a" ]
  [ -e "$CACHE/ab/fresh-a" ]
}

# --- go-cache-wipe -----------------------------------------------------------

stubs() {
  printf '#!/bin/sh\nexit %s\n' "$1" >"$TEST_DIR/pgrep"
  printf '#!/bin/sh\necho "GOCACHE=$GOCACHE $*" >>"%s/go.calls"\n' "$TEST_DIR" >"$TEST_DIR/go"
  chmod +x "$TEST_DIR/pgrep" "$TEST_DIR/go"
  export PGDR_PGREP_CMD="$TEST_DIR/pgrep" PGDR_GO_CMD="$TEST_DIR/go"
}

@test "wipe: a live go process exits 75 and deletes nothing" {
  mkreadme
  entry old-a 100
  stubs 0
  run --separate-stderr "$SCRIPT_UNDER_TEST" go-cache-wipe --cache "$CACHE"
  [ "$status" -eq 75 ]
  [ ! -e "$TEST_DIR/go.calls" ]
  [ -e "$CACHE/ab/old-a" ]
}

@test "wipe: with no live go it runs go clean -cache with GOCACHE=DIR" {
  mkreadme
  stubs 1
  run --separate-stderr "$SCRIPT_UNDER_TEST" go-cache-wipe --cache "$CACHE"
  [ "$status" -eq 0 ]
  [ "$(cat "$TEST_DIR/go.calls")" = "GOCACHE=$CACHE clean -cache" ]
}

@test "wipe: dry run prints the command and calls nothing; guarded dry run is 75" {
  mkreadme
  stubs 1
  run --separate-stderr "$SCRIPT_UNDER_TEST" go-cache-wipe --cache "$CACHE" --dry-run
  [ "$status" -eq 0 ]
  [[ $output == *"GOCACHE=$CACHE go clean -cache"* ]]
  [ ! -e "$TEST_DIR/go.calls" ]
  stubs 0
  run --separate-stderr "$SCRIPT_UNDER_TEST" go-cache-wipe --cache "$CACHE" --dry-run
  [ "$status" -eq 75 ]
}

@test "wipe: a directory without the README is refused, exit 2" {
  stubs 1
  run --separate-stderr "$SCRIPT_UNDER_TEST" go-cache-wipe --cache "$CACHE"
  [ "$status" -eq 2 ]
  [ ! -e "$TEST_DIR/go.calls" ]
}

@test "integration: the packaged go wipes a real cache directory" {
  [[ ${PGDR_INTEGRATION:-} == 1 ]] || skip "needs PGDR_INTEGRATION=1 (checks.<system>.test-pg-disk-reclaimer-integration)"
  mkreadme
  entry old-a 100
  mkdir -p "$TEST_DIR/gopath"
  GOPATH="$TEST_DIR/gopath" GOFLAGS=-modcacherw run --separate-stderr "$SCRIPT_UNDER_TEST" go-cache-wipe --cache "$CACHE"
  [ "$status" -eq 0 ]
  [ ! -e "$CACHE/ab" ]
}
