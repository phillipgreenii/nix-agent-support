#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Runner B of the async-runners design (bead pg2-j0itft, F2): the `tips` source
# and the `verify` role of pg-repo-runner. The real script is run as a subprocess
# against a real temporary workspace (a pn-workspace.toml and real git clones of
# bare local remotes under it) and the real verdict store.
#
# Two tiers of nix. The classification tests (green, red, superseded, signal
# exit, duplicate, duration) run the script with PG_REPO_RUNNER_NIX_BIN naming a
# small recorder script that stands in for the nix binary, because what they pin
# is what the RUNNER does with nix's exit status and argv, and a signal exit
# cannot be produced on demand by nix itself. The "real nix" tests at the end run
# `nix flake check` for real on a tiny fixture flake (real git, real snapshot
# URL); they skip with a reason where no usable nix is on PATH (inside a nix build
# sandbox). Nothing here pushes, and every repo in the fixture config has
# push = false.

bats_require_minimum_version 1.5.0

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  if [[ -z ${GFH_LIB:-} ]]; then
    echo "GFH_LIB is not set: run under the workspace .envrc (direnv) or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # shellcheck disable=SC1090,SC1091
  source "$GFH_LIB"
  gfh_save_env SCRIPTS_DIR SCRIPT_UNDER_TEST TEST_SUPPORT GFH_LIB
  gfh_setup "pg-repo-runner"
  gfh_restore_env

  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    RUNNER=("$SCRIPT_UNDER_TEST")
  else
    RUNNER=(bash -euo pipefail "$SCRIPTS_DIR/pg-repo-runner.sh")
  fi

  TEST_DIR="$GFH_WORK"
  WS="$TEST_DIR/ws"
  mkdir -p "$WS" "$TEST_DIR/remotes" "$TEST_DIR/elsewhere"

  make_repo "$WS/nix-alpha" alpha
  make_repo "$WS/nix-beta" beta
  make_repo "$WS/nix-ghost" ghost
  make_repo "$TEST_DIR/elsewhere/nix-link-target" link
  ln -s "$TEST_DIR/elsewhere/nix-link-target" "$WS/nix-link"
  ALPHA_SHA="$(git -C "$WS/nix-alpha" rev-parse HEAD)"

  # nix-ghost is a clone on disk but deliberately NOT a pn-workspace.toml key.
  write_workspace nix-alpha nix-beta nix-link
  write_config "$WS"

  export XDG_STATE_HOME="$TEST_DIR/state"
  export PG_REPO_RUNNER_CONFIG="$TEST_DIR/config.toml"
  export PG_REPO_RUNNER_LOADAVG=0.50
  # the one-check-per-hour throttle is off for the suite; its own tests turn it on
  export PG_REPO_RUNNER_CHECK_INTERVAL=0
  STATE="$XDG_STATE_HOME/pg-repo-runner"
  SLEEPER=""
  make_nix_shim
  make_pn_shim
}

teardown() {
  if [[ -n ${SLEEPER:-} ]]; then
    kill "$SLEEPER" 2>/dev/null || true
  fi
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

# commit_in DIR: a new local commit (never pushed).
commit_in() {
  echo "$RANDOM$RANDOM" >>"$1/f"
  git -C "$1" add f
  git -C "$1" commit -q -m "change"
}

# write_workspace KEY...: pn-workspace.toml with these repo keys, branch main.
write_workspace() {
  local k
  {
    printf "[workspace]\nid = 'fixture'\n\n[repos]\n"
    for k in "$@"; do
      printf "[repos.%s]\nurl = 'x'\nbranch = 'main'\n" "$k"
    done
  } >"$WS/pn-workspace.toml"
}

# write_config ROOT: verify is on for alpha, link, ghost and flk; off for beta.
# push is false for EVERY repo, in every test.
write_config() {
  cat >"$TEST_DIR/config.toml" <<EOF
workspace_root = "$1"
kill_switch = "$TEST_DIR/config-kill"

[thresholds]
max_load = 11.0
nice_level = 10

[repos.nix-alpha]
verify = true
sync = false
push = false

[repos.nix-beta]
verify = false
sync = false
push = false

[repos.nix-link]
verify = true
sync = false
push = false

[repos.nix-ghost]
verify = true
sync = false
push = false

[repos.nix-flk]
verify = true
sync = false
push = false
EOF
}

# make_nix_shim: the recorder that stands in for nix in the classification tests.
# It appends its argv (one line per run) to $TEST_DIR/nix.argv, sleeps
# $SHIM_SLEEP seconds, then ends as $SHIM_MODE says: "exit" (with $SHIM_EXIT, the
# default 0) or "kill" (SIGKILL on itself, so its parent sees 137).
make_nix_shim() {
  cat >"$TEST_DIR/nix-shim" <<EOF
#!/usr/bin/env bash
echo "\$*" >>"$TEST_DIR/nix.argv"
[[ -z \${SHIM_SLEEP:-} ]] || sleep "\$SHIM_SLEEP"
echo "shim output line"
[[ -z \${SHIM_TEXT:-} ]] || printf '%b\\n' "\$SHIM_TEXT"
if [[ \${SHIM_MODE:-exit} == kill ]]; then
  kill -KILL \$\$
fi
exit "\${SHIM_EXIT:-0}"
EOF
  chmod +x "$TEST_DIR/nix-shim"
  export PG_REPO_RUNNER_NIX_BIN="$TEST_DIR/nix-shim"
}

# make_pn_shim: the recorder that stands in for pn in the override-check tests. It appends
# $PN_WORKSPACE_ROOT and its argv to $TEST_DIR/pn.argv and ends as $PN_SHIM_EXIT says
# (default 0: green with the sibling overrides). A signal exit is PN_SHIM_MODE=kill.
make_pn_shim() {
  cat >"$TEST_DIR/pn-shim" <<SHIMEOF
#!/usr/bin/env bash
echo "\$PN_WORKSPACE_ROOT :: \$*" >>"$TEST_DIR/pn.argv"
echo "pn shim output line"
[[ -z \${PN_SHIM_TEXT:-} ]] || printf '%b\\n' "\$PN_SHIM_TEXT"
if [[ \${PN_SHIM_MODE:-exit} == kill ]]; then
  kill -KILL \$\$
fi
exit "\${PN_SHIM_EXIT:-0}"
SHIMEOF
  chmod +x "$TEST_DIR/pn-shim"
  export PG_REPO_RUNNER_PN_BIN="$TEST_DIR/pn-shim"
}

pn_runs() {
  if [[ -f $TEST_DIR/pn.argv ]]; then wc -l <"$TEST_DIR/pn.argv" | tr -d ' '; else echo 0; fi
}

override_file() { # REPO SHA
  printf '%s/verdicts/%s/%s.check-override.json' "$STATE" "$1" "$2"
}

# lib_eval CODE: runs CODE with the library sourced and the state directory of the test.
lib_eval() {
  run bash -c "source '$SCRIPTS_DIR/pg-repo-runner.bash'; RR_STATE_DIR='$STATE'; $1"
}

nix_runs() {
  if [[ -f $TEST_DIR/nix.argv ]]; then wc -l <"$TEST_DIR/nix.argv" | tr -d ' '; else echo 0; fi
}

# verify [ITEM]: run the verify role (default: nix-alpha at its tip).
verify() {
  local item="${1-nix-alpha@$ALPHA_SHA}"
  PG_ROUTER_ITEM_ID="$item" run "${RUNNER[@]}" verify
}

verdict_file() { # REPO SHA
  printf '%s/verdicts/%s/%s.check.json' "$STATE" "$1" "$2"
}

assert_no_lease() {
  [ ! -e "$STATE/lease" ] && [ ! -L "$STATE/lease" ]
}

# hold_lease: a live process that is not this test owns the lease.
hold_lease() {
  sleep 300 >/dev/null 2>&1 3>&- &
  SLEEPER=$!
  mkdir -p "$STATE"
  ln -s "$SLEEPER:$(ps -o lstart= -p "$SLEEPER" | tr -s ' ')" "$STATE/lease"
}

# ---- the check verdicts ----------------------------------------------------

@test "a passing check is a green verdict, run on an immutable snapshot of the tip" {
  verify
  [ "$status" -eq 0 ]
  local f
  f="$(verdict_file nix-alpha "$ALPHA_SHA")"
  [ -f "$f" ]
  [ "$(jq -r .result "$f")" = green ]
  [ "$(jq -r .kind "$f")" = check ]
  [ "$(jq -r .inputs "$f")" = locked ]
  [ "$(jq -r .exit "$f")" = 0 ]
  [ "$(jq -r .repo "$f")" = nix-alpha ]
  [ "$(jq -r .sha "$f")" = "$ALPHA_SHA" ]
  [ "$(nix_runs)" -eq 1 ]
  # the exact command: flake check, no lock write, the commit as a git+file snapshot
  local real
  real="$(realpath "$WS")"
  [ "$(cat "$TEST_DIR/nix.argv")" = "flake check --no-write-lock-file git+file://$real/nix-alpha?rev=$ALPHA_SHA" ]
  # its output is kept where the verdict says
  [ -s "$(jq -r .logPath "$f")" ]
  grep -q 'shim output line' "$(jq -r .logPath "$f")"
  assert_no_lease
}

@test "a failing check is a red verdict, the run still exits 0, and a red is not retried" {
  local code f
  for code in 1 2 100; do
    rm -rf "$STATE/verdicts" "$TEST_DIR/nix.argv"
    SHIM_EXIT=$code verify
    [ "$status" -eq 0 ] || {
      echo "exit $code: run exited $status: $output"
      return 1
    }
    f="$(verdict_file nix-alpha "$ALPHA_SHA")"
    [ "$(jq -r .result "$f")" = red ]
    [ "$(jq -r .exit "$f")" = "$code" ]
    [ "$(nix_runs)" -eq 1 ]
  done
  # the same event again: the red stays and nix is not run a second time
  verify
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 1 ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$ALPHA_SHA")")" = red ]
}

# ---- the check throttle: at most one finished check per repo per interval ----

@test "throttle: two tips inside an hour run one check; the second is declined (exit 9), no verdict, no nix" {
  local tip2
  export PG_REPO_RUNNER_CHECK_INTERVAL=3600
  verify
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 1 ]
  commit_in "$WS/nix-alpha"
  tip2="$(git -C "$WS/nix-alpha" rev-parse HEAD)"
  verify "nix-alpha@$tip2"
  [ "$status" -eq 9 ]
  [ "$(nix_runs)" -eq 1 ]
  [ ! -e "$(verdict_file nix-alpha "$tip2")" ]
  grep -q 'busy: a check of nix-alpha finished less than 3600s ago' "$STATE/run.log"
  assert_no_lease
  # a repeat inside the window is declined again, never marked superseded or done
  verify "nix-alpha@$tip2"
  [ "$status" -eq 9 ]
  [ ! -e "$(verdict_file nix-alpha "$tip2")" ]
}

@test "throttle: once the interval has passed the newest tip is checked" {
  local tip2
  export PG_REPO_RUNNER_CHECK_INTERVAL=3600
  verify
  [ "$status" -eq 0 ]
  commit_in "$WS/nix-alpha"
  tip2="$(git -C "$WS/nix-alpha" rev-parse HEAD)"
  PG_REPO_RUNNER_EPOCH=$(($(date -u +%s) + 3700)) verify "nix-alpha@$tip2"
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$tip2")")" = green ]
  [ "$(nix_runs)" -eq 2 ]
}

@test "throttle: a red check throttles too, and the interval is configurable" {
  local tip2
  export PG_REPO_RUNNER_CHECK_INTERVAL=3600
  SHIM_EXIT=1 verify
  [ "$status" -eq 0 ]
  commit_in "$WS/nix-alpha"
  tip2="$(git -C "$WS/nix-alpha" rev-parse HEAD)"
  verify "nix-alpha@$tip2"
  [ "$status" -eq 9 ]
  # the config key drives it when the env override is absent
  unset PG_REPO_RUNNER_CHECK_INTERVAL
  sd '\[thresholds\]' $'[thresholds]\ncheck_interval = 10' "$TEST_DIR/config.toml"
  verify "nix-alpha@$tip2"
  [ "$status" -eq 9 ]
  PG_REPO_RUNNER_EPOCH=$(($(date -u +%s) + 20)) verify "nix-alpha@$tip2"
  [ "$status" -eq 0 ]
}

@test "throttle: a superseded tip is stored before the throttle is consulted, and does not start the interval" {
  local old
  export PG_REPO_RUNNER_CHECK_INTERVAL=3600
  old="$ALPHA_SHA"
  commit_in "$WS/nix-alpha"
  verify "nix-alpha@$old"
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$old")")" = superseded ]
  verify "nix-alpha@$(git -C "$WS/nix-alpha" rev-parse HEAD)"
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 1 ]
}

@test "a signal exit is a malfunction, not a red: exit 1, no verdict, and the next dispatch runs again" {
  SHIM_MODE="kill" verify
  [ "$status" -eq 1 ]
  [ ! -e "$(verdict_file nix-alpha "$ALPHA_SHA")" ]
  [[ $output == *"signal 9"* ]]
  grep -q 'exit=1' "$STATE/run.log"
  assert_no_lease
  # an explicit 128+n exit is the same malfunction
  rm -f "$TEST_DIR/nix.argv"
  SHIM_EXIT=143 verify
  [ "$status" -eq 1 ]
  [ ! -e "$(verdict_file nix-alpha "$ALPHA_SHA")" ]
  # pg-router's retry: the item is dispatched again and now the check can finish
  verify
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$ALPHA_SHA")")" = green ]
  [ "$(nix_runs)" -eq 2 ]
}

@test "the signal classifier is exactly 129 to 192: 128 and 193 are reds" {
  SHIM_EXIT=128 verify
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$ALPHA_SHA")")" = red ]
  rm -rf "$STATE/verdicts"
  SHIM_EXIT=129 verify
  [ "$status" -eq 1 ]
  rm -rf "$STATE/verdicts"
  SHIM_EXIT=192 verify
  [ "$status" -eq 1 ]
  rm -rf "$STATE/verdicts"
  SHIM_EXIT=193 verify
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$ALPHA_SHA")")" = red ]
}

# ---- the sibling-override check: lock skew versus defect --------------------

@test "a green locked check never runs the override check" {
  verify
  [ "$status" -eq 0 ]
  [ "$(pn_runs)" -eq 0 ]
  [ ! -e "$(override_file nix-alpha "$ALPHA_SHA")" ]
}

@test "locked red and override green is lock skew: both verdicts stored, the locked one stays red" {
  SHIM_EXIT=1 PN_SHIM_EXIT=0 verify
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$ALPHA_SHA")")" = red ]
  [ "$(jq -r .inputs "$(verdict_file nix-alpha "$ALPHA_SHA")")" = locked ]
  local o
  o="$(override_file nix-alpha "$ALPHA_SHA")"
  [ "$(jq -r .result "$o")" = green ]
  [ "$(jq -r .inputs "$o")" = override ]
  [ "$(jq -r .kind "$o")" = check ]
  [ "$(jq -r .sha "$o")" = "$ALPHA_SHA" ]
  [ "$(pn_runs)" -eq 1 ]
  # pn's own single-repo evaluation of exactly the tip, in the canonical workspace
  [ "$(cat "$TEST_DIR/pn.argv")" = "$WS :: workspace flake-check --no-build --rev $ALPHA_SHA nix-alpha" ]
  lib_eval "rr_override_class nix-alpha $ALPHA_SHA"
  [ "$output" = skew ]
  # the status listing tells the two verdicts apart
  run "${RUNNER[@]}" status
  [[ $output == *$'check\tred\tlocked'* ]]
  [[ $output == *$'check\tgreen\toverride'* ]]
}

@test "a bare pn missing from the service PATH is found in a profile bin directory" {
  mkdir -p "$TEST_DIR/profile/bin"
  cp "$TEST_DIR/pn-shim" "$TEST_DIR/profile/bin/pn-rr-test"
  # a bare name that no PATH directory holds, as pn is not on a launchd job PATH
  export PG_REPO_RUNNER_PN_BIN=pn-rr-test
  export PG_REPO_RUNNER_PN_SEARCH_DIRS="$TEST_DIR/profile/bin"
  SHIM_EXIT=1 PN_SHIM_EXIT=0 verify
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(override_file nix-alpha "$ALPHA_SHA")")" = green ]
  [ "$(pn_runs)" -eq 1 ]
}

@test "pn absent everywhere: no override verdict and a log naming the missing prerequisite" {
  export PG_REPO_RUNNER_PN_BIN=pn-rr-test
  export PG_REPO_RUNNER_PN_SEARCH_DIRS="$TEST_DIR/no-such-dir"
  SHIM_EXIT=1 verify
  [ "$status" -eq 0 ]
  [ ! -e "$(override_file nix-alpha "$ALPHA_SHA")" ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$ALPHA_SHA")")" = red ]
  grep -q 'prerequisite missing: pn' "$STATE/logs/nix-alpha/$ALPHA_SHA.override.log"
  ! grep -q "No such file" "$STATE/logs/nix-alpha/$ALPHA_SHA.override.log"
}

@test "locked red and override red is a defect" {
  SHIM_EXIT=1 PN_SHIM_EXIT=10 verify
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(override_file nix-alpha "$ALPHA_SHA")")" = red ]
  [ "$(jq -r .exit "$(override_file nix-alpha "$ALPHA_SHA")")" = 10 ]
  lib_eval "rr_override_class nix-alpha $ALPHA_SHA"
  [ "$output" = defect ]
}

# the two evidence texts a real run writes
BUILD_FAIL_TEXT="error: failed to build attribute 'checks.aarch64-darwin.x-go-tests', build of 'a.drv^*' failed: Cannot build 'a.drv'."
SAME_REV_TEXT="• Updated input 'sib':\n    'github:o/sib/$(printf 'a%.0s' {1..40})?narHash=sha256-xyz' (2026-11-05)\n  → 'git+file:///ws/sib?ref=refs/heads/main&rev=$(printf 'a%.0s' {1..40})' (2026-11-05)"
DIFF_REV_TEXT="• Updated input 'sib':\n    'github:o/sib/$(printf 'a%.0s' {1..40})?narHash=sha256-xyz' (2026-11-05)\n  → 'git+file:///ws/sib?ref=refs/heads/main&rev=$(printf 'b%.0s' {1..40})' (2026-11-06)"

@test "a locked red that is a build-stage failure is unclassified, not skew, and no relock is claimed" {
  SHIM_EXIT=1 SHIM_TEXT="$BUILD_FAIL_TEXT" PN_SHIM_EXIT=0 verify
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$ALPHA_SHA")")" = red ]
  # the evaluation-only override check is not run and stores nothing
  [ "$(pn_runs)" -eq 0 ]
  [ ! -e "$(override_file nix-alpha "$ALPHA_SHA")" ]
  lib_eval "rr_override_class nix-alpha $ALPHA_SHA"
  [ "$output" = unclassified ]
  grep -q 'build/test failure; the evaluation-only override check cannot classify it' "$STATE/run.log"
  run "${RUNNER[@]}" status
  ! grep -qi 'relock is owed\|LOCK SKEW' "$STATE/run.log"
}

@test "locked red where every overridden input resolves to the locked revision is not skew" {
  SHIM_EXIT=1 PN_SHIM_EXIT=0 PN_SHIM_TEXT="$SAME_REV_TEXT" verify
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(override_file nix-alpha "$ALPHA_SHA")")" = green ]
  [ "$(jq -r .class "$(override_file nix-alpha "$ALPHA_SHA")")" = defect ]
  lib_eval "rr_override_class nix-alpha $ALPHA_SHA"
  [ "$output" = defect ]
  lib_eval "rr_override_reason nix-alpha $ALPHA_SHA"
  [[ $output == *'not lock skew'* ]]
  lib_eval "rr_override_text defect '$output'"
  [[ $output == DEFECT:* ]]
  [[ $output != *'relock is owed'* ]]
  lib_eval "RR_M_PREFIX=pg_repo_runner; rr_m_add() { echo \"\$1 \$2 \$3\"; }; rr_surface_enabled() { return 1; }; rr_m_state_points nix-alpha"
  [[ $output == *'pg_repo_runner_check_latest_red_class 1 repo=nix-alpha,class=defect'* ]]
  [[ $output == *'pg_repo_runner_check_latest_red_class 0 repo=nix-alpha,class=skew'* ]]
}

@test "locked red at evaluation with differing override revisions and override green is still skew" {
  SHIM_EXIT=1 SHIM_TEXT="error: attribute 'x' missing" PN_SHIM_EXIT=0 PN_SHIM_TEXT="$DIFF_REV_TEXT" verify
  [ "$status" -eq 0 ]
  [ "$(pn_runs)" -eq 1 ]
  lib_eval "rr_override_class nix-alpha $ALPHA_SHA"
  [ "$output" = skew ]
  lib_eval "rr_override_text skew"
  [[ $output == 'LOCK SKEW:'*'a relock is owed'* ]]
}

@test "an override check that does not run to a result stores no override verdict: unclassified, the run still exits 0" {
  local mode
  for mode in 1 2 127 143; do
    rm -rf "$STATE/verdicts" "$TEST_DIR/pn.argv"
    SHIM_EXIT=1 PN_SHIM_EXIT=$mode verify
    [ "$status" -eq 0 ] || {
      echo "pn exit $mode: $output"
      return 1
    }
    [ ! -e "$(override_file nix-alpha "$ALPHA_SHA")" ]
    [ "$(jq -r .result "$(verdict_file nix-alpha "$ALPHA_SHA")")" = red ]
  done
  rm -rf "$STATE/verdicts"
  SHIM_EXIT=1 PN_SHIM_MODE=kill verify
  [ "$status" -eq 0 ]
  [ ! -e "$(override_file nix-alpha "$ALPHA_SHA")" ]
  lib_eval "rr_override_class nix-alpha $ALPHA_SHA"
  [ "$output" = unclassified ]
}

@test "the override verdict never satisfies the locked-verdict gate" {
  SHIM_EXIT=1 PN_SHIM_EXIT=0 verify
  [ "$status" -eq 0 ]
  lib_eval "rr_verdict_green nix-alpha $ALPHA_SHA"
  [ "$status" -ne 0 ]
  # even a green override verdict copied over the locked file is not a locked green
  cp "$(override_file nix-alpha "$ALPHA_SHA")" "$(verdict_file nix-alpha "$ALPHA_SHA")"
  lib_eval "rr_verdict_green nix-alpha $ALPHA_SHA"
  [ "$status" -ne 0 ]
}

@test "the metrics split the two verdicts and name the class of the latest red" {
  SHIM_EXIT=1 PN_SHIM_EXIT=0 verify
  [ "$status" -eq 0 ]
  lib_eval "RR_M_PREFIX=pg_repo_runner; rr_m_add() { echo \"\$1 \$2 \$3\"; }; rr_surface_enabled() { return 1; }; rr_m_state_points nix-alpha"
  [[ $output == *'pg_repo_runner_check_verdicts 1 repo=nix-alpha,result=red'* ]]
  [[ $output == *'pg_repo_runner_check_verdicts 0 repo=nix-alpha,result=green'* ]]
  [[ $output == *'pg_repo_runner_check_override_verdicts 1 repo=nix-alpha,result=green'* ]]
  [[ $output == *'pg_repo_runner_check_latest_red_class 1 repo=nix-alpha,class=skew'* ]]
  [[ $output == *'pg_repo_runner_check_latest_red_class 0 repo=nix-alpha,class=defect'* ]]
}

@test "a tip that moved before the run is a superseded verdict and nix is never run" {
  commit_in "$WS/nix-alpha"
  local new f
  new="$(git -C "$WS/nix-alpha" rev-parse HEAD)"
  [ "$new" != "$ALPHA_SHA" ]
  verify "nix-alpha@$ALPHA_SHA"
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 0 ]
  f="$(verdict_file nix-alpha "$ALPHA_SHA")"
  [ "$(jq -r .result "$f")" = superseded ]
  [ "$(jq -r .kind "$f")" = check ]
  grep -q 'superseded' "$STATE/run.log"
  # the same stale event again is absorbed by the stored verdict
  verify "nix-alpha@$ALPHA_SHA"
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 0 ]
  # the newer tip has its own event and is checked normally
  verify "nix-alpha@$new"
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 1 ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$new")")" = green ]
}

@test "a duplicate event whose verdict is stored does not run nix, even under load or a held lease" {
  verify
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 1 ]
  local before
  before="$(cat "$(verdict_file nix-alpha "$ALPHA_SHA")")"
  verify
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 1 ]
  # the stored verdict ends the run before the load check: no "busy" answer
  PG_REPO_RUNNER_LOADAVG=99 verify
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 1 ]
  # ... and before the lease
  hold_lease
  verify
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 1 ]
  [ "$(cat "$(verdict_file nix-alpha "$ALPHA_SHA")")" = "$before" ]
  grep -q 'a check verdict for nix-alpha@.* is already stored' "$STATE/run.log"
}

@test "the measured duration and the sampled load are recorded in the verdict" {
  SHIM_SLEEP=2 PG_REPO_RUNNER_LOADAVG=3.25 verify
  [ "$status" -eq 0 ]
  local f
  f="$(verdict_file nix-alpha "$ALPHA_SHA")"
  [ "$(jq '.durationS >= 2 and .durationS < 30' "$f")" = true ]
  [ "$(jq -r .loadStart "$f")" = 3.25 ]
  [ "$(jq -r .loadEnd "$f")" = 3.25 ]
  [ "$(jq '(.finishedAt | fromdateiso8601) - (.startedAt | fromdateiso8601) >= 2' "$f")" = true ]
  grep -q 'check end: exit=0 after [0-9]*s (load 3.25)' "$STATE/run.log"
  # status --json carries it
  run "${RUNNER[@]}" status --json --repo nix-alpha
  [ "$(jq -r '.[0].durationS >= 2' <<<"$output")" = true ]
}

@test "an abbreviated sha resolves and the verdict is keyed by the full sha" {
  verify "nix-alpha@${ALPHA_SHA:0:10}"
  [ "$status" -eq 0 ]
  [ -f "$(verdict_file nix-alpha "$ALPHA_SHA")" ]
  [[ "$(cat "$TEST_DIR/nix.argv")" == *"?rev=$ALPHA_SHA" ]]
}

@test "a sha that is not a commit of the repo is skipped with exit 0: no nix, no verdict" {
  verify "nix-alpha@0123456789abcdef0123456789abcdef01234567"
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 0 ]
  [ ! -e "$(verdict_file nix-alpha 0123456789abcdef0123456789abcdef01234567)" ]
  grep -q 'is not a commit of nix-alpha' "$STATE/run.log"
}

@test "verify needs a sha: a bare repo key is a usage error, exit 2" {
  verify nix-alpha
  [ "$status" -eq 2 ]
  [[ $output == *"verify needs an item id"* ]]
  [ "$(nix_runs)" -eq 0 ]
}

@test "the canonical working tree is never read or written: a dirty tree changes neither the run nor the tree" {
  echo dirty >>"$WS/nix-alpha/f"
  echo new >"$WS/nix-alpha/untracked"
  local before
  before="$(git -C "$WS/nix-alpha" status --porcelain)"
  verify
  [ "$status" -eq 0 ]
  [ "$(git -C "$WS/nix-alpha" status --porcelain)" = "$before" ]
  [ "$(git -C "$WS/nix-alpha" rev-parse HEAD)" = "$ALPHA_SHA" ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$ALPHA_SHA")")" = green ]
  # nothing was pushed
  [ "$(git -C "$TEST_DIR/remotes/alpha.git" rev-parse main)" = "$ALPHA_SHA" ]
}

@test "the primary branch comes from pn-workspace.toml, not from the checked-out branch" {
  git -C "$WS/nix-alpha" branch trunk
  git -C "$WS/nix-alpha" checkout -q trunk
  commit_in "$WS/nix-alpha"
  local trunk_tip
  trunk_tip="$(git -C "$WS/nix-alpha" rev-parse HEAD)"
  git -C "$WS/nix-alpha" checkout -q main
  # main is the workspace branch: the trunk commit is not its tip, so it is superseded
  verify "nix-alpha@$trunk_tip"
  [ "$(jq -r .result "$(verdict_file nix-alpha "$trunk_tip")")" = superseded ]
  [ "$(nix_runs)" -eq 0 ]
  # and when the workspace names trunk, trunk's tip is the one that is checked
  write_workspace nix-beta nix-link
  printf "[repos.nix-alpha]\nurl = 'x'\nbranch = 'trunk'\n" >>"$WS/pn-workspace.toml"
  rm -rf "$STATE/verdicts"
  verify "nix-alpha@$trunk_tip"
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$trunk_tip")")" = green ]
}

@test "the kernel still gates verify: kill switch, load and a held lease answer exit 9 with no nix" {
  mkdir -p "$STATE"
  touch "$STATE/push.disabled"
  verify
  [ "$status" -eq 9 ]
  rm "$STATE/push.disabled"
  PG_REPO_RUNNER_LOADAVG=50 verify
  [ "$status" -eq 9 ]
  hold_lease
  verify
  [ "$status" -eq 9 ]
  [ "$(nix_runs)" -eq 0 ]
  [ ! -e "$(verdict_file nix-alpha "$ALPHA_SHA")" ]
}

@test "a repo with verify off, one outside the workspace file and a symlinked one are not checked" {
  verify "nix-beta@$(git -C "$WS/nix-beta" rev-parse HEAD)"
  [ "$status" -eq 0 ]
  verify "nix-ghost@$(git -C "$WS/nix-ghost" rev-parse HEAD)"
  [ "$status" -eq 2 ]
  verify "nix-link@$(git -C "$WS/nix-link" rev-parse HEAD)"
  [ "$status" -eq 2 ]
  [ "$(nix_runs)" -eq 0 ]
}

@test "an unconfigured nix binary is a malfunction, exit 1, and stores no verdict" {
  # the built script injects its own store nix, so only a raw run proves the guard
  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    skip "the built script carries its own nix path"
  fi
  PG_REPO_RUNNER_NIX_BIN="" RR_NIX_BIN="" verify
  [ "$status" -eq 1 ]
  [ ! -e "$(verdict_file nix-alpha "$ALPHA_SHA")" ]
}

# ---- the tips source --------------------------------------------------------

tips() { run --separate-stderr "${RUNNER[@]}" tips "$@"; }

# only_alpha_verified: turn verify off for the repos the fixture config lists but
# that cannot be read (nix-link, nix-ghost, nix-flk), so no-argument tips sees
# healthy repos only. Every other tips test starts from this; the failure tests
# do not call it.
only_alpha_verified() {
  awk '/^\[/ { off = ($0 ~ /^\[repos\.nix-(link|ghost|flk)\]$/) }
    off && /^verify = true$/ { print "verify = false"; next }
    { print }' "$TEST_DIR/config.toml" >"$TEST_DIR/config.toml.new"
  mv -f "$TEST_DIR/config.toml.new" "$TEST_DIR/config.toml"
  [ "$(tq -o json -f "$TEST_DIR/config.toml" . | jq '[.repos[] | select(.verify == true)] | length')" -eq 1 ]
}

@test "tips: a repo that cannot be read is a failure; the healthy record still prints and the run exits 1" {
  tips
  [ "$status" -eq 1 ]
  # nix-alpha only: nix-beta is verify=false, nix-ghost is not a workspace key,
  # nix-link is a symlink, nix-flk is in neither the workspace file nor on disk
  [ "$(wc -l <<<"$output" | tr -d ' ')" -eq 1 ]
  [ "$(jq -r .id <<<"$output")" = "nix-alpha@$ALPHA_SHA" ]
  [ "$(jq -r .type <<<"$output")" = repo-tip ]
  [ "$(jq -r .title <<<"$output")" = "nix-alpha tip" ]
  [ "$(jq -r .metadata.repo <<<"$output")" = nix-alpha ]
  [ "$(jq -r .metadata.sha <<<"$output")" = "$ALPHA_SHA" ]
  [ "$(jq -r .metadata.branch <<<"$output")" = main ]
  # the default retry window is an hour
  [ "$(jq '(.expiresAt | fromdateiso8601) - (.at | fromdateiso8601)' <<<"$output")" = 3600 ]
  # the failed entries are explained in the run log, not on stdout
  grep -q "tips: FAILED 'nix-ghost'" "$STATE/run.log"
  grep -q "tips: FAILED 'nix-link'" "$STATE/run.log"
  grep -q "tips: FAILED 'nix-flk'" "$STATE/run.log"
  ! grep -q "tips: skipped" "$STATE/run.log"
}

@test "tips --repo: a healthy repo emits its record and exits 0" {
  tips --repo nix-alpha
  [ "$status" -eq 0 ]
  [ "$(wc -l <<<"$output" | tr -d ' ')" -eq 1 ]
  [ "$(jq -r .id <<<"$output")" = "nix-alpha@$ALPHA_SHA" ]
  ! grep -q "tips: FAILED" "$STATE/run.log"
}

@test "tips --repo: a repo that cannot be read exits 1 with a FAILED log line and no output" {
  tips --repo nix-ghost
  [ "$status" -eq 1 ]
  [ -z "$output" ]
  grep -q "tips: FAILED 'nix-ghost': not a repo key of pn-workspace.toml" "$STATE/run.log"
  tips --repo nix-link
  [ "$status" -eq 1 ]
  grep -q "tips: FAILED 'nix-link': .*not the top level of a git repository" "$STATE/run.log"
}

@test "tips --repo: a branch ref that does not exist is a failure" {
  git -C "$WS/nix-alpha" branch -m main renamed
  tips --repo nix-alpha
  [ "$status" -eq 1 ]
  [ -z "$output" ]
  grep -q "tips: FAILED 'nix-alpha': refs/heads/main does not exist" "$STATE/run.log"
}

@test "tips --repo: a key outside the allow-list is a refusal (exit 2) that names the key" {
  tips --repo nix-unknown
  [ "$status" -eq 2 ]
  [ -z "$output" ]
  [[ $stderr == *nix-unknown* ]]
}

@test "tips --repo: a key whose verify is off prints nothing and exits 0" {
  tips --repo nix-beta
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  ! grep -q "tips: FAILED" "$STATE/run.log"
}

@test "tips --repo: needs exactly one key, and other arguments are refused" {
  tips --repo
  [ "$status" -eq 2 ]
  tips --repo nix-alpha --repo nix-beta
  [ "$status" -eq 2 ]
  tips --bogus
  [ "$status" -eq 2 ]
}

@test "tips: every repo already verified exits 0 with no output" {
  only_alpha_verified
  verify
  tips
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  tips --repo nix-alpha
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "tips: the retry window is configurable, and stdout is nothing but JSON lines" {
  only_alpha_verified
  PG_REPO_RUNNER_RETRY_WINDOW=120 tips
  [ "$(jq '(.expiresAt | fromdateiso8601) - (.at | fromdateiso8601)' <<<"$output")" = 120 ]
  [ -z "$stderr" ]
  sed -i.bak 's/^nice_level = 10/nice_level = 10\nretry_window = 900/' "$TEST_DIR/config.toml"
  tips
  [ "$(jq '(.expiresAt | fromdateiso8601) - (.at | fromdateiso8601)' <<<"$output")" = 900 ]
}

@test "tips: a tip with a verdict is not emitted, a new tip is, and the id carries the sha" {
  only_alpha_verified
  verify
  tips
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  commit_in "$WS/nix-alpha"
  local new again
  new="$(git -C "$WS/nix-alpha" rev-parse HEAD)"
  tips
  [ "$(jq -r .id <<<"$output")" = "nix-alpha@$new" ]
  # emitted again for the same tip: the same id (the duplicate pg-router absorbs)
  again="$output"
  tips
  [ "$(jq -r .id <<<"$output")" = "$(jq -r .id <<<"$again")" ]
}

@test "tips: a signal exit stores no verdict, so the tip stays offered" {
  SHIM_MODE="kill" verify
  [ "$status" -eq 1 ]
  tips
  [ "$(jq -r .id <<<"$output")" = "nix-alpha@$ALPHA_SHA" ]
}

@test "tips reads the primary branch from pn-workspace.toml and ignores a checked-out feature branch" {
  git -C "$WS/nix-alpha" checkout -q -b feature
  commit_in "$WS/nix-alpha"
  tips
  [ "$(jq -r .id <<<"$output")" = "nix-alpha@$ALPHA_SHA" ]
}

@test "tips does not take the lease or consult the load: it only reads refs and the store" {
  only_alpha_verified
  hold_lease
  local owner
  owner="$(readlink "$STATE/lease")"
  PG_REPO_RUNNER_LOADAVG=99 tips
  [ "$status" -eq 0 ]
  [ "$(jq -r .id <<<"$output")" = "nix-alpha@$ALPHA_SHA" ]
  [ "$(readlink "$STATE/lease")" = "$owner" ]
}

@test "tips: a tip item is exactly what verify consumes" {
  tips
  local id
  id="$(jq -r .id <<<"$output")"
  verify "$id"
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$ALPHA_SHA")")" = green ]
}

@test "tips refuses stray arguments, and a broken config is a malfunction" {
  run "${RUNNER[@]}" tips extra
  [ "$status" -eq 2 ]
  rm "$TEST_DIR/config.toml"
  run "${RUNNER[@]}" tips
  [ "$status" -eq 1 ]
}

@test "tips and verify: no repo has push enabled in the fixture, and nothing reaches a remote" {
  [ "$(tq -o json -f "$TEST_DIR/config.toml" . | jq '[.repos[].push] | map(select(. == true)) | length')" = 0 ]
  local before
  before="$(git -C "$TEST_DIR/remotes/alpha.git" rev-parse main)"
  tips
  verify
  [ "$(git -C "$TEST_DIR/remotes/alpha.git" rev-parse main)" = "$before" ]
}

# ---- real nix, on a fixture flake -------------------------------------------

# require_real_nix: skip unless a usable nix is on PATH (not inside a nix build
# sandbox). Sets NIX_SYSTEM and points PG_REPO_RUNNER_NIX_BIN at the real nix.
require_real_nix() {
  local nix
  nix="$(command -v nix)" || skip "no nix on PATH (a nix build sandbox has none)"
  NIX_SYSTEM="$(timeout 60 "$nix" eval --impure --raw --expr builtins.currentSystem 2>/dev/null)" ||
    skip "nix is on PATH but cannot evaluate here (no usable store or daemon)"
  export PG_REPO_RUNNER_NIX_BIN="$nix"
}

# write_flake DIR EXITCODE: a flake with one derivation check whose builder
# exits EXITCODE (0: the check passes).
write_flake() {
  cat >"$1/flake.nix" <<EOF
{
  description = "pg-repo-runner fixture";
  outputs = { self }: {
    checks.$NIX_SYSTEM.fixture = derivation {
      name = "fixture-check";
      system = "$NIX_SYSTEM";
      builder = "/bin/sh";
      args = [ "-c" "echo fixture-builder-ran; echo ok > \$out; exit $2" ];
    };
  };
}
EOF
}

# make_flake_repo EXITCODE: a workspace repo nix-flk whose tip is the flake.
make_flake_repo() {
  make_repo "$WS/nix-flk" flk
  write_workspace nix-alpha nix-beta nix-link nix-flk
  write_flake "$WS/nix-flk" "$1"
  git -C "$WS/nix-flk" add flake.nix
  git -C "$WS/nix-flk" commit -q -m "flake"
  FLK_SHA="$(git -C "$WS/nix-flk" rev-parse HEAD)"
}

@test "real nix: a passing flake check is green, with the duration recorded" {
  require_real_nix
  make_flake_repo 0
  verify "nix-flk@$FLK_SHA"
  [ "$status" -eq 0 ] || {
    echo "$output"
    cat "$STATE/run.log"
    return 1
  }
  local f
  f="$(verdict_file nix-flk "$FLK_SHA")"
  [ "$(jq -r .result "$f")" = green ]
  [ "$(jq -r .exit "$f")" = 0 ]
  [ "$(jq '.durationS >= 0' "$f")" = true ]
  [ -f "$(jq -r .logPath "$f")" ]
  # a duplicate event does not run nix again: the stored verdict is byte-identical
  local before
  before="$(cat "$f")"
  verify "nix-flk@$FLK_SHA"
  [ "$status" -eq 0 ]
  [ "$(cat "$f")" = "$before" ]
}

@test "real nix: a failing flake check is red, the run exits 0, and the failure is in the kept log" {
  require_real_nix
  make_flake_repo 1
  verify "nix-flk@$FLK_SHA"
  [ "$status" -eq 0 ] || {
    echo "$output"
    cat "$STATE/run.log"
    return 1
  }
  local f
  f="$(verdict_file nix-flk "$FLK_SHA")"
  [ "$(jq -r .result "$f")" = red ]
  [ "$(jq '.exit != 0 and .exit <= 128' "$f")" = true ]
  grep -qi 'fail\|error' "$(jq -r .logPath "$f")"
}

@test "real nix: the check reads the committed snapshot, never the working tree" {
  require_real_nix
  make_flake_repo 0
  # an uncommitted change that would turn the check red, and an untracked file
  write_flake "$WS/nix-flk" 1
  echo junk >"$WS/nix-flk/untracked"
  local before
  before="$(git -C "$WS/nix-flk" status --porcelain)"
  verify "nix-flk@$FLK_SHA"
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(verdict_file nix-flk "$FLK_SHA")")" = green ]
  [ "$(git -C "$WS/nix-flk" status --porcelain)" = "$before" ]
}

@test "real nix: a tip that moved before the run is superseded and starts no nix" {
  require_real_nix
  make_flake_repo 0
  commit_in "$WS/nix-flk"
  verify "nix-flk@$FLK_SHA"
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(verdict_file nix-flk "$FLK_SHA")")" = superseded ]
  [ ! -d "$STATE/logs/nix-flk" ] || [ -z "$(ls -A "$STATE/logs/nix-flk")" ]
}

# pg2-jwwrv1: under pg-router's strict base env only BSD date is on PATH, which has
# no `date -d`. The runner must use its injected date (RR_DATE_BIN), not PATH's.
@test "tips works when only a BSD-style date (no -d) is on PATH" {
  local gnu
  gnu="$(command -v date)"
  "$gnu" -u -d @0 +%s >/dev/null 2>&1 || skip "no GNU date available to inject"
  mkdir -p "$TEST_DIR/bsd-bin"
  cat >"$TEST_DIR/bsd-bin/date" <<'SH'
#!/bin/sh
for a in "$@"; do
  if [ "$a" = "-d" ]; then
    echo "date: illegal option -- d" >&2
    exit 1
  fi
done
exec /bin/date "$@"
SH
  chmod +x "$TEST_DIR/bsd-bin/date"
  only_alpha_verified
  PATH="$TEST_DIR/bsd-bin:$PATH" RR_DATE_BIN="$gnu" tips
  [ "$status" -eq 0 ]
  [ "$(jq -r .id <<<"$output")" = "nix-alpha@$ALPHA_SHA" ]
  [ -n "$(jq -r .expiresAt <<<"$output")" ]
}

# pg2-8y330k: under pg-router's strict base env (PATH is only /usr/bin:/bin:
# /usr/sbin:/sbin) realpath is BSD realpath, which has no -e. The path check used to
# call `realpath -e` and so refused EVERY repo: tips skipped them all (and exited 0)
# and sync died with exit 2. The tools below are injected by absolute path, as the
# nix build does; PATH holds nothing else.
strict_path_env() {
  STRICT_ENV=(
    "PATH=/usr/bin:/bin:/usr/sbin:/sbin"
    "RR_GIT_BIN=$(command -v git)" "RR_JQ_BIN=$(command -v jq)" "RR_TQ_BIN=$(command -v tq)"
    "RR_DATE_BIN=$(command -v date)"
  )
  sd -s "sync = false" "sync = true" "$TEST_DIR/config.toml"
  STRICT_RUNNER=("${RUNNER[@]}")
  # The raw-source runner starts with a bare `bash`; keep the nix one, not /bin/bash 3.2.
  [[ ${STRICT_RUNNER[0]} != bash ]] || STRICT_RUNNER[0]="$(command -v bash)"
  # The fixture must fail on the old code: BSD-only PATH has no `realpath -e`.
  if PATH=/usr/bin:/bin:/usr/sbin:/sbin realpath -e -- / >/dev/null 2>&1; then
    skip "this host's base-PATH realpath supports -e, so it cannot reproduce the defect"
  fi
}

@test "tips reports the repo under a BSD-only PATH (no realpath -e)" {
  strict_path_env
  only_alpha_verified
  run --separate-stderr env "${STRICT_ENV[@]}" "${STRICT_RUNNER[@]}" tips
  [ "$status" -eq 0 ]
  [ "$(jq -r .id <<<"$output")" = "nix-alpha@$ALPHA_SHA" ]
  [[ $stderr != *'not the top level of a git repository'* ]]
}

@test "sync's path check passes under a BSD-only PATH (no realpath -e)" {
  strict_path_env
  PG_ROUTER_ITEM_ID="nix-alpha@$ALPHA_SHA" run --separate-stderr env "${STRICT_ENV[@]}" "${STRICT_RUNNER[@]}" sync
  [ "$status" -ne 2 ]
  [[ $stderr != *'path check failed'* ]]
  [[ $stderr != *'illegal option'* ]]
}

@test "the path check still refuses a symlinked key under a BSD-only PATH" {
  strict_path_env
  PG_ROUTER_ITEM_ID="nix-link@$ALPHA_SHA" run --separate-stderr env "${STRICT_ENV[@]}" "${STRICT_RUNNER[@]}" sync
  grep -q 'path check failed' <<<"$stderr"
  [ "$status" -eq 2 ]
}
