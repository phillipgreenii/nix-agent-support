#!/usr/bin/env bats
# bats file_tags=type:unit
# Regression guard for scripts/check-no-reexec-test-doubles.sh, the body of the
# `no-reexec-test-doubles` pre-commit hook (bead pg2-ty6mjm; shared command runner ADR
# in repo x, decision 5). Deterministic logic (global rule V-3): the REAL script runs
# against throwaway Go test file trees, so a seeded untagged re-exec file is shown to
# fail and a tagged or allowlisted one to pass.

bats_require_minimum_version 1.5.0

SCRIPT="$BATS_TEST_DIRNAME/../scripts/check-no-reexec-test-doubles.sh"
REPO_ALLOWLIST="$BATS_TEST_DIRNAME/../scripts/reexec-test-double-allowlist.txt"

setup() {
  ROOT="$BATS_TEST_TMPDIR/root"
  ALLOW="$BATS_TEST_TMPDIR/allow.txt"
  mkdir -p "$ROOT/pkg"
  : >"$ALLOW"
}

# seed <relpath> [build-line]: a test file that uses the re-exec env var
seed() {
  local f="$ROOT/$1"
  mkdir -p "$(dirname "$f")"
  {
    [ -n "${2:-}" ] && printf '%s\n\n' "$2"
    printf 'package x\n\nfunc TestMain(m *testing.M) {\n\tif os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {\n\t}\n}\n'
  } >"$f"
}

check() { run --separate-stderr "$SCRIPT" --root "$ROOT" --allowlist "$ALLOW" "$@"; }

@test "a seeded untagged re-exec file fails and is named" {
  seed pkg/a_test.go
  check
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"pkg/a_test.go: names GO_WANT_*HELPER_PROCESS in an untagged test file"* ]]
}

@test "the same file fails when passed as an argument (the hook mode)" {
  seed pkg/a_test.go
  check pkg/a_test.go
  [ "$status" -eq 1 ]
}

@test "the conformance-style env name is covered too" {
  mkdir -p "$ROOT/pkg"
  printf 'package x\n\nvar _ = "GO_WANT_CONFORMANCE_HELPER_PROCESS"\n' >"$ROOT/pkg/c_test.go"
  check
  [ "$status" -eq 1 ]
}

@test "integration, contract and combined tags pass" {
  seed pkg/i_test.go '//go:build integration'
  seed pkg/c_test.go '//go:build contract'
  seed pkg/e_test.go '//go:build integration || contract'
  seed pkg/h_test.go '//go:build hostile'
  check
  [ "$status" -eq 0 ]
}

@test "a negated tag does not count as tagged" {
  seed pkg/n_test.go '//go:build !integration'
  check
  [ "$status" -eq 1 ]
}

@test "an unrelated tag does not count as tagged" {
  seed pkg/d_test.go '//go:build darwin'
  check
  [ "$status" -eq 1 ]
}

@test "a mention in a comment line, a non-test file and a testdata file are ignored" {
  printf 'package x\n\n// the GO_WANT_HELPER_PROCESS pattern used to live here\n' >"$ROOT/pkg/m_test.go"
  printf 'package x\n\nvar _ = "GO_WANT_HELPER_PROCESS"\n' >"$ROOT/pkg/helper.go"
  seed pkg/testdata/f_test.go
  check
  [ "$status" -eq 0 ]
}

@test "an allowlisted path passes, and only that path" {
  seed pkg/a_test.go
  seed pkg/b_test.go
  printf '# comment\npkg/a_test.go | kept until migrated\n' >"$ALLOW"
  check
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"pkg/b_test.go"* ]]
  [[ "$stderr" != *"pkg/a_test.go"* ]]
}

@test "a stale allowlist entry fails the whole-tree run but not a file-argument run" {
  seed pkg/gone_test.go '//go:build integration'
  printf 'pkg/gone_test.go | was untagged\npkg/missing_test.go | file was deleted\n' >"$ALLOW"
  check
  [ "$status" -eq 1 ]
  [[ "$stderr" == *"pkg/gone_test.go: listed in the allowlist"* ]]
  [[ "$stderr" == *"pkg/missing_test.go: listed in the allowlist"* ]]
  check pkg/gone_test.go
  [ "$status" -eq 0 ]
}

@test "an allowlist line without a reason is a usage error" {
  seed pkg/a_test.go
  printf 'pkg/a_test.go\n' >"$ALLOW"
  check
  [ "$status" -eq 2 ]
}

@test "the repository's own tree passes with its own allowlist" {
  run --separate-stderr "$SCRIPT" --root "$BATS_TEST_DIRNAME/.." --allowlist "$REPO_ALLOWLIST"
  [ "$status" -eq 0 ]
}
