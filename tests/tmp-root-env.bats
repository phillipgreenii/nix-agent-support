#!/usr/bin/env bats
# Behaviour of the guarded shell snippets rendered by the build-tmp and
# claude-code-tmp home-manager modules (bead pg2-2qirl, packet A). The snippet
# text comes from the REAL modules (tests/tmp-root-modules.nix, via
# checks.<system>.test-tmp-root-modules), with the root spelled
# /ROOT_PLACEHOLDER/<basename>; each test points it at a real mktemp directory
# and sources it in a hermetic `env -i` bash AND zsh.
#
# Inputs (paths of the rendered snippets). A bare `bats tests/` has none of them
# and SKIPS; the nix check provides them and fails on any skip:
#   SNIPPET_BUILD         build-tmp, exportTmpdir = false
#   SNIPPET_BUILD_TMPDIR  build-tmp, exportTmpdir = true
#   SNIPPET_BUILD_SPACE   build-tmp, path containing a space
#   SNIPPET_CLAUDE        claude-code-tmp

setup() {
  if [ -z "${SNIPPET_BUILD:-}" ] || [ -z "${SNIPPET_BUILD_TMPDIR:-}" ] || [ -z "${SNIPPET_BUILD_SPACE:-}" ] || [ -z "${SNIPPET_CLAUDE:-}" ]; then
    skip "rendered snippets not provided; run checks.<system>.test-tmp-root-modules"
  fi
  # Short paths: the 104-byte unix socket limit applies to anything made here.
  VOL="$(mktemp -d "${TMPDIR:-/tmp}/pgtr.XXXXXX")"
  BUILD_ROOT="$VOL/build_tmp"
  CLAUDE_ROOT="$VOL/claude_code_tmp"
  render() { sed "s#/ROOT_PLACEHOLDER#$VOL#g" "$1" >"$2"; }
  render "$SNIPPET_BUILD" "$VOL/build.sh"
  render "$SNIPPET_BUILD_TMPDIR" "$VOL/build-tmpdir.sh"
  render "$SNIPPET_CLAUDE" "$VOL/claude.sh"
  SHELLS="bash"
  if command -v zsh >/dev/null 2>&1; then SHELLS="bash zsh"; fi
}

teardown() {
  [ -n "${VOL:-}" ] || return 0
  chmod -R u+rwx "$VOL" 2>/dev/null || true
  rm -rf "$VOL"
}

# source_in SHELL SNIPPET: source the snippet in a hermetic shell that starts
# with TMPDIR=/orig/tmp and prints the resulting environment.
source_in() {
  local sh="$1" snippet="$2"
  case "$sh" in
    bash) env -i PATH="$PATH" HOME="$VOL" TMPDIR=/orig/tmp bash --noprofile --norc -c '. "$1"; env' _ "$snippet" ;;
    zsh) env -i PATH="$PATH" HOME="$VOL" TMPDIR=/orig/tmp zsh -f -c '. "$1"; env' _ "$snippet" ;;
  esac
}

@test "a present, writable build_tmp exports GOTMPDIR and PG_TEST_RUNNER_TMP_ROOT, not TMPDIR" {
  mkdir -m 0700 "$BUILD_ROOT"
  for sh in $SHELLS; do
    run source_in "$sh" "$VOL/build.sh"
    [ "$status" -eq 0 ]
    grep -Fx "GOTMPDIR=$BUILD_ROOT" <<<"$output"
    grep -Fx "PG_TEST_RUNNER_TMP_ROOT=$BUILD_ROOT" <<<"$output"
    grep -Fx "TMPDIR=/orig/tmp" <<<"$output"
    ! grep -q '^CLAUDE_CODE_TMPDIR=' <<<"$output"
  done
}

@test "exportTmpdir = true also exports TMPDIR" {
  mkdir -m 0700 "$BUILD_ROOT"
  for sh in $SHELLS; do
    run source_in "$sh" "$VOL/build-tmpdir.sh"
    [ "$status" -eq 0 ]
    grep -Fx "TMPDIR=$BUILD_ROOT" <<<"$output"
    grep -Fx "GOTMPDIR=$BUILD_ROOT" <<<"$output"
    grep -Fx "PG_TEST_RUNNER_TMP_ROOT=$BUILD_ROOT" <<<"$output"
  done
}

@test "a present, writable claude_code_tmp exports CLAUDE_CODE_TMPDIR only" {
  mkdir -m 0700 "$CLAUDE_ROOT"
  for sh in $SHELLS; do
    run source_in "$sh" "$VOL/claude.sh"
    [ "$status" -eq 0 ]
    grep -Fx "CLAUDE_CODE_TMPDIR=$CLAUDE_ROOT" <<<"$output"
    grep -Fx "TMPDIR=/orig/tmp" <<<"$output"
    ! grep -q '^GOTMPDIR=' <<<"$output"
    ! grep -q '^PG_TEST_RUNNER_TMP_ROOT=' <<<"$output"
  done
}

@test "a missing directory exports nothing and leaves TMPDIR unchanged" {
  for sh in $SHELLS; do
    for snippet in build.sh build-tmpdir.sh claude.sh; do
      run source_in "$sh" "$VOL/$snippet"
      [ "$status" -eq 0 ]
      grep -Fx "TMPDIR=/orig/tmp" <<<"$output"
      ! grep -Eq '^(GOTMPDIR|PG_TEST_RUNNER_TMP_ROOT|CLAUDE_CODE_TMPDIR)=' <<<"$output"
    done
  done
}

@test "a present but non-writable directory exports nothing" {
  if [ "$(id -u)" -eq 0 ]; then skip "root ignores directory modes"; fi
  mkdir -m 0500 "$BUILD_ROOT" "$CLAUDE_ROOT"
  for sh in $SHELLS; do
    for snippet in build.sh build-tmpdir.sh claude.sh; do
      run source_in "$sh" "$VOL/$snippet"
      [ "$status" -eq 0 ]
      grep -Fx "TMPDIR=/orig/tmp" <<<"$output"
      ! grep -Eq '^(GOTMPDIR|PG_TEST_RUNNER_TMP_ROOT|CLAUDE_CODE_TMPDIR)=' <<<"$output"
    done
  done
}

@test "a regular file in place of the directory exports nothing" {
  : >"$BUILD_ROOT"
  for sh in $SHELLS; do
    run source_in "$sh" "$VOL/build.sh"
    [ "$status" -eq 0 ]
    ! grep -Eq '^(GOTMPDIR|PG_TEST_RUNNER_TMP_ROOT)=' <<<"$output"
  done
}

@test "no variable named CETA_EXTRA_TEMP_ROOTS is ever exported, directory present or not" {
  mkdir -m 0700 "$BUILD_ROOT" "$CLAUDE_ROOT"
  for snippet in build.sh build-tmpdir.sh claude.sh; do
    ! grep -q CETA_EXTRA_TEMP_ROOTS "$VOL/$snippet"
    for sh in $SHELLS; do
      run source_in "$sh" "$VOL/$snippet"
      [ "$status" -eq 0 ]
      ! grep -q '^CETA_' <<<"$output"
    done
  done
}

@test "a path with a space is handled (the module quotes it)" {
  SP="$VOL/with space"
  mkdir -p "$SP/build_tmp"
  sed "s#/ROOT_PLACEHOLDER#$VOL#g" "$SNIPPET_BUILD_SPACE" >"$VOL/space.sh"
  for sh in $SHELLS; do
    run source_in "$sh" "$VOL/space.sh"
    [ "$status" -eq 0 ]
    grep -Fx "GOTMPDIR=$SP/build_tmp" <<<"$output"
  done
}

@test "sourcing defines no shell function in the user's shell" {
  mkdir -m 0700 "$BUILD_ROOT"
  run env -i PATH="$PATH" HOME="$VOL" bash --noprofile --norc -c '
    before="$(compgen -A function)"
    . "$1"
    after="$(compgen -A function)"
    [ "$before" = "$after" ]' _ "$VOL/build.sh"
  [ "$status" -eq 0 ]
}
