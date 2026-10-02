#!/usr/bin/env bats
# bats file_tags=type:integration
#
# handoff-create against a REAL bd throwaway database (embedded mode, in a
# temp dir; see test-support/bd_fixture.bash for the isolation invariants).
# No dolt server is started and the shared tracker is never touched: every
# test asserts `bd where` resolves into the temp dir before it writes.

bats_require_minimum_version 1.5.0

# SCRIPTS_DIR / TEST_SUPPORT: injected by the nix check (raw src dir /
# vendored support dir), or computed relative to this test for a local
# `bats tests/` run.
if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi
if [[ -z ${TEST_SUPPORT:-} ]]; then
  TEST_SUPPORT="$(cd "${SCRIPTS_DIR}/../test-support" && pwd)"
fi
load "$TEST_SUPPORT/bd_fixture"

setup_file() {
  command -v bd >/dev/null 2>&1 || skip "bd not on PATH"
  bd_fixture_setup_file
}

teardown_file() {
  bd_fixture_teardown_file
}

setup() {
  bd_fixture_setup
  bd_fixture_assert_isolated
  bd_fixture_register_handoff_type
  # The builder composes <name>.bash + <name>.sh under strict mode; do the same.
  cat >"$TDIR/run-handoff-create" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "${SCRIPTS_DIR}/handoff-create.bash"
source "${SCRIPTS_DIR}/handoff-create.sh"
WRAPPER
  chmod +x "$TDIR/run-handoff-create"
  HC="$TDIR/run-handoff-create"
  printf 'Carry-over: tracked in pg2-aaa11.\nFirst step: bd show pg2-aaa11\n' >"$TDIR/body.md"
}

@test "isolation guard: the fixture database is the temp embedded one" {
  bd_fixture_assert_isolated
  [ "$PWD" = "$TDIR" ]
}

@test "attended: creates a handoff carrying the human label" {
  run --separate-stderr "$HC" --attended --session-id sess-1 --title "finish retry work" --body-file "$TDIR/body.md"
  [ "$status" -eq 0 ]
  id="$output"
  [ -n "$id" ]
  json="$(bd_fixture_show_json "$id")"
  [ "$(jq -r '.issue_type' <<<"$json")" = "handoff" ]
  jq -e '.labels | index("human") != null' <<<"$json"
}

@test "unattended: creates a handoff WITHOUT human, keeping passthrough labels" {
  run --separate-stderr "$HC" --unattended --session-id sess-2 --title "finish retry work" \
    --body-file "$TDIR/body.md" --label auto-session-wrapped
  [ "$status" -eq 0 ]
  json="$(bd_fixture_show_json "$output")"
  jq -e '.labels | index("human") == null' <<<"$json"
  jq -e '.labels | index("auto-session-wrapped") != null' <<<"$json"
}

@test "attended with --label auto-session-wrapped carries both labels" {
  run --separate-stderr "$HC" --attended --session-id sess-3 --title "t" --body-file "$TDIR/body.md" --label auto-session-wrapped --label human
  [ "$status" -eq 0 ]
  json="$(bd_fixture_show_json "$output")"
  jq -e '.labels | index("human") != null and index("auto-session-wrapped") != null' <<<"$json"
}

@test "neither --attended nor --unattended: usage error, nothing created" {
  before="$(bd_fixture_count)"
  run --separate-stderr "$HC" --session-id sess-4 --title "t" --body-file "$TDIR/body.md"
  [ "$status" -eq 2 ]
  [[ $stderr == *"--attended or --unattended is required"* ]]
  [ "$(bd_fixture_count)" -eq "$before" ]
}

@test "unattended with --label human is refused, nothing created" {
  before="$(bd_fixture_count)"
  run --separate-stderr "$HC" --unattended --session-id sess-5 --title "t" --body-file "$TDIR/body.md" --label human
  [ "$status" -eq 2 ]
  [[ $stderr == *"MUST NOT carry human"* ]]
  [ "$(bd_fixture_count)" -eq "$before" ]
}

@test "fields: type, priority 0, title prefix, first body line, rest of body, metadata, actor" {
  run --separate-stderr "$HC" --attended --session-id sess-6 --title "finish retry work" --body-file "$TDIR/body.md"
  [ "$status" -eq 0 ]
  json="$(bd_fixture_show_json "$output")"
  [ "$(jq -r '.issue_type' <<<"$json")" = "handoff" ]
  [ "$(jq -r '.priority' <<<"$json")" = "0" ]
  [ "$(jq -r '.title' <<<"$json")" = "Handoff: finish retry work" ]
  [ "$(jq -r '.description | split("\n") | .[0]' <<<"$json")" = "Handoff from session sess-6" ]
  jq -e '.description | contains("Carry-over: tracked in pg2-aaa11.")' <<<"$json"
  [ "$(jq -r '.metadata.handed_off_from_session' <<<"$json")" = "sess-6" ]
  [ "$(jq -r '.created_by' <<<"$json")" = "sess-6" ]
}

@test "an explicit --actor overrides the session-id default" {
  run --separate-stderr "$HC" --unattended --session-id sess-7 --title "t" --body-file "$TDIR/body.md" --actor sess-7-wrapup
  [ "$status" -eq 0 ]
  [ "$(jq -r '.created_by' <<<"$(bd_fixture_show_json "$output")")" = "sess-7-wrapup" ]
}

@test "title already starting Handoff: is not double-prefixed" {
  run --separate-stderr "$HC" --unattended --session-id sess-8 --title "Handoff: already prefixed" --body-file "$TDIR/body.md"
  [ "$status" -eq 0 ]
  [ "$(jq -r '.title' <<<"$(bd_fixture_show_json "$output")")" = "Handoff: already prefixed" ]
}

@test "stdout is exactly the id; the verification report is on stderr" {
  run --separate-stderr "$HC" --unattended --session-id sess-9 --title "t" --body-file "$TDIR/body.md"
  [ "$status" -eq 0 ]
  [[ $output =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]
  [[ $stderr == *"ok: type = handoff"* ]]
  [[ $stderr == *"ok: metadata handed_off_from_session = sess-9"* ]]
  [[ $stderr == *"ok: human label present (1=yes) = 0"* ]]
}

@test "--bd-dir runs bd against that tracker root, not the caller's cwd" {
  elsewhere="$(mktemp -d)"
  cd "$elsewhere"
  run --separate-stderr "$HC" --unattended --session-id sess-10 --title "via bd-dir" --body-file "$TDIR/body.md" --bd-dir "$TDIR"
  [ "$status" -eq 0 ]
  [ "$(jq -r '.title' <<<"$(bd_fixture_show_json "$output")")" = "Handoff: via bd-dir" ]
  cd "$TDIR"
  rm -rf "$elsewhere"
}

@test "read-back works on the {data:[..]} envelope (BD_JSON_ENVELOPE=1)" {
  BD_JSON_ENVELOPE=1 run --separate-stderr "$HC" --attended --session-id sess-11 --title "enveloped" --body-file "$TDIR/body.md"
  [ "$status" -eq 0 ]
  [[ $stderr == *"ok: id = $output"* ]]
}

@test "read-back works on the bare-array shape (BD_JSON_ENVELOPE=0)" {
  BD_JSON_ENVELOPE=0 run --separate-stderr "$HC" --attended --session-id sess-12 --title "bare array" --body-file "$TDIR/body.md"
  [ "$status" -eq 0 ]
  [[ $stderr == *"ok: id = $output"* ]]
}

@test "unregistered type: exactly one fallback to task with identical content" {
  bd_fixture_unregister_types
  bd_call_log_install
  run --separate-stderr "$HC" --attended --session-id sess-13 --title "fallback" --body-file "$TDIR/body.md" --label auto-session-wrapped
  [ "$status" -eq 0 ]
  id="$output"
  [[ $stderr == *"retrying ONCE as type task"* ]]

  mapfile -t creates < <(grep -E '(^| )create( |$)' "$BD_CALL_LOG")
  [ "${#creates[@]}" -eq 2 ]
  [[ ${creates[0]} == *"-t handoff"* ]]
  [[ ${creates[1]} == *"-t task"* ]]
  # identical apart from the type
  [ "${creates[0]/-t handoff/-t X}" = "${creates[1]/-t task/-t X}" ]

  json="$(bd_fixture_show_json "$id")"
  [ "$(jq -r '.issue_type' <<<"$json")" = "task" ]
  [ "$(jq -r '.title' <<<"$json")" = "Handoff: fallback" ]
  [ "$(jq -r '.metadata.handed_off_from_session' <<<"$json")" = "sess-13" ]
  [ "$(jq -r '.description | split("\n") | .[0]' <<<"$json")" = "Handoff from session sess-13" ]
  jq -e '.labels | index("human") != null and index("auto-session-wrapped") != null' <<<"$json"
}

@test "a different create failure is reported and NOT retried" {
  bd_call_log_install "Error: database not found"
  before="$(bd_fixture_count)"
  run --separate-stderr "$HC" --attended --session-id sess-14 --title "doomed" --body-file "$TDIR/body.md"
  [ "$status" -eq 3 ]
  [[ $stderr == *"database not found"* ]]
  [ "$(grep -cE '(^| )create( |$)' "$BD_CALL_LOG")" -eq 1 ]
  [ "$(bd_fixture_count)" -eq "$before" ]
}

@test "the assembled artifact (when the nix check provides it) behaves the same" {
  [ -n "${SCRIPT_UNDER_TEST:-}" ] || skip "SCRIPT_UNDER_TEST is set only by the nix check"
  run --separate-stderr "$SCRIPT_UNDER_TEST" --unattended --session-id sess-15 --title "assembled" --body-file "$TDIR/body.md"
  [ "$status" -eq 0 ]
  [ "$(jq -r '.title' <<<"$(bd_fixture_show_json "$output")")" = "Handoff: assembled" ]
}
