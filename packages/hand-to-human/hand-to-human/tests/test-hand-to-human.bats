#!/usr/bin/env bats
# bats file_tags=type:integration
#
# hand-to-human against a REAL bd throwaway database (embedded mode, in a temp
# dir; see test-support/bd_fixture.bash for the isolation invariants). No dolt
# server is started and the shared tracker is never touched: every test asserts
# `bd where` resolves into the temp dir before it writes.

bats_require_minimum_version 1.5.0

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
  bd_call_log_install
  # The builder composes <name>.bash + <name>.sh under strict mode; do the same.
  cat >"$TDIR/run-hand-to-human" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "${SCRIPTS_DIR}/hand-to-human.bash"
source "${SCRIPTS_DIR}/hand-to-human.sh"
WRAPPER
  chmod +x "$TDIR/run-hand-to-human"
  H="$TDIR/run-hand-to-human"
}

teardown() {
  rm -rf "$BD_SHIM_DIR"
}

# new_bead <label>...: create a bead carrying the labels, print its id.
new_bead() {
  local -a args=(create --title "bead $RANDOM" --silent)
  local l
  for l in "$@"; do
    args+=(-l "$l")
  done
  bd "${args[@]}" 2>/dev/null
}

labels_of() { # id -> sorted labels, one per line
  bd_fixture_show_json "$1" | jq -r '(.labels // []) | sort | .[]'
}

@test "isolation guard: the fixture database is the temp embedded one" {
  bd_fixture_assert_isolated
  [ "$PWD" = "$TDIR" ]
}

@test "escalated bead: ends with human + attention, escalated gone, other labels kept" {
  id="$(new_bead escalated keep-me)"
  : >"$BD_CALL_LOG"
  run --separate-stderr "$H" "$id" --actor test-actor
  [ "$status" -eq 0 ]
  [ "$output" = "$id" ]
  [ "$(labels_of "$id")" = $'attention\nhuman\nkeep-me' ]
}

@test "the label change is ONE bd update call, never a separate label verb" {
  id="$(new_bead escalated)"
  : >"$BD_CALL_LOG"
  run --separate-stderr "$H" "$id"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^update ' "$BD_CALL_LOG")" -eq 1 ]
  [ "$(grep -c '^label ' "$BD_CALL_LOG")" -eq 0 ]
  # adds are ahead of the removal in the one call
  line="$(grep '^update ' "$BD_CALL_LOG")"
  [[ ${line%%--remove-label*} == *"--add-label human"* ]]
  [[ ${line%%--remove-label*} == *"--add-label attention"* ]]
}

@test "bead that never carried escalated: removal is a no-op, pair is added" {
  id="$(new_bead keep-me)"
  run --separate-stderr "$H" "$id"
  [ "$status" -eq 0 ]
  [ "$(labels_of "$id")" = $'attention\nhuman\nkeep-me' ]
}

@test "bead already carrying human only (planned work) gains attention, no error" {
  id="$(new_bead human)"
  run --separate-stderr "$H" "$id"
  [ "$status" -eq 0 ]
  [ "$(labels_of "$id")" = $'attention\nhuman' ]
}

@test "--also-remove drops the extra label in the same call" {
  id="$(new_bead escalated needs-split-review)"
  : >"$BD_CALL_LOG"
  run --separate-stderr "$H" "$id" --also-remove needs-split-review
  [ "$status" -eq 0 ]
  [ "$(grep -c '^update ' "$BD_CALL_LOG")" -eq 1 ]
  [ "$(labels_of "$id")" = $'attention\nhuman' ]
}

@test "idempotent: a second hand-off changes nothing and still succeeds" {
  id="$(new_bead escalated)"
  run --separate-stderr "$H" "$id"
  [ "$status" -eq 0 ]
  run --separate-stderr "$H" "$id"
  [ "$status" -eq 0 ]
  [ "$(labels_of "$id")" = $'attention\nhuman' ]
}

@test "works under either bd --json envelope (BD_JSON_ENVELOPE=1 and 0)" {
  for env in 1 0; do
    id="$(new_bead escalated)"
    BD_JSON_ENVELOPE=$env run --separate-stderr "$H" "$id"
    [ "$status" -eq 0 ]
    [ "$(labels_of "$id")" = $'attention\nhuman' ]
  done
}

@test "--bd-dir targets that tracker root" {
  id="$(new_bead escalated)"
  cd /
  run --separate-stderr "$H" "$id" --bd-dir "$TDIR"
  [ "$status" -eq 0 ]
  [ "$(labels_of "$id")" = $'attention\nhuman' ]
}

@test "unknown bead id: exit 3, nothing created" {
  before="$(bd_fixture_count)"
  run --separate-stderr "$H" tst-nope99
  [ "$status" -eq 3 ]
  [ "$(bd_fixture_count)" -eq "$before" ]
}

# --also-add / --note against real bd (bead pg2-8num0)

notes_of() { bd_fixture_show_json "$1" | jq -r '.notes // ""'; }

@test "--also-add + --note: one update lands the marker label, the note, and the hand-off pair" {
  id="$(new_bead escalated keep-me)"
  : >"$BD_CALL_LOG"
  run --separate-stderr "$H" "$id" --also-add stale-escalation --note "Automation timed out." --actor test-actor
  [ "$status" -eq 0 ]
  [ "$output" = "$id" ]
  [ "$(grep -c '^update ' "$BD_CALL_LOG")" -eq 1 ]
  [ "$(labels_of "$id")" = $'attention\nhuman\nkeep-me\nstale-escalation' ]
  [ "$(notes_of "$id")" = "Automation timed out." ]
  [[ $stderr == *"ok: label stale-escalation present"* ]]
  [[ $stderr == *"ok: note present"* ]]
}

@test "--note is appended to existing notes, multi-line text intact" {
  id="$(new_bead escalated)"
  bd update --notes "earlier note" -- "$id" >/dev/null 2>&1
  run --separate-stderr "$H" "$id" --note $'line one\nline two'
  [ "$status" -eq 0 ]
  [ "$(notes_of "$id")" = $'earlier note\nline one\nline two' ]
}

@test "--also-add alone (no note) works and leaves notes untouched" {
  id="$(new_bead escalated)"
  run --separate-stderr "$H" "$id" --also-add stale-escalation
  [ "$status" -eq 0 ]
  [ "$(labels_of "$id")" = $'attention\nhuman\nstale-escalation' ]
  [ "$(notes_of "$id")" = "" ]
}

@test "--note against an unknown bead: exit 3, nothing created" {
  before="$(bd_fixture_count)"
  run --separate-stderr "$H" tst-nope99 --also-add stale-escalation --note "x"
  [ "$status" -eq 3 ]
  [ "$(bd_fixture_count)" -eq "$before" ]
}

# What packet 2 (the escalation watchdog) needs to know about bd's own guards:
# does `bd update --if-assignee/--if-status` combine with --add-label and
# --append-notes? Recorded against the real bd, not through hand-to-human.

@test "bd guard: --if-assignee '' --if-status open combine with --add-label and --append-notes (match writes both)" {
  id="$(new_bead escalated)"
  run --separate-stderr bd update --if-assignee "" --if-status open --add-label guarded --remove-label escalated --append-notes "note" -- "$id"
  [ "$status" -eq 0 ]
  [ "$(labels_of "$id")" = "guarded" ]
  [ "$(notes_of "$id")" = "note" ]
}

@test "bd guard: a mismatch exits 13 and writes NOTHING (labels and notes untouched)" {
  id="$(new_bead escalated)"
  bd update -a someone -- "$id" >/dev/null 2>&1
  run --separate-stderr bd update --if-assignee "" --if-status open --add-label guarded --append-notes "note" -- "$id"
  [ "$status" -eq 13 ]
  [ "$(labels_of "$id")" = "escalated" ]
  [ "$(notes_of "$id")" = "" ]
  run --separate-stderr bd update --if-status closed --add-label guarded --append-notes "note" -- "$id"
  [ "$status" -eq 13 ]
  [ "$(labels_of "$id")" = "escalated" ]
}

@test "bd guard: with ONLY label edits the guard is refused (exit 1); a field update such as --append-notes is required" {
  id="$(new_bead escalated)"
  run --separate-stderr bd update --if-assignee "" --if-status open --add-label guarded -- "$id"
  [ "$status" -eq 1 ]
  [[ $stderr == *"require at least one field update"* ]]
  [ "$(labels_of "$id")" = "escalated" ]
}

# hand-to-human's own claim guard against the real bd (bead pg2-f9x7i).

@test "guard match (unassigned + open): handed off, one update, note and marker written" {
  id="$(new_bead escalated)"
  : >"$BD_CALL_LOG"
  run --separate-stderr "$H" "$id" --also-add stale-escalation --note "timed out" --if-assignee "" --if-status open
  [ "$status" -eq 0 ]
  [ "$(grep -c '^update ' "$BD_CALL_LOG")" -eq 1 ]
  [ "$(labels_of "$id")" = $'attention\nhuman\nstale-escalation' ]
  [ "$(notes_of "$id")" = "timed out" ]
}

@test "guard mismatch (assigned): exit 5 and NOTHING written" {
  id="$(new_bead escalated)"
  bd update -a someone -- "$id" >/dev/null 2>&1
  run --separate-stderr "$H" "$id" --also-add stale-escalation --note "timed out" --if-assignee "" --if-status open
  [ "$status" -eq 5 ]
  [[ $stderr == *"claim guard mismatch, nothing written"* ]]
  [ "$(labels_of "$id")" = "escalated" ]
  [ "$(notes_of "$id")" = "" ]
}

@test "guard mismatch (status): exit 5 and NOTHING written" {
  id="$(new_bead escalated)"
  bd update --status blocked -- "$id" >/dev/null
  run --separate-stderr "$H" "$id" --note "timed out" --if-status open
  [ "$status" -eq 5 ]
  [ "$(labels_of "$id")" = "escalated" ]
  [ "$(notes_of "$id")" = "" ]
}

@test "an unknown bead with a guard is still exit 3 (a failure, not a guard mismatch)" {
  run --separate-stderr "$H" tst-nope99 --note x --if-status open
  [ "$status" -eq 3 ]
}
