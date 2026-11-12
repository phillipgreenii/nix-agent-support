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
  cat >"$ROOT/update-locks.sh" <<'SH'
#!/usr/bin/env bash
for t in "$(dirname "$0")"/packages/*/gomod2nix.toml; do basename "$(dirname "$t")"; done
SH
  mod alpha "go 1.26.8"
  mod beta "go 1.26.8"
}

# mod <name> <go-line>: a wired module with that directive
mod() {
  mkdir -p "$ROOT/packages/$1"
  printf 'module example.com/%s\n\n%s\n' "$1" "$2" >"$ROOT/packages/$1/go.mod"
  : >"$ROOT/packages/$1/gomod2nix.toml"
  printf '#!/usr/bin/env bash\nexec ../update-gomod2nix-deps.sh\n' >"$ROOT/packages/$1/update-deps.sh"
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
