#!/usr/bin/env bats
# bats file_tags=type:integration
#
# Failure surfacing of the workspace build (beads pg2-xg9ppr, F8, on top of pg2-loswfh, F3):
# a RED build is a build-red transition, which files exactly ONE P1 bead (the terminal
# repo's label, or an explicit `workspace` label) and posts ONE notification; a repeated
# red or a newer red tuple is a comment, never a second bead; a later green build closes
# the runner's own bead. Real everything except the build command (a script of the
# fixture), the notifier (a recording script) and, where there is no pn, the stand-in of
# test-support/workspace_fixture.bash. The bd is the REAL bd over an embedded throwaway
# database (test-support/bd_fixture.bash: no dolt server, never the shared tracker).
# Nothing here pushes, and every repo in the fixture config has push = false.

# shellcheck disable=SC2034  # RUNNER and WS_SURFACE are read by test-support/workspace_fixture.bash
bats_require_minimum_version 1.5.0

if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi
if [[ -z ${TEST_SUPPORT:-} ]]; then
  TEST_SUPPORT="$(cd "${SCRIPTS_DIR}/../test-support" && pwd)"
fi
load "$TEST_SUPPORT/bd_fixture"
load "$TEST_SUPPORT/workspace_fixture"

setup_file() {
  command -v bd >/dev/null 2>&1 || skip "bd not on PATH"
  bd_fixture_setup_file
}

teardown_file() {
  bd_fixture_teardown_file
}

setup() {
  if [[ -z ${GFH_LIB:-} ]]; then
    echo "GFH_LIB is not set: run under the workspace .envrc (direnv) or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # shellcheck disable=SC1090,SC1091
  source "$GFH_LIB"
  gfh_save_env SCRIPTS_DIR SCRIPT_UNDER_TEST TEST_SUPPORT GFH_LIB TDIR RR_TEST_PN
  gfh_setup "pg-repo-runner"
  gfh_restore_env
  bd_fixture_setup
  bd_fixture_assert_isolated
  # the database is shared by every test of the file: only beads created by THIS test count
  BASE_IDS="$(bd -C "$TDIR" list --status all -n 0 --json | jq -c '(if type=="object" and has("data") then .data else . end) | map(.id)')"

  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    RUNNER=("$SCRIPT_UNDER_TEST")
  else
    RUNNER=(bash -euo pipefail "$SCRIPTS_DIR/pg-repo-runner.sh")
  fi
  TEST_DIR="$GFH_WORK"
  WS_SURFACE="$TDIR"
  ws_fixture_setup
  make_notifier
}

teardown() {
  [[ -z ${RR_DEBUG:-} ]] || cat "$STATE/run.log" >&3
  # the database is shared by the file: close what this test left open, so the
  # runner's own find-before-create of the next test cannot adopt it
  local id
  for id in $(bd -C "$TDIR" list --metadata-field runner=pg-repo-runner --status open -n 0 --json 2>/dev/null |
    jq -r '(if type=="object" and has("data") then .data else . end) | .[].id'); do
    bd -C "$TDIR" close "$id" >/dev/null 2>&1 || true
  done
  gfh_teardown
}

# make_notifier: the recording stand-in for osascript (one line per call).
make_notifier() {
  cat >"$TEST_DIR/osascript-stub" <<EOF
#!/usr/bin/env bash
n=\$#
printf '%s | %s\n' "\${@:\$((n-1)):1}" "\${@:\$n:1}" >>"$TEST_DIR/notify.log"
EOF
  chmod +x "$TEST_DIR/osascript-stub"
  export PG_REPO_RUNNER_OSASCRIPT_BIN="$TEST_DIR/osascript-stub"
}

notifications() {
  if [[ -f $TEST_DIR/notify.log ]]; then wc -l <"$TEST_DIR/notify.log" | tr -d ' '; else echo 0; fi
}

# runner_beads: every bead the runner owns for the workspace (any status), as a JSON array.
runner_beads() {
  bd -C "$TDIR" list --metadata-field runner=pg-repo-runner --metadata-field repo=workspace \
    --status all -n 0 --json |
    jq -c --argjson base "$BASE_IDS" \
      '(if type=="object" and has("data") then .data else . end) | map(select(.id as $i | $base | index($i) | not))'
}

bead_count() { runner_beads | jq 'length'; }

comments_of() {
  bd -C "$TDIR" comments "$1" --json |
    jq -r '(if type=="object" and has("data") then .data else . end) | .[].text // .[].body // empty'
}

# build_with EXIT: one build run of the current tuple whose build command ends with EXIT.
build_with() {
  echo "$1" >"$TEST_DIR/build.exit"
  ws_build
}

@test "a red build files exactly one P1 bead under the terminal repo's label and one notification" {
  local before h b
  before="$(bd_fixture_count)"
  h="$(ws_hash)"
  build_with 3
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(ws_verdict_file "$h")")" = red ]
  [ "$(bd_fixture_count)" -eq $((before + 1)) ]
  [ "$(bead_count)" -eq 1 ]
  b="$(runner_beads | jq -c '.[0]')"
  [ "$(jq -r .priority <<<"$b")" = 1 ]
  [ "$(jq -r .status <<<"$b")" = open ]
  [ "$(jq -r .issue_type <<<"$b")" = task ]
  [ "$(jq -r '.metadata.runner' <<<"$b")" = pg-repo-runner ]
  [ "$(jq -r '.metadata.repo' <<<"$b")" = workspace ]
  [ "$(jq -r '.metadata.kind' <<<"$b")" = build-red ]
  [ "$(jq -r '.metadata.sha' <<<"$b")" = "$h" ]
  [ "$(jq -r .title <<<"$b")" = "workspace: workspace build is red at ${h:0:12}" ]
  [ "$(bd_fixture_show_json "$(jq -r .id <<<"$b")" | jq -r '.labels | index("terminal-label") != null')" = true ]
  [ "$(notifications)" -eq 1 ]
  [ -z "$(ws_sets)" ]
}

@test "an explicit workspace label wins over the terminal repo's" {
  sed -i.bak 's/^nix-gamma = "terminal-label"$/nix-gamma = "terminal-label"\nworkspace = "explicit-label"/' "$TEST_DIR/config.toml"
  build_with 3
  [ "$status" -eq 0 ]
  local id
  id="$(runner_beads | jq -r '.[0].id')"
  [ "$(bd_fixture_show_json "$id" | jq -r '.labels | index("explicit-label") != null')" = true ]
  [ "$(bd_fixture_show_json "$id" | jq -r '.labels | index("terminal-label") != null')" = false ]
}

@test "a repeated red files no second bead; a newer red tuple is a comment, not a bead or a notification" {
  build_with 3
  [ "$status" -eq 0 ]
  # pg-router re-offers the same item: the stored verdict ends it, nothing is filed
  ws_build
  [ "$status" -eq 0 ]
  [ "$(bead_count)" -eq 1 ]
  [ "$(notifications)" -eq 1 ]
  # a newer tuple that is red too: the same story
  ws_commit_in "$WS/nix-alpha"
  local h2 id
  h2="$(ws_hash)"
  build_with 3
  [ "$status" -eq 0 ]
  [ "$(bead_count)" -eq 1 ]
  [ "$(notifications)" -eq 1 ]
  id="$(runner_beads | jq -r '.[0].id')"
  comments_of "$id" | grep -q "$h2"
}

@test "a later green build comments on the runner's own bead and closes it, and notifies nothing" {
  build_with 3
  local id h2
  id="$(runner_beads | jq -r '.[0].id')"
  ws_commit_in "$WS/nix-beta"
  h2="$(ws_hash)"
  build_with 0
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(ws_verdict_file "$h2")")" = green ]
  [ "$(bd_fixture_show_json "$id" | jq -r .status)" = closed ]
  comments_of "$id" | grep -q "Green workspace build for workspace@$h2"
  [ "$(bead_count)" -eq 1 ]
  [ "$(notifications)" -eq 1 ]
}

@test "a green build with no open transition files nothing" {
  build_with 0
  [ "$status" -eq 0 ]
  [ "$(bead_count)" -eq 0 ]
  [ "$(notifications)" -eq 0 ]
}

@test "with no label for the workspace or its terminal repo no bead is filed, and nothing is lost" {
  sed -i.bak '/^nix-gamma = "terminal-label"$/d' "$TEST_DIR/config.toml"
  build_with 3
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(ws_verdict_file)")" = red ]
  [ "$(bead_count)" -eq 0 ]
  [ "$(jq -r .bead "$STATE/surface/workspace.build-red.json")" = "" ]
}
