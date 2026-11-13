#!/usr/bin/env bats
# Listener tests against the real bash in a temporary state directory. Tag `fast`: reading a line,
# the burst of 200, the directory rules. Tag `slow`: heal, replace, stale file and parent-death
# cleanup, which wait for the one-second health check.

setup() {
  LISTEN="${PGCC_LISTEN:-$BATS_TEST_DIRNAME/../scripts/listen.sh}"
  STATE="$(mktemp -d)"
  export PG_CLAUDE_CONTROL_STATE_DIR="$STATE"
  OUT="$STATE.out"
  ERR="$STATE.err"
  PIDF="$STATE.pid"
  OUT2="$STATE.second"
  PARENT=""
}

teardown() {
  [ -n "$PARENT" ] && kill -9 "$PARENT" 2>/dev/null || true
  [ -f "$PIDF" ] && kill -9 "$(cat "$PIDF")" 2>/dev/null || true
  rm -rf "$STATE" "$OUT" "$ERR" "$PIDF" "$OUT2" "$STATE.stale-inode"
}

# Starts the listener as a child of a throwaway bash (its $PPID), which stays alive until killed.
start_listener() {
  (exec bash -c 'bash "$1" >"$2" 2>"$3" & echo $! >"$4"; wait' _ "$LISTEN" "$OUT" "$ERR" "$PIDF") &
  PARENT=$!
  FIFO="$STATE/inbox/$PARENT.fifo"
  for _ in $(seq 1 50); do
    [ -p "$FIFO" ] && return 0
    sleep 0.1
  done
  return 1
}

wait_for() { # <timeout-s> <cmd...>
  local n=$(($1 * 10))
  shift
  for _ in $(seq 1 "$n"); do
    "$@" && return 0
    sleep 0.1
  done
  return 1
}

out_lines() { [ "$(wc -l <"$OUT" | tr -d ' ')" -eq "$1" ]; }

mode_of() { ls -ld "$1" | cut -c1-10; }

# bats test_tags=fast
@test "the listener holds <pid>.fifo (0600) in an inbox (0700) and is a child of its parent" {
  start_listener
  [ "$(mode_of "$STATE/inbox")" = "drwx------" ]
  [ "$(mode_of "$FIFO")" = "prw-------" ]
  [ "$(mode_of "$STATE/inbox/payloads")" = "drwx------" ]
  # the FIFO is named by the listener's $PPID, so the parent's pid in its name is the proof
  [ "$FIFO" = "$STATE/inbox/$PARENT.fifo" ]
  [ "$(cat "$PIDF")" != "$PARENT" ]
}

# bats test_tags=fast
@test "an existing looser inbox directory is tightened to 0700" {
  mkdir -p "$STATE/inbox"
  chmod 755 "$STATE/inbox"
  start_listener
  [ "$(mode_of "$STATE/inbox")" = "drwx------" ]
}

# bats test_tags=fast
@test "a symbolic link as the inbox directory is refused with exit 20 and an event" {
  mkdir -p "$STATE/elsewhere"
  ln -s "$STATE/elsewhere" "$STATE/inbox"
  run bash "$LISTEN"
  [ "$status" -eq 20 ]
  [[ "$output" == *"inbox_dir_refused"* ]]
}

# bats test_tags=fast
@test "a directory owned by someone else is refused" {
  [ ! -O / ] || skip "the root directory is ours"
  PG_CLAUDE_CONTROL_STATE_DIR=/ run bash "$LISTEN"
  [ "$status" -eq 20 ]
  [[ "$output" == *"not owned"* ]]
}

# bats test_tags=fast
@test "a line written to the FIFO arrives on stdout" {
  start_listener
  printf 'id=a1 ping\n' >"$FIFO"
  wait_for 3 grep -qx 'id=a1 ping' "$OUT"
}

# bats test_tags=fast
@test "a burst of 200 lines is received completely and in order" {
  start_listener
  for i in $(seq 1 200); do printf 'ping n=%s\n' "$i"; done >"$FIFO"
  wait_for 5 out_lines 200
  [ "$(head -1 "$OUT")" = "ping n=1" ]
  [ "$(tail -1 "$OUT")" = "ping n=200" ]
}

# bats test_tags=fast
@test "two concurrent writers of lines within the 512-byte cap do not interleave" {
  start_listener
  pad="$(printf 'x%.0s' $(seq 1 400))"
  (for i in $(seq 1 60); do printf 'A%s %s\n' "$i" "$pad"; done >"$FIFO") &
  (for i in $(seq 1 60); do printf 'B%s %s\n' "$i" "$pad"; done >"$FIFO") &
  wait_for 8 out_lines 120
  [ "$(grep -c "^[AB][0-9]* $pad\$" "$OUT")" -eq 120 ]
}

# bats test_tags=slow
@test "a deleted FIFO is recreated" {
  start_listener
  rm -f "$FIFO"
  wait_for 4 test -p "$FIFO"
  printf 'ping after-heal\n' >"$FIFO"
  wait_for 3 grep -qx 'ping after-heal' "$OUT"
}

# bats test_tags=slow
@test "a FIFO replaced by another file is detected and healed" {
  start_listener
  rm -f "$FIFO"
  echo not-a-fifo >"$FIFO"
  wait_for 4 test -p "$FIFO"
  printf 'ping after-replace\n' >"$FIFO"
  wait_for 3 grep -qx 'ping after-replace' "$OUT"
}

# bats test_tags=slow
@test "a stale FIFO left by a killed listener is replaced when the next listener of that pid starts" {
  # one parent starts a listener, SIGKILLs it (its FIFO stays), then starts a second one
  (exec bash -c '
    bash "$1" >"$2" 2>"$3" & first=$!
    for _ in $(seq 1 50); do [ -p "$5"/inbox/$$.fifo ] && break; sleep 0.1; done
    kill -9 $first
    ls -i "$5"/inbox/$$.fifo | cut -d" " -f1 >"$5.stale-inode"
    bash "$1" >"${6:?second output path required}" 2>"$3" & echo $! >"$4"
    wait' _ "$LISTEN" "$OUT" "$ERR" "$PIDF" "$STATE" "$OUT2") &
  PARENT=$!
  FIFO="$STATE/inbox/$PARENT.fifo"
  wait_for 6 test -s "$PIDF"
  stale="$(cat "$STATE.stale-inode")"
  # the second listener replaces the stale FIFO with a new one (a new inode)
  fresh_inode() { [ "$(ls -i "$FIFO" | cut -d' ' -f1)" != "$stale" ]; }
  wait_for 6 fresh_inode
  exec 9<>"$FIFO" # read-write open never blocks, whoever is reading
  printf 'ping second\n' >&9
  exec 9>&-
  wait_for 3 grep -qx 'ping second' "$OUT2"
}

# bats test_tags=slow
@test "when the parent dies by SIGKILL the listener removes its FIFO and exits within 3 seconds" {
  start_listener
  lpid="$(cat "$PIDF")"
  kill -9 "$PARENT"
  wait_for 3 test ! -e "$FIFO"
  wait_for 3 bash -c "! kill -0 $lpid 2>/dev/null"
  PARENT=""
}
