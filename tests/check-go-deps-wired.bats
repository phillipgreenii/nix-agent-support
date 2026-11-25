#!/usr/bin/env bats
# bats file_tags=type:unit
# Regression guard for the go-directive assertion in scripts/check-go-deps-wired.sh
# (the body of the `go-deps-wired` flake check). Deterministic logic (global rule V-3):
# the REAL script runs against a throwaway repo tree whose modules are fully wired, so
# only the go.mod directives decide the verdict. Seeds a module with a different
# directive and shows the check fails and names it.

bats_require_minimum_version 1.5.0

SCRIPT="$BATS_TEST_DIRNAME/../scripts/check-go-deps-wired.sh"
REPO="$BATS_TEST_DIRNAME/.."

setup() {
  ROOT="$BATS_TEST_TMPDIR/root"
  mkdir -p "$ROOT/packages"
  cp "$REPO/flake.lock" "$ROOT/flake.lock"
  cp "$REPO/packages/update-gomod2nix-deps.sh" "$ROOT/packages/update-gomod2nix-deps.sh"
  # Stand-in for update-locks.sh: lists every module dir that has a gomod2nix.toml.
  cat >"$ROOT/update-locks.sh" <<SH
#!$BASH
for t in "\$(dirname "\$0")"/packages/*/gomod2nix.toml; do basename "\$(dirname "\$t")"; done
SH
  mod alpha "go 1.26.8"
  mod beta "go 1.26.8"
}

# mod <name> <go-line>: a wired module with that directive
mod() {
  mkdir -p "$ROOT/packages/$1"
  printf 'module example.com/%s\n\n%s\n' "$1" "$2" >"$ROOT/packages/$1/go.mod"
  : >"$ROOT/packages/$1/gomod2nix.toml"
  printf '#!%s\nexec ../update-gomod2nix-deps.sh\n' "$BASH" >"$ROOT/packages/$1/update-deps.sh"
  chmod +x "$ROOT/packages/$1/update-deps.sh"
}

@test "uniform directives equal to the pinned version pass" {
  run --separate-stderr bash "$SCRIPT" "$ROOT" 1.26.8
  [ "$status" -eq 0 ]
}

@test "a module with a different directive fails and is named" {
  mod beta "go 1.25.9"
  run --separate-stderr bash "$SCRIPT" "$ROOT" 1.26.8
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"beta: go.mod says go 1.25.9, expected go 1.26.8"* ]]
  [[ "$stderr" != *"alpha:"* ]]
}

@test "uniform directives that differ from the pinned version fail" {
  run --separate-stderr bash "$SCRIPT" "$ROOT" 1.26.9
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"alpha: go.mod says go 1.26.8, expected go 1.26.9"* ]]
}

@test "without a pinned version, disagreeing modules fail" {
  mod beta "go 1.26.0"
  run --separate-stderr bash "$SCRIPT" "$ROOT"
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"disagree on the go directive"* ]]
}

@test "a go.mod with no go directive fails" {
  printf 'module example.com/beta\n' >"$ROOT/packages/beta/go.mod"
  run --separate-stderr bash "$SCRIPT" "$ROOT" 1.26.8
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"beta: packages/beta/go.mod has no go directive"* ]]
}

# --- gomod2nix.toml scope (scripts/check-gomod2nix-cache-scope.sh) -----------
# A toml generated while a root go.work was in effect lists other workspace
# modules' packages in cachePackages and their dependencies under [mod]. The
# builder drops only LOCAL-REPLACE modules from the cache roots, so any other
# first-party entry breaks go-cache-env. These cases run the REAL scope script.

SCOPE="$BATS_TEST_DIRNAME/../scripts/check-gomod2nix-cache-scope.sh"

# toml <module> <cache entries...>: write a gomod2nix-layout toml
toml() {
  local m="$1" e
  shift
  {
    printf 'schema = 3\n'
    if [ "$#" -gt 0 ]; then
      printf 'cachePackages = [\n'
      for e in "$@"; do printf "  '%s',\n" "$e"; done
      printf ']\n'
    fi
    printf '\n[mod]\n'
  } >"$ROOT/packages/$m/gomod2nix.toml"
}

@test "scope: the module's own package in cachePackages fails" {
  toml alpha 'example.com/alpha/internal/x'
  run --separate-stderr bash "$SCOPE" "$ROOT"
  [ "$status" -eq 3 ]
  [[ "$stderr" == *"alpha: cachePackages entry example.com/alpha/internal/x belongs to workspace module example.com/alpha"* ]]
}

@test "scope: a sibling that go.mod does not replace fails and names module and entry" {
  toml alpha 'example.com/beta/cmd/beta'
  run --separate-stderr bash "$SCOPE" "$ROOT"
  [ "$status" -eq 3 ]
  [[ "$stderr" == *"alpha: cachePackages entry example.com/beta/cmd/beta belongs to workspace module example.com/beta"* ]]
  [[ "$stderr" == *"GOWORK=off"* ]]
}

@test "scope: a sibling replaced in a replace block passes" {
  printf 'module example.com/alpha\n\ngo 1.26.8\n\nreplace (\n\texample.com/beta => ../beta\n)\n' >"$ROOT/packages/alpha/go.mod"
  toml alpha 'example.com/beta' 'example.com/beta/sub'
  run --separate-stderr bash "$SCOPE" "$ROOT"
  [ "$status" -eq 0 ]
}

@test "scope: single-line versioned and ./ replaces pass" {
  mod gamma "go 1.26.8"
  printf 'module example.com/alpha\n\ngo 1.26.8\n\nreplace example.com/beta v0.0.0 => ../beta\nreplace example.com/gamma => ./../gamma\n' >"$ROOT/packages/alpha/go.mod"
  toml alpha 'example.com/beta/x' 'example.com/gamma/y'
  run --separate-stderr bash "$SCOPE" "$ROOT"
  [ "$status" -eq 0 ]
}

@test "scope: a prose comment mentioning a replace does not count as one" {
  printf 'module example.com/alpha\n\ngo 1.26.8\n\n// the same "local replace => ../beta" pattern\n' >"$ROOT/packages/alpha/go.mod"
  toml alpha 'example.com/beta/x'
  run --separate-stderr bash "$SCOPE" "$ROOT"
  [ "$status" -eq 3 ]
}

@test "scope: a replace target is not a prefix match for a longer module name" {
  mod beta-handlers "go 1.26.8"
  printf 'module example.com/alpha\n\ngo 1.26.8\n\nreplace example.com/beta => ../beta\n' >"$ROOT/packages/alpha/go.mod"
  toml alpha 'example.com/beta-handlers/x'
  run --separate-stderr bash "$SCOPE" "$ROOT"
  [ "$status" -eq 3 ]
  [[ "$stderr" == *"belongs to workspace module example.com/beta-handlers"* ]]
}

@test "scope: third-party and non-workspace first-party entries pass" {
  toml alpha 'github.com/BurntSushi/toml' 'example.com/x/cmdrun' 'example.com/alphabet/y'
  printf 'github.com/BurntSushi/toml v1.0.0 h1:x=\nexample.com/x v0.1.0 h1:y=\n' >"$ROOT/packages/alpha/go.sum"
  run --separate-stderr bash "$SCOPE" "$ROOT"
  [ "$status" -eq 0 ]
}

@test "scope: a [mod] entry missing from go.sum fails; one present passes" {
  toml alpha
  printf "\n  [mod.'github.com/BurntSushi/toml']\n    version = 'v1.0.0'\n  [mod.'cel.dev/expr']\n    version = 'v0.1.0'\n" >>"$ROOT/packages/alpha/gomod2nix.toml"
  printf 'github.com/BurntSushi/toml v1.0.0 h1:x=\ngithub.com/BurntSushi/toml v1.0.0/go.mod h1:y=\n' >"$ROOT/packages/alpha/go.sum"
  run --separate-stderr bash "$SCOPE" "$ROOT"
  [ "$status" -eq 3 ]
  [[ "$stderr" == *"alpha: [mod] entry cel.dev/expr is not in go.sum"* ]]
  [[ "$stderr" != *"[mod] entry github.com/BurntSushi/toml"* ]]
}

@test "scope: a [mod] version go.sum has no source hash for is stale and fails" {
  toml alpha
  printf "\n  [mod.'github.com/BurntSushi/toml']\n    version = 'v1.0.0'\n" >>"$ROOT/packages/alpha/gomod2nix.toml"
  printf 'github.com/BurntSushi/toml v1.1.0 h1:x=\ngithub.com/BurntSushi/toml v1.0.0/go.mod h1:y=\n' >"$ROOT/packages/alpha/go.sum"
  run --separate-stderr bash "$SCOPE" "$ROOT"
  [ "$status" -eq 3 ]
  [[ "$stderr" == *"alpha: stale gomod2nix.toml: [mod] entry github.com/BurntSushi/toml is at v1.0.0, which go.sum has no source hash for"* ]]
}

@test "scope: a [mod] version differing from the go.mod requirement is stale and fails" {
  printf 'module example.com/alpha\n\ngo 1.26.8\n\nrequire (\n\tgithub.com/BurntSushi/toml v1.1.0 // indirect\n)\n' >"$ROOT/packages/alpha/go.mod"
  toml alpha
  printf "\n  [mod.'github.com/BurntSushi/toml']\n    version = 'v1.0.0'\n" >>"$ROOT/packages/alpha/gomod2nix.toml"
  printf 'github.com/BurntSushi/toml v1.0.0 h1:x=\ngithub.com/BurntSushi/toml v1.1.0 h1:y=\n' >"$ROOT/packages/alpha/go.sum"
  run --separate-stderr bash "$SCOPE" "$ROOT"
  [ "$status" -eq 3 ]
  [[ "$stderr" == *"alpha: stale gomod2nix.toml: [mod] entry github.com/BurntSushi/toml is at v1.0.0 but go.mod requires v1.1.0"* ]]
}

@test "scope: a [mod] version matching go.mod and go.sum passes" {
  printf 'module example.com/alpha\n\ngo 1.26.8\n\nrequire github.com/BurntSushi/toml v1.1.0\n' >"$ROOT/packages/alpha/go.mod"
  toml alpha
  printf "\n  [mod.'github.com/BurntSushi/toml']\n    version = 'v1.1.0'\n" >>"$ROOT/packages/alpha/gomod2nix.toml"
  printf 'github.com/BurntSushi/toml v1.1.0 h1:x=\ngithub.com/BurntSushi/toml v1.1.0/go.mod h1:y=\n' >"$ROOT/packages/alpha/go.sum"
  run --separate-stderr bash "$SCOPE" "$ROOT"
  [ "$status" -eq 0 ]
}

@test "scope: naming one module checks only that module" {
  toml beta 'example.com/alpha/x'
  run --separate-stderr bash "$SCOPE" "$ROOT" alpha
  [ "$status" -eq 0 ]
  [[ "$output" == *"OK: 1 module"* ]]
}

@test "scope: an unknown module name is a usage error" {
  run --separate-stderr bash "$SCOPE" "$ROOT" nosuch
  [ "$status" -eq 2 ]
  [[ "$stderr" == *"nosuch"* ]]
}

@test "scope: a root with no Go modules fails loudly" {
  run --separate-stderr bash "$SCOPE" "$BATS_TEST_TMPDIR/empty"
  [ "$status" -eq 2 ]
}

@test "go-deps-wired reports scope findings as problems" {
  toml alpha 'example.com/beta/cmd/beta'
  run --separate-stderr bash "$SCRIPT" "$ROOT" 1.26.8
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"alpha: cachePackages entry example.com/beta/cmd/beta belongs to workspace module example.com/beta"* ]]
}

@test "go-deps-wired fails hard when the scope check cannot run" {
  printf 'go 1.26.8\n' >"$ROOT/packages/beta/go.mod"
  run --separate-stderr bash "$SCRIPT" "$ROOT" 1.26.8
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"gomod2nix.toml scope check could not run"* ]]
}
