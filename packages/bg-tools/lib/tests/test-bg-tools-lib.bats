#!/usr/bin/env bats
# bats file_tags=type:unit

# `run !` (used for the rejection asserts) needs bats >= 1.5.0.
bats_require_minimum_version 1.5.0

if [[ -n ${TEST_SUPPORT:-} ]]; then
  source "$TEST_SUPPORT/test_helper.bash"
else
  source "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/test_helper.bash"
fi

load_lib() {
  # shellcheck disable=SC1090 # composed lib path is runtime-resolved (nix check injects LIB_PATH; locally it is the source dir)
  source "$(resolve_lib)"
}

@test "bg_state_dir honors BG_DIR override" {
  load_lib
  BG_DIR="/some/dir" run bg_state_dir
  [ "$status" -eq 0 ]
  [ "$output" = "/some/dir" ]
}

@test "bg_state_dir defaults under TMPDIR per user" {
  load_lib
  unset BG_DIR
  TMPDIR="/tmp/t" USER="alice" run bg_state_dir
  [ "$status" -eq 0 ]
  [ "$output" = "/tmp/t/pg-bg-alice" ]
}

@test "bg_validate_name accepts sane names and rejects path-hostile ones" {
  load_lib
  bg_validate_name "flake-check"
  bg_validate_name "build.2"
  bg_validate_name "a_b-c.d"
  run ! bg_validate_name ""
  run ! bg_validate_name ".hidden"
  run ! bg_validate_name "has space"
  run ! bg_validate_name "../escape"
  run ! bg_validate_name "a/b"
}

@test "path helpers compose state dir and name" {
  load_lib
  [ "$(bg_log_path job1)" = "$BG_DIR/job1.log" ]
  [ "$(bg_pid_path job1)" = "$BG_DIR/job1.pid" ]
  [ "$(bg_exit_path job1)" = "$BG_DIR/job1.exit" ]
  [ "$(bg_meta_path job1)" = "$BG_DIR/job1.meta" ]
}

@test "bg_status: UNKNOWN with exit 1 when nothing recorded" {
  load_lib
  mkdir -p "$BG_DIR"
  run bg_status ghost
  [ "$status" -eq 1 ]
  [ "$output" = "UNKNOWN" ]
}

@test "bg_status: DONE wins even when the recorded pid is still alive" {
  load_lib
  mkdir -p "$BG_DIR"
  echo "$$" >"$BG_DIR/j.pid" # our own live pid
  echo "7" >"$BG_DIR/j.exit"
  run bg_status j
  [ "$status" -eq 0 ]
  [ "$output" = "DONE exit=7" ]
}

@test "bg_status: RUNNING for a live pid without exit record" {
  load_lib
  mkdir -p "$BG_DIR"
  echo "$$" >"$BG_DIR/j.pid"
  run bg_status j
  [ "$status" -eq 0 ]
  [[ "$output" == RUNNING\ pid=$$\ etime=* ]]
}

@test "bg_status: EXITED unknown for a dead pid without exit record" {
  load_lib
  mkdir -p "$BG_DIR"
  # A pid that is certainly dead: spawn-and-reap.
  bash -c 'exit 0' &
  local_pid=$!
  wait "$local_pid"
  echo "$local_pid" >"$BG_DIR/j.pid"
  run bg_status j
  [ "$status" -eq 0 ]
  [ "$output" = "EXITED unknown" ]
}

@test "bg_list_names lists recorded jobs and tolerates an empty dir" {
  load_lib
  run bg_list_names
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  mkdir -p "$BG_DIR"
  touch "$BG_DIR/a.pid" "$BG_DIR/b.2.pid"
  run bg_list_names
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "a" ]
  [ "${lines[1]}" = "b.2" ]
}

@test "bg_meta_get returns the FIRST line carrying the key, and fails when absent" {
  load_lib
  mkdir -p "$BG_DIR"
  printf 'session: first\ncmd: echo a\nsession: second\n' >"$BG_DIR/m.meta"
  run bg_meta_get m session
  [ "$status" -eq 0 ]
  [ "$output" = "first" ]
  run bg_meta_get m start
  [ "$status" -eq 1 ]
  run bg_meta_get nofile session
  [ "$status" -eq 1 ]
}

@test "bg_proc_start and bg_proc_command describe a live pid and are empty for a dead one" {
  load_lib
  [ -n "$(bg_proc_start $$)" ]
  [ -n "$(bg_proc_command $$)" ]
  bash -c 'exit 0' &
  dead=$!
  wait "$dead"
  [ -z "$(bg_proc_start "$dead")" ]
  [ -z "$(bg_proc_command "$dead")" ]
}

@test "bg_descendants lists every transitive child deepest-first, excluding the root" {
  load_lib
  bash -c 'bash -c "sleep 30 & wait" & wait' &
  root=$!
  # Bounded wait for the three-deep tree to exist.
  for _ in $(seq 1 100); do
    [ "$(bg_descendants "$root" | wc -l)" -ge 2 ] && break
    sleep 0.05
  done
  mapfile -t got < <(bg_descendants "$root")
  [ "${#got[@]}" -eq 2 ]
  [[ "$(bg_proc_command "${got[0]}")" == *"sleep"* ]] # the leaf comes first
  for p in "${got[@]}"; do
    [ "$p" != "$root" ]
  done
  kill "${got[0]}" "${got[1]}" "$root" 2>/dev/null || true
}
