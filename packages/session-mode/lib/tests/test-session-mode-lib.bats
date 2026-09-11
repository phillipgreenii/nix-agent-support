#!/usr/bin/env bats
# bats file_tags=type:unit

# `run !` (used for the rejection asserts) needs bats >= 1.5.0.
bats_require_minimum_version 1.5.0

if [[ -n ${TEST_SUPPORT:-} ]]; then
  source "$TEST_SUPPORT/test_helper.bash"
else
  source "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/test_helper.bash"
fi

load_lib() {
  # shellcheck disable=SC1090 # composed lib path is runtime-resolved (nix check injects LIB_PATH; locally it is the source dir)
  source "$(resolve_lib)"
}

# --- session_mode_encode_cwd ---

@test "session_mode_encode_cwd replaces slashes with a single dash" {
  load_lib
  run session_mode_encode_cwd "/Users/phillipg/repo"
  [ "$status" -eq 0 ]
  [ "$output" = "-Users-phillipg-repo" ]
}

@test "session_mode_encode_cwd replaces underscores" {
  load_lib
  run session_mode_encode_cwd "/Users/phillipg/phillipg_mbp"
  [ "$status" -eq 0 ]
  [ "$output" = "-Users-phillipg-phillipg-mbp" ]
}

@test "session_mode_encode_cwd replaces dots" {
  load_lib
  run session_mode_encode_cwd "/Users/phillipg/repo.name"
  [ "$status" -eq 0 ]
  [ "$output" = "-Users-phillipg-repo-name" ]
}

@test "session_mode_encode_cwd collapses consecutive non-alnum to consecutive dashes (no dedup)" {
  load_lib
  run session_mode_encode_cwd "/a//b"
  [ "$status" -eq 0 ]
  [ "$output" = "-a--b" ]
}

@test "session_mode_encode_cwd handles a leading slash" {
  load_lib
  run session_mode_encode_cwd "/x"
  [ "$status" -eq 0 ]
  [ "$output" = "-x" ]
}

# --- session_mode_state_dir ---

@test "session_mode_state_dir: SESSION_MODE_STATE_DIR override wins over everything" {
  load_lib
  SESSION_MODE_STATE_DIR="/override" run session_mode_state_dir "/some/tx/dir/s.jsonl"
  [ "$status" -eq 0 ]
  [ "$output" = "/override" ]
}

@test "session_mode_state_dir: transcript-path arg wins over the cwd fallback" {
  load_lib
  unset SESSION_MODE_STATE_DIR
  run session_mode_state_dir "/tx/dir/s.jsonl"
  [ "$status" -eq 0 ]
  [ "$output" = "/tx/dir" ]
}

@test "session_mode_state_dir: falls back to the encoded cwd under HOME when no arg given" {
  load_lib
  unset SESSION_MODE_STATE_DIR
  HOME="/Users/phillipg" PWD="/Users/phillipg/repo" run session_mode_state_dir
  [ "$status" -eq 0 ]
  [ "$output" = "/Users/phillipg/.claude/projects/-Users-phillipg-repo" ]
}

# --- session_mode_file_path ---

@test "session_mode_file_path composes DIR/SESSION_ID.session-mode.json" {
  load_lib
  run session_mode_file_path "/some/dir" "sess-1"
  [ "$status" -eq 0 ]
  [ "$output" = "/some/dir/sess-1.session-mode.json" ]
}

# --- session_mode_validate_kind ---

@test "session_mode_validate_kind accepts a novel slug (proves extensibility)" {
  load_lib
  session_mode_validate_kind "some-brand-new-kind"
  session_mode_validate_kind "drain-beads"
  session_mode_validate_kind "a"
}

@test "session_mode_validate_kind rejects malformed slugs" {
  load_lib
  run ! session_mode_validate_kind ""
  run ! session_mode_validate_kind "Drain-Beads"
  run ! session_mode_validate_kind "1-starts-with-digit"
  run ! session_mode_validate_kind "has space"
  run ! session_mode_validate_kind "has_underscore"
}

# --- session_mode_validate_state ---

@test "session_mode_validate_state accepts exactly the 3 enum values" {
  load_lib
  session_mode_validate_state "running"
  session_mode_validate_state "stopping"
  session_mode_validate_state "finished"
}

@test "session_mode_validate_state rejects anything else" {
  load_lib
  run ! session_mode_validate_state ""
  run ! session_mode_validate_state "RUNNING"
  run ! session_mode_validate_state "done"
}

# --- session_mode_build_record ---

@test "session_mode_build_record omits an empty detail" {
  load_lib
  run session_mode_build_record "drain-beads" "" "running" "2026-09-11T14:32:00Z" "2026-09-11T14:32:00Z"
  [ "$status" -eq 0 ]
  [[ "$output" != *'"detail"'* ]]
  [[ "$output" == *'"kind":"drain-beads"'* ]]
  [[ "$output" == *'"state":"running"'* ]]
}

@test "session_mode_build_record leaves a short detail untouched" {
  load_lib
  run session_mode_build_record "drain-beads" "P1 only" "running" "2026-09-11T14:32:00Z" "2026-09-11T14:32:00Z"
  [ "$status" -eq 0 ]
  [[ "$output" == *'"detail":"P1 only"'* ]]
}

@test "session_mode_build_record truncates an over-long detail to 40 chars with a trailing ellipsis" {
  load_lib
  local long
  long="$(printf 'a%.0s' {1..80})" # 80 'a's
  run session_mode_build_record "drain-beads" "$long" "running" "2026-09-11T14:32:00Z" "2026-09-11T14:32:00Z"
  [ "$status" -eq 0 ]
  detail="$(printf '%s' "$output" | jq -r '.detail')"
  # Exact-string check (not a `${#detail}` character count, which is
  # locale-sensitive for the multi-byte ellipsis): 39 'a's, then the ellipsis,
  # nothing more.
  [[ "$detail" == "$(printf 'a%.0s' {1..39})…" ]]
}

@test "session_mode_build_record emits every other field" {
  load_lib
  run session_mode_build_record "wrap-up-session" "" "finished" "2026-09-11T14:32:00Z" "2026-09-11T15:00:00Z"
  [ "$status" -eq 0 ]
  [[ "$output" == *'"kind":"wrap-up-session"'* ]]
  [[ "$output" == *'"state":"finished"'* ]]
  [[ "$output" == *'"started_at":"2026-09-11T14:32:00Z"'* ]]
  [[ "$output" == *'"updated_at":"2026-09-11T15:00:00Z"'* ]]
}

# --- session_mode_read ---

@test "session_mode_read fails on a missing file" {
  load_lib
  run session_mode_read "$TEST_DIR/nope.json"
  [ "$status" -ne 0 ]
  [ -z "$output" ]
}

@test "session_mode_read fails on a zero-byte file (treated like absent)" {
  load_lib
  : >"$TEST_DIR/empty.json"
  run session_mode_read "$TEST_DIR/empty.json"
  [ "$status" -ne 0 ]
  [ -z "$output" ]
}

@test "session_mode_read prints the file content when present" {
  load_lib
  printf '{"kind":"drain-beads"}' >"$TEST_DIR/rec.json"
  run session_mode_read "$TEST_DIR/rec.json"
  [ "$status" -eq 0 ]
  [ "$output" = '{"kind":"drain-beads"}' ]
}

# --- session_mode_write_atomic ---

@test "session_mode_write_atomic lands the file via a same-dir mktemp+mv (no leftover temp)" {
  load_lib
  local target="$TEST_DIR/state/sess-1.session-mode.json"
  session_mode_write_atomic "$target" '{"kind":"drain-beads"}'
  [ -f "$target" ]
  [ "$(cat "$target")" = '{"kind":"drain-beads"}' ]
  # No leftover temp file in the same directory.
  local leftovers
  leftovers=$(find "$TEST_DIR/state" -name '.session-mode.tmp.*' 2>/dev/null)
  [ -z "$leftovers" ]
}

@test "session_mode_write_atomic creates the directory if absent" {
  load_lib
  local target="$TEST_DIR/does/not/exist/yet/sess-1.session-mode.json"
  session_mode_write_atomic "$target" '{"kind":"x"}'
  [ -f "$target" ]
}

@test "session_mode_write_atomic overwrites an existing file" {
  load_lib
  local target="$TEST_DIR/state/sess-1.session-mode.json"
  session_mode_write_atomic "$target" '{"kind":"a"}'
  session_mode_write_atomic "$target" '{"kind":"b"}'
  [ "$(cat "$target")" = '{"kind":"b"}' ]
}
