#!/usr/bin/env bats
# bats file_tags=type:unit
# Regression guard for scripts/check-trimpath-safe-go.sh, the body of the
# `trimpath-safe-go` pre-commit hook. Deterministic logic (global rule V-3): the
# REAL script runs against throwaway Go file trees, so a reintroduced
# runtime.Caller path lookup is shown to fail (the negative control) and a line
# carrying the allow marker to pass.

bats_require_minimum_version 1.5.0

SCRIPT="$BATS_TEST_DIRNAME/../scripts/check-trimpath-safe-go.sh"

setup() {
  ROOT="$BATS_TEST_TMPDIR/root"
  mkdir -p "$ROOT/pkg"
}

# seed <relpath> <go statement>: a Go file whose function body is the statement
seed() {
  local f="$ROOT/$1"
  mkdir -p "$(dirname "$f")"
  printf 'package x\n\nfunc f() {\n\t%s\n}\n' "$2" >"$f"
}

check() { run --separate-stderr "$SCRIPT" --root "$ROOT" "$@"; }

@test "negative control: a reintroduced runtime.Caller path lookup fails and is named with its line" {
  seed pkg/helper.go '_, file, _, _ := runtime.Caller(0)'
  check
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"pkg/helper.go:4: derives a path or GOROOT from compiled-in data"* ]]
}

@test "the same file fails when passed as an argument (the hook mode)" {
  seed pkg/helper.go '_, file, _, _ := runtime.Caller(0)'
  check pkg/helper.go
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"pkg/helper.go:4:"* ]]
}

@test "every flagged form fails: Callers, CallersFrames, .Frame.File and importer.Default" {
  seed pkg/a.go 'n := runtime.Callers(0, pcs)'
  seed pkg/b.go 'frames := runtime.CallersFrames(pcs)'
  seed pkg/c.go 'p := rec.Frame.File'
  seed pkg/d_test.go 'imp := importer.Default()'
  check
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"pkg/a.go:4:"* ]]
  [[ "$stderr" == *"pkg/b.go:4:"* ]]
  [[ "$stderr" == *"pkg/c.go:4:"* ]]
  [[ "$stderr" == *"pkg/d_test.go:4:"* ]]
}

@test "build tags do not hide a use" {
  mkdir -p "$ROOT/pkg"
  printf '//go:build integration\n\npackage x\n\nvar _, f, _, _ = runtime.Caller(0)\n' >"$ROOT/pkg/i_test.go"
  check
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"pkg/i_test.go:5:"* ]]
}

@test "a line carrying the allow marker with a reason passes" {
  seed pkg/helper.go '_, _, line, _ := runtime.Caller(0) // trimpath-ok: only the line number is used'
  check
  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
}

@test "the marker without a reason does not allow the line" {
  seed pkg/helper.go '_, file, _, _ := runtime.Caller(0) // trimpath-ok:'
  check
  [ "$status" -eq 1 ]
}

@test "the marker on another line does not allow the use" {
  printf 'package x\n\n// trimpath-ok: wrong line\nvar _, f, _, _ = runtime.Caller(0)\n' >"$ROOT/pkg/helper.go"
  check
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"pkg/helper.go:4:"* ]]
}

@test "a comment line naming a form, a near-miss name and a non-Go file are ignored" {
  printf 'package x\n\n// runtime.Caller(0) used to locate the source here\n' >"$ROOT/pkg/m.go"
  seed pkg/n.go 'x := runtimeCaller(0)'
  printf '_, file, _, _ := runtime.Caller(0)\n' >"$ROOT/pkg/notes.md"
  check
  [ "$status" -eq 0 ]
  check pkg/notes.md
  [ "$status" -eq 0 ]
}

@test "a deleted file passed as an argument is skipped" {
  check pkg/gone.go
  [ "$status" -eq 0 ]
}

@test "agent worktrees, workforests, vendor and .git are not scanned" {
  seed .claude/worktrees/other/pkg/a.go '_, f, _, _ := runtime.Caller(0)'
  seed .worktrees/b/pkg/a.go '_, f, _, _ := runtime.Caller(0)'
  seed .workforests/s/repo/a.go '_, f, _, _ := runtime.Caller(0)'
  seed vendor/dep/a.go '_, f, _, _ := runtime.Caller(0)'
  seed .git/a.go '_, f, _, _ := runtime.Caller(0)'
  check
  [ "$status" -eq 0 ]
}

@test "an unknown option and a missing root are usage errors" {
  check --bogus
  [ "$status" -eq 2 ]
  run --separate-stderr "$SCRIPT" --root "$BATS_TEST_TMPDIR/missing"
  [ "$status" -eq 2 ]
}

@test "the repository's own tree passes" {
  run --separate-stderr "$SCRIPT" --root "$BATS_TEST_DIRNAME/.."
  [ "$status" -eq 0 ]
}
