#!/usr/bin/env bats
# bats file_tags=type:unit

# Unit tests for the status-line session-mode read helper (bead pg2-gzrn2). The
# wrapper injects session-mode-status.bash verbatim via `builtins.readFile`
# (see scripts.nix) and calls read_session_mode against the sibling
# <session_id>.session-mode.json file; testing the functions DIRECTLY
# (sourced) covers the pattern-match plucker, escape handling, and
# present/absent combinations WITHOUT a full render. Mirrors
# test-capture-status.bats.
#
# bats runs against the whole directory (flake.nix `bats <dir>`), so this
# file is auto-discovered by both status-line check derivations. This helper
# is nerd-font-agnostic.

setup() {
  # $BATS_TEST_DIRNAME (not a bare relative path): under `bats <dir>` the CWD
  # is not the test dir.
  source "$BATS_TEST_DIRNAME/session-mode-status.bash"
  TEST_DIR="$(mktemp -d)"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# --- json_string_field ---

@test "json_string_field plucks a plain quoted value" {
  run json_string_field '{"kind":"drain-beads","state":"running"}' kind
  [ "$status" -eq 0 ]
  [ "$output" = "drain-beads" ]
}

@test "json_string_field plucks a later field regardless of position" {
  run json_string_field '{"kind":"drain-beads","state":"running"}' state
  [ "$status" -eq 0 ]
  [ "$output" = "running" ]
}

@test "json_string_field decodes an escaped double quote" {
  run json_string_field '{"detail":"say \"hi\""}' detail
  [ "$status" -eq 0 ]
  [ "$output" = 'say "hi"' ]
}

@test "json_string_field decodes an escaped backslash" {
  # The JSON text below carries two literal backslash characters (bash single
  # quotes apply no escaping) — the one valid JSON encoding of a single
  # literal backslash in the decoded value.
  run json_string_field '{"detail":"a\\b"}' detail
  [ "$status" -eq 0 ]
  [ "$output" = 'a\b' ]
}

@test "json_string_field returns empty for an absent key" {
  run json_string_field '{"kind":"drain-beads"}' detail
  [ "$status" -eq 0 ]
  [ "$output" = "" ]
}

@test "json_string_field handles a value containing a colon and comma" {
  run json_string_field '{"detail":"P1, priority: high"}' detail
  [ "$status" -eq 0 ]
  [ "$output" = "P1, priority: high" ]
}

# --- read_session_mode ---

@test "read_session_mode assigns kind/state/detail when all present" {
  local f="$TEST_DIR/rec.json"
  printf '{"kind":"drain-beads","detail":"P1 only","state":"running","started_at":"x","updated_at":"y"}\n' >"$f"
  read_session_mode "$f"
  [ "$CLAUDE_SL_SESSION_MODE_KIND" = "drain-beads" ]
  [ "$CLAUDE_SL_SESSION_MODE_STATE" = "running" ]
  [ "$CLAUDE_SL_SESSION_MODE_DETAIL" = "P1 only" ]
}

@test "read_session_mode leaves detail empty when the record omits it" {
  local f="$TEST_DIR/rec.json"
  printf '{"kind":"wrap-up-session","state":"finished","started_at":"x","updated_at":"y"}\n' >"$f"
  read_session_mode "$f"
  [ "$CLAUDE_SL_SESSION_MODE_KIND" = "wrap-up-session" ]
  [ "$CLAUDE_SL_SESSION_MODE_STATE" = "finished" ]
  [ "$CLAUDE_SL_SESSION_MODE_DETAIL" = "" ]
}

@test "read_session_mode leaves all three empty when the file is missing" {
  read_session_mode "$TEST_DIR/nope.json"
  [ "$CLAUDE_SL_SESSION_MODE_KIND" = "" ]
  [ "$CLAUDE_SL_SESSION_MODE_STATE" = "" ]
  [ "$CLAUDE_SL_SESSION_MODE_DETAIL" = "" ]
}

@test "read_session_mode leaves all three empty when the file is zero-byte" {
  local f="$TEST_DIR/empty.json"
  : >"$f"
  read_session_mode "$f"
  [ "$CLAUDE_SL_SESSION_MODE_KIND" = "" ]
  [ "$CLAUDE_SL_SESSION_MODE_STATE" = "" ]
  [ "$CLAUDE_SL_SESSION_MODE_DETAIL" = "" ]
}

@test "read_session_mode resets stale values from a previous call" {
  local f="$TEST_DIR/rec.json"
  printf '{"kind":"drain-beads","detail":"P1 only","state":"running","started_at":"x","updated_at":"y"}\n' >"$f"
  read_session_mode "$f"
  [ "$CLAUDE_SL_SESSION_MODE_DETAIL" = "P1 only" ]
  read_session_mode "$TEST_DIR/nope.json"
  [ "$CLAUDE_SL_SESSION_MODE_KIND" = "" ]
  [ "$CLAUDE_SL_SESSION_MODE_DETAIL" = "" ]
}

@test "read_session_mode never invokes jq" {
  # Shadow jq with a failing stub in a mock dir prepended to PATH: if
  # read_session_mode shells out to jq, this proves it (and would fail the
  # test), matching capture-status.bash's own jq-free discipline.
  local mock_bin="$TEST_DIR/mock-bin"
  mkdir -p "$mock_bin"
  cat >"$mock_bin/jq" <<'EOF'
#!/usr/bin/env bash
echo "jq should not be invoked by read_session_mode" >&2
exit 1
EOF
  chmod +x "$mock_bin/jq"
  local f="$TEST_DIR/rec.json"
  printf '{"kind":"drain-beads","detail":"P1 only","state":"running","started_at":"x","updated_at":"y"}\n' >"$f"
  PATH="$mock_bin:$PATH" read_session_mode "$f"
  [ "$CLAUDE_SL_SESSION_MODE_KIND" = "drain-beads" ]
}
