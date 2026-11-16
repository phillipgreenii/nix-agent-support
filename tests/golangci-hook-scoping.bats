#!/usr/bin/env bats
# The golangci pre-commit hook (flake.nix golangci-hook-entry) against the REAL
# golangci-lint and go in a throwaway git repo holding two Go modules under
# packages/. Env (set by the flake check golangci-hook-scoping): HOOK,
# FILES_REGEX, GO_MINOR.

setup() {
  [[ -n "${HOOK-}" ]] || skip "HOOK comes from the flake check golangci-hook-scoping"
  work="$BATS_TEST_TMPDIR"
  export HOME="$work/home" GOCACHE="$work/gocache" GOPATH="$work/gopath" \
    GOLANGCI_LINT_CACHE="$work/lintcache" GOPROXY=off
  mkdir -p "$HOME"
  unset NIX_BUILD_TOP IN_NIX_BUILD GOFLAGS PG_GOLANGCI_BASE_REV PG_TEST_RUNNER_TMP_ROOT
  repo="$work/repo"
  mkdir -p "$repo/packages/moda" "$repo/packages/modb"
  git -C "$repo" init -q -b main
  git -C "$repo" config user.email t@example.com
  git -C "$repo" config user.name t
  printf 'version: "2"\n' >"$repo/.golangci.yml"
  for m in moda modb; do
    printf 'module example.com/%s\n\ngo %s\n' "$m" "$GO_MINOR" >"$repo/packages/$m/go.mod"
    printf 'package %s\n\nfunc F() int { return 1 }\n' "$m" >"$repo/packages/$m/f.go"
  done
  git -C "$repo" add -A
  git -C "$repo" commit -q -m base
  base="$(git -C "$repo" rev-parse HEAD)"
  # moda gains a NEW errcheck finding; modb gains an equally bad one that the
  # hook must not even look at because the test hands it only moda's file.
  bad='package %s\n\nimport "os"\n\nfunc G() { os.Remove("x") }\n'
  # shellcheck disable=SC2059 # $bad is the format string
  printf "$bad" moda >"$repo/packages/moda/g.go"
  # shellcheck disable=SC2059
  printf "$bad" modb >"$repo/packages/modb/g.go"
  git -C "$repo" add -A
}

run_hook() {
  cd "$repo" || return 1
  PG_GOLANGCI_BASE_REV="${BASE_OVERRIDE:-$base}" run "$HOOK" "$@"
}

@test "blocks on a new finding in the touched module" {
  run_hook packages/moda/g.go
  [ "$status" -eq 1 ]
  [[ "$output" == *"packages/moda"* ]]
  [[ "$output" == *"errcheck"* ]]
}

@test "lints the touched module only: an untouched module with a finding is not linted" {
  run_hook packages/moda/g.go
  [[ "$output" != *"packages/modb"* ]]
}

@test "a module touched only through a clean file passes" {
  mkdir -p "$repo/packages/modc"
  printf 'module example.com/modc\n\ngo %s\n' "$GO_MINOR" >"$repo/packages/modc/go.mod"
  printf 'package modc\n\nfunc H() int { return 2 }\n' >"$repo/packages/modc/h.go"
  git -C "$repo" add -A
  run_hook packages/modc/h.go
  [ "$status" -eq 0 ]
}

@test "a pre-existing finding does not block (new findings only)" {
  git -C "$repo" commit -q -m "add findings"
  BASE_OVERRIDE="$(git -C "$repo" rev-parse HEAD)" run_hook packages/moda/g.go
  [ "$status" -eq 0 ]
}

@test "skips with a notice inside the nix build sandbox" {
  NIX_BUILD_TOP=/build run_hook packages/moda/g.go
  [ "$status" -eq 0 ]
  [[ "$output" == *"skipping"* ]]
  [[ "$output" != *"errcheck"* ]]
}

@test "files regex selects Go files under packages/ only" {
  [[ "packages/moda/g.go" =~ $FILES_REGEX ]]
  [[ "packages/moda/go.mod" =~ $FILES_REGEX ]]
  [[ "packages/moda/sub/go.sum" =~ $FILES_REGEX ]]
  [[ ! "scripts/tool/main.go" =~ $FILES_REGEX ]]
  [[ ! "packages/moda/README.md" =~ $FILES_REGEX ]]
}
