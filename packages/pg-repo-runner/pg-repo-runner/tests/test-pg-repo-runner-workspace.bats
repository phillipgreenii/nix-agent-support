#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Runner C of the async-runners design (bead pg2-xg9ppr, F8): the `workspace-tips`
# source (the Debounce) and the `build` role (a throwaway coordinated workforest set,
# `pn workspace build`, a verdict, removal, leftover cleanup). A miniature workspace
# (three tiny repos, real git, bare local remotes, a real pn-workspace.toml) with the
# REAL pn where one is on PATH, else a stand-in that does the same set operations with
# real git (see test-support/workspace_fixture.bash, RR_TEST_PN=fake forces it). The
# build command is a script of the fixture, so no nix evaluation happens and the suite
# is fast. Nothing here pushes, and every repo in the fixture config has push = false.
# The red-build bead is in test-pg-repo-runner-workspace-surface.bats.

bats_require_minimum_version 1.5.0

if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi
if [[ -z ${TEST_SUPPORT:-} ]]; then
  TEST_SUPPORT="$(cd "${SCRIPTS_DIR}/../test-support" && pwd)"
fi
load "$TEST_SUPPORT/workspace_fixture"

setup() {
  if [[ -z ${GFH_LIB:-} ]]; then
    echo "GFH_LIB is not set: run under the workspace .envrc (direnv) or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # shellcheck disable=SC1090,SC1091
  source "$GFH_LIB"
  gfh_save_env SCRIPTS_DIR SCRIPT_UNDER_TEST TEST_SUPPORT GFH_LIB RR_TEST_PN
  gfh_setup "pg-repo-runner"
  gfh_restore_env

  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    RUNNER=("$SCRIPT_UNDER_TEST")
  else
    RUNNER=(bash -euo pipefail "$SCRIPTS_DIR/pg-repo-runner.sh")
  fi
  TEST_DIR="$GFH_WORK"
  ws_fixture_setup
  # pn must NEVER be wrapped in pg-nix-log-wrapped: a sentinel records any call
  cat >"$TEST_DIR/wrapper-sentinel" <<EOF
#!/usr/bin/env bash
echo "\$*" >>"$TEST_DIR/wrapper.called"
exec "\$@"
EOF
  chmod +x "$TEST_DIR/wrapper-sentinel"
  export RR_NIX_LOG_WRAPPED_BIN="$TEST_DIR/wrapper-sentinel"
}

teardown() {
  [[ -z ${RR_DEBUG:-} ]] || cat "$STATE/run.log" >&3
  gfh_teardown
}

# tips_at EPOCH: one workspace-tips run at the (seam) time EPOCH.
tips_at() {
  PG_REPO_RUNNER_EPOCH="$1" run "${RUNNER[@]}" workspace-tips
}

# build_count: how many times the fixture's build command ran.
build_count() {
  if [[ -f $TEST_DIR/build.count ]]; then wc -l <"$TEST_DIR/build.count" | tr -d ' '; else echo 0; fi
}

# ---- workspace-tips: the Debounce -------------------------------------------

@test "a burst of tip changes emits exactly one event, for the final tuple, after the quiet window" {
  local t0=1793000000 id
  # t0: first sight of the tuple starts the window; nothing is emitted
  tips_at "$t0"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  # a burst: alpha moves at +300, beta at +600
  ws_commit_in "$WS/nix-alpha"
  tips_at $((t0 + 300))
  [ -z "$output" ]
  ws_commit_in "$WS/nix-beta"
  tips_at $((t0 + 600))
  [ -z "$output" ]
  # the final tuple has been quiet for 300 and 600 seconds: still waiting
  tips_at $((t0 + 900))
  [ -z "$output" ]
  tips_at $((t0 + 1199))
  [ -z "$output" ]
  # 900 s after the LAST change (t0 + 600): offered
  tips_at $((t0 + 1500))
  [ "$status" -eq 0 ]
  [ "$(wc -l <<<"$output" | tr -d ' ')" -eq 1 ]
  id="$(jq -r .id <<<"$output")"
  [ "$id" = "workspace@$(ws_hash)" ]
  [ "$(jq -r .type <<<"$output")" = workspace-tips ]
  [ "$(jq -r '.metadata.hash' <<<"$output")" = "$(ws_hash)" ]
  [ "$(jq -r '.metadata.tips["nix-alpha"]' <<<"$output")" = "$(git -C "$WS/nix-alpha" rev-parse HEAD)" ]
  [ "$(jq -r '.metadata.branch' <<<"$output")" = main ]
  [ "$(jq -r .expiresAt <<<"$output")" != "$(jq -r .at <<<"$output")" ]
  # later ticks re-offer the SAME event (same id), never a second one
  tips_at $((t0 + 1800))
  [ "$(jq -r .id <<<"$output")" = "$id" ]
  # the whole burst produced one distinct id, and nothing but the final tuple
  [ "$(ws_hash)" = "$(ws_tips | sha256sum | cut -d' ' -f1)" ]
}

@test "a tip that changes inside the quiet window restarts it" {
  local t0=1793000000
  tips_at "$t0"
  tips_at $((t0 + 800))
  [ -z "$output" ]
  ws_commit_in "$WS/nix-gamma"
  # 900 s after the FIRST sight, but the change is first seen now (+900): the window restarts
  tips_at $((t0 + 900))
  [ -z "$output" ]
  tips_at $((t0 + 1799))
  [ -z "$output" ]
  tips_at $((t0 + 1800))
  [ "$(jq -r .id <<<"$output")" = "workspace@$(ws_hash)" ]
}

@test "the quiet window is the config value, and an environment value overrides it" {
  local t0=1793000000
  tips_at "$t0"
  PG_REPO_RUNNER_QUIET_WINDOW=60 tips_at $((t0 + 59))
  [ -z "$output" ]
  PG_REPO_RUNNER_QUIET_WINDOW=60 tips_at $((t0 + 60))
  [ "$(jq -r .id <<<"$output")" = "workspace@$(ws_hash)" ]
}

@test "a tuple that already has a build verdict is not offered again" {
  local t0=1793000000 h
  h="$(ws_hash)"
  tips_at "$t0"
  tips_at $((t0 + 1000))
  [ "$(jq -r .id <<<"$output")" = "workspace@$h" ]
  mkdir -p "$STATE/verdicts/workspace"
  jq -n --arg h "$h" '{repo: "workspace", sha: $h, kind: "build", result: "green", inputs: "override", startedAt: "a", finishedAt: "b", exit: 0, logPath: ""}' \
    >"$STATE/verdicts/workspace/$h.build.json"
  tips_at $((t0 + 2000))
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  # a NEW tuple is a new event once it has been quiet
  ws_commit_in "$WS/nix-alpha"
  tips_at $((t0 + 3000))
  [ -z "$output" ]
  tips_at $((t0 + 4000))
  [ "$(jq -r .id <<<"$output")" = "workspace@$(ws_hash)" ]
  [ "$(ws_hash)" != "$h" ]
}

@test "a repo that is not opted in to build keeps the source silent and the log says why" {
  ws_write_config nix-beta
  tips_at 1793000000
  tips_at 1793009000
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  grep -q "workspace-tips: not emitting: build is not enabled for repo 'nix-beta'" "$STATE/run.log"
}

@test "workspace-tips takes no arguments and needs no lease, load or kill switch" {
  run "${RUNNER[@]}" workspace-tips extra
  [ "$status" -eq 2 ]
  mkdir -p "$STATE"
  touch "$STATE/push.disabled"
  ln -s "999999:Thu Jan 1 00:00:00 1970" "$STATE/lease"
  PG_REPO_RUNNER_LOADAVG=99 tips_at 1793000000
  [ "$status" -eq 0 ]
}

# ---- build: refusals and idempotence ----------------------------------------

@test "build needs an item id workspace@<64 hex digits>" {
  run env -u PG_ROUTER_ITEM_ID "${RUNNER[@]}" build
  [ "$status" -eq 2 ]
  [[ $output == *"PG_ROUTER_ITEM_ID is not set"* ]]
  PG_ROUTER_ITEM_ID="nix-alpha@$(ws_hash)" run "${RUNNER[@]}" build
  [ "$status" -eq 2 ]
  PG_ROUTER_ITEM_ID="workspace@abc1234" run "${RUNNER[@]}" build
  [ "$status" -eq 2 ]
  PG_ROUTER_ITEM_ID="workspace" run "${RUNNER[@]}" build
  [ "$status" -eq 2 ]
  [ "$(build_count)" -eq 0 ]
}

@test "a repo that is not opted in refuses the whole run before anything is created" {
  ws_write_config nix-beta
  local before
  before="$(ws_state)"
  ws_build
  [ "$status" -eq 2 ]
  [[ $output == *"workspace build refused: build is not enabled for repo 'nix-beta'"* ]]
  [ ! -d "$WS/.workforests" ] || [ -z "$(ls -A "$WS/.workforests")" ]
  [ -z "$(ws_sets)" ]
  [ "$(build_count)" -eq 0 ]
  [ "$(ws_state)" = "$before" ]
}

@test "a repo that is not in the allow-list refuses the run" {
  # nix-gamma is a workspace repo with no [repos.nix-gamma] entry in the runner config
  sed -i.bak '/^\[repos.nix-gamma\]/,$d' "$TEST_DIR/config.toml"
  ws_build
  [ "$status" -eq 2 ]
  [[ $output == *"repo 'nix-gamma' is not in the allow-list"* ]]
  [ "$(build_count)" -eq 0 ]
}

@test "a verdict that already exists ends the run at once: no set, no pn" {
  ws_build
  [ "$status" -eq 0 ]
  [ "$(build_count)" -eq 1 ]
  local f before
  f="$(ws_verdict_file)"
  before="$(cat "$f")"
  ws_build
  [ "$status" -eq 0 ]
  [ "$(build_count)" -eq 1 ]
  [ "$(cat "$f")" = "$before" ]
  grep -q "skipped: a build verdict for workspace@" "$STATE/run.log"
}

@test "the kill switch and the lease keep a build from starting" {
  mkdir -p "$STATE"
  touch "$STATE/push.disabled"
  ws_build
  [ "$status" -eq 9 ]
  rm "$STATE/push.disabled"
  sleep 30 &
  local sleeper=$!
  ln -s "$sleeper:$(ps -o lstart= -p "$sleeper" | tr -s ' ')" "$STATE/lease"
  ws_build
  kill "$sleeper"
  [ "$status" -eq 9 ]
  [ "$(build_count)" -eq 0 ]
  [ -z "$(ws_sets)" ]
}

@test "a tuple the tips have moved past is superseded: no set, no pn" {
  local old
  old="$(ws_hash)"
  ws_commit_in "$WS/nix-alpha"
  ws_build "$old"
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(ws_verdict_file "$old")")" = superseded ]
  [ "$(jq -r .supersededBy "$(ws_verdict_file "$old")")" = "$(ws_hash)" ]
  [ "$(build_count)" -eq 0 ]
  [ -z "$(ws_sets)" ]
}

# ---- build: the set lifecycle ------------------------------------------------

@test "the set is created at the tips, built in, and removed; the verdict is green" {
  local h before
  h="$(ws_hash)"
  before="$(ws_state)"
  ws_build
  [ "$status" -eq 0 ] || {
    echo "$output"
    cat "$STATE/run.log"
    return 1
  }
  # what the build command saw: a set root under .workforests, the reserved prefix, cwd in it
  local root cwd
  root="$(sed -n 's/^root=//p' "$TEST_DIR/build.record")"
  cwd="$(sed -n 's/^cwd=//p' "$TEST_DIR/build.record")"
  [[ $root == */ws/.workforests/runner-build-* ]]
  [[ $cwd == */ws/.workforests/runner-build-*/nix-gamma ]]
  [ "$(basename "$(dirname "$cwd")")" = "$(basename "$root")" ]
  # the set held every repo of the workspace, each at exactly the tip of the tuple
  local k
  for k in "${WS_REPOS[@]}"; do
    [ "$(sed -n "s/^head.$k=//p" "$TEST_DIR/build.record")" = "$(git -C "$WS/$k" rev-parse refs/heads/main)" ]
    [[ $(sed -n 's/^lsroot=//p' "$TEST_DIR/build.record") == *"$k"* ]]
  done
  # under nice
  [ "$(sed -n 's/^nice=//p' "$TEST_DIR/build.record")" -ge 10 ]
  # and pn was never wrapped
  [ ! -e "$TEST_DIR/wrapper.called" ]
  # the verdict, keyed by the tuple
  local f
  f="$(ws_verdict_file "$h")"
  [ "$(jq -r .result "$f")" = green ]
  [ "$(jq -r .kind "$f")" = build ]
  [ "$(jq -r .inputs "$f")" = override ]
  [ "$(jq -r .sha "$f")" = "$h" ]
  [ "$(jq -r .exit "$f")" = 0 ]
  [ "$(jq -r '.tips["nix-beta"]' "$f")" = "$(git -C "$WS/nix-beta" rev-parse refs/heads/main)" ]
  [[ $(jq -r .set "$f") == runner-build-* ]]
  [ "$(jq '.durationS >= 0' "$f")" = true ]
  [ -f "$(jq -r .logPath "$f")" ]
  # the set and its branches are gone, the lease is released
  [ -z "$(ws_sets)" ]
  [ ! -e "$STATE/lease" ]
  # the canonical clones are byte-identical: working trees, .git, refs
  [ "$(ws_state)" = "$before" ]
  # status lists the verdict
  run "${RUNNER[@]}" status
  [[ $output == *"workspace"*"build"*"green"* ]]
}

@test "a red build is a red verdict, the run exits 0, the set is removed and the clones are untouched" {
  local before
  before="$(ws_state)"
  echo 3 >"$TEST_DIR/build.exit"
  ws_build
  [ "$status" -eq 0 ]
  local f
  f="$(ws_verdict_file)"
  [ "$(jq -r .result "$f")" = red ]
  [ "$(jq -r .exit "$f")" = 3 ]
  [ -f "$(jq -r .logPath "$f")" ]
  [ -z "$(ws_sets)" ]
  [ "$(ws_state)" = "$before" ]
}

@test "pn answering busy (exit 75) is not a verdict: the run exits 9 and the set is removed" {
  echo 75 >"$TEST_DIR/build.exit"
  ws_build
  [ "$status" -eq 9 ]
  [ ! -e "$(ws_verdict_file)" ]
  [ -z "$(ws_sets)" ]
  [ ! -e "$STATE/lease" ]
}

@test "a pn that cannot run is a malfunction: exit 1, no verdict, nothing left behind" {
  local before
  before="$(ws_state)"
  PG_REPO_RUNNER_PN_BIN="$TEST_DIR/no-such-pn" ws_build
  [ "$status" -eq 1 ]
  [ ! -e "$(ws_verdict_file)" ]
  [ -z "$(ws_sets)" ]
  [ "$(ws_state)" = "$before" ]
}

@test "a tip that moves while the set is created: the set is removed and no verdict is stored" {
  cat >"$TEST_DIR/mover-pn" <<EOF
#!/usr/bin/env bash
if [[ "\${1:-} \${2:-} \${3:-}" == "workspace workforest add" && -e "$TEST_DIR/mover.armed" ]]; then
  rm -f "$TEST_DIR/mover.armed"
  echo moved >>"$WS/nix-alpha/f"
  git -C "$WS/nix-alpha" add f
  git -C "$WS/nix-alpha" commit -q -m "landed during the set creation"
fi
exec "$PN_REAL" "\$@"
EOF
  chmod +x "$TEST_DIR/mover-pn"
  touch "$TEST_DIR/mover.armed"
  local h
  h="$(ws_hash)"
  PG_REPO_RUNNER_PN_BIN="$TEST_DIR/mover-pn" ws_build "$h"
  [ "$status" -eq 0 ]
  [ ! -e "$(ws_verdict_file "$h")" ]
  [ "$(build_count)" -eq 0 ]
  [ -z "$(ws_sets)" ]
  grep -q "a tip moved while the set was created" "$STATE/run.log"
  # the new tuple builds normally
  PG_REPO_RUNNER_PN_BIN="$TEST_DIR/mover-pn" ws_build
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(ws_verdict_file)")" = green ]
}

# ---- build: crash and leftovers ---------------------------------------------

@test "a crash leaves a leftover set, and the next run removes it before it builds" {
  local before h
  before="$(ws_state)"
  h="$(ws_hash)"
  # a pn whose build SIGKILLs the runner itself (no trap can run): the set stays behind
  cat >"$TEST_DIR/crash-pn" <<EOF
#!/usr/bin/env bash
if [[ "\${1:-} \${2:-}" == "workspace build" && -e "$TEST_DIR/crash.armed" ]]; then
  rm -f "$TEST_DIR/crash.armed"
  runner="\$(sed -n 's/.* pid=\([0-9]*\) role=build .* admitted\$/\1/p' "$STATE/run.log" | tail -n 1)"
  kill -9 "\$runner"
  exit 137
fi
exec "$PN_REAL" "\$@"
EOF
  chmod +x "$TEST_DIR/crash-pn"
  touch "$TEST_DIR/crash.armed"
  PG_REPO_RUNNER_PN_BIN="$TEST_DIR/crash-pn" ws_build "$h"
  [ "$status" -ne 0 ]
  [ ! -e "$(ws_verdict_file "$h")" ]
  # the leftover: the set directory and a branch in every canonical repo, a dead lease
  [ "$(ws_sets | grep -c '^runner-build-')" -eq 4 ]
  [ -L "$STATE/lease" ]
  # the next run (a later start) removes it first, then builds and removes its own set
  PG_REPO_RUNNER_EPOCH=1793000100 PG_REPO_RUNNER_PN_BIN="$TEST_DIR/crash-pn" ws_build "$h"
  [ "$status" -eq 0 ] || {
    echo "$output"
    cat "$STATE/run.log"
    return 1
  }
  grep -q "workspace: removing leftover set runner-build-" "$STATE/run.log"
  [ "$(jq -r .result "$(ws_verdict_file "$h")")" = green ]
  [ -z "$(ws_sets)" ]
  [ ! -e "$STATE/lease" ]
  [ "$(ws_state)" = "$before" ]
}

@test "leftovers: only reserved-prefix sets older than this run go; other sets and newer ones stay" {
  local now=1793000000 old newer
  old="runner-build-$(date -u -d "@$((now - 86400))" +%Y%m%dT%H%M%SZ)"
  newer="runner-build-$(date -u -d "@$((now + 86400))" +%Y%m%dT%H%M%SZ)"
  PN_WORKSPACE_ROOT="$WS" "$PN_REAL" workspace workforest add "$old" refs/heads/main --no-hooks >/dev/null 2>&1
  PN_WORKSPACE_ROOT="$WS" "$PN_REAL" workspace workforest add "$newer" refs/heads/main --no-hooks >/dev/null 2>&1
  PN_WORKSPACE_ROOT="$WS" "$PN_REAL" workspace workforest add agent-owned-set refs/heads/main --no-hooks >/dev/null 2>&1
  # an orphan branch of a crashed add (no set directory) in one repo only
  git -C "$WS/nix-beta" branch runner-build-20200101T000000Z refs/heads/main
  ws_build
  [ "$status" -eq 0 ]
  # gone: the old set (directory and branches) and the orphan branch
  [ ! -d "$WS/.workforests/$old" ]
  run git -C "$WS/nix-alpha" show-ref --verify -q "refs/heads/$old"
  [ "$status" -ne 0 ]
  run git -C "$WS/nix-beta" show-ref --verify -q refs/heads/runner-build-20200101T000000Z
  [ "$status" -ne 0 ]
  # kept: a set from the future (cannot be a crashed predecessor) and one without the prefix
  [ -d "$WS/.workforests/$newer" ]
  [ -d "$WS/.workforests/agent-owned-set" ]
  git -C "$WS/nix-gamma" show-ref --verify -q "refs/heads/agent-owned-set"
  grep -q "$newer is newer than this run" "$STATE/run.log"
}

@test "a set directory that was deleted by hand is cleaned up by the next run" {
  local old
  old="runner-build-$(date -u -d "@$((1793000000 - 3600))" +%Y%m%dT%H%M%SZ)"
  PN_WORKSPACE_ROOT="$WS" "$PN_REAL" workspace workforest add "$old" refs/heads/main --no-hooks >/dev/null 2>&1
  rm -rf "${WS:?}/.workforests/$old"
  ws_build
  [ "$status" -eq 0 ]
  [ -z "$(ws_sets)" ]
  [ "$(git -C "$WS/nix-alpha" worktree list | wc -l | tr -d ' ')" -eq 1 ]
}

# ---- the real pn's own refusals (a stand-in has no trust gate or wsid registry) --

@test "real pn: a canonical workspace that was never trusted is a malfunction, not a red verdict" {
  ws_require_real_pn
  PN_WORKSPACE_ROOT="$WS" pn workspace deny >/dev/null 2>&1
  ws_build
  [ "$status" -ne 0 ]
  [ ! -e "$(ws_verdict_file)" ]
  [ -z "$(ws_sets)" ]
}

@test "real pn: a workspace id the canonical workspace already registered is a malfunction, not a red verdict" {
  ws_require_real_pn
  # the canonical workspace builds once (as the operator's own pn workspace build does)
  # and so owns the wsid in the machine registry
  (cd "$WS/nix-gamma" && PN_WORKSPACE_ROOT="$WS" pn workspace build >/dev/null 2>&1)
  ws_build
  [ "$status" -eq 1 ]
  [[ $output == *"already registered to the canonical workspace"* ]]
  [ ! -e "$(ws_verdict_file)" ]
  [ -z "$(ws_sets)" ]
}
