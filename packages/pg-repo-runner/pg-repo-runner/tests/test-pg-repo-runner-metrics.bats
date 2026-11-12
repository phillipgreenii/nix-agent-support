#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Best-effort OTLP gauges of pg-repo-runner. The real script runs as a subprocess
# against a real temporary workspace (real git clones of bare local remotes) and the
# real verdict store; `ccpool` is a fake that records the argv of every call
# (`emit-gauge --name N --value V --label k=v ...`, one line per call). What the suite
# pins: the points of a healthy and of a failing `tips --repo`, that nothing but the
# JSONL record reaches stdout (even when the fake writes to stdout and stderr itself),
# that a failing or hanging ccpool never changes the exit status, that the run
# points of a role (including the busy load) are emitted, and that an empty
# ccpool binary means no call at all. The state files are the only source of the
# points: no test here counts git or nix runs, because the emission starts none.

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
  mkdir -p "$WS" "$TEST_DIR/remotes"
  make_repo "$WS/nix-alpha" alpha
  ALPHA_SHA="$(git -C "$WS/nix-alpha" rev-parse HEAD)"
  # nix-ghost is on the allow-list but is not a repo key of pn-workspace.toml: unreadable
  write_workspace nix-alpha
  write_config "$WS"

  export XDG_STATE_HOME="$TEST_DIR/state"
  export PG_REPO_RUNNER_CONFIG="$TEST_DIR/config.toml"
  export PG_REPO_RUNNER_LOADAVG=0.50
  export PG_REPO_RUNNER_CHECK_INTERVAL=0
  STATE="$XDG_STATE_HOME/pg-repo-runner"
  CALLS="$TEST_DIR/ccpool.argv"
  make_fake_ccpool
  make_nix_shim
}

teardown() {
  gfh_teardown
}

make_repo() {
  local dir="$1" name="$2"
  gfh_init_bare "$TEST_DIR/remotes/$name.git"
  gfh_clone "$TEST_DIR/remotes/$name.git" "$dir" "pg-repo-runner"
  echo "$name" >"$dir/f"
  git -C "$dir" add f
  git -C "$dir" commit -q -m "seed $name"
  git -C "$dir" push -q origin main
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

write_config() {
  cat >"$TEST_DIR/config.toml" <<EOF
workspace_root = "$1"
kill_switch = "$TEST_DIR/config-kill"

[thresholds]
max_load = 11.0
nice_level = 10

[repos.nix-alpha]
verify = true
sync = true
push = false

[repos.nix-ghost]
verify = true
sync = false
push = false
EOF
}

# make_nix_shim: stands in for nix; a green check.
make_nix_shim() {
  printf '#!/usr/bin/env bash\nexit 0\n' >"$TEST_DIR/nix-shim"
  chmod +x "$TEST_DIR/nix-shim"
  export PG_REPO_RUNNER_NIX_BIN="$TEST_DIR/nix-shim"
}

# make_fake_ccpool: records "$*" per call; FAKE_CCPOOL_MODE=fail exits 3, =hang sleeps.
# It also writes to stdout and stderr, which the runner MUST keep away from its own.
make_fake_ccpool() {
  cat >"$TEST_DIR/ccpool-fake" <<EOF
#!/usr/bin/env bash
echo "\$*" >>"$CALLS"
echo "ccpool-noise-on-stdout"
echo "ccpool-noise-on-stderr" >&2
case "\${FAKE_CCPOOL_MODE:-ok}" in
fail) exit 3 ;;
hang) exec sleep 30 ;;
esac
exit 0
EOF
  chmod +x "$TEST_DIR/ccpool-fake"
  export PG_REPO_RUNNER_CCPOOL_BIN="$TEST_DIR/ccpool-fake"
}

tips() { run --separate-stderr "${RUNNER[@]}" tips "$@"; }

# point NAME VALUE [k=v]...: the fake was called with exactly this point.
point() {
  local name="$1" value="$2" want l
  shift 2
  want="emit-gauge --name $name --value $value"
  for l in "$@"; do want+=" --label $l"; done
  grep -qxF -- "$want" "$CALLS" || {
    echo "missing point: $want" >&2
    echo "--- calls:" >&2
    cat "$CALLS" >&2 2>/dev/null || true
    return 1
  }
}

# no_point NAME: no call for this metric name.
no_point() {
  ! grep -qF -- "--name $1 " "$CALLS" 2>/dev/null
}

# a green check verdict for alpha, a sync record and a run of verify to get them
seed_state() {
  PG_ROUTER_ITEM_ID="nix-alpha@$ALPHA_SHA" run "${RUNNER[@]}" verify
  [ "$status" -eq 0 ]
  mkdir -p "$STATE/sync"
  cat >"$STATE/sync/nix-alpha.json" <<'EOF'
{"repo":"nix-alpha","branch":"main","outcome":"ahead","behind":0,"ahead":3,"behindBeforeFetch":0,"detail":"push withheld: push is not enabled for this repo","at":"2026-11-01T00:00:00Z"}
EOF
  : >"$CALLS"
}

# ---- (a) a healthy tips --repo ---------------------------------------------

@test "tips --repo: a healthy repo emits its tips, verdict, sync and surface points" {
  seed_state
  tips --repo nix-alpha
  [ "$status" -eq 0 ]
  point pg_repo_runner_tips_ok 1 repo=nix-alpha
  grep -q -- '--name pg_repo_runner_tips_last_run_timestamp_seconds --value [0-9]\+ --label repo=nix-alpha$' "$CALLS"
  point pg_repo_runner_unverified_tip 0 repo=nix-alpha
  point pg_repo_runner_check_verdicts 1 repo=nix-alpha result=green
  point pg_repo_runner_check_verdicts 0 repo=nix-alpha result=red
  point pg_repo_runner_check_verdicts 0 repo=nix-alpha result=superseded
  point pg_repo_runner_check_verdicts 0 repo=nix-alpha result=error
  grep -q -- '--name pg_repo_runner_check_last_timestamp_seconds --value [0-9]\+ --label repo=nix-alpha --label result=green$' "$CALLS"
  # no red verdict: no red timestamp point
  run grep -q -- '--name pg_repo_runner_check_last_timestamp_seconds .*result=red' "$CALLS"
  [ "$status" -eq 1 ]
  grep -q -- '--name pg_repo_runner_check_last_duration_seconds --value [0-9]\+ --label repo=nix-alpha$' "$CALLS"
  grep -q -- '--name pg_repo_runner_check_last_green_or_epoch_seconds --value [0-9]\+ --label repo=nix-alpha$' "$CALLS"
  point pg_repo_runner_check_latest_red 0 repo=nix-alpha
  point pg_repo_runner_sync_ahead 3 repo=nix-alpha
  point pg_repo_runner_sync_behind 0 repo=nix-alpha
  point pg_repo_runner_sync_diverged 0 repo=nix-alpha
  point pg_repo_runner_push_withheld 1 repo=nix-alpha
  local kind
  for kind in red diverged needs-relock build-red; do
    point pg_repo_runner_surface_open 0 repo=nix-alpha kind=$kind
  done
}

@test "tips --repo: an unverified tip is 1 and a red latest verdict is 1" {
  mkdir -p "$STATE/verdicts/nix-alpha"
  jq -n --arg sha "$ALPHA_SHA" '{repo:"nix-alpha",sha:$sha,kind:"check",result:"red",inputs:"locked",startedAt:"2026-11-01T00:00:00Z",finishedAt:"2026-11-01T00:01:00Z",exit:1,durationS:60,failedChecks:["checks.x.a","checks.x.b"]}' \
    >"$STATE/verdicts/nix-alpha/old.check.json"
  tips --repo nix-alpha
  [ "$status" -eq 0 ]
  point pg_repo_runner_unverified_tip 1 repo=nix-alpha
  point pg_repo_runner_check_latest_red 1 repo=nix-alpha
  point pg_repo_runner_check_verdicts 1 repo=nix-alpha result=red
  point pg_repo_runner_check_last_timestamp_seconds "$(jq -n '"2026-11-01T00:01:00Z" | fromdateiso8601')" repo=nix-alpha result=red
  point pg_repo_runner_check_last_duration_seconds 60 repo=nix-alpha
  no_point pg_repo_runner_sync_ahead
}

@test "tips --repo: the last-green gauge is the green finish time, else the oldest verdict, else absent" {
  mkdir -p "$STATE/verdicts/nix-alpha"
  mk() { # FILE RESULT FINISHED
    jq -n --arg sha "$ALPHA_SHA" --arg r "$2" --arg f "$3" '{repo:"nix-alpha",sha:$sha,kind:"check",result:$r,inputs:"locked",startedAt:$f,finishedAt:$f,exit:0,durationS:1,failedChecks:[]}' \
      >"$STATE/verdicts/nix-alpha/$1.check.json"
  }
  # no verdict at all: no point
  tips --repo nix-alpha
  [ "$status" -eq 0 ]
  no_point pg_repo_runner_check_last_green_or_epoch_seconds
  # never green: the oldest verdict of any result is the epoch
  mk a red 2026-11-01T00:01:00Z
  mk b red 2026-11-03T00:01:00Z
  : >"$CALLS"
  tips --repo nix-alpha
  point pg_repo_runner_check_last_green_or_epoch_seconds "$(jq -n '"2026-11-01T00:01:00Z" | fromdateiso8601')" repo=nix-alpha
  # a stored green wins over the epoch
  mk c green 2026-11-02T00:01:00Z
  : >"$CALLS"
  tips --repo nix-alpha
  point pg_repo_runner_check_last_green_or_epoch_seconds "$(jq -n '"2026-11-02T00:01:00Z" | fromdateiso8601')" repo=nix-alpha
}

# ---- (b) a failing repo ----------------------------------------------------

@test "tips --repo: a repo that cannot be read emits tips_ok 0 and the run still exits 1" {
  tips --repo nix-ghost
  [ "$status" -eq 1 ]
  point pg_repo_runner_tips_ok 0 repo=nix-ghost
  no_point pg_repo_runner_unverified_tip
}

# ---- (c) stdout is the JSONL protocol and nothing else ----------------------

@test "tips: stdout is exactly the JSONL records, whatever ccpool writes" {
  tips --repo nix-alpha
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 1 ]
  jq -e '.type == "repo-tip" and .metadata.repo == "nix-alpha"' <<<"$output" >/dev/null
  [[ $output != *ccpool-noise* ]]
  [[ $stderr != *ccpool-noise* ]]
  [ -s "$CALLS" ]
}

# ---- (d) a bad ccpool never changes the exit status --------------------------

@test "a failing ccpool does not change the exit status and is logged once" {
  export FAKE_CCPOOL_MODE=fail
  tips --repo nix-alpha
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 1 ]
  [ "$(grep -c 'metrics: ccpool emit-gauge failed' "$STATE/run.log")" -eq 1 ]
  [ "$(wc -l <"$CALLS" | tr -d ' ')" -eq 1 ]
  tips --repo nix-ghost
  [ "$status" -eq 1 ]
}

@test "a missing ccpool does not change the exit status and is logged once" {
  export PG_REPO_RUNNER_CCPOOL_BIN="$TEST_DIR/no-such-ccpool"
  tips --repo nix-alpha
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 1 ]
  [ "$(grep -c 'is not runnable' "$STATE/run.log")" -eq 1 ]
}

@test "a hanging ccpool is cut off and does not change the exit status" {
  local t
  t="$(command -v timeout || command -v gtimeout || true)"
  [ -n "$t" ] || skip "no timeout binary on PATH"
  export FAKE_CCPOOL_MODE=hang RR_TIMEOUT_BIN="$t" PG_REPO_RUNNER_CCPOOL_TIMEOUT=1
  tips --repo nix-alpha
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 1 ]
  grep -q 'metrics: ccpool emit-gauge failed (exit 124)' "$STATE/run.log"
}

# ---- (e) no ccpool configured: no call ---------------------------------------

@test "with no ccpool configured nothing is called" {
  [[ -z ${SCRIPT_UNDER_TEST:-} ]] || skip "the built script carries its own ccpool"
  unset PG_REPO_RUNNER_CCPOOL_BIN
  export RR_CCPOOL_BIN=""
  tips --repo nix-alpha
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 1 ]
  [ ! -e "$CALLS" ]
  PG_ROUTER_ITEM_ID="nix-alpha@$ALPHA_SHA" run "${RUNNER[@]}" verify
  [ "$status" -eq 0 ]
  [ ! -e "$CALLS" ]
}

# ---- the run points of a role -------------------------------------------------

@test "verify emits the run points; a busy deferral also emits the load" {
  PG_ROUTER_ITEM_ID="nix-alpha@$ALPHA_SHA" run "${RUNNER[@]}" verify
  [ "$status" -eq 0 ]
  point pg_repo_runner_last_run_exit 0 role=verify repo=nix-alpha
  grep -q -- '--name pg_repo_runner_last_run_timestamp_seconds --value [0-9]\+ --label role=verify --label repo=nix-alpha$' "$CALLS"
  grep -q -- '--name pg_repo_runner_last_run_duration_seconds --value [0-9]\+ --label role=verify --label repo=nix-alpha$' "$CALLS"
  no_point pg_repo_runner_last_busy_load
  : >"$CALLS"
  rm -rf "$STATE/verdicts"
  PG_REPO_RUNNER_LOADAVG=42.5 PG_ROUTER_ITEM_ID="nix-alpha@$ALPHA_SHA" run "${RUNNER[@]}" verify
  [ "$status" -eq 9 ]
  point pg_repo_runner_last_run_exit 9 role=verify repo=nix-alpha
  point pg_repo_runner_last_busy_load 42.5 role=verify
}

@test "a refused role run (exit 2) emits its exit with an empty repo" {
  PG_ROUTER_ITEM_ID="" run "${RUNNER[@]}" verify
  [ "$status" -eq 2 ]
  point pg_repo_runner_last_run_exit 2 role=verify repo=
}

@test "workspace-tips emits the run points with an empty repo" {
  run "${RUNNER[@]}" workspace-tips
  [ "$status" -eq 0 ]
  point pg_repo_runner_last_run_exit 0 role=workspace-tips repo=
}
