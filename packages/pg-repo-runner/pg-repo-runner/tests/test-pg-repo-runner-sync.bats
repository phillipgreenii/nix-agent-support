#!/usr/bin/env bats
# bats file_tags=type:integration
#
# Runner A, report-only (bead pg2-28h96s, F5): the `sync` role. Fetch the primary
# branch, classify behind/ahead, fast-forward a behind clone, record an ahead one,
# surface a diverged one, and leave a canonical clone that is not steady untouched.
#
# Real everything except two seams: git (real clones of bare LOCAL remotes under a
# real pn-workspace.toml; every git call of the runner goes through a recording
# wrapper that execs the real git), bd (the REAL bd over an embedded throwaway
# database, see test-support/bd_fixture.bash: never the shared tracker) and the real
# script run as a subprocess. The only stand-in is the macOS notifier (a recording
# script on PG_REPO_RUNNER_OSASCRIPT_BIN).
#
# The push step has its own suite (test-pg-repo-runner-push.bats). Here, one test
# forces `push = true` for a repo WITHOUT a green verdict to prove that the flag alone
# publishes nothing; the recorded git argv, a pre-push hook and the remote's ref prove
# that no push happens.

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
  BASE_IDS="$(bd -C "$TDIR" list --status all -n 0 --json | jq -c '(if type=="object" and has("data") then .data else . end) | map(.id)')"

  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    RUNNER=("$SCRIPT_UNDER_TEST")
  else
    RUNNER=(bash -euo pipefail "$SCRIPTS_DIR/pg-repo-runner.sh")
  fi

  TEST_DIR="$GFH_WORK"
  WS="$TEST_DIR/ws"
  A="$WS/nix-alpha"
  mkdir -p "$WS" "$TEST_DIR/remotes"
  make_repo "$A" alpha
  make_repo "$WS/nix-beta" beta
  write_workspace nix-alpha nix-beta
  write_config "$TDIR"

  export XDG_STATE_HOME="$TEST_DIR/state"
  export PG_REPO_RUNNER_CONFIG="$TEST_DIR/config.toml"
  export PG_REPO_RUNNER_LOADAVG=0.50
  STATE="$XDG_STATE_HOME/pg-repo-runner"
  REMOTE="$TEST_DIR/remotes/alpha.git"
  make_git_wrapper
  make_notifier
  # a pre-push hook in the canonical clone: if anything ever pushed from it, the marker appears
  mkdir -p "$A/.git/hooks"
  printf '#!/bin/sh\ntouch "%s/pre-push.ran"\nexit 0\n' "$TEST_DIR" >"$A/.git/hooks/pre-push"
  chmod +x "$A/.git/hooks/pre-push"
}

teardown() {
  [[ -z ${RR_DEBUG:-} ]] || cat "$STATE/run.log" >&3
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

# commit_file DIR FILE: a new commit on the current branch of DIR touching FILE.
commit_file() {
  echo "$RANDOM$RANDOM" >>"$1/$2"
  git -C "$1" add "$2"
  git -C "$1" commit -q -m "change $2"
}

# remote_commit FILE: another clone adds a commit to the remote's main (what a
# land from another machine does). The canonical clone's origin/main stays STALE.
remote_commit() {
  local other="$TEST_DIR/other-alpha"
  if [[ ! -d $other ]]; then gfh_clone "$REMOTE" "$other" "pg-repo-runner"; fi
  git -C "$other" pull -q --rebase origin main
  commit_file "$other" "$1"
  git -C "$other" push -q origin main
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

# write_config TRACKER [PUSH_ALPHA] [SURFACE]: sync is on for both repos. PUSH_ALPHA
# (default false) is the forced-push proof of one test; SURFACE is "on" or "off".
write_config() {
  local tracker="$1" push_alpha="${2:-false}" surface="${3:-on}"
  {
    cat <<EOF
workspace_root = "$WS"
kill_switch = "$TEST_DIR/config-kill"

[thresholds]
max_load = 11.0
nice_level = 10

[repos.nix-alpha]
verify = false
sync = true
push = $push_alpha

[repos.nix-beta]
verify = false
sync = true
push = false
EOF
    if [[ $surface == on ]]; then
      printf '\n[surface]\nenabled = true\ntracker_dir = "%s"\nnotify = true\n' "$tracker"
      printf '\n[surface.repo_labels]\nnix-alpha = "agent-support"\nnix-beta = "base"\n'
    fi
  } >"$TEST_DIR/config.toml"
}

# make_git_wrapper: every git call of the runner is logged (one line of argv) and
# then handed to the real git.
make_git_wrapper() {
  local real
  real="$(command -v git)"
  cat >"$TEST_DIR/git-wrapper" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$TEST_DIR/git.argv"
exec "$real" "\$@"
EOF
  chmod +x "$TEST_DIR/git-wrapper"
  export PG_REPO_RUNNER_GIT_BIN="$TEST_DIR/git-wrapper"
}

make_notifier() {
  echo 0 >"$TEST_DIR/notify.exit"
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

# sync REPO: one sync pass for REPO.
sync_pass() {
  PG_ROUTER_ITEM_ID="$1" run "${RUNNER[@]}" sync
}

runner_beads() {
  bd -C "$TDIR" list --metadata-field runner=pg-repo-runner --metadata-field "repo=$1" \
    --status all -n 0 --json |
    jq -c --argjson base "$BASE_IDS" \
      '(if type=="object" and has("data") then .data else . end) | map(select(.id as $i | $base | index($i) | not))'
}

bead_count() { runner_beads "$1" | jq 'length'; }

comments_of() {
  bd -C "$TDIR" comments "$1" --json |
    jq -r '(if type=="object" and has("data") then .data else . end) | .[].text // .[].body // empty'
}

# rec REPO FIELD: a field of the repo's sync record.
rec() { jq -r ".$2" "$STATE/sync/$1.json"; }

# git_state DIR: a digest of every file under DIR/.git (name, size, checksum).
git_state() {
  (cd "$1/.git" && find . -type f -exec cksum {} + | sort)
}

# the recorded git argv: the subcommand of every call (the first word after -C <dir>)
git_verbs() {
  [[ -f $TEST_DIR/git.argv ]] || return 0
  sed -E 's/^-C [^ ]+ //' "$TEST_DIR/git.argv" | awk '{print $1}' | sort -u
}

# no_verb REGEX: fails when the runner ran a git subcommand matching REGEX. An
# explicit `if`, because a negated pipeline never fails a bats test.
no_verb() {
  if git_verbs | grep -qxE "$1"; then
    echo "the runner ran a git subcommand matching $1:"
    cat "$TEST_DIR/git.argv"
    return 1
  fi
}

# no_argv REGEX: fails when any recorded git argv line matches REGEX.
no_argv() {
  if [[ -f $TEST_DIR/git.argv ]] && grep -qE -- "$1" "$TEST_DIR/git.argv"; then
    echo "a recorded git argv matches $1:"
    cat "$TEST_DIR/git.argv"
    return 1
  fi
}

# nothing the runner did may have written to a remote, rebased, reset, forced or switched
assert_no_history_rewriting() {
  local bad
  [ ! -e "$TEST_DIR/pre-push.ran" ]
  no_verb 'push|rebase|reset|checkout|switch|stash|clean|cherry-pick|commit|branch|update-ref'
  no_argv '--force|--no-verify'
  # the one `+` refspec is the fetch's, and it names a remote-tracking ref
  if [[ -f $TEST_DIR/git.argv ]]; then
    bad="$(grep -E ' \+refs/' "$TEST_DIR/git.argv" |
      grep -vE '^(-C [^ ]+ )?fetch .* \+refs/heads/[^:]+:refs/remotes/origin/[^ ]+$' || true)"
    [ -z "$bad" ]
  fi
}

# ---- 0,0: nothing ----------------------------------------------------------

@test "0,0 does nothing: no ref moves, no bead, no notification, no merge" {
  local tip before_state
  tip="$(git -C "$A" rev-parse HEAD)"
  before_state="$(git -C "$A" reflog | wc -l)"
  sync_pass nix-alpha
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = in-sync ]
  [ "$(rec nix-alpha behind)" = 0 ]
  [ "$(rec nix-alpha ahead)" = 0 ]
  [ "$(git -C "$A" rev-parse HEAD)" = "$tip" ]
  [ "$(git -C "$A" reflog | wc -l)" -eq "$before_state" ]
  git_verbs | grep -qx fetch
  no_verb merge
  [ "$(bead_count nix-alpha)" -eq 0 ]
  [ "$(notifications)" -eq 0 ]
  assert_no_history_rewriting
}

# ---- behind only: fast-forward ---------------------------------------------

@test "behind-only fast-forwards the canonical clone, re-reading behind after a real fetch" {
  local new
  remote_commit g
  new="$(git -C "$REMOTE" rev-parse refs/heads/main)"
  # the remote-tracking ref is stale: before the fetch the clone looks in sync
  [ "$(git -C "$A" rev-list --count refs/heads/main..refs/remotes/origin/main)" = 0 ]
  sync_pass nix-alpha
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = fast-forwarded ]
  [ "$(rec nix-alpha behindBeforeFetch)" = 0 ]
  [ "$(rec nix-alpha behind)" = 1 ]
  [ "$(git -C "$A" rev-parse refs/heads/main)" = "$new" ]
  [ "$(git -C "$A" rev-parse HEAD)" = "$new" ]
  [ -f "$A/g" ]
  [ -z "$(git -C "$A" status --porcelain)" ]
  git_verbs | grep -qx fetch
  grep -q 'merge --ff-only' "$TEST_DIR/git.argv"
  [ "$(git -C "$REMOTE" rev-parse refs/heads/main)" = "$new" ]
  [ "$(bead_count nix-alpha)" -eq 0 ]
  assert_no_history_rewriting
  # and the next pass is 0,0
  sync_pass nix-alpha
  [ "$(rec nix-alpha outcome)" = in-sync ]
}

@test "several behind commits fast-forward in one pass" {
  remote_commit g
  remote_commit h
  sync_pass nix-alpha
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha behind)" = 2 ]
  [ "$(git -C "$A" rev-parse HEAD)" = "$(git -C "$REMOTE" rev-parse refs/heads/main)" ]
}

@test "a refused fast-forward is recorded, not retried and not forced" {
  remote_commit g
  local tip
  tip="$(git -C "$A" rev-parse HEAD)"
  # an index lock makes `git merge` refuse; the read-only precheck does not care
  touch "$A/.git/index.lock"
  sync_pass nix-alpha
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = ff-refused ]
  [ "$(grep -c 'merge --ff-only' "$TEST_DIR/git.argv")" -eq 1 ]
  [ "$(git -C "$A" rev-parse HEAD)" = "$tip" ]
  assert_no_history_rewriting
}

# ---- ahead only: record the count, never push ------------------------------

@test "ahead-only records the count and runs no git push, even with push forced to true but no green verdict" {
  write_config "$TDIR" true
  commit_file "$A" f
  commit_file "$A" f
  local tip remote_tip
  tip="$(git -C "$A" rev-parse HEAD)"
  remote_tip="$(git -C "$REMOTE" rev-parse refs/heads/main)"
  [ "$(tq -o json -f "$TEST_DIR/config.toml" . | jq -r '.repos["nix-alpha"].push')" = true ]
  sync_pass nix-alpha
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = ahead ]
  [ "$(rec nix-alpha ahead)" = 2 ]
  [ "$(rec nix-alpha behind)" = 0 ]
  [ "$(git -C "$A" rev-parse HEAD)" = "$tip" ]
  [ "$(git -C "$REMOTE" rev-parse refs/heads/main)" = "$remote_tip" ]
  no_verb push
  no_verb merge
  assert_no_history_rewriting
  # no bead and no notification: unpushed commits are not in themselves a problem
  [ "$(bead_count nix-alpha)" -eq 0 ]
  [ "$(notifications)" -eq 0 ]
  # the count is in status
  run "${RUNNER[@]}" status
  [ "$status" -eq 0 ]
  [[ $output == *"nix-alpha"*"ahead"*"ahead 2"* ]]
}

# ---- diverged: one deduplicated bead, no ref change ------------------------

@test "diverged files one deduplicated bead and changes no ref, and a later resolution closes it" {
  local before
  before="$(bd_fixture_count)"
  commit_file "$A" f
  remote_commit g
  local local_tip remote_tip
  local_tip="$(git -C "$A" rev-parse refs/heads/main)"
  remote_tip="$(git -C "$REMOTE" rev-parse refs/heads/main)"
  sync_pass nix-alpha
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = diverged ]
  [ "$(rec nix-alpha behind)" = 1 ]
  [ "$(rec nix-alpha ahead)" = 1 ]
  # the local branch and the remote branch did not move; the working tree is clean
  [ "$(git -C "$A" rev-parse refs/heads/main)" = "$local_tip" ]
  [ "$(git -C "$A" rev-parse HEAD)" = "$local_tip" ]
  [ "$(git -C "$REMOTE" rev-parse refs/heads/main)" = "$remote_tip" ]
  [ -z "$(git -C "$A" status --porcelain)" ]
  no_verb merge
  assert_no_history_rewriting
  # one bead, the repo's label, the resolution advice; one notification
  [ "$(bd_fixture_count)" -eq $((before + 1)) ]
  [ "$(bead_count nix-alpha)" -eq 1 ]
  local id b
  b="$(runner_beads nix-alpha | jq -c '.[0]')"
  id="$(jq -r .id <<<"$b")"
  [ "$(jq -r '.metadata.kind' <<<"$b")" = diverged ]
  [ "$(jq -r .priority <<<"$b")" = 1 ]
  [[ "$(jq -r .title <<<"$b")" == *"pn workspace rebase or /pn-workspace-sync"* ]]
  [ "$(bd_fixture_show_json "$id" | jq -r '.labels | index("agent-support") != null')" = true ]
  [ "$(notifications)" -eq 1 ]
  # a repeated pass is the same story: no second bead, no second notification, no ref change
  sync_pass nix-alpha
  [ "$status" -eq 0 ]
  sync_pass nix-alpha
  [ "$status" -eq 0 ]
  [ "$(bead_count nix-alpha)" -eq 1 ]
  [ "$(notifications)" -eq 1 ]
  [ "$(git -C "$A" rev-parse refs/heads/main)" = "$local_tip" ]
  [ "$(git -C "$REMOTE" rev-parse refs/heads/main)" = "$remote_tip" ]
  # someone resolves it by hand (a rebase: what `pn workspace rebase` does); the next
  # pass finds the repo ahead only, and closes the runner's own bead
  git -C "$A" rebase -q refs/remotes/origin/main
  sync_pass nix-alpha
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = ahead ]
  [ "$(bd_fixture_show_json "$id" | jq -r .status)" = closed ]
  comments_of "$id" | grep -q "No longer diverged"
  [ "$(bead_count nix-alpha)" -eq 1 ]
}

@test "diverged with surfacing off is recorded and still touches nothing" {
  write_config "$TDIR" false off
  commit_file "$A" f
  remote_commit g
  local local_tip
  local_tip="$(git -C "$A" rev-parse refs/heads/main)"
  sync_pass nix-alpha
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = diverged ]
  [ "$(git -C "$A" rev-parse refs/heads/main)" = "$local_tip" ]
  [ "$(bead_count nix-alpha)" -eq 0 ]
  assert_no_history_rewriting
}

# ---- a canonical clone that is not steady is skipped and left untouched ----

# assert_yielded REASON-FRAGMENT STATE-BEFORE: the pass exited 0, recorded
# `yielded`, ran no fetch and no merge, and the whole .git directory is unchanged.
assert_yielded() {
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = yielded ]
  [[ "$(rec nix-alpha detail)" == "canonical not steady: "*"$1"* ]]
  no_verb 'fetch|merge'
  [ "$(git_state "$A")" = "$2" ]
  [ "$(bead_count nix-alpha)" -eq 0 ]
  assert_no_history_rewriting
}

@test "a dirty canonical clone (modified tracked file) is skipped and left untouched" {
  remote_commit g
  echo dirty >>"$A/f"
  local state content
  state="$(git_state "$A")"
  content="$(cat "$A/f")"
  sync_pass nix-alpha
  assert_yielded "not clean" "$state"
  [ "$(cat "$A/f")" = "$content" ]
  [ ! -e "$A/g" ]
}

@test "a canonical clone with an untracked file is skipped and left untouched" {
  remote_commit g
  echo x >"$A/untracked"
  local state
  state="$(git_state "$A")"
  sync_pass nix-alpha
  assert_yielded "not clean" "$state"
  [ -f "$A/untracked" ]
}

@test "a canonical clone in the middle of a real rebase is skipped and left untouched" {
  echo one >"$A/c"
  git -C "$A" add c
  git -C "$A" commit -q -m c1
  git -C "$A" push -q origin main
  echo local >"$A/c"
  git -C "$A" commit -q -am local
  local other="$TEST_DIR/other-alpha"
  gfh_clone "$REMOTE" "$other" "pg-repo-runner"
  echo remote >"$other/c"
  git -C "$other" commit -q -am remote
  git -C "$other" push -q origin main
  git -C "$A" fetch -q origin main
  # this rebase stops on a conflict and leaves the clone mid-rebase
  run git -C "$A" rebase refs/remotes/origin/main
  [ "$status" -ne 0 ]
  [ -d "$A/.git/rebase-merge" ] || [ -d "$A/.git/rebase-apply" ]
  local state
  state="$(git_state "$A")"
  sync_pass nix-alpha
  assert_yielded "" "$state"
  [ -d "$A/.git/rebase-merge" ] || [ -d "$A/.git/rebase-apply" ]
}

@test "each in-progress marker alone, on an otherwise clean clone, is skipped and left untouched" {
  local m state
  for m in rebase-merge rebase-apply MERGE_HEAD CHERRY_PICK_HEAD REVERT_HEAD; do
    rm -f "$TEST_DIR/git.argv"
    if [[ $m == *_HEAD ]]; then
      git -C "$A" rev-parse HEAD >"$A/.git/$m"
    else
      mkdir "$A/.git/$m"
    fi
    [ -z "$(git -C "$A" status --porcelain)" ] || {
      echo "marker $m made the tree dirty"
      return 1
    }
    state="$(git_state "$A")"
    sync_pass nix-alpha
    assert_yielded "$m" "$state"
    rm -rf "${A:?}/.git/$m"
  done
}

@test "a canonical clone on another branch or on a detached HEAD is skipped and left untouched" {
  git -C "$A" checkout -q -b feature
  local state
  state="$(git_state "$A")"
  sync_pass nix-alpha
  assert_yielded "not main" "$state"
  [ "$(git -C "$A" symbolic-ref --short HEAD)" = feature ]
  git -C "$A" checkout -q --detach
  rm -f "$TEST_DIR/git.argv"
  state="$(git_state "$A")"
  sync_pass nix-alpha
  assert_yielded "detached" "$state"
}

@test "a canonical clone with core.worktree set is skipped and left untouched" {
  git -C "$A" config core.worktree "$A"
  local state
  state="$(git_state "$A")"
  sync_pass nix-alpha
  assert_yielded "core.worktree" "$state"
}

# ---- fetch and remote problems ---------------------------------------------

@test "a fetch that fails is recorded, exits 0 and changes nothing" {
  git -C "$A" remote set-url origin "$TEST_DIR/remotes/does-not-exist.git"
  local tip
  tip="$(git -C "$A" rev-parse HEAD)"
  sync_pass nix-alpha
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = fetch-failed ]
  [ "$(git -C "$A" rev-parse HEAD)" = "$tip" ]
  assert_no_history_rewriting
}

@test "an item for a repo whose sync flag is off is skipped before any git runs" {
  sed -i.bak '/nix-alpha/,/push/ s/sync = true/sync = false/' "$TEST_DIR/config.toml"
  sync_pass nix-alpha
  [ "$status" -eq 0 ]
  no_verb 'fetch|merge'
  [ ! -e "$STATE/sync/nix-alpha.json" ]
}

# ---- the other repo is its own pass ----------------------------------------

@test "a pass touches only the repo of its item" {
  remote_commit g
  local beta_state
  beta_state="$(git_state "$WS/nix-beta")"
  sync_pass nix-alpha
  [ "$status" -eq 0 ]
  [ "$(git_state "$WS/nix-beta")" = "$beta_state" ]
  [ ! -e "$STATE/sync/nix-beta.json" ]
}

# ---- status ----------------------------------------------------------------

@test "status lists the last sync result per repo, and --repo filters" {
  run "${RUNNER[@]}" status
  [[ $output == *"sync: none"* ]]
  sync_pass nix-alpha
  sync_pass nix-beta
  run "${RUNNER[@]}" status
  [ "$status" -eq 0 ]
  [[ $output == *"sync:"* ]]
  [[ $output == *"nix-alpha"*"in-sync"* ]]
  [[ $output == *"nix-beta"*"in-sync"* ]]
  run "${RUNNER[@]}" status --repo nix-beta
  [[ $output != *"nix-alpha"*"in-sync"* ]]
  [[ $output == *"nix-beta"*"in-sync"* ]]
}

# ---- the sync role itself has no push -----------------------------------------

@test "the entry point (the sync role body) never hands push to git, and no code names a force flag" {
  local f
  for f in "$SCRIPTS_DIR/pg-repo-runner.sh" "$SCRIPTS_DIR/pg-repo-runner.bash"; do
    run bash -c "grep -vE '^[[:space:]]*#' \"\$1\" | grep -nE -- '--force|force-with-lease|--no-verify|reset --hard|rebase'" _ "$f"
    # the help text and the bead body talk ABOUT rebase; no argv carries it
    [[ $output != *"rr_git"* ]] || {
      echo "$f: $output"
      return 1
    }
  done
  run bash -c "sed -e \"/<<'HELP'/,/^HELP\\$/d\" -e '/^[[:space:]]*#/d' \"\$1\" | grep -nE '(rr_git|RR_GIT_BIN|\\bgit)\\b.*\\bpush\\b'" _ "$SCRIPTS_DIR/pg-repo-runner.sh"
  [ "$status" -eq 1 ] || {
    echo "$output"
    return 1
  }
}
