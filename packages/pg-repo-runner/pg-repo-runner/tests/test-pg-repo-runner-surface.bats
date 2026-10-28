#!/usr/bin/env bats
# bats file_tags=type:integration
#
# Failure surfacing of the async runners (bead pg2-loswfh, F3): one bead and one
# notification per TRANSITION, never per steady state; the runner closes only its
# own beads, and only on a green descendant of the red tip.
#
# Real everything except two seams: git (real clones of bare local remotes under a
# real pn-workspace.toml), bd (the REAL bd over an embedded throwaway database, see
# test-support/bd_fixture.bash: no dolt server, never the shared tracker), the real
# script run as a subprocess. The stand-ins are nix (a recorder whose exit status the
# test sets: what is pinned is what the runner does with the verdict) and the macOS
# notifier (a recording script on PG_REPO_RUNNER_OSASCRIPT_BIN: the only stub of the
# surfacing path). Nothing here pushes, and every repo in the fixture config has
# push = false.

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
  if [[ -z ${GFH_LIB:-} ]]; then
    echo "GFH_LIB is not set: run under the workspace .envrc (direnv) or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # shellcheck disable=SC1090,SC1091
  source "$GFH_LIB"
  gfh_save_env SCRIPTS_DIR SCRIPT_UNDER_TEST TEST_SUPPORT GFH_LIB TDIR
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
  WS="$TEST_DIR/ws"
  mkdir -p "$WS" "$TEST_DIR/remotes"
  make_repo "$WS/nix-alpha" alpha
  make_repo "$WS/nix-beta" beta
  write_workspace nix-alpha nix-beta
  write_config "$TDIR"
  ALPHA_SHA="$(git -C "$WS/nix-alpha" rev-parse HEAD)"
  REMOTE_ALPHA_SHA="$(git -C "$TEST_DIR/remotes/alpha.git" rev-parse refs/heads/main)"

  export XDG_STATE_HOME="$TEST_DIR/state"
  export PG_REPO_RUNNER_CONFIG="$TEST_DIR/config.toml"
  export PG_REPO_RUNNER_LOADAVG=0.50
  STATE="$XDG_STATE_HOME/pg-repo-runner"
  make_nix_shim
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

# make_repo DIR NAME: a clone of a fresh bare remote, with one commit pushed.
make_repo() {
  local dir="$1" name="$2"
  gfh_init_bare "$TEST_DIR/remotes/$name.git"
  gfh_clone "$TEST_DIR/remotes/$name.git" "$dir" "pg-repo-runner"
  echo "$name" >"$dir/f"
  git -C "$dir" add f
  git -C "$dir" commit -q -m "seed $name"
  git -C "$dir" push -q origin main
}

# commit_in DIR: a new local commit on main (never pushed); prints the new sha.
commit_in() {
  echo "$RANDOM$RANDOM" >>"$1/f"
  git -C "$1" add f
  git -C "$1" commit -q -m "change"
  git -C "$1" rev-parse HEAD
}

write_workspace() {
  local k
  {
    printf "[workspace]\nid = 'fixture'\n\n[repos]\n"
    for k in "$@"; do
      printf "[repos.%s]\nurl = 'x'\nbranch = 'main'\n" "$k"
    done
  } >"$WS/pn-workspace.toml"
}

# write_config TRACKER [LABELS]: verify is on for both repos; push for NONE.
# LABELS is "full" (a label per repo, the default), "none" or "off" (no [surface]).
write_config() {
  local tracker="$1" labels="${2:-full}"
  {
    cat <<EOF
workspace_root = "$WS"
kill_switch = "$TEST_DIR/config-kill"

[thresholds]
max_load = 11.0
nice_level = 10

[repos.nix-alpha]
verify = true
sync = false
push = false

[repos.nix-beta]
verify = true
sync = false
push = false
EOF
    if [[ $labels != off ]]; then
      printf '\n[surface]\nenabled = true\ntracker_dir = "%s"\nnotify = true\n' "$tracker"
      printf '\n[surface.repo_labels]\n'
      printf 'nix-alpha = "agent-support"\n'
      [[ $labels == none ]] || printf 'nix-beta = "base"\n'
    fi
  } >"$TEST_DIR/config.toml"
}

# make_nix_shim: stands in for nix; ends with the status in $TEST_DIR/nix.exit.
make_nix_shim() {
  echo 0 >"$TEST_DIR/nix.exit"
  cat >"$TEST_DIR/nix-shim" <<EOF
#!/usr/bin/env bash
echo "\$*" >>"$TEST_DIR/nix.argv"
exit "\$(cat "$TEST_DIR/nix.exit")"
EOF
  chmod +x "$TEST_DIR/nix-shim"
  export PG_REPO_RUNNER_NIX_BIN="$TEST_DIR/nix-shim"
}

# make_notifier: the recording stand-in for osascript. One line per call: the
# arguments of the title and body (items 1 and 2 of the script's argv). It ends
# with the status in $TEST_DIR/notify.exit.
make_notifier() {
  echo 0 >"$TEST_DIR/notify.exit"
  cat >"$TEST_DIR/osascript-stub" <<EOF
#!/usr/bin/env bash
n=\$#
[[ \$(cat "$TEST_DIR/notify.exit") -eq 0 ]] || exit 1
printf '%s | %s\n' "\${@:\$((n-1)):1}" "\${@:\$n:1}" >>"$TEST_DIR/notify.log"
EOF
  chmod +x "$TEST_DIR/osascript-stub"
  export PG_REPO_RUNNER_OSASCRIPT_BIN="$TEST_DIR/osascript-stub"
}

notifications() {
  if [[ -f $TEST_DIR/notify.log ]]; then wc -l <"$TEST_DIR/notify.log" | tr -d ' '; else echo 0; fi
}

# verify REPO SHA EXIT: one verify run whose check ends with EXIT.
verify() {
  echo "$3" >"$TEST_DIR/nix.exit"
  PG_ROUTER_ITEM_ID="$1@$2" run "${RUNNER[@]}" verify
}

# runner_beads REPO: every bead the runner owns for REPO (any status), as a JSON
# array, whatever envelope this bd emits.
runner_beads() {
  bd -C "$TDIR" list --metadata-field runner=pg-repo-runner --metadata-field "repo=$1" \
    --status all -n 0 --json |
    jq -c --argjson base "$BASE_IDS" \
      '(if type=="object" and has("data") then .data else . end) | map(select(.id as $i | $base | index($i) | not))'
}

bead_count() { runner_beads "$1" | jq 'length'; }

# comments_of ID: the bead's comments, one text per line.
comments_of() {
  bd -C "$TDIR" comments "$1" --json |
    jq -r '(if type=="object" and has("data") then .data else . end) | .[].text // .[].body // empty'
}

record() { # REPO KIND
  cat "$STATE/surface/$1.$2.json"
}

# the repos are never moved, edited or published by anything surfacing does
assert_repos_untouched() {
  [ "$(git -C "$TEST_DIR/remotes/alpha.git" rev-parse refs/heads/main)" = "$REMOTE_ALPHA_SHA" ]
  [ -z "$(git -C "$WS/nix-alpha" status --porcelain)" ]
  [ -z "$(git -C "$WS/nix-beta" status --porcelain)" ]
}

# ---- green to red ----------------------------------------------------------

@test "green to red files exactly one P1 bead with the repo label and the runner metadata" {
  local before
  before="$(bd_fixture_count)"
  verify nix-alpha "$ALPHA_SHA" 1
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$STATE/verdicts/nix-alpha/$ALPHA_SHA.check.json")" = red ]
  [ "$(bd_fixture_count)" -eq $((before + 1)) ]
  [ "$(bead_count nix-alpha)" -eq 1 ]
  local b
  b="$(runner_beads nix-alpha | jq -c '.[0]')"
  [ "$(jq -r .priority <<<"$b")" = 1 ]
  [ "$(jq -r .status <<<"$b")" = open ]
  [ "$(jq -r .issue_type <<<"$b")" = task ]
  [ "$(jq -r '.metadata.runner' <<<"$b")" = pg-repo-runner ]
  [ "$(jq -r '.metadata.repo' <<<"$b")" = nix-alpha ]
  [ "$(jq -r '.metadata.kind' <<<"$b")" = red ]
  [ "$(jq -r '.metadata.sha' <<<"$b")" = "$ALPHA_SHA" ]
  [ "$(bd_fixture_show_json "$(jq -r .id <<<"$b")" | jq -r '.labels | index("agent-support") != null')" = true ]
  [ "$(notifications)" -eq 1 ]
  grep -q "nix-alpha" "$TEST_DIR/notify.log"
  [ "$(record nix-alpha red | jq -r .bead)" = "$(jq -r .id <<<"$b")" ]
  assert_repos_untouched
}

@test "the bead of another repo carries that repo's label" {
  local sha
  sha="$(git -C "$WS/nix-beta" rev-parse HEAD)"
  verify nix-beta "$sha" 1
  [ "$status" -eq 0 ]
  [ "$(bead_count nix-beta)" -eq 1 ]
  local id
  id="$(runner_beads nix-beta | jq -r '.[0].id')"
  [ "$(bd_fixture_show_json "$id" | jq -r '.labels | index("base") != null')" = true ]
  [ "$(bd_fixture_show_json "$id" | jq -r '.labels | index("agent-support") != null')" = false ]
}

@test "a repeated red files no second bead and no second notification" {
  verify nix-alpha "$ALPHA_SHA" 1
  [ "$status" -eq 0 ]
  # the same verdict again (pg-router re-offers the item): nothing runs, nothing is filed
  verify nix-alpha "$ALPHA_SHA" 1
  [ "$status" -eq 0 ]
  [ "$(bead_count nix-alpha)" -eq 1 ]
  [ "$(notifications)" -eq 1 ]
  # a NEWER red tip is the same story: a comment, not a bead and not a notification
  local sha2 id
  sha2="$(commit_in "$WS/nix-alpha")"
  verify nix-alpha "$sha2" 1
  [ "$status" -eq 0 ]
  [ "$(bead_count nix-alpha)" -eq 1 ]
  [ "$(notifications)" -eq 1 ]
  id="$(runner_beads nix-alpha | jq -r '.[0].id')"
  comments_of "$id" | grep -q "$sha2"
  [ "$(record nix-alpha red | jq -r .lastSha)" = "$sha2" ]
  assert_repos_untouched
}

# ---- green again -----------------------------------------------------------

@test "a later green descendant comments on the runner's own bead and closes it, and notifies nothing" {
  verify nix-alpha "$ALPHA_SHA" 1
  local id sha2
  id="$(runner_beads nix-alpha | jq -r '.[0].id')"
  sha2="$(commit_in "$WS/nix-alpha")"
  verify nix-alpha "$sha2" 0
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$STATE/verdicts/nix-alpha/$sha2.check.json")" = green ]
  [ "$(bd_fixture_show_json "$id" | jq -r .status)" = closed ]
  comments_of "$id" | grep -q "Green verdict for nix-alpha@$sha2"
  [ "$(bead_count nix-alpha)" -eq 1 ]
  [ "$(record nix-alpha red | jq -r .state)" = clear ]
  [ "$(notifications)" -eq 1 ]
  assert_repos_untouched
}

@test "after the recovery a new red is a new transition: a second bead and a second notification" {
  verify nix-alpha "$ALPHA_SHA" 1
  local sha2 sha3
  sha2="$(commit_in "$WS/nix-alpha")"
  verify nix-alpha "$sha2" 0
  sha3="$(commit_in "$WS/nix-alpha")"
  verify nix-alpha "$sha3" 1
  [ "$status" -eq 0 ]
  [ "$(bead_count nix-alpha)" -eq 2 ]
  [ "$(runner_beads nix-alpha | jq '[.[] | select(.status == "open")] | length')" -eq 1 ]
  [ "$(notifications)" -eq 2 ]
}

@test "the runner never closes a bead that is not its own" {
  verify nix-alpha "$ALPHA_SHA" 1
  # a bead somebody else filed, which the transition record is made to point at
  local foreign sha2 rec
  foreign="$(bd -C "$TDIR" create "someone else's bead" --type task --labels agent-support --silent)"
  rec="$(record nix-alpha red | jq -c --arg b "$foreign" '.bead = $b')"
  printf '%s\n' "$rec" >"$STATE/surface/nix-alpha.red.json"
  sha2="$(commit_in "$WS/nix-alpha")"
  verify nix-alpha "$sha2" 0
  [ "$status" -eq 0 ]
  [ "$(bd_fixture_show_json "$foreign" | jq -r .status)" = open ]
  [ -z "$(comments_of "$foreign")" ]
  [ "$(record nix-alpha red | jq -r .state)" = clear ]
  # and the runner's own bead was not touched by the pointer swap either
  [ "$(runner_beads nix-alpha | jq -r '.[0].status')" = open ]
}

@test "a green that does not descend from the red tip leaves the bead open" {
  verify nix-alpha "$ALPHA_SHA" 1
  local id unrelated sha2 rec
  id="$(runner_beads nix-alpha | jq -r '.[0].id')"
  # a commit with no ancestry in common with main
  unrelated="$(git -C "$WS/nix-alpha" commit-tree "$(git -C "$WS/nix-alpha" hash-object -t tree /dev/null)" -m unrelated)"
  rec="$(record nix-alpha red | jq -c --arg s "$unrelated" '.lastSha = $s')"
  printf '%s\n' "$rec" >"$STATE/surface/nix-alpha.red.json"
  sha2="$(commit_in "$WS/nix-alpha")"
  verify nix-alpha "$sha2" 0
  [ "$status" -eq 0 ]
  [ "$(bd_fixture_show_json "$id" | jq -r .status)" = open ]
  [ "$(record nix-alpha red | jq -r .state)" = open ]
}

# ---- diverged and the other transitions (the library, as runner A will call it) ----

# lib CODE: runs CODE with the kernel library loaded against the fixture config.
lib() {
  RR_OSASCRIPT_BIN="$PG_REPO_RUNNER_OSASCRIPT_BIN" run bash -c 'source "$1/pg-repo-runner.bash" && rr_init_state && rr_load_config && '"$1" _ "$SCRIPTS_DIR"
}

@test "diverged files one deduplicated bead and one notification, and resolving closes it" {
  local before
  before="$(bd_fixture_count)"
  lib 'rr_surface_raise nix-beta diverged '"$ALPHA_SHA"' "ahead 1, behind 1"'
  [ "$status" -eq 0 ]
  lib 'rr_surface_raise nix-beta diverged '"$ALPHA_SHA"' "ahead 1, behind 1"'
  [ "$status" -eq 0 ]
  lib 'rr_surface_raise nix-beta diverged '"$ALPHA_SHA"' "ahead 2, behind 1"'
  [ "$status" -eq 0 ]
  [ "$(bd_fixture_count)" -eq $((before + 1)) ]
  [ "$(bead_count nix-beta)" -eq 1 ]
  local id
  id="$(runner_beads nix-beta | jq -r '.[0].id')"
  [ "$(runner_beads nix-beta | jq -r '.[0].metadata.kind')" = diverged ]
  [ "$(bd_fixture_show_json "$id" | jq -r '.labels | index("base") != null')" = true ]
  [ "$(notifications)" -eq 1 ]
  lib 'rr_surface_resolve nix-beta diverged '"$ALPHA_SHA"' "fast-forwarded: no longer diverged"'
  [ "$status" -eq 0 ]
  [ "$(bd_fixture_show_json "$id" | jq -r .status)" = closed ]
  comments_of "$id" | grep -q "no longer diverged"
}

@test "a bead the runner filed before its id was stored is adopted, not duplicated" {
  lib 'rr_surface_raise nix-beta needs-relock '"$ALPHA_SHA"
  [ "$status" -eq 0 ]
  # the crash window: the bead exists, the record never learned its id
  local rec
  rec="$(record nix-beta needs-relock | jq -c '.bead = "" | .notified = true')"
  printf '%s\n' "$rec" >"$STATE/surface/nix-beta.needs-relock.json"
  lib 'rr_surface_flush nix-beta needs-relock'
  [ "$status" -eq 0 ]
  [ "$(bead_count nix-beta)" -eq 1 ]
  [ "$(record nix-beta needs-relock | jq -r .bead)" = "$(runner_beads nix-beta | jq -r '.[0].id')" ]
  [ "$(notifications)" -eq 1 ]
}

@test "a bad kind is refused and files nothing" {
  local before
  before="$(bd_fixture_count)"
  lib 'rr_surface_raise nix-beta explosion '"$ALPHA_SHA"
  [ "$status" -eq 2 ]
  [ "$(bd_fixture_count)" -eq "$before" ]
  [ "$(notifications)" -eq 0 ]
}

# ---- failures stay owed ------------------------------------------------------

@test "an unreachable tracker keeps the bead pending, notifies once, and files nowhere else" {
  local before
  before="$(bd_fixture_count)"
  write_config "$TEST_DIR/no-such-tracker"
  verify nix-alpha "$ALPHA_SHA" 1
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$STATE/verdicts/nix-alpha/$ALPHA_SHA.check.json")" = red ]
  [ "$(bd_fixture_count)" -eq "$before" ]
  [ "$(record nix-alpha red | jq -r .bead)" = "" ]
  [ "$(notifications)" -eq 1 ]
  run "${RUNNER[@]}" status
  [[ $output == *bead-pending* ]]
  # the tracker comes back: the next pass files the bead, and notifies nothing again
  write_config "$TDIR"
  local sha2
  sha2="$(commit_in "$WS/nix-alpha")"
  verify nix-alpha "$sha2" 1
  [ "$status" -eq 0 ]
  [ "$(bead_count nix-alpha)" -eq 1 ]
  [ "$(notifications)" -eq 1 ]
  [ "$(record nix-alpha red | jq -r .bead)" != "" ]
}

@test "a repo with no label is never filed label-less" {
  local before sha
  before="$(bd_fixture_count)"
  write_config "$TDIR" none
  sha="$(git -C "$WS/nix-beta" rev-parse HEAD)"
  verify nix-beta "$sha" 1
  [ "$status" -eq 0 ]
  [ "$(bd_fixture_count)" -eq "$before" ]
  [ "$(notifications)" -eq 1 ]
  [ "$(record nix-beta red | jq -r .bead)" = "" ]
}

@test "a failed notification stays owed and is delivered exactly once on a later pass" {
  echo 1 >"$TEST_DIR/notify.exit"
  verify nix-alpha "$ALPHA_SHA" 1
  [ "$status" -eq 0 ]
  [ "$(bead_count nix-alpha)" -eq 1 ]
  [ "$(notifications)" -eq 0 ]
  [ "$(record nix-alpha red | jq -r .notified)" = false ]
  echo 0 >"$TEST_DIR/notify.exit"
  lib 'rr_surface_flush_repo nix-alpha'
  [ "$status" -eq 0 ]
  lib 'rr_surface_flush_repo nix-alpha'
  [ "$status" -eq 0 ]
  [ "$(notifications)" -eq 1 ]
  [ "$(bead_count nix-alpha)" -eq 1 ]
}

@test "a close that could not be delivered stays pending and completes on a later pass" {
  verify nix-alpha "$ALPHA_SHA" 1
  local id sha2
  id="$(runner_beads nix-alpha | jq -r '.[0].id')"
  write_config "$TEST_DIR/no-such-tracker"
  sha2="$(commit_in "$WS/nix-alpha")"
  verify nix-alpha "$sha2" 0
  [ "$status" -eq 0 ]
  [ "$(record nix-alpha red | jq -r .state)" = open ]
  [ "$(bd_fixture_show_json "$id" | jq -r .status)" = open ]
  write_config "$TDIR"
  lib 'rr_surface_flush_repo nix-alpha'
  [ "$status" -eq 0 ]
  [ "$(bd_fixture_show_json "$id" | jq -r .status)" = closed ]
  [ "$(record nix-alpha red | jq -r .state)" = clear ]
}

# ---- defaults and the no-edit, no-push guarantee -----------------------------

@test "without a [surface] table nothing is filed, notified or recorded" {
  local before
  before="$(bd_fixture_count)"
  write_config "$TDIR" off
  verify nix-alpha "$ALPHA_SHA" 1
  [ "$status" -eq 0 ]
  [ "$(bd_fixture_count)" -eq "$before" ]
  [ "$(notifications)" -eq 0 ]
  [ ! -e "$STATE/surface" ]
}

@test "status lists an open transition and no repo is enabled for push" {
  verify nix-alpha "$ALPHA_SHA" 1
  run "${RUNNER[@]}" status
  [ "$status" -eq 0 ]
  [[ $output == *"open transitions:"* ]]
  [[ $output == *"nix-alpha"*"red"* ]]
  [ "$(tq -o json -f "$TEST_DIR/config.toml" . | jq '[.repos[] | select(.push == true)] | length')" -eq 0 ]
}

@test "the runner contains no code path that pushes, resets, reverts or edits a repo" {
  # git is only ever read (rev-parse, merge-base); no command line in the runner
  # pushes, resets, reverts, checks out, commits, rebases or cleans
  [ -z "$(grep -nE '^\s*(rr_git|git)\b.* (push|reset|revert|checkout|commit|rebase|cherry-pick|stash|clean)( |$)' "$SCRIPTS_DIR/pg-repo-runner.bash" "$SCRIPTS_DIR/pg-repo-runner.sh")" ]
}
