#!/usr/bin/env bats
# bats file_tags=type:integration
#
# Runner A, the push step (bead pg2-nvye55, F6, ADR 0108). The sync role pushes a
# repo's primary tip ONLY when every gate holds, with exactly
#
#   git -C <canonical clone> push origin <verified-sha>:refs/heads/main
#
# Real everything except two seams: git (real clones of bare LOCAL remotes under a
# real pn-workspace.toml; every git call of the runner goes through a recording
# wrapper that execs the real git, and that can fire a one-shot hook while the gates
# are being checked, to move the tip, the remote or the kill switch mid-pass) and
# the macOS notifier. bd is the REAL bd over an embedded throwaway database, never
# the shared tracker.
#
# Only THIS file's fixture configs set `push = true`, for one fixture repo; no
# production default, module or deployment config does. Nothing here touches a
# network or a real remote: every remote is a bare repository under the test root.

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
  B="$WS/nix-beta"
  mkdir -p "$WS" "$TEST_DIR/remotes"
  make_repo "$A" alpha
  make_repo "$B" beta
  write_workspace nix-alpha nix-beta
  REAL_GIT="$(command -v git)"

  export XDG_STATE_HOME="$TEST_DIR/state"
  export PG_REPO_RUNNER_CONFIG="$TEST_DIR/config.toml"
  export PG_REPO_RUNNER_LOADAVG=0.50
  STATE="$XDG_STATE_HOME/pg-repo-runner"
  REMOTE="$TEST_DIR/remotes/alpha.git"
  write_config push=true
  make_git_wrapper
  make_notifier
  # a pre-push hook in the canonical clone: it leaves a marker, which proves that a
  # push RUNS the repo's hooks (no --no-verify)
  mkdir -p "$A/.git/hooks"
  printf '#!/bin/sh\ntouch "%s/pre-push.ran"\nexit 0\n' "$TEST_DIR" >"$A/.git/hooks/pre-push"
  chmod +x "$A/.git/hooks/pre-push"
  # the harness disables hooks (core.hooksPath=/dev/null); this clone turns its own on
  git -C "$A" config core.hooksPath "$A/.git/hooks"
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

commit_file() {
  echo "$RANDOM$RANDOM" >>"$1/$2"
  git -C "$1" add "$2"
  git -C "$1" commit -q -m "change $2"
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

# write_config [push=BOOL] [dry_run=BOOL] [consumes=TOML-LIST-BODY] [verify=BOOL]
# [surface=on|off]: the fixture config; nix-alpha is the repo under test (sync on).
# dry_run and consumes are omitted from the file when not given.
write_config() {
  local push=false dry_run="" consumes="" verify=false surface=on a
  for a in "$@"; do
    case "$a" in
    push=*) push="${a#push=}" ;;
    dry_run=*) dry_run="${a#dry_run=}" ;;
    consumes=*) consumes="${a#consumes=}" ;;
    verify=*) verify="${a#verify=}" ;;
    surface=*) surface="${a#surface=}" ;;
    esac
  done
  {
    cat <<EOF
workspace_root = "$WS"
kill_switch = "$TEST_DIR/config-kill"

[thresholds]
max_load = 11.0
nice_level = 10

[repos.nix-alpha]
verify = $verify
sync = true
push = $push
EOF
    [[ -z $dry_run ]] || printf 'dry_run = %s\n' "$dry_run"
    [[ -z $consumes ]] || printf 'consumes = [%s]\n' "$consumes"
    cat <<EOF

[repos.nix-beta]
verify = false
sync = true
push = false
EOF
    if [[ $surface == on ]]; then
      printf '\n[surface]\nenabled = true\ntracker_dir = "%s"\nnotify = true\n' "$TDIR"
      printf '\n[surface.repo_labels]\nnix-alpha = "agent-support"\nnix-beta = "base"\n'
    fi
  } >"$TEST_DIR/config.toml"
}

# make_git_wrapper: every git call of the runner is logged (one line of argv) and
# handed to the real git. A one-shot hook script at $TEST_DIR/on-merge-base runs
# (once) just before the ancestry gate's `git merge-base`, which is how a test moves
# the world in the middle of a pass.
make_git_wrapper() {
  cat >"$TEST_DIR/git-wrapper" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$TEST_DIR/git.argv"
if [[ "\$*" == *merge-base* && -x "$TEST_DIR/on-merge-base" ]]; then
  mv "$TEST_DIR/on-merge-base" "$TEST_DIR/on-merge-base.ran"
  "$TEST_DIR/on-merge-base.ran"
fi
exec "$REAL_GIT" "\$@"
EOF
  chmod +x "$TEST_DIR/git-wrapper"
  export PG_REPO_RUNNER_GIT_BIN="$TEST_DIR/git-wrapper"
}

# set_hook BODY: the one-shot hook (a bash script body, run with the real git on PATH).
set_hook() {
  printf '#!/usr/bin/env bash\nset -e\n%s\n' "$1" >"$TEST_DIR/on-merge-base"
  chmod +x "$TEST_DIR/on-merge-base"
}

make_notifier() {
  cat >"$TEST_DIR/osascript-stub" <<EOF
#!/usr/bin/env bash
n=\$#
printf '%s | %s\n' "\${@:\$((n-1)):1}" "\${@:\$n:1}" >>"$TEST_DIR/notify.log"
EOF
  chmod +x "$TEST_DIR/osascript-stub"
  export PG_REPO_RUNNER_OSASCRIPT_BIN="$TEST_DIR/osascript-stub"
}

# write_verdict SHA RESULT [INPUTS]: a stored check verdict of nix-alpha, in the shape
# rr_verdict_write stores.
write_verdict() {
  mkdir -p "$STATE/verdicts/nix-alpha"
  jq -n --arg sha "$1" --arg result "$2" --arg inputs "${3:-locked}" \
    '{repo: "nix-alpha", sha: $sha, kind: "check", result: $result, inputs: $inputs,
      startedAt: "2026-10-28T00:00:00Z", finishedAt: "2026-10-28T00:01:00Z", exit: 0,
      logPath: "", durationS: 60}' >"$STATE/verdicts/nix-alpha/$1.check.json"
}

go_live() { mkdir -p "$STATE/push-live" && : >"$STATE/push-live/nix-alpha"; }

sync_pass() { PG_ROUTER_ITEM_ID="${1:-nix-alpha}" run "${RUNNER[@]}" sync; }

rec() { jq -r ".$2" "$STATE/sync/$1.json"; }

remote_main() { git -C "$REMOTE" rev-parse refs/heads/main; }

remote_refs() { git -C "$REMOTE" for-each-ref --format='%(refname) %(objectname)'; }

runner_beads() {
  bd -C "$TDIR" list --metadata-field runner=pg-repo-runner --metadata-field "repo=$1" \
    --status all -n 0 --json |
    jq -c --argjson base "$BASE_IDS" \
      '(if type=="object" and has("data") then .data else . end) | map(select(.id as $i | $base | index($i) | not))'
}

# push_lines: the recorded git argv lines whose subcommand is push.
push_lines() {
  [[ -f $TEST_DIR/git.argv ]] || return 0
  sed -E 's/^-C [^ ]+ //' "$TEST_DIR/git.argv" | grep -nE '^push( |$)' || true
}

# no_push: the runner ran no git push, wrote no remote ref and ran no pre-push hook.
no_push() {
  local before="$1"
  if [[ -n "$(push_lines)" ]]; then
    echo "the runner ran a git push:"
    cat "$TEST_DIR/git.argv"
    return 1
  fi
  [ ! -e "$TEST_DIR/pre-push.ran" ]
  [ "$(remote_refs)" = "$before" ]
}

# assert_argv_clean: whatever the runner ran, no argv in the recording carries a
# forbidden flag, a + refspec other than the fetch's own, or a ref other than the
# primary branch; and every push line is exactly the one allowed shape.
assert_argv_clean() {
  local bad line a_real
  a_real="$(realpath "$A")"
  [[ -f $TEST_DIR/git.argv ]] || return 0
  if grep -qE -- '--force|force-with-lease|--no-verify|--delete|--mirror|--tags|--all|--prune|--follow-tags|--atomic| -f( |$)| -d( |$)| -n( |$)|--push-option| -o ' "$TEST_DIR/git.argv"; then
    echo "a forbidden flag is in a recorded git argv:"
    cat "$TEST_DIR/git.argv"
    return 1
  fi
  bad="$(grep -E ' \+|:\+' "$TEST_DIR/git.argv" |
    grep -vE '^(-C [^ ]+ )?fetch .* \+refs/heads/[^:]+:refs/remotes/origin/[^ ]+$' || true)"
  [ -z "$bad" ]
  while IFS= read -r line; do
    [[ $line =~ ^-C\ [^\ ]+\ push\  ]] || continue
    [[ $line =~ ^-C\ $a_real\ push\ origin\ [0-9a-f]{40}:refs/heads/main$ ]] || {
      echo "a push argv is not the one allowed shape: $line"
      return 1
    }
  done <"$TEST_DIR/git.argv"
  if grep -qE 'pn workspace|workspace (push|update|apply)' "$TEST_DIR/git.argv"; then return 1; fi
}

# ---- the happy path: exactly the refspec, only when every gate holds ----------

@test "all gates hold and the dry run is lifted: pushes exactly <sha>:refs/heads/main and nothing else" {
  local tip refs_before
  commit_file "$A" f
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  write_verdict "$tip" green
  go_live
  # a second ref on the remote must stay as it is
  git -C "$A" push -q origin "$(git -C "$A" rev-parse HEAD~1):refs/heads/side"
  rm -f "$TEST_DIR/pre-push.ran"
  refs_before="$(remote_refs)"
  sync_pass
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = pushed ]
  [ "$(remote_main)" = "$tip" ]
  # no other ref changed
  [ "$(remote_refs | grep -v '^refs/heads/main ')" = "$(grep -v '^refs/heads/main ' <<<"$refs_before")" ]
  # the exact argv, once
  [ "$(grep -c -E '^-C [^ ]+ push ' "$TEST_DIR/git.argv")" -eq 1 ]
  grep -qxF -- "-C $(realpath "$A") push origin $tip:refs/heads/main" "$TEST_DIR/git.argv"
  # the repo's own hook ran (no --no-verify)
  [ -e "$TEST_DIR/pre-push.ran" ]
  assert_argv_clean
  # the next pass is in sync: no second push
  rm -f "$TEST_DIR/git.argv"
  sync_pass
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = in-sync ]
  [ -z "$(push_lines)" ]
}

@test "a verdict written by the real verify role opens the gate" {
  local tip fake_nix
  write_config push=true verify=true
  fake_nix="$TEST_DIR/fake-nix"
  printf '#!/bin/sh\nexit 0\n' >"$fake_nix"
  chmod +x "$fake_nix"
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  PG_REPO_RUNNER_NIX_BIN="$fake_nix" PG_ROUTER_ITEM_ID="nix-alpha@$tip" run "${RUNNER[@]}" verify
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$STATE/verdicts/nix-alpha/$tip.check.json")" = green ]
  go_live
  sync_pass
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = pushed ]
  [ "$(remote_main)" = "$tip" ]
}

# ---- audit ----------------------------------------------------------------

@test "a push writes one audit record: repo, remote, old and new remote sha, verdict reference, time" {
  local tip old
  old="$(remote_main)"
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  write_verdict "$tip" green
  go_live
  sync_pass
  [ "$status" -eq 0 ]
  # the structured line also goes to stderr (bats merges it into $output)
  [[ $output == *"push-audit {"* ]]
  [ "$(wc -l <"$STATE/push-audit.jsonl" | tr -d ' ')" -eq 1 ]
  local r="$STATE/push-audit.jsonl"
  [ "$(jq -r .event "$r")" = push ]
  [ "$(jq -r .repo "$r")" = nix-alpha ]
  [ "$(jq -r .remote "$r")" = "$REMOTE" ]
  [ "$(jq -r .branch "$r")" = main ]
  [ "$(jq -r .oldRemoteSha "$r")" = "$old" ]
  [ "$(jq -r .newRemoteSha "$r")" = "$tip" ]
  [ "$(jq -r .verdict.sha "$r")" = "$tip" ]
  [ "$(jq -r .verdict.result "$r")" = green ]
  [ "$(jq -r .verdict.path "$r")" = "$STATE/verdicts/nix-alpha/$tip.check.json" ]
  [[ "$(jq -r .at "$r")" =~ ^20[0-9]{2}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z$ ]]
  # the same record is in the run log
  grep -q "push-audit .*\"newRemoteSha\":\"$tip\"" "$STATE/run.log"
}

@test "a remote url that carries credentials is stored without them" {
  local tip
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  write_verdict "$tip" green
  go_live
  # the same bare remote through a url with userinfo is not a valid local path, so
  # only the sanitiser is exercised: a pushurl is ignored by get-url, so use insteadOf
  git -C "$A" config url."$REMOTE".insteadOf "file://user:secret@host/alpha.git"
  git -C "$A" remote set-url origin "file://user:secret@host/alpha.git"
  sync_pass
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = pushed ]
  [[ "$(jq -r .remote "$STATE/push-audit.jsonl")" != *secret* ]]
}

# ---- no push without the authorization ------------------------------------

@test "push = false: nothing is pushed even with a green verdict and the dry run lifted" {
  local tip before
  write_config push=false
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  write_verdict "$tip" green
  go_live
  before="$(remote_refs)"
  sync_pass
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = ahead ]
  no_push "$before"
  assert_argv_clean
}

@test "the kill-switch file in the state directory stops the pass before any git runs" {
  local tip before
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  write_verdict "$tip" green
  go_live
  mkdir -p "$STATE"
  : >"$STATE/push.disabled"
  before="$(remote_refs)"
  sync_pass
  [ "$status" -eq 9 ]
  no_push "$before"
  # removing it takes effect on the very next pass, no restart
  rm "$STATE/push.disabled"
  sync_pass
  [ "$status" -eq 0 ]
  [ "$(remote_main)" = "$tip" ]
}

@test "the kill-switch file named by the config stops the pass too" {
  local tip before
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  write_verdict "$tip" green
  go_live
  : >"$TEST_DIR/config-kill"
  before="$(remote_refs)"
  sync_pass
  [ "$status" -eq 9 ]
  no_push "$before"
}

@test "a kill switch that appears in the middle of the pass withholds the push" {
  local tip before
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  write_verdict "$tip" green
  go_live
  set_hook ": >\"$STATE/push.disabled\""
  before="$(remote_refs)"
  sync_pass
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = ahead ]
  [[ "$(rec nix-alpha detail)" == *"kill switch present"* ]]
  no_push "$before"
}

# ---- no push without a green verdict for exactly the tip -------------------

@test "no verdict: not pushed" {
  local before
  commit_file "$A" f
  go_live
  before="$(remote_refs)"
  sync_pass
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = ahead ]
  [[ "$(rec nix-alpha detail)" == "push withheld: no green check verdict"* ]]
  no_push "$before"
}

@test "a red verdict: not pushed (and no relock transition without a consumed sibling)" {
  local tip before
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  write_verdict "$tip" red
  go_live
  before="$(remote_refs)"
  sync_pass
  [ "$status" -eq 0 ]
  [[ "$(rec nix-alpha detail)" == *"is red"* ]]
  no_push "$before"
  [ "$(runner_beads nix-alpha | jq length)" -eq 0 ]
}

@test "error, superseded and override-input verdicts do not open the gate" {
  local tip before r
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  go_live
  before="$(remote_refs)"
  for r in error superseded; do
    write_verdict "$tip" "$r"
    sync_pass
    [ "$status" -eq 0 ]
    no_push "$before"
  done
  write_verdict "$tip" green override
  sync_pass
  [ "$status" -eq 0 ]
  no_push "$before"
}

@test "a green verdict for an OLDER sha does not push a newer tip, and no partial push of the green ancestor" {
  local old tip before
  commit_file "$A" f
  old="$(git -C "$A" rev-parse HEAD)"
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  write_verdict "$old" green
  go_live
  before="$(remote_refs)"
  sync_pass
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = ahead ]
  no_push "$before"
  [ "$(remote_main)" != "$old" ]
  [ "$(remote_main)" != "$tip" ]
}

@test "the tip moves while the gates are being checked: not pushed" {
  local tip before
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  write_verdict "$tip" green
  go_live
  set_hook "$REAL_GIT -C \"$A\" commit -q --allow-empty -m moved"
  before="$(remote_refs)"
  sync_pass
  [ "$status" -eq 0 ]
  [ "$(git -C "$A" rev-parse HEAD)" != "$tip" ]
  [[ "$(rec nix-alpha detail)" == *"the tip moved"* ]]
  no_push "$before"
  assert_argv_clean
}

# ---- no push from a canonical clone that is not steady ---------------------

@test "a dirty canonical clone: the pass yields and nothing is pushed" {
  local tip before
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  write_verdict "$tip" green
  go_live
  echo dirty >>"$A/f"
  before="$(remote_refs)"
  sync_pass
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = yielded ]
  no_push "$before"
}

@test "a canonical clone off its primary branch: nothing is pushed" {
  local tip before
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  write_verdict "$tip" green
  go_live
  git -C "$A" checkout -q -b feature
  before="$(remote_refs)"
  sync_pass
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = yielded ]
  no_push "$before"
}

# ---- no push unless the remote is an ancestor -------------------------------

@test "a diverged repo (the remote is not an ancestor): not pushed, not forced, surfaced" {
  local tip before
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  write_verdict "$tip" green
  go_live
  gfh_clone "$REMOTE" "$TEST_DIR/other" "pg-repo-runner"
  commit_file "$TEST_DIR/other" g
  git -C "$TEST_DIR/other" push -q origin main
  before="$(remote_refs)"
  sync_pass
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = diverged ]
  no_push "$before"
  [ "$(git -C "$A" rev-parse HEAD)" = "$tip" ]
  [ "$(runner_beads nix-alpha | jq length)" -eq 1 ]
  assert_argv_clean
}

@test "a remote that moves after the ancestry check: the push is refused by git, never forced" {
  local tip other_tip
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  write_verdict "$tip" green
  go_live
  gfh_clone "$REMOTE" "$TEST_DIR/other" "pg-repo-runner"
  set_hook "echo g >\"$TEST_DIR/other/g\"; $REAL_GIT -C \"$TEST_DIR/other\" add g; $REAL_GIT -C \"$TEST_DIR/other\" commit -q -m other; $REAL_GIT -C \"$TEST_DIR/other\" push -q origin main"
  sync_pass
  [ "$status" -eq 0 ]
  other_tip="$(git -C "$TEST_DIR/other" rev-parse HEAD)"
  [ "$(rec nix-alpha outcome)" = push-rejected ]
  [ "$(remote_main)" = "$other_tip" ]
  [ "$(git -C "$A" rev-parse HEAD)" = "$tip" ]
  # one attempt only, with the one allowed shape
  [ "$(grep -c -E '^-C [^ ]+ push ' "$TEST_DIR/git.argv")" -eq 1 ]
  assert_argv_clean
}

# ---- dry run ---------------------------------------------------------------

@test "dry run is the default: the range is logged and nothing is pushed until the flag file exists" {
  local tip old before
  old="$(remote_main)"
  commit_file "$A" f
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  write_verdict "$tip" green
  before="$(remote_refs)"
  sync_pass
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = push-dry-run ]
  [[ "$(rec nix-alpha detail)" == "dry run: would push $old..$tip (2 commit(s))"* ]]
  grep -q "dry run: would push $old..$tip (2 commit(s)) to origin refs/heads/main; nothing pushed" "$STATE/run.log"
  [ "$(grep -c 'dry run: would push [0-9a-f]\{7,\} change f' "$STATE/run.log")" -eq 2 ]
  no_push "$before"
  [ ! -e "$STATE/push-audit.jsonl" ]
  # the operator removes the dry run for this repo: the next pass pushes
  go_live
  sync_pass
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = pushed ]
  [ "$(remote_main)" = "$tip" ]
}

@test "dry_run = false in the config lifts the dry run, and dry_run = true keeps it" {
  local tip before
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  write_verdict "$tip" green
  before="$(remote_refs)"
  write_config push=true dry_run=true
  sync_pass
  [ "$(rec nix-alpha outcome)" = push-dry-run ]
  no_push "$before"
  write_config push=true dry_run=false
  sync_pass
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = pushed ]
  [ "$(remote_main)" = "$tip" ]
}

# ---- consumer red under locked inputs: needs relock ------------------------

@test "a red consumer tip while a consumed sibling is unpublished: blocked, needs sibling push and relock" {
  local tip before id
  write_config push=true consumes='"nix-beta"'
  commit_file "$B" f
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  write_verdict "$tip" red
  go_live
  before="$(remote_refs)"
  sync_pass
  [ "$status" -eq 0 ]
  [[ "$(rec nix-alpha detail)" == *"blocked, needs sibling push and relock"*"pn workspace push"* ]]
  no_push "$before"
  [ "$(runner_beads nix-alpha | jq length)" -eq 1 ]
  [ "$(runner_beads nix-alpha | jq -r '.[0].metadata.kind')" = needs-relock ]
  [[ "$(runner_beads nix-alpha | jq -r '.[0].title')" == *"push blocked: needs relock"* ]]
  # a repeat is the same transition: no second bead
  sync_pass
  [ "$(runner_beads nix-alpha | jq length)" -eq 1 ]
  # the sibling is published (by the operator: not by the runner) and a green verdict for
  # a later tip arrives: the push goes out and the runner closes its own bead
  id="$(runner_beads nix-alpha | jq -r '.[0].id')"
  git -C "$B" push -q origin main
  commit_file "$A" f
  tip="$(git -C "$A" rev-parse HEAD)"
  write_verdict "$tip" green
  sync_pass
  [ "$status" -eq 0 ]
  [ "$(rec nix-alpha outcome)" = pushed ]
  [ "$(remote_main)" = "$tip" ]
  [ "$(bd -C "$TDIR" show "$id" --json | jq -r '(if type=="object" and has("data") then .data else . end) | (if type=="array" then .[0] else . end) | .status')" = closed ]
}

# ---- source guard: the one push, nothing else ------------------------------

@test "the role code runs git push in exactly one place, with exactly the allowed argv" {
  local f
  # code lines only: comments and the --help heredoc dropped
  code() { sed -e "/<<'HELP'/,/^HELP\$/d" -e '/^[[:space:]]*#/d' "$1"; }
  # exactly one code line pushes, it is the allowed argv, and the line before it is the
  # git binary call of rr_push_exec
  [ "$(code "$SCRIPTS_DIR/pg-repo-runner.bash" | grep -c -F ' push origin ')" -eq 1 ]
  code "$SCRIPTS_DIR/pg-repo-runner.bash" | grep -B1 -F -- '-C "$1" push origin "$2:refs/heads/$3"' | head -n 1 | grep -qF '"${RR_GIT_BIN:-git}"'
  for f in "$SCRIPTS_DIR/pg-repo-runner.sh" "$SCRIPTS_DIR/pg-repo-runner.bash"; do
    run bash -c "$(declare -f code); code \"\$1\" | grep -v ' fetch --quiet ' | grep -nE -- '--force|force-with-lease|--no-verify|--delete|--mirror|--tags|reset --hard|\"\\+refs|pn +workspace +(update|apply)'" _ "$f"
    [ "$status" -eq 1 ] || {
      echo "$f: $output"
      return 1
    }
    # the only `pn workspace push` text is the advice in a message, never a command
    run bash -c "$(declare -f code); code \"\$1\" | grep -E 'pn +workspace +push' | grep -vE 'run pn workspace push'" _ "$f"
    [ "$status" -eq 1 ]
  done
  # the sync role body (the entry point) never hands push to git
  run bash -c "$(declare -f code); code \"\$1\" | grep -nE '(rr_git|RR_GIT_BIN|\\bgit)\\b.*\\bpush\\b'" _ "$SCRIPTS_DIR/pg-repo-runner.sh"
  [ "$status" -eq 1 ]
}
