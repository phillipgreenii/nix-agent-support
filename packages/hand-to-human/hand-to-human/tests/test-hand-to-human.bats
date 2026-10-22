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
