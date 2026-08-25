#!/usr/bin/env bats
# bats file_tags=type:unit

# Unit tests for the status-line rate_limits capture helper (ADR 0021 §1). The
# wrapper injects capture-status.bash verbatim via `builtins.readFile` (see
# scripts.nix) and calls capture_status_line best-effort; testing the functions
# DIRECTLY (sourced) covers clamp/validate, append-on-change, per-field optionality,
# the 0600 mode, and the secret non-leak allowlist WITHOUT a full render.
#
# bats runs against the whole directory (flake.nix `bats <dir>`), so this file is
# auto-discovered by both status-line check derivations. The capture lib is
# nerd-font-agnostic.

setup() {
  # $BATS_TEST_DIRNAME (not a bare relative path): under `bats <dir>` the CWD is
  # not the test dir.
  source "$BATS_TEST_DIRNAME/capture-status.bash"
  TEST_DIR="$(mktemp -d)"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# --- clamp_pct: range validation + bug #52326 ---

@test "clamp_pct passes a valid integer percentage" {
  run clamp_pct "34"
  [ "$status" -eq 0 ]
  [ "$output" = "34" ]
}

@test "clamp_pct passes 0 and 100 (boundaries)" {
  run clamp_pct "0"
  [ "$output" = "0" ]
  run clamp_pct "100"
  [ "$output" = "100" ]
}

@test "clamp_pct passes a fractional percentage keeping precision" {
  run clamp_pct "12.5"
  [ "$output" = "12.5" ]
}

@test "clamp_pct rejects >100 as absent (empty output)" {
  run clamp_pct "101"
  [ "$output" = "" ]
}

@test "clamp_pct rejects the #52326 epoch value as absent" {
  # Claude Code bug #52326 returns an epoch-sized number for an empty window.
  run clamp_pct "1782958200"
  [ "$output" = "" ]
}

@test "clamp_pct rejects a negative percentage as absent" {
  run clamp_pct "-5"
  [ "$output" = "" ]
}

@test "clamp_pct rejects non-numeric input as absent" {
  run clamp_pct "abc"
  [ "$output" = "" ]
  run clamp_pct "3x"
  [ "$output" = "" ]
}

@test "clamp_pct treats empty input as absent" {
  run clamp_pct ""
  [ "$output" = "" ]
}

@test "clamp_pct does not read a leading-zero value as octal" {
  run clamp_pct "08"
  [ "$output" = "08" ]
}

# --- is_epoch: resets_at validation ---

@test "is_epoch passes a positive epoch" {
  run is_epoch "1782958200"
  [ "$output" = "1782958200" ]
}

@test "is_epoch rejects empty / zero / non-numeric" {
  run is_epoch ""
  [ "$output" = "" ]
  run is_epoch "0"
  [ "$output" = "" ]
  run is_epoch "notanumber"
  [ "$output" = "" ]
}

# --- build_status_record: allowlist + optionality ---

@test "build_status_record emits only allowlisted fields, all present" {
  run build_status_record "1700000000" "sess-1" "myhost" "34" "1782958200" "0" "1783000000"
  [ "$status" -eq 0 ]
  [ "$output" = '{"ts":1700000000,"session_id":"sess-1","hostname":"myhost","five_hour_pct":34,"five_hour_resets_at":1782958200,"seven_day_pct":0,"seven_day_resets_at":1783000000}' ]
}

@test "build_status_record omits an absent seven_day window entirely" {
  # Phase 0 observed seven_day absent: omit, never 0 / 1970.
  run build_status_record "1700000000" "sess-1" "myhost" "34" "1782958200" "" ""
  [ "$status" -eq 0 ]
  [ "$output" = '{"ts":1700000000,"session_id":"sess-1","hostname":"myhost","five_hour_pct":34,"five_hour_resets_at":1782958200}' ]
}

@test "build_status_record omits a clamped-away percentage" {
  # five_hour_pct is the #52326 epoch => omitted; five_hour_resets_at still present.
  run build_status_record "1700000000" "sess-1" "myhost" "1782958200" "1782958200" "" ""
  [ "$output" = '{"ts":1700000000,"session_id":"sess-1","hostname":"myhost","five_hour_resets_at":1782958200}' ]
}

@test "build_status_record fails when ts is empty" {
  run build_status_record "" "sess-1" "myhost" "34" "" "" ""
  [ "$status" -ne 0 ]
  [ "$output" = "" ]
}

@test "build_status_record does NOT leak arbitrary env into the record" {
  # Secret non-leak: even with a secret in the environment, only allowlisted args appear.
  export SSH_AUTH_SOCK="/private/tmp/secret.sock"
  export MY_SECRET="hunter2"
  run build_status_record "1700000000" "sess-1" "myhost" "34" "" "" ""
  [ "$status" -eq 0 ]
  [[ "$output" != *"hunter2"* ]]
  [[ "$output" != *"secret.sock"* ]]
  [[ "$output" != *"SSH_AUTH_SOCK"* ]]
}

@test "build_status_record json-escapes a session id with special chars" {
  run build_status_record "1700000000" 'a"b\c' "myhost" "" "" "" ""
  [ "$status" -eq 0 ]
  [ "$output" = '{"ts":1700000000,"session_id":"a\"b\\c","hostname":"myhost"}' ]
}

# --- capture_status_line: append-on-change against a real file ---

@test "capture writes a file at mode 0600 on first capture" {
  local f="$TEST_DIR/sess.status.jsonl"
  capture_status_line "$f" "1700000000" "sess-1" "host" "34" "1782958200" "" ""
  [ -f "$f" ]
  # Portable octal perms. Try GNU (stat -c) first — it is what the nix check
  # sandbox provides — then fall back to BSD (stat -f) for a native macOS shell.
  local mode
  mode=$(stat -c '%a' "$f" 2>/dev/null || stat -f '%Lp' "$f")
  [ "$mode" = "600" ]
  [ "$(wc -l <"$f")" -eq 1 ]
}

@test "capture is append-on-change: unchanged value writes no new line" {
  local f="$TEST_DIR/sess.status.jsonl"
  capture_status_line "$f" "1700000000" "sess-1" "host" "34" "1782958200" "" ""
  # Same clamped values, DIFFERENT ts (as happens on the next render) => no new line.
  capture_status_line "$f" "1700000099" "sess-1" "host" "34" "1782958200" "" ""
  [ "$(wc -l <"$f")" -eq 1 ]
}

@test "capture appends a new line when a value changes" {
  local f="$TEST_DIR/sess.status.jsonl"
  capture_status_line "$f" "1700000000" "sess-1" "host" "34" "1782958200" "" ""
  capture_status_line "$f" "1700000099" "sess-1" "host" "35" "1782958200" "" ""
  [ "$(wc -l <"$f")" -eq 2 ]
}

@test "capture does not append when the only change is the clamped-away epoch (#52326)" {
  # First render: valid 34. Next render: five_hour_pct is the #52326 epoch (clamped
  # away) while resets unchanged => signature unchanged => no flood.
  local f="$TEST_DIR/sess.status.jsonl"
  capture_status_line "$f" "1700000000" "sess-1" "host" "34" "1782958200" "" ""
  capture_status_line "$f" "1700000099" "sess-1" "host" "1782958200" "1782958200" "" ""
  # The second record's five_hour_pct is absent, which IS a change from 34 -> absent.
  # So it DOES append (a window going unknown is a real change), but it must not
  # append a SECOND time on a repeat.
  capture_status_line "$f" "1700000150" "sess-1" "host" "1782958200" "1782958200" "" ""
  [ "$(wc -l <"$f")" -eq 2 ]
}

@test "capture skips entirely when no rate_limits value is present" {
  local f="$TEST_DIR/sess.status.jsonl"
  capture_status_line "$f" "1700000000" "sess-1" "host" "" "" "" ""
  [ ! -e "$f" ]
}

@test "capture returns non-zero without side effects when the dir is unwritable" {
  local f="$TEST_DIR/nonexistent-subdir/sess.status.jsonl"
  run capture_status_line "$f" "1700000000" "sess-1" "host" "34" "1782958200" "" ""
  [ "$status" -ne 0 ]
  [ ! -e "$f" ]
}

@test "capture written record contains no environment secrets" {
  export MY_SECRET="hunter2"
  local f="$TEST_DIR/sess.status.jsonl"
  capture_status_line "$f" "1700000000" "sess-1" "host" "34" "1782958200" "12" "1783000000"
  run cat "$f"
  [[ "$output" != *"hunter2"* ]]
  [[ "$output" == *'"five_hour_pct":34'* ]]
}
