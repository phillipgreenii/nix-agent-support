#!/usr/bin/env bats
# bats file_tags=type:unit

# Unit tests for the status-line compaction marker read helper (bead pg2-thkh0.4).
# The wrapper injects compaction-status.bash verbatim via `builtins.readFile` (see
# scripts.nix) and calls read_compaction_marker against the sibling
# <session_id>.compaction.json file; testing the functions DIRECTLY (sourced) covers the
# pattern-match plucker, validation and clamping WITHOUT a full render. Mirrors
# test-session-mode-status.bats.
#
# NOW is passed as an argument: bash 5.3 ignores an injected EPOCHSECONDS, and the
# part scripts are store paths bats cannot call. The functions assign globals, so they
# are called directly (not through `run`, which would discard the assignment).
#
# bats runs against the whole directory (flake.nix `bats <dir>`), so this file is
# auto-discovered by both status-line check derivations. It is nerd-font-agnostic.

setup() {
  # $BATS_TEST_DIRNAME (not a bare relative path): under `bats <dir>` the CWD is not
  # the test dir.
  source "$BATS_TEST_DIRNAME/compaction-status.bash"
  TEST_DIR="$(mktemp -d)"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# --- read_compaction_marker: valid markers ---

@test "fresh marker yields the age in seconds and the trigger" {
  printf '{"ts":1000,"trigger":"auto"}\n' >"$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 1090
  [ "$CLAUDE_SL_COMPACT_AGE" = "90" ]
  [ "$CLAUDE_SL_COMPACT_TRIGGER" = "auto" ]
}

@test "marker without a trailing newline is still read" {
  printf '{"ts":1000,"trigger":"manual"}' >"$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 1060
  [ "$CLAUDE_SL_COMPACT_AGE" = "60" ]
  [ "$CLAUDE_SL_COMPACT_TRIGGER" = "manual" ]
}

@test "age is the raw elapsed time; the window check lives in the wrapper" {
  printf '{"ts":1000,"trigger":"auto"}\n' >"$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 999999
  [ "$CLAUDE_SL_COMPACT_AGE" = "998999" ]
}

@test "ts equal to now yields age 0" {
  printf '{"ts":1000,"trigger":"auto"}\n' >"$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 1000
  [ "$CLAUDE_SL_COMPACT_AGE" = "0" ]
}

@test "ts in the future (clock skew) is clamped to age 0" {
  printf '{"ts":5000,"trigger":"auto"}\n' >"$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 1000
  [ "$CLAUDE_SL_COMPACT_AGE" = "0" ]
}

@test "ts is plucked regardless of field order" {
  printf '{"trigger":"unknown","ts":1000}\n' >"$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 1030
  [ "$CLAUDE_SL_COMPACT_AGE" = "30" ]
  [ "$CLAUDE_SL_COMPACT_TRIGGER" = "unknown" ]
}

@test "a marker in the exact shape the writer emits is accepted" {
  # Byte-for-byte what session_mode_build_compaction_record's `jq -nc` prints (compact,
  # ts first); jq itself is not on the check's PATH, so the literal is spelled out.
  printf '{"ts":1700000000,"trigger":"manual"}\n' >"$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 1700000120
  [ "$CLAUDE_SL_COMPACT_AGE" = "120" ]
  [ "$CLAUDE_SL_COMPACT_TRIGGER" = "manual" ]
}

@test "a trigger outside manual|auto|unknown is dropped but the age still counts" {
  printf '{"ts":1000,"trigger":"bogus"}\n' >"$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 1010
  [ "$CLAUDE_SL_COMPACT_AGE" = "10" ]
  [ -z "$CLAUDE_SL_COMPACT_TRIGGER" ]
}

# --- read_compaction_marker: nothing-to-show inputs ---

@test "absent file yields nothing and succeeds" {
  CLAUDE_SL_COMPACT_AGE=stale
  CLAUDE_SL_COMPACT_TRIGGER=stale
  run read_compaction_marker "$TEST_DIR/missing.json" 1000
  [ "$status" -eq 0 ]
  read_compaction_marker "$TEST_DIR/missing.json" 1000
  [ -z "$CLAUDE_SL_COMPACT_AGE" ]
  [ -z "$CLAUDE_SL_COMPACT_TRIGGER" ]
}

@test "zero-byte file yields nothing" {
  : >"$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 1000
  [ -z "$CLAUDE_SL_COMPACT_AGE" ]
}

@test "unreadable file yields nothing" {
  printf '{"ts":1000,"trigger":"auto"}\n' >"$TEST_DIR/m.json"
  chmod 000 "$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 1010
  chmod 600 "$TEST_DIR/m.json"
  # root ignores the mode bits; only assert when the file really was unreadable.
  if [ "$(id -u)" -ne 0 ]; then
    [ -z "$CLAUDE_SL_COMPACT_AGE" ]
  fi
}

@test "a directory in place of the file yields nothing" {
  mkdir "$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 1000
  [ -z "$CLAUDE_SL_COMPACT_AGE" ]
}

@test "malformed (non-JSON) file yields nothing" {
  printf 'this is not json\n' >"$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 1000
  [ -z "$CLAUDE_SL_COMPACT_AGE" ]
}

@test "marker without a ts field yields nothing" {
  printf '{"trigger":"auto"}\n' >"$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 1000
  [ -z "$CLAUDE_SL_COMPACT_AGE" ]
}

@test "non-numeric ts yields nothing" {
  printf '{"ts":"soon","trigger":"auto"}\n' >"$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 1000
  [ -z "$CLAUDE_SL_COMPACT_AGE" ]
}

@test "zero ts yields nothing (positive integer required)" {
  printf '{"ts":0,"trigger":"auto"}\n' >"$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 1000
  [ -z "$CLAUDE_SL_COMPACT_AGE" ]
}

@test "negative ts yields nothing" {
  printf '{"ts":-5,"trigger":"auto"}\n' >"$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 1000
  [ -z "$CLAUDE_SL_COMPACT_AGE" ]
}

@test "fractional ts yields nothing" {
  printf '{"ts":10.5,"trigger":"auto"}\n' >"$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 1000
  [ -z "$CLAUDE_SL_COMPACT_AGE" ]
}

@test "ts with a leading zero yields nothing (no octal surprises)" {
  printf '{"ts":0100,"trigger":"auto"}\n' >"$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 1000
  [ -z "$CLAUDE_SL_COMPACT_AGE" ]
}

@test "absurdly long ts yields nothing (no arithmetic overflow)" {
  printf '{"ts":99999999999999999999,"trigger":"auto"}\n' >"$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 1000
  [ -z "$CLAUDE_SL_COMPACT_AGE" ]
}

@test "non-integer NOW yields nothing" {
  printf '{"ts":1000,"trigger":"auto"}\n' >"$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" "abc"
  [ -z "$CLAUDE_SL_COMPACT_AGE" ]
  read_compaction_marker "$TEST_DIR/m.json" ""
  [ -z "$CLAUDE_SL_COMPACT_AGE" ]
}

@test "a second call with a bad file resets the outputs of the previous good call" {
  printf '{"ts":1000,"trigger":"auto"}\n' >"$TEST_DIR/good.json"
  printf 'garbage\n' >"$TEST_DIR/bad.json"
  read_compaction_marker "$TEST_DIR/good.json" 1010
  [ "$CLAUDE_SL_COMPACT_AGE" = "10" ]
  read_compaction_marker "$TEST_DIR/bad.json" 1010
  [ -z "$CLAUDE_SL_COMPACT_AGE" ]
  [ -z "$CLAUDE_SL_COMPACT_TRIGGER" ]
}

# --- the lib must not clobber the wrapper's top-level names ---

@test "helpers do not leak variables into the caller's scope" {
  # Names the wrapper uses at top level (plus the helpers' own locals) must survive a call.
  local names="d gitdir gl hl out parts line lines cur seg rest ts path now" n
  for n in $names; do printf -v "$n" keep; done
  printf '{"ts":1000,"trigger":"auto"}\n' >"$TEST_DIR/m.json"
  read_compaction_marker "$TEST_DIR/m.json" 1010
  for n in $names; do
    [ "${!n}" = keep ] || { echo "clobbered: $n"; return 1; }
  done
}

# --- plucks ---

@test "compaction_pluck_ts tolerates whitespace around the value" {
  run compaction_pluck_ts '{"ts": 42 , "trigger":"auto"}'
  [ "$status" -eq 0 ]
  [ "$output" = "42" ]
}

@test "compaction_pluck_ts does not match a key that merely ends in ts" {
  run compaction_pluck_ts '{"pts":42,"trigger":"auto"}'
  [ -z "$output" ]
}

@test "the lib stays jq-free" {
  run bash -c "grep -v '^[[:space:]]*#' '$BATS_TEST_DIRNAME/compaction-status.bash' | grep -w jq"
  [ "$status" -eq 1 ]
}
