#!/usr/bin/env bats
# bats file_tags=type:integration
#
# pg-rescue-resolve-conflict (docket pg2-q6a96, design D4 and D9) against the
# REAL git (a bare remote whose base branch has diverged from the PR branch),
# the REAL pg-rescue binary with fixture handlers in a test config, the REAL
# pg-rescue-push-branch and the REAL bd (an embedded throwaway database, see
# test-support/bd_fixture.bash: no dolt server, never the shared tracker). The
# one stub is a pg-rescue that exits 9 or 75, used only to prove the exit
# mapping. Revision 2 (design D4, D5, D9): a stale head and a rejected push
# lease close the item, handlers cannot push, PUSH_AGENT_RESOLVED, the sweep,
# the failure cap and the run log.

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
printf '%s\n' "\$PG_ROUTER_ITEM_ID" >"$HLOG/resolve.item"
printf '%s\n' "\${PG_RESCUE_ITEM_ID-unset}" >"$HLOG/resolve.old-item-var"
printf '%s\n' "\$BEADS_DIR" >"$HLOG/resolve.beads-dir"
printf '%s\n' "\$BEADS_ACTOR" >"$HLOG/resolve.actor"
if [ -f .git ]; then echo linked >"$HLOG/resolve.linked"; fi
printf 'one\nresolved\nthree\n' >f
git add f
GIT_EDITOR=true git rebase --continue >&2
EOF
  # Same resolution; the config tags one instance agent and one deterministic.
  cp "$HANDLERS/resolve.sh" "$HANDLERS/agent.sh"
  cp "$HANDLERS/resolve.sh" "$HANDLERS/det.sh"
  cat >"$HANDLERS/give-up.sh" <<EOF
#!/usr/bin/env bash
set -eu
touch "$HLOG/give-up.ran"
printf '%s\n' "\$PG_ROUTER_ITEM_ID" >"$HLOG/give-up.item"
bd update "\$PG_ROUTER_ITEM_ID" --add-label script-gave-up >&2
exit 3
EOF
  cat >"$HANDLERS/decline.sh" <<EOF
#!/usr/bin/env bash
touch "$HLOG/decline.ran"
exit 2
EOF
  # A handler that tries to publish by itself (design D5): it must fail.
  cat >"$HANDLERS/push.sh" <<EOF
#!/usr/bin/env bash
touch "$HLOG/push.ran"
rc=0
git push origin HEAD:refs/heads/handler-pushed >"$HLOG/push.out" 2>&1 || rc=\$?
printf '%s\n' "\$rc" >"$HLOG/push.rc"
exit 2
EOF
  # A handler that is still running when the test signals the shim.
  cat >"$HANDLERS/slow.sh" <<EOF
#!/usr/bin/env bash
touch "$HLOG/slow.started"
exec sleep 60
EOF
  # Resolves the conflict, then moves the remote branch from a second clone
  # (without the handler's unusable push URL): the push lease is then stale.
  cat >"$HANDLERS/mover.sh" <<EOF
#!/usr/bin/env bash
set -eu
printf 'one\nresolved\nthree\n' >f
git add f
GIT_EDITOR=true git rebase --continue >&2
(
  unset GIT_CONFIG_COUNT GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0
  git -C "$DEV" checkout -q pr
  echo moved >"$DEV/moved.txt"
  git -C "$DEV" add moved.txt
  git -C "$DEV" commit -q -m moved
  git -C "$DEV" push -q origin pr
  git -C "$DEV" checkout -q main
) >&2
EOF
  chmod +x "$HANDLERS"/*.sh
}

# write_chain HANDLER...: a pg-rescue config whose conflict chain is the list;
# the give-up chain (the failure cap, the agent-resolved switch) is the give-up
# handler alone. The agent instance carries the tag agent, det the tag
# deterministic, as the module's instances do.
write_chain() {
  local names="" h
  for h in "$@"; do
    names+="\"$h\", "
  done
  cat >"$PG_RESCUE_CONFIG" <<EOF
[handler.resolve]
command = ["$HANDLERS/resolve.sh"]
timeout = "1m"

[handler.agent]
command = ["$HANDLERS/agent.sh"]
timeout = "1m"
tags = ["agent"]

[handler.det]
command = ["$HANDLERS/det.sh"]
timeout = "1m"
tags = ["deterministic"]

[handler.give-up]
command = ["$HANDLERS/give-up.sh"]
timeout = "1m"

[handler.decline]
command = ["$HANDLERS/decline.sh"]
timeout = "1m"

[handler.push]
command = ["$HANDLERS/push.sh"]
timeout = "1m"

[handler.slow]
command = ["$HANDLERS/slow.sh"]
timeout = "1m"

[handler.mover]
command = ["$HANDLERS/mover.sh"]
timeout = "1m"

[chain.conflict]
handlers = [${names%, }]

[chain.give-up]
handlers = ["give-up"]
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
  # The handler ran with PG_ROUTER_ITEM_ID (and no PG_RESCUE_ITEM_ID), in the
  # linked worktree (the -C directory), with the tracker and actor of the shim.
  [ "$(cat "$HLOG/resolve.item")" = "$ITEM" ]
  [ "$(cat "$HLOG/resolve.old-item-var")" = "unset" ]
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

assert_closed_stale() {
  [ "$status" -eq 0 ]
  [ "$(item_field '.status')" = "closed" ]
  [[ "$(item_field '.close_reason // ""')" == *"stale"* ]]
  [[ "$(item_field '.close_reason // ""')" == *"pg-rescue run "* ]]
  bd_fixture_show_json "$ITEM" | jq -e '(.labels // []) | index("script-gave-up") | not' >/dev/null
  [ "$(awk '{print $2}' "$(outcome_file)")" = "stale" ]
  assert_no_worktree
}

@test "a head that differs from origin/<branch> closes the item as stale: exit 0, no worktree, no handler, no marker, nothing pushed" {
  make_pr conflict
  ITEM=$(new_item)
  write_chain resolve give-up
  SHIM_HEAD=1111111111111111111111111111111111111111 run_shim
  assert_closed_stale
  # The close reason names both shas.
  [[ "$(item_field '.close_reason // ""')" == *"$HEAD_SHA"* ]]
  [[ "$(item_field '.close_reason // ""')" == *"1111111111111111111111111111111111111111"* ]]
  assert_pushed_nothing
  # No agent handler (nor any other) ran, and no worktree was ever added.
  [ ! -e "$HLOG/resolve.item" ]
  [ ! -e "$HLOG/give-up.ran" ]
}

@test "the stale-head comparison ignores the case of the recorded sha" {
  make_pr clean
  ITEM=$(new_item)
  write_chain give-up
  SHIM_HEAD=$(tr '[:lower:]' '[:upper:]' <<<"$HEAD_SHA") run_shim
  assert_resolved
}

@test "a push whose lease is rejected after the rebase (the remote moved after the fetch) closes the item as stale: exit 0, no marker, nothing pushed by the shim" {
  make_pr conflict
  ITEM=$(new_item)
  write_chain mover give-up
  run_shim
  assert_closed_stale
  # The remote carries the other clone's commit and nothing of the shim's.
  [ "$(remote_ref pr)" != "$HEAD_SHA" ]
  [ "$(git -C "$REMOTE" show pr:moved.txt)" = "moved" ]
  [ "$(git -C "$REMOTE" show pr:f | grep -cx resolved || true)" = "0" ]
  [ "$(remote_ref main)" = "$MAIN_SHA" ]
  [ ! -e "$HLOG/give-up.ran" ]
  [[ "$(item_field '.close_reason // ""')" == *"lease"* ]]
}

@test "a handler that runs git push origin from the worktree fails and the remote is unchanged" {
  make_pr conflict
  ITEM=$(new_item)
  write_chain push give-up
  run_shim
  # The handler declined after its failed push, the chain gave up.
  [ -e "$HLOG/push.ran" ]
  [ "$(cat "$HLOG/push.rc")" -ne 0 ]
  [ "$(remote_ref handler-pushed)" = "none" ]
  assert_pushed_nothing
  [ "$status" -eq 0 ]
  assert_released
  assert_no_worktree
}

@test "the shim does not write the unusable push URL into any repository configuration" {
  make_pr clean
  ITEM=$(new_item)
  write_chain give-up
  run_shim
  assert_resolved
  [ -z "$(git -C "$REPO" config --get-all remote.origin.pushurl || true)" ]
  [ "$(git -C "$REPO" config --get remote.origin.url)" = "$REMOTE" ]
}

@test "the push helper runs without the unusable push URL and receives --worktree-root" {
  make_pr clean
  ITEM=$(new_item)
  write_chain give-up
  # A recording wrapper first on PATH: it logs its argv and the git-config
  # environment it sees, then runs the real helper.
  real_helper=$(command -v pg-rescue-push-branch)
  mkdir -p "$GFH_WORK/helper-bin"
  cat >"$GFH_WORK/helper-bin/pg-rescue-push-branch" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >"$GFH_WORK/helper.args"
printf '%s\n' "\${GIT_CONFIG_COUNT-unset}" >"$GFH_WORK/helper.gitcfg"
exec "$real_helper" "\$@"
EOF
  chmod +x "$GFH_WORK/helper-bin/pg-rescue-push-branch"
  PATH="$GFH_WORK/helper-bin:$PATH"
  export PATH
  run_shim
  assert_resolved
  [[ "$(cat "$GFH_WORK/helper.args")" == *"--worktree-root $WTROOT "* ]]
  [[ "$(cat "$GFH_WORK/helper.args")" == *"--expected-sha $HEAD_SHA" ]]
  [ "$(cat "$GFH_WORK/helper.gitcfg")" = "unset" ]
}

@test "PUSH_AGENT_RESOLVED defaults to true: an agent-resolved rebase is pushed and the item closed" {
  make_pr conflict
  ITEM=$(new_item)
  write_chain agent give-up
  run_shim
  assert_resolved
  [ ! -e "$HLOG/give-up.ran" ]
  git -C "$REMOTE" show pr:f | grep -qx resolved
}

@test "PUSH_AGENT_RESOLVED=false: an agent-resolved rebase is not pushed; the give-up chain marks the item, claim released, outcome gave-up" {
  make_pr conflict
  ITEM=$(new_item)
  write_chain agent give-up
  PUSH_AGENT_RESOLVED=false run_shim
  [ "$status" -eq 0 ]
  [ -e "$HLOG/give-up.ran" ]
  [ "$(cat "$HLOG/give-up.item")" = "$ITEM" ]
  assert_released
  bd_fixture_show_json "$ITEM" | jq -e '.labels | index("script-gave-up")' >/dev/null
  assert_pushed_nothing
  assert_no_worktree
  [ "$(awk '{print $2}' "$(outcome_file)")" = "gave-up" ]
}

@test "PUSH_AGENT_RESOLVED=false: a deterministic-tagged handler's resolution is still pushed" {
  make_pr conflict
  ITEM=$(new_item)
  write_chain det give-up
  PUSH_AGENT_RESOLVED=false run_shim
  assert_resolved
  [ ! -e "$HLOG/give-up.ran" ]
}

@test "PUSH_AGENT_RESOLVED=false: a plain rebase (no handler) is still pushed" {
  make_pr clean
  ITEM=$(new_item)
  write_chain give-up
  PUSH_AGENT_RESOLVED=false run_shim
  assert_resolved
  [ ! -e "$HLOG/give-up.ran" ]
}

@test "PUSH_AGENT_RESOLVED=false: an unreadable result file counts as agent-resolved and is not pushed" {
  make_pr clean
  ITEM=$(new_item)
  write_chain give-up
  # A pg-rescue stand-in that succeeds but writes a result file that is not JSON.
  STUB_DIR="$GFH_WORK/stub-bin"
  mkdir -p "$STUB_DIR"
  cat >"$STUB_DIR/pg-rescue" <<EOF
#!/usr/bin/env bash
while [ \$# -gt 0 ]; do
  if [ "\$1" = "--result-file" ]; then echo 'not json' >"\$2"; fi
  shift
done
exit 0
EOF
  chmod +x "$STUB_DIR/pg-rescue"
  PATH="$STUB_DIR:$PATH"
  export PATH
  PUSH_AGENT_RESOLVED=false run_shim
  # Not provably plain, so not pushed; the (stub) give-up chain ended the run.
  [ "$status" -eq 0 ]
  assert_released
  assert_pushed_nothing
  [ "$(awk '{print $2}' "$(outcome_file)")" = "gave-up" ]
}

@test "PUSH_AGENT_RESOLVED other than true or false is a usage error before bd is touched" {
  make_pr clean
  ITEM=$(new_item)
  : >"$BD_CALL_LOG"
  PUSH_AGENT_RESOLVED=maybe run_shim
  [ "$status" -eq 2 ]
  [ ! -s "$BD_CALL_LOG" ]
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

@test "a claim on an already closed item exits 0 with outcome gone and changes nothing" {
  make_pr conflict
  ITEM=$(new_item)
  bd -C "$TDIR" close "$ITEM" --reason "already done" >/dev/null 2>&1
  before=$(bd_fixture_show_json "$ITEM")
  [ "$(jq -r .status <<<"$before")" = "closed" ]
  write_chain resolve give-up
  : >"$BD_CALL_LOG"
  run_shim
  [ "$status" -eq 0 ]
  [ "$(bd_fixture_show_json "$ITEM")" = "$before" ]
  # One claim attempt and the re-read; nothing else touched bd.
  [ "$(grep -c -- '^update ' "$BD_CALL_LOG")" = "1" ]
  [ -z "$(grep -E -- '^(close|create) ' "$BD_CALL_LOG" || true)" ]
  assert_pushed_nothing
  [ ! -e "$HLOG/resolve.item" ]
  [ "$(awk '{print $2}' "$(outcome_file)")" = "gone" ]
  [ ! -d "$WTROOT" ] || [ -z "$(ls -A "$WTROOT")" ]
}

# signal_running_shim SIGNAL: start the shim with a handler that blocks, wait
# until the handler runs, send SIGNAL to the shim and wait for it to end.
# Job control gives the background shim its own process group and leaves INT
# trappable (a background job of a non-interactive shell would ignore it).
signal_running_shim() {
  local sig=$1 pid
  make_pr conflict
  ITEM=$(new_item)
  write_chain slow give-up
  set -m
  "${SHIM[@]}" --item-id "$ITEM" --repo acme/widget --branch pr --base main \
    --head-sha "$HEAD_SHA" --repo-dir-map "widget=$REPO" --worktree-root "$WTROOT" \
    --tracker-dir "$TDIR" >/dev/null 2>&1 3>&- &
  pid=$!
  set +m
  for _ in $(seq 1 300); do
    [ -e "$HLOG/slow.started" ] && break
    sleep 0.1
  done
  [ -e "$HLOG/slow.started" ]
  [ "$(item_field '.assignee // ""')" != "" ]
  kill -"$sig" "$pid"
  status=0
  wait "$pid" || status=$?
}

assert_run_left_nothing() {
  assert_released
  assert_no_worktree
  assert_pushed_nothing
  [ -z "$(ls "$STATE_DIR"/*.claim 2>/dev/null || true)" ]
  [ "$(awk '{print $2}' "$(outcome_file)")" = "failed" ]
}

@test "an INT sent to a running shim leaves no claim and no worktree" {
  signal_running_shim INT
  [ "$status" -eq 130 ]
  assert_run_left_nothing
}

@test "a TERM sent to a running shim leaves no claim and no worktree" {
  signal_running_shim TERM
  [ "$status" -eq 143 ]
  assert_run_left_nothing
}

@test "the sweep releases a stranded claim whose file is older than SWEEP_AGE, removes its worktree, and keeps a fresh claim" {
  make_pr clean
  ITEM=$(new_item)
  STALE=$(new_item)
  FRESH=$(new_item)
  write_chain give-up
  stale_actor=pg-rescue/20000101T000000Z-1
  fresh_actor=pg-rescue/20990101T000000Z-2
  bd -C "$TDIR" update "$STALE" --claim --actor "$stale_actor" >/dev/null 2>&1
  bd -C "$TDIR" update "$FRESH" --claim --actor "$fresh_actor" >/dev/null 2>&1
  [ "$(bd_fixture_show_json "$STALE" | jq -r .assignee)" = "$stale_actor" ]
  mkdir -p "$STATE_DIR" "$WTROOT/widget-20000101T000000Z-1" "$WTROOT/widget-20990101T000000Z-2"
  echo stranded >"$WTROOT/widget-20000101T000000Z-1/f"
  printf '%s\n%s\n' "$STALE" "$stale_actor" >"$STATE_DIR/20000101T000000Z-1.claim"
  printf '%s\n%s\n' "$FRESH" "$fresh_actor" >"$STATE_DIR/20990101T000000Z-2.claim"
  touch -t 200001010000 "$STATE_DIR/20000101T000000Z-1.claim"
  run_shim
  [ "$status" -eq 0 ]
  # The stale claim is released, its file and worktree directory are gone.
  [ "$(bd_fixture_show_json "$STALE" | jq -r '.status')" = "open" ]
  [ -z "$(bd_fixture_show_json "$STALE" | jq -r '.assignee // ""')" ]
  [ ! -e "$STATE_DIR/20000101T000000Z-1.claim" ]
  [ ! -e "$WTROOT/widget-20000101T000000Z-1" ]
  # A claim newer than SWEEP_AGE is left alone (its run may still be live).
  [ "$(bd_fixture_show_json "$FRESH" | jq -r .assignee)" = "$fresh_actor" ]
  [ -e "$STATE_DIR/20990101T000000Z-2.claim" ]
  [ -d "$WTROOT/widget-20990101T000000Z-2" ]
  # And the run itself resolved its own item.
  [ "$(item_field '.status')" = "closed" ]
  [ "$(remote_ref main)" = "$MAIN_SHA" ]
}

@test "the sweep leaves a stranded claim alone when the item no longer names that actor" {
  make_pr clean
  ITEM=$(new_item)
  OTHER=$(new_item)
  write_chain give-up
  bd -C "$TDIR" update "$OTHER" --claim --actor someone-else >/dev/null 2>&1
  mkdir -p "$STATE_DIR"
  printf '%s\n%s\n' "$OTHER" "pg-rescue/20000101T000000Z-1" >"$STATE_DIR/20000101T000000Z-1.claim"
  touch -t 200001010000 "$STATE_DIR/20000101T000000Z-1.claim"
  run_shim
  [ "$status" -eq 0 ]
  [ "$(bd_fixture_show_json "$OTHER" | jq -r .assignee)" = "someone-else" ]
  [ ! -e "$STATE_DIR/20000101T000000Z-1.claim" ]
}

@test "SWEEP_AGE is overridable: a larger value leaves an old claim file alone" {
  make_pr clean
  ITEM=$(new_item)
  STALE=$(new_item)
  write_chain give-up
  bd -C "$TDIR" update "$STALE" --claim --actor pg-rescue/20000101T000000Z-1 >/dev/null 2>&1
  mkdir -p "$STATE_DIR"
  printf '%s\n%s\n' "$STALE" "pg-rescue/20000101T000000Z-1" >"$STATE_DIR/20000101T000000Z-1.claim"
  touch -t 200001010000 "$STATE_DIR/20000101T000000Z-1.claim"
  SWEEP_AGE=999999999999 run_shim
  [ "$status" -eq 0 ]
  # Still younger than the age: untouched.
  [ "$(bd_fixture_show_json "$STALE" | jq -r .assignee)" = "pg-rescue/20000101T000000Z-1" ]
  [ -e "$STATE_DIR/20000101T000000Z-1.claim" ]
}

@test "a claim file is recorded after the claim and removed when the item is closed" {
  make_pr clean
  ITEM=$(new_item)
  write_chain give-up
  run_shim
  assert_resolved
  [ -z "$(ls "$STATE_DIR"/*.claim 2>/dev/null || true)" ]
}

@test "an item at SCRIPT_MAX_FAILURES runs no rebase and no agent handler; the give-up chain marks it; exit 0, claim released" {
  make_pr conflict
  ITEM=$(new_item)
  bd -C "$TDIR" update "$ITEM" --set-metadata script_failures=3 >/dev/null 2>&1
  write_chain resolve give-up
  run_shim
  [ "$status" -eq 0 ]
  [ ! -e "$HLOG/resolve.item" ]
  [ -e "$HLOG/give-up.ran" ]
  [ "$(cat "$HLOG/give-up.item")" = "$ITEM" ]
  assert_released
  bd_fixture_show_json "$ITEM" | jq -e '.labels | index("script-gave-up")' >/dev/null
  # The counter is not bumped by a give-up.
  [ "$(item_field '.metadata.script_failures')" = "3" ]
  assert_pushed_nothing
  assert_no_worktree
  [ "$(awk '{print $2}' "$(outcome_file)")" = "gave-up" ]
}

@test "SCRIPT_MAX_FAILURES is overridable: a lower cap sends an item with one failure to the give-up chain" {
  make_pr clean
  ITEM=$(new_item)
  bd -C "$TDIR" update "$ITEM" --set-metadata script_failures=1 >/dev/null 2>&1
  write_chain give-up
  SCRIPT_MAX_FAILURES=1 run_shim
  [ "$status" -eq 0 ]
  assert_released
  assert_pushed_nothing
  [ "$(awk '{print $2}' "$(outcome_file)")" = "gave-up" ]
}

@test "an unparsable script_failures counts as zero" {
  make_pr clean
  ITEM=$(new_item)
  bd -C "$TDIR" update "$ITEM" --set-metadata script_failures=many >/dev/null 2>&1
  write_chain give-up
  run_shim
  assert_resolved
}

@test "a failed run adds one to script_failures in the same call that releases the claim" {
  make_pr conflict
  ITEM=$(new_item)
  bd -C "$TDIR" update "$ITEM" --set-metadata script_failures=1 >/dev/null 2>&1
  write_chain decline
  : >"$BD_CALL_LOG"
  run_shim
  calls=$(cat "$BD_CALL_LOG")
  [ "$status" -eq 1 ]
  assert_released
  [ "$(item_field '.metadata.script_failures')" = "2" ]
  # One releasing call, carrying the counter; the claim was not released twice.
  [ "$(grep -c -E -- "^update $ITEM --status open --assignee +--actor pg-rescue/.* --set-metadata script_failures=2\$" <<<"$calls")" = "1" ]
  [ "$(grep -c -- '--status open' <<<"$calls")" = "1" ]
}

@test "a run that is not a failure does not touch script_failures" {
  make_pr conflict
  ITEM=$(new_item)
  write_chain give-up
  : >"$BD_CALL_LOG"
  run_shim
  calls=$(cat "$BD_CALL_LOG")
  [ "$status" -eq 0 ]
  assert_released
  [ -z "$(grep -- 'script_failures' <<<"$calls" || true)" ]
  [ "$(item_field '.metadata.script_failures // "none"')" = "none" ]
}

@test "the shim's stderr is kept in <run-id>.log, and a failed run says why" {
  make_pr conflict
  ITEM=$(new_item)
  write_chain decline
  run_shim
  [ "$status" -eq 1 ]
  log=$(printf '%s\n' "$STATE_DIR"/*.log | head -1)
  [ -s "$log" ]
  grep -q "pg-rescue exited" "$log"
  # The log and the outcome record share the run id.
  [ "$(basename "$log" .log)" = "$(basename "$(outcome_file)" .outcome)" ]
}

@test "PG_RESCUE_ITEM_ID appears nowhere in the shim" {
  if [ -f "$SCRIPTS_DIR/pg-rescue-resolve-conflict.sh" ]; then
    [ "$(grep -c "PG_RESCUE_ITEM_ID" "$SCRIPTS_DIR/pg-rescue-resolve-conflict.sh" || true)" = "0" ]
  else
    skip "shim source is not beside the tests"
  fi
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
