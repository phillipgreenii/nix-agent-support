#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Unit tests for pg-repo-runner.bash (bead pg2-zj2v7m): the kernel steps called
# one at a time, against real processes (the lease), a real filesystem (the
# verdict store) and the real jq. Nothing here needs git or nix.

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  # shellcheck disable=SC1091  # runtime-computed path (SCRIPTS_DIR)
  source "$SCRIPTS_DIR/pg-repo-runner.bash"

  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  export HOME="$TEST_DIR/home"
  export XDG_STATE_HOME="$TEST_DIR/state"
  mkdir -p "$HOME"
  rr_init_state
  SLEEPER=""
}

teardown() {
  if [[ -n $SLEEPER ]]; then
    kill "$SLEEPER" 2>/dev/null || true
  fi
  rm -rf "$TEST_DIR"
}

# start_sleeper: a live process that is not this test; sets SLEEPER (its pid)
# and SLEEPER_OWNER (the lease owner string that names it). fd 3 is bats's.
start_sleeper() {
  sleep 300 >/dev/null 2>&1 3>&- &
  SLEEPER=$!
  SLEEPER_OWNER="$SLEEPER:$(rr_proc_stamp "$SLEEPER")"
}

# ---- state directory ------------------------------------------------------

@test "rr_init_state puts everything under XDG_STATE_HOME/pg-repo-runner, mode 0700" {
  [ "$RR_STATE_DIR" = "$XDG_STATE_HOME/pg-repo-runner" ]
  [ "$RR_LEASE" = "$RR_STATE_DIR/lease" ]
  [ "$RR_RUN_LOG" = "$RR_STATE_DIR/run.log" ]
  [ -d "$RR_STATE_DIR/verdicts" ]
  [ "$(stat -c %a "$RR_STATE_DIR" 2>/dev/null || stat -f %Lp "$RR_STATE_DIR")" = 700 ]
}

# ---- item id and keys -----------------------------------------------------

@test "rr_parse_item_id accepts <key> and <key>@<sha>" {
  rr_parse_item_id "nix-alpha"
  [ "$RR_ITEM_REPO" = nix-alpha ]
  [ -z "$RR_ITEM_SHA" ]
  rr_parse_item_id "nix-alpha@0123abc"
  [ "$RR_ITEM_REPO" = nix-alpha ]
  [ "$RR_ITEM_SHA" = 0123abc ]
}

@test "rr_parse_item_id refuses path-shaped keys and malformed shas" {
  local bad
  for bad in "" "../alpha" "a/b" ".hidden" "a..b" "-x" "nix-alpha@" "nix-alpha@XYZ" "nix-alpha@abc" "nix-alpha@$(printf 'a%.0s' {1..65})" "workspace@0123abc/x"; do
    run rr_parse_item_id "$bad"
    [ "$status" -ne 0 ] || {
      echo "accepted: $bad"
      return 1
    }
  done
}

# ---- load check -----------------------------------------------------------

@test "rr_to_centi reads hundredths without floating point" {
  [ "$(rr_to_centi 11)" = 1100 ]
  [ "$(rr_to_centi 1.5)" = 150 ]
  [ "$(rr_to_centi 0.05)" = 5 ]
  [ "$(rr_to_centi 10.999)" = 1099 ]
  [ "$(rr_to_centi 08.09)" = 809 ]
}

@test "rr_load_admits is strictly below the threshold" {
  rr_load_admits 10.99 11
  rr_load_admits 0 11
  run rr_load_admits 11 11
  [ "$status" -ne 0 ]
  run rr_load_admits 11.01 11
  [ "$status" -ne 0 ]
  run rr_load_admits 36.16 11
  [ "$status" -ne 0 ]
}

@test "rr_loadavg1 takes the override, rejects a malformed one, and reads a real value otherwise" {
  PG_REPO_RUNNER_LOADAVG=3.25 run rr_loadavg1
  [ "$status" -eq 0 ]
  [ "$output" = 3.25 ]
  PG_REPO_RUNNER_LOADAVG=abc run rr_loadavg1
  [ "$status" -ne 0 ]
  # the real reading: a non-negative decimal on this machine
  run rr_loadavg1
  [ "$status" -eq 0 ]
  [[ $output =~ ^[0-9]+(\.[0-9]+)?$ ]]
}

@test "rr_max_load: env, then config, then the core count" {
  export RR_CONFIG_JSON='{"thresholds":{"max_load":7.5}}'
  PG_REPO_RUNNER_NCPU=4 run rr_max_load
  [ "$output" = 7.5 ]
  PG_REPO_RUNNER_MAX_LOAD=2 PG_REPO_RUNNER_NCPU=4 run rr_max_load
  [ "$output" = 2 ]
  export RR_CONFIG_JSON='{}'
  PG_REPO_RUNNER_NCPU=4 run rr_max_load
  [ "$output" = 4 ]
  PG_REPO_RUNNER_NCPU=0 run rr_max_load
  [ "$status" -ne 0 ]
}

# ---- kill switch ----------------------------------------------------------

@test "rr_kill_switch_active sees the state-dir file and the configured file" {
  export RR_CONFIG_JSON="{\"kill_switch\":\"$TEST_DIR/kill\"}"
  run rr_kill_switch_active
  [ "$status" -ne 0 ]
  touch "$TEST_DIR/kill"
  run rr_kill_switch_active
  [ "$status" -eq 0 ]
  [ "$output" = "$TEST_DIR/kill" ]
  rm "$TEST_DIR/kill"
  touch "$RR_STATE_DIR/push.disabled"
  run rr_kill_switch_active
  [ "$status" -eq 0 ]
  [ "$output" = "$RR_STATE_DIR/push.disabled" ]
}

# ---- lease ----------------------------------------------------------------

@test "rr_lease_acquire takes a free lease and records <pid>:<start time>" {
  local rc=0
  rr_lease_acquire || rc=$?
  [ "$rc" -eq 0 ]
  [ -L "$RR_LEASE" ]
  [ "$(readlink "$RR_LEASE")" = "$$:$(rr_proc_stamp $$)" ]
}

@test "a second acquire while the lease is held by a live process is refused" {
  local rc=0
  rr_lease_acquire || rc=$?
  [ "$rc" -eq 0 ]
  local first
  first="$(readlink "$RR_LEASE")"
  rc=0
  rr_lease_acquire || rc=$?
  [ "$rc" -eq 1 ]
  [ "$(readlink "$RR_LEASE")" = "$first" ]
}

@test "a lease held by ANOTHER live process is refused and left alone" {
  start_sleeper
  ln -s "$SLEEPER_OWNER" "$RR_LEASE"
  local rc=0
  rr_lease_acquire || rc=$?
  [ "$rc" -eq 1 ]
  [ "$(readlink "$RR_LEASE")" = "$SLEEPER_OWNER" ]
}

@test "a lease whose owner is dead is reclaimed" {
  sleep 0.2 &
  local dead=$!
  wait "$dead"
  ln -s "$dead:Thu Jan 1 00:00:00 1970" "$RR_LEASE"
  local rc=0
  rr_lease_acquire || rc=$?
  [ "$rc" -eq 0 ]
  [ "$(readlink "$RR_LEASE")" = "$$:$(rr_proc_stamp $$)" ]
  # nothing left behind by the reclaim
  [ -z "$(find "$RR_STATE_DIR" -maxdepth 1 -name '.reclaim.*')" ]
}

@test "a lease whose pid was recycled by a different process is reclaimed" {
  start_sleeper
  ln -s "$SLEEPER:Thu Jan 1 00:00:00 1970" "$RR_LEASE"
  local rc=0
  rr_lease_acquire || rc=$?
  [ "$rc" -eq 0 ]
  [ "$(readlink "$RR_LEASE")" = "$$:$(rr_proc_stamp $$)" ]
}

@test "a malformed lease owner is treated as stale" {
  ln -s "not-an-owner" "$RR_LEASE"
  local rc=0
  rr_lease_acquire || rc=$?
  [ "$rc" -eq 0 ]
}

@test "rr_lease_release removes our lease, and only ours" {
  local rc=0
  rr_lease_acquire || rc=$?
  rr_lease_release
  [ ! -e "$RR_LEASE" ] && [ ! -L "$RR_LEASE" ]

  # a lease someone else took after we were wrongly judged dead is not removed
  rr_lease_acquire || rc=$?
  start_sleeper
  rm "$RR_LEASE"
  ln -s "$SLEEPER_OWNER" "$RR_LEASE"
  rr_lease_release
  [ "$(readlink "$RR_LEASE")" = "$SLEEPER_OWNER" ]
}

# ---- verdict store --------------------------------------------------------

SHA=0123456789abcdef0123456789abcdef01234567

write_verdict() { # REPO [SHA] [KIND] [RESULT]
  rr_verdict_write "$1" "${2:-$SHA}" "${3:-check}" "${4:-green}" locked \
    2026-10-28T00:00:00Z 2026-10-28T00:05:00Z 0 "$RR_RUN_LOG"
}

@test "rr_verdict_write stores the documented fields at verdicts/<repo>/<sha>.<kind>.json" {
  write_verdict nix-alpha
  local f="$RR_STATE_DIR/verdicts/nix-alpha/$SHA.check.json"
  [ -f "$f" ]
  run jq -c 'to_entries | map(.key) | sort' "$f"
  [ "$output" = '["exit","finishedAt","inputs","kind","logPath","repo","result","sha","startedAt"]' ]
  [ "$(jq -r .repo "$f")" = nix-alpha ]
  [ "$(jq -r .sha "$f")" = "$SHA" ]
  [ "$(jq -r .kind "$f")" = check ]
  [ "$(jq -r .result "$f")" = green ]
  [ "$(jq -r .inputs "$f")" = locked ]
  [ "$(jq -r .exit "$f")" = 0 ]
  [ "$(jq -r .logPath "$f")" = "$RR_RUN_LOG" ]
}

@test "a verdict write leaves no temp file, and an overwrite replaces the verdict whole" {
  write_verdict nix-alpha
  rr_verdict_write nix-alpha "$SHA" check red override 2026-10-28T01:00:00Z 2026-10-28T01:10:00Z 1 "$RR_RUN_LOG"
  [ "$(find "$RR_STATE_DIR/verdicts" -type f | wc -l | tr -d ' ')" = 1 ]
  [ -z "$(find "$RR_STATE_DIR/verdicts" -name '.*' -type f)" ]
  local f="$RR_STATE_DIR/verdicts/nix-alpha/$SHA.check.json"
  [ "$(jq -r .result "$f")" = red ]
  [ "$(jq -r .inputs "$f")" = override ]
  [ "$(jq -r .exit "$f")" = 1 ]
}

@test "a failing write (jq missing) fails and leaves neither a verdict nor a temp file" {
  RR_JQ_BIN=/nonexistent/jq run write_verdict nix-alpha
  [ "$status" -ne 0 ]
  [ -z "$(find "$RR_STATE_DIR/verdicts" -type f)" ]
}

@test "rr_verdict_write rejects bad fields before touching the store" {
  run rr_verdict_write ../escape "$SHA" check green locked a b 0 x
  [ "$status" -eq 2 ]
  run rr_verdict_write nix-alpha not-a-sha check green locked a b 0 x
  [ "$status" -eq 2 ]
  run rr_verdict_write nix-alpha "$SHA" lint green locked a b 0 x
  [ "$status" -eq 2 ]
  run rr_verdict_write nix-alpha "$SHA" check amber locked a b 0 x
  [ "$status" -eq 2 ]
  run rr_verdict_write nix-alpha "$SHA" check green maybe a b 0 x
  [ "$status" -eq 2 ]
  run rr_verdict_write nix-alpha "$SHA" check green locked a b one x
  [ "$status" -eq 2 ]
  [ -z "$(find "$RR_STATE_DIR/verdicts" -mindepth 1)" ]
}

@test "rr_verdicts_json lists every verdict, filters by repo, and never reads a temp file" {
  run rr_verdicts_json
  [ "$output" = '[]' ]
  write_verdict nix-beta "$SHA" check red
  write_verdict nix-alpha "$SHA" check green
  write_verdict nix-alpha abcdef1 build error
  echo '{"junk":true}' >"$RR_STATE_DIR/verdicts/nix-alpha/.0123.check.XXXXXX"
  run rr_verdicts_json
  [ "$status" -eq 0 ]
  [ "$(jq length <<<"$output")" = 3 ]
  [ "$(jq -r '[.[].repo] | join(",")' <<<"$output")" = nix-alpha,nix-alpha,nix-beta ]
  run rr_verdicts_json nix-beta
  [ "$(jq -r '[.[].result] | join(",")' <<<"$output")" = red ]
}

# ---- nix, as the kernel offers it -----------------------------------------

# Stand-ins that record their argv: the real nix is not run (a check build
# cannot run nix); what is pinned is the COMMAND SHAPE, which is the rule.

@test "rr_nix_run is nice -n <level> pg-nix-log-wrapped nix ARGS, every binary by absolute path" {
  cat >"$TEST_DIR/nice" <<EOF
#!/usr/bin/env bash
echo "nice \$1 \$2" >>"$TEST_DIR/argv"
shift 2
exec "\$@"
EOF
  cat >"$TEST_DIR/wrapped" <<EOF
#!/usr/bin/env bash
echo "wrapped \$(basename "\$1")" >>"$TEST_DIR/argv"
exec "\$@"
EOF
  cat >"$TEST_DIR/nix" <<EOF
#!/usr/bin/env bash
echo "nix \$*" >>"$TEST_DIR/argv"
EOF
  chmod +x "$TEST_DIR/nice" "$TEST_DIR/wrapped" "$TEST_DIR/nix"
  RR_NICE_BIN="$TEST_DIR/nice" RR_NIX_LOG_WRAPPED_BIN="$TEST_DIR/wrapped" RR_NIX_BIN="$TEST_DIR/nix" \
    rr_nix_run flake check --no-write-lock-file
  [ "$(cat "$TEST_DIR/argv")" = "nice -n 10
wrapped nix
nix flake check --no-write-lock-file" ]
}

@test "rr_nix_run uses the configured nice level" {
  export RR_CONFIG_JSON='{"thresholds":{"nice_level":15}}'
  [ "$(rr_nice_level)" = 15 ]
  export RR_CONFIG_JSON='{"thresholds":{"nice_level":99}}'
  [ "$(rr_nice_level)" = 10 ]
  export RR_CONFIG_JSON='{}'
  [ "$(rr_nice_level)" = 10 ]
}

@test "rr_nix_run without a configured nix is a malfunction, and without the wrapper it says so" {
  RR_NIX_BIN="" run rr_nix_run flake check
  [ "$status" -eq 1 ]
  [[ $output == *"no nix binary"* ]]

  printf '#!/usr/bin/env bash\necho ran "$@"\n' >"$TEST_DIR/nix"
  chmod +x "$TEST_DIR/nix"
  printf '#!/usr/bin/env bash\nshift 2\nexec "$@"\n' >"$TEST_DIR/nice"
  chmod +x "$TEST_DIR/nice"
  RR_NIX_BIN="$TEST_DIR/nix" RR_NIX_LOG_WRAPPED_BIN="" RR_NICE_BIN="$TEST_DIR/nice" \
    run rr_nix_run build .#x
  [ "$status" -eq 0 ]
  [[ $output == *"running nix without it"* ]]
}

@test "rr_verdict_write accepts superseded, and merges an extra JSON object into the record" {
  rr_verdict_write nix-alpha "$SHA" check superseded locked a b 0 ""
  [ "$(jq -r .result "$RR_STATE_DIR/verdicts/nix-alpha/$SHA.check.json")" = superseded ]
  rr_verdict_write nix-alpha "$SHA" check green locked a b 0 x '{"durationS":42,"loadStart":1.5,"loadEnd":null}'
  local f="$RR_STATE_DIR/verdicts/nix-alpha/$SHA.check.json"
  [ "$(jq -r .durationS "$f")" = 42 ]
  [ "$(jq -r .loadStart "$f")" = 1.5 ]
  [ "$(jq -r .loadEnd "$f")" = null ]
  [ "$(jq -r .result "$f")" = green ]
  # an extra that is not an object is refused before the store is touched
  run rr_verdict_write nix-alpha "$SHA" build green locked a b 0 x '[1]'
  [ "$status" -eq 2 ]
  [ ! -e "$RR_STATE_DIR/verdicts/nix-alpha/$SHA.build.json" ]
}

# ---- runner B helpers -----------------------------------------------------

@test "rr_verdict_exists is true for exactly the stored repo, sha and kind" {
  write_verdict nix-alpha
  rr_verdict_exists nix-alpha "$SHA" check
  run rr_verdict_exists nix-alpha "$SHA" build
  [ "$status" -eq 1 ]
  run rr_verdict_exists nix-beta "$SHA" check
  [ "$status" -eq 1 ]
  run rr_verdict_exists nix-alpha abcdef1 check
  [ "$status" -eq 1 ]
}

@test "rr_signal_exit is exactly 129 to 192" {
  local n
  for n in 129 130 137 143 192; do rr_signal_exit "$n"; done
  for n in 0 1 2 100 127 128 193 255 "" x; do
    if rr_signal_exit "$n"; then
      echo "wrongly a signal exit: '$n'"
      return 1
    fi
  done
}

@test "rr_url_path_encode keeps path characters and encodes the ones a URL gives meaning to" {
  [ "$(rr_url_path_encode /a/b-c_d.e~f)" = /a/b-c_d.e~f ]
  [ "$(rr_url_path_encode '/a b/c?d#e%f&g')" = '/a%20b/c%3Fd%23e%25f%26g' ]
}

@test "rr_snapshot_url is a git+file URL of the exact commit" {
  [ "$(rr_snapshot_url '/ws/nix alpha' "$SHA")" = "git+file:///ws/nix%20alpha?rev=$SHA" ]
}

@test "rr_iso_at formats an epoch as RFC 3339 UTC" {
  [ "$(rr_iso_at 0)" = 1970-01-01T00:00:00Z ]
  [ "$(rr_iso_at 1790000000)" = 2026-09-21T14:13:20Z ]
}

@test "rr_retry_window: environment, then config, then an hour; junk falls back" {
  export RR_CONFIG_JSON='{}'
  [ "$(rr_retry_window)" = 3600 ]
  export RR_CONFIG_JSON='{"thresholds":{"retry_window":900}}'
  [ "$(rr_retry_window)" = 900 ]
  PG_REPO_RUNNER_RETRY_WINDOW=60 run rr_retry_window
  [ "$output" = 60 ]
  PG_REPO_RUNNER_RETRY_WINDOW=soon run rr_retry_window
  [ "$output" = 3600 ]
  export RR_CONFIG_JSON='{"thresholds":{"retry_window":0}}'
  [ "$(rr_retry_window)" = 3600 ]
}
