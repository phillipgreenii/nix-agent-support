#!/usr/bin/env bats
# bats file_tags=type:integration
#
# pg-rescue-resolve-conflict (docket pg2-q6a96, design D4 and D9) against the
# REAL git (a bare remote whose base branch has diverged from the PR branch),
# the REAL pg-rescue binary with fixture handlers in a test config, the REAL
# pg-rescue-push-branch and the REAL bd (an embedded throwaway database, see
# test-support/bd_fixture.bash: no dolt server, never the shared tracker). The
# one stub is a pg-rescue that exits 9 or 75, used only to prove the exit
# mapping.

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
  gfh_setup "pg-rescue-resolve-conflict"
  gfh_restore_env
  bd_fixture_setup
  bd_fixture_assert_isolated
  bd_call_log_install

  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    SHIM=("$SCRIPT_UNDER_TEST")
  else
    SHIM=(bash -euo pipefail "$SCRIPTS_DIR/pg-rescue-resolve-conflict.sh")
  fi

  REMOTE="$GFH_WORK/remote.git"
  DEV="$GFH_WORK/dev"
  REPO="$GFH_WORK/repos/widget"
  WTROOT="$GFH_WORK/worktrees"
  HANDLERS="$GFH_WORK/handlers"
  HLOG="$GFH_WORK/handler-log"
  export XDG_STATE_HOME="$GFH_WORK/state"
  STATE_DIR="$XDG_STATE_HOME/pg-rescue-resolve-conflict"
  mkdir -p "$HANDLERS" "$HLOG" "$GFH_WORK/repos"
  export PG_RESCUE_CONFIG="$GFH_WORK/pg-rescue.toml"
  write_handlers
}

teardown() {
  rm -rf "$BD_SHIM_DIR"
  gfh_teardown
}

# ---------------------------------------------------------------- fixtures

# write_handlers: the fixture handler scripts (pg-rescue design 3.2: exit 0
# resolved, 2 declined, 3 deferred, anything else failed). They log to files,
# never to stdout (handler stdout must be empty or one JSON object).
write_handlers() {
  cat >"$HANDLERS/resolve.sh" <<EOF
#!/usr/bin/env bash
set -eu
pwd -P >"$HLOG/resolve.cwd"
printf '%s\n' "\$PG_RESCUE_ITEM_ID" >"$HLOG/resolve.item"
printf '%s\n' "\$BEADS_DIR" >"$HLOG/resolve.beads-dir"
printf '%s\n' "\$BEADS_ACTOR" >"$HLOG/resolve.actor"
if [ -f .git ]; then echo linked >"$HLOG/resolve.linked"; fi
printf 'one\nresolved\nthree\n' >f
git add f
GIT_EDITOR=true git rebase --continue >&2
EOF
  cat >"$HANDLERS/give-up.sh" <<EOF
#!/usr/bin/env bash
set -eu
touch "$HLOG/give-up.ran"
bd update "\$PG_RESCUE_ITEM_ID" --add-label script-gave-up >&2
exit 3
EOF
  cat >"$HANDLERS/decline.sh" <<EOF
#!/usr/bin/env bash
touch "$HLOG/decline.ran"
exit 2
EOF
  chmod +x "$HANDLERS"/*.sh
}

# write_chain HANDLER...: a pg-rescue config whose conflict chain is the list.
write_chain() {
  local names="" h
  for h in "$@"; do
    names+="\"$h\", "
  done
  cat >"$PG_RESCUE_CONFIG" <<EOF
[handler.resolve]
command = ["$HANDLERS/resolve.sh"]
timeout = "1m"

[handler.give-up]
command = ["$HANDLERS/give-up.sh"]
timeout = "1m"

[handler.decline]
command = ["$HANDLERS/decline.sh"]
timeout = "1m"

[chain.conflict]
handlers = [${names%, }]
EOF
}

# make_pr clean|conflict: a bare remote with main and a PR branch `pr`; REPO is
# cloned BEFORE main advances, so the shim must fetch to see the diverged base.
make_pr() {
  local kind=$1
  gfh_init_bare "$REMOTE"
  gfh_clone "$REMOTE" "$DEV" "pg-rescue-resolve-conflict"
  printf 'one\ntwo\nthree\n' >"$DEV/f"
  git -C "$DEV" add f
  git -C "$DEV" commit -q -m seed
  git -C "$DEV" push -q origin main
  git -C "$DEV" checkout -q -b pr
  if [[ $kind == clean ]]; then
    echo pr >"$DEV/pr.txt"
    git -C "$DEV" add pr.txt
  else
    printf 'one\npr-two\nthree\n' >"$DEV/f"
    git -C "$DEV" add f
  fi
  git -C "$DEV" commit -q -m "pr change"
  git -C "$DEV" push -q origin pr
  git -C "$DEV" checkout -q main
  gfh_clone "$REMOTE" "$REPO" "pg-rescue-resolve-conflict"
  # The base diverges after the clone.
  if [[ $kind == clean ]]; then
    echo main >"$DEV/main.txt"
    git -C "$DEV" add main.txt
  else
    printf 'one\nmain-two\nthree\n' >"$DEV/f"
    git -C "$DEV" add f
  fi
  git -C "$DEV" commit -q -m "main change"
  git -C "$DEV" push -q origin main
  HEAD_SHA=$(git -C "$REMOTE" rev-parse refs/heads/pr)
  MAIN_SHA=$(git -C "$REMOTE" rev-parse refs/heads/main)
}

# new_item: create the work item in the throwaway database, print its id.
new_item() {
  bd -C "$TDIR" create --title "resolve-conflict: acme/widget#7" --label worker-ready --silent 2>/dev/null
}

# run_shim: the shim with the full valid command line; SHIM_ITEM, SHIM_REPO,
# SHIM_BRANCH and SHIM_HEAD override the item id, --repo, --branch and
# --head-sha.
run_shim() {
  run --separate-stderr "${SHIM[@]}" \
    --item-id "${SHIM_ITEM:-$ITEM}" --repo "${SHIM_REPO:-acme/widget}" \
    --branch "${SHIM_BRANCH:-pr}" --base main --head-sha "${SHIM_HEAD:-$HEAD_SHA}" \
    --repo-dir-map "widget=$REPO" --worktree-root "$WTROOT" --tracker-dir "$TDIR"
}

item_field() { bd_fixture_show_json "$ITEM" | jq -r "$1"; }
remote_ref() { git -C "$REMOTE" rev-parse --verify -q "refs/heads/$1" || echo none; }

outcome_file() {
  local -a files=("$STATE_DIR"/*.outcome)
  [ "${#files[@]}" -eq 1 ] && [ -f "${files[0]}" ]
  printf '%s\n' "${files[0]}"
}

assert_no_worktree() {
  if [ -d "$WTROOT" ]; then
    [ -z "$(ls -A "$WTROOT")" ]
  fi
  [ "$(git -C "$REPO" worktree list | wc -l | tr -d ' ')" -eq 1 ]
}

# assert_released: open, unassigned (the item is claimable again).
assert_released() {
  [ "$(item_field '.status')" = "open" ]
  [ -z "$(item_field '.assignee // ""')" ]
}

assert_pushed_nothing() {
  [ "$(remote_ref pr)" = "$HEAD_SHA" ]
  [ "$(remote_ref main)" = "$MAIN_SHA" ]
}

assert_resolved() {
  [ "$status" -eq 0 ]
  [ "$(item_field '.status')" = "closed" ]
  [ "$(remote_ref main)" = "$MAIN_SHA" ]
  [ "$(remote_ref pr)" != "$HEAD_SHA" ]
  git -C "$REMOTE" merge-base --is-ancestor main pr
  assert_no_worktree
  [ "$(awk '{print $2}' "$(outcome_file)")" = "resolved" ]
  [[ "$(item_field '.close_reason // ""')" == *"pg-rescue run "* ]]
}

# --------------------------------------------------------------------- tests

@test "isolation guard: the fixture database is the temp embedded one" {
  bd_fixture_assert_isolated
  [ "$PWD" = "$TDIR" ]
}

@test "clean rebase: pushes, closes the item, leaves no worktree" {
  make_pr clean
  ITEM=$(new_item)
  write_chain give-up
  run_shim
  assert_resolved
  [ ! -e "$HLOG/give-up.ran" ]
  # The outcome record is "<run-id> <outcome> <item id>".
  [[ "$(cat "$(outcome_file)")" =~ ^[0-9]{8}T[0-9]{6}Z-[0-9]+\ resolved\ $ITEM$ ]]
}

@test "conflicting rebase resolved by a handler: pushes, closes, no worktree; handler sees the item id and the worktree" {
  make_pr conflict
  ITEM=$(new_item)
  write_chain resolve give-up
  run_shim
  assert_resolved
  [ ! -e "$HLOG/give-up.ran" ]
  # The handler ran with PG_RESCUE_ITEM_ID, in the linked worktree (the -C
  # directory), with the tracker and actor of the shim.
  [ "$(cat "$HLOG/resolve.item")" = "$ITEM" ]
  [ -f "$HLOG/resolve.linked" ]
  wtroot_real=$(cd "$WTROOT" && pwd -P)
  [[ "$(cat "$HLOG/resolve.cwd")" == "$wtroot_real"/widget-* ]]
  [ "$(cat "$HLOG/resolve.beads-dir")" = "$TDIR/.beads" ]
  [[ "$(cat "$HLOG/resolve.actor")" == pg-rescue/* ]]
  # The pushed branch carries the handler's resolution on top of main.
  git -C "$REMOTE" show pr:f | grep -qx resolved
}

@test "every bd call of the run carries the run's actor, and the claim is bd update --claim" {
  make_pr clean
  ITEM=$(new_item)
  write_chain give-up
  : >"$BD_CALL_LOG"
  run_shim
  # Snapshot the shim's calls before the assertions below make their own.
  calls=$(cat "$BD_CALL_LOG")
  assert_resolved
  [ "$(grep -c -- "^update $ITEM --claim --actor pg-rescue/" <<<"$calls")" = "1" ]
  [ "$(grep -c -- "^close $ITEM --actor pg-rescue/.* --reason pg-rescue run " <<<"$calls")" = "1" ]
  # No call of a successful run lacks the actor.
  [ -z "$(grep -v -- "--actor pg-rescue/" <<<"$calls" || true)" ]
}

@test "the chain gives up (fixture give-up adds the marker): exit 0, nothing pushed, item open, unclaimed, marked" {
  make_pr conflict
  ITEM=$(new_item)
  write_chain give-up
  run_shim
  [ "$status" -eq 0 ]
  [ -e "$HLOG/give-up.ran" ]
  assert_released
  bd_fixture_show_json "$ITEM" | jq -e '.labels | index("script-gave-up")' >/dev/null
  assert_pushed_nothing
  assert_no_worktree
  [ "$(awk '{print $2}' "$(outcome_file)")" = "gave-up" ]
}

@test "a rejected push lease exits 0, sets no marker, releases the claim, pushes nothing" {
  make_pr clean
  ITEM=$(new_item)
  write_chain give-up
  SHIM_HEAD=1111111111111111111111111111111111111111 run_shim
  [ "$status" -eq 0 ]
  assert_released
  bd_fixture_show_json "$ITEM" | jq -e '(.labels // []) | index("script-gave-up") | not' >/dev/null
  assert_pushed_nothing
  assert_no_worktree
  [ "$(awk '{print $2}' "$(outcome_file)")" = "lease-rejected" ]
}

@test "every handler declines a conflict: exit 1, claim released, nothing pushed" {
  make_pr conflict
  ITEM=$(new_item)
  write_chain decline
  run_shim
  [ "$status" -eq 1 ]
  [ -e "$HLOG/decline.ran" ]
  assert_released
  assert_pushed_nothing
  assert_no_worktree
  [ "$(awk '{print $2}' "$(outcome_file)")" = "failed" ]
}

@test "a config with no conflict chain (pg-rescue exit 70) is a failure: exit 1, claim released" {
  make_pr clean
  ITEM=$(new_item)
  cat >"$PG_RESCUE_CONFIG" <<EOF
[handler.decline]
command = ["$HANDLERS/decline.sh"]

[chain.other]
handlers = ["decline"]
EOF
  run_shim
  [ "$status" -eq 1 ]
  assert_released
  assert_pushed_nothing
  assert_no_worktree
}

@test "a fetch failure (no such branch on the remote): exit 1, claim released, no worktree" {
  make_pr clean
  ITEM=$(new_item)
  write_chain give-up
  SHIM_BRANCH=no-such-branch run_shim
  [ "$status" -eq 1 ]
  assert_released
  assert_pushed_nothing
  assert_no_worktree
  [ "$(awk '{print $2}' "$(outcome_file)")" = "failed" ]
}

# A pg-rescue stub first on PATH, exiting with the code in its name: the one
# test per exit code that proves the exit mapping (every other test runs the
# real binary).
stub_pg_rescue() { # EXIT_CODE
  STUB_DIR="$GFH_WORK/stub-bin"
  STUB_LOG="$GFH_WORK/stub-pg-rescue.args"
  mkdir -p "$STUB_DIR"
  cat >"$STUB_DIR/pg-rescue" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >"$STUB_LOG"
exit $1
EOF
  chmod +x "$STUB_DIR/pg-rescue"
  PATH="$STUB_DIR:$PATH"
  export PATH
}

@test "a passed-through pg-rescue exit 9 is remapped to 1; the claim is released and nothing is pushed" {
  make_pr clean
  ITEM=$(new_item)
  stub_pg_rescue 9
  run_shim
  [ "$status" -eq 1 ]
  assert_released
  assert_pushed_nothing
  assert_no_worktree
  [ "$(awk '{print $2}' "$(outcome_file)")" = "failed" ]
}

@test "pg-rescue exit 75 maps to 0; the claim is released, nothing is pushed, no marker is added by the shim" {
  make_pr clean
  ITEM=$(new_item)
  stub_pg_rescue 75
  run_shim
  [ "$status" -eq 0 ]
  assert_released
  bd_fixture_show_json "$ITEM" | jq -e '(.labels // []) | index("script-gave-up") | not' >/dev/null
  assert_pushed_nothing
  assert_no_worktree
  [ "$(awk '{print $2}' "$(outcome_file)")" = "gave-up" ]
}

@test "pg-rescue exit 2 is a failure: the shim exits 1" {
  make_pr clean
  ITEM=$(new_item)
  stub_pg_rescue 2
  run_shim
  [ "$status" -eq 1 ]
  assert_released
}

@test "the pg-rescue invocation carries --chain conflict, -C <worktree>, the verify, the result file and the rebase argv" {
  make_pr clean
  ITEM=$(new_item)
  stub_pg_rescue 75
  run_shim
  [ "$status" -eq 0 ]
  args=$(cat "$STUB_LOG")
  [[ $args == "--chain conflict -C $WTROOT/widget-"* ]]
  [[ $args == *"--context resolve-conflict repo=acme/widget branch=pr item=$ITEM"* ]]
  [[ $args == *"--verify "*"git merge-base --is-ancestor"* ]]
  [[ $args == *"--result-file $STATE_DIR/"*".result.json -- git rebase origin/main" ]]
}

@test "the real run writes pg-rescue's result file next to the outcome record" {
  make_pr clean
  ITEM=$(new_item)
  write_chain give-up
  run_shim
  assert_resolved
  [ -s "$(printf '%s\n' "$STATE_DIR"/*.result.json | head -1)" ]
}

@test "a claim lost to a peer exits 0 and leaves the item untouched" {
  make_pr conflict
  ITEM=$(new_item)
  bd -C "$TDIR" update "$ITEM" --assignee other-actor --status in_progress --actor other-actor >/dev/null 2>&1
  before=$(bd_fixture_show_json "$ITEM")
  [ "$(jq -r .assignee <<<"$before")" = "other-actor" ]
  write_chain resolve give-up
  run_shim
  [ "$status" -eq 0 ]
  [ "$(bd_fixture_show_json "$ITEM")" = "$before" ]
  assert_pushed_nothing
  [ ! -e "$HLOG/resolve.item" ]
  [ ! -d "$WTROOT" ] || [ -z "$(ls -A "$WTROOT")" ]
  [ "$(awk '{print $2}' "$(outcome_file)")" = "peer-held" ]
}

@test "a claim on a missing item is a failure: exit 1" {
  make_pr clean
  ITEM=$(new_item)
  write_chain give-up
  SHIM_ITEM=tst-nosuchitem run_shim
  [ "$status" -eq 1 ]
  assert_pushed_nothing
  assert_no_worktree
}

@test "a repository with no repo-dir-map entry is a usage error: exit 2, bd untouched" {
  make_pr clean
  ITEM=$(new_item)
  : >"$BD_CALL_LOG"
  SHIM_REPO=acme/unmapped run_shim
  [ "$status" -eq 2 ]
  [[ $stderr == *"no --repo-dir-map entry for repository unmapped"* ]]
  [ ! -s "$BD_CALL_LOG" ]
  [ "$(item_field '.status')" = "open" ]
  [ -z "$(item_field '.assignee // ""')" ]
  [ ! -e "$STATE_DIR" ] || [ -z "$(ls -A "$STATE_DIR")" ]
}

@test "usage errors exit 2 before bd is touched" {
  make_pr clean
  ITEM=$(new_item)
  : >"$BD_CALL_LOG"
  run --separate-stderr "${SHIM[@]}" --bogus
  [ "$status" -eq 2 ]
  run --separate-stderr "${SHIM[@]}" --item-id "$ITEM"
  [ "$status" -eq 2 ]
  # A bad sha, a relative worktree root and a tracker without .beads.
  SHIM_HEAD=abc run_shim
  [ "$status" -eq 2 ]
  run --separate-stderr "${SHIM[@]}" --item-id "$ITEM" --repo acme/widget --branch pr --base main \
    --head-sha "$HEAD_SHA" --repo-dir-map "widget=$REPO" --worktree-root relative/dir --tracker-dir "$TDIR"
  [ "$status" -eq 2 ]
  run --separate-stderr "${SHIM[@]}" --item-id "$ITEM" --repo acme/widget --branch pr --base main \
    --head-sha "$HEAD_SHA" --repo-dir-map "widget=$REPO" --worktree-root "$WTROOT" --tracker-dir "$GFH_WORK"
  [ "$status" -eq 2 ]
  [ ! -s "$BD_CALL_LOG" ]
}

@test "--help prints usage on stdout and exits 0" {
  run "${SHIM[@]}" --help
  [ "$status" -eq 0 ]
  [[ $output == *"Usage: pg-rescue-resolve-conflict"* ]]
}
