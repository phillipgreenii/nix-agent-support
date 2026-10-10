#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Script-level (subprocess) tests for the pg-task-focus-swiftbar renderer.
#
# Rendering: `--render` reads one state document (the shape of GET /state, which
# is also each line of `status --watch`) and prints the frame. PG_TASK_FOCUS_SWIFTBAR_NOW
# and TZ are pinned so every countdown and clock string is deterministic. TZ uses a
# POSIX fixed-offset string (EST5 = UTC-5, no DST) so no tzdata is needed in a sandbox.
#
# Streaming: a stub `pg-task-focus` on PG_TASK_FOCUS_BIN plays the part of
# `status --watch`, so the real renderer's loop, tick, separator and reconnect
# are exercised without a daemon. What these tests CANNOT prove is how the live
# SwiftBar GUI treats the separator: that is the one unverified assumption named
# in the renderer's STREAM_SEPARATOR comment.

setup() {
  TEST_DIR="$(mktemp -d)"
  export HOME="$TEST_DIR/home"
  mkdir -p "$HOME"

  local scripts_dir="${SCRIPTS_DIR:-}"
  if [[ -z $scripts_dir ]]; then
    scripts_dir="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  # Prefer the assembled artifact (strict mode, runtimeDeps wrapper) when the
  # nix check provides it; fall back to the raw source for a local `bats tests/`.
  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    PLUGIN=("$SCRIPT_UNDER_TEST")
  else
    PLUGIN=(bash "$scripts_dir/pg-task-focus-swiftbar.sh")
  fi

  # NOW = 2026-10-08T03:46:00Z = 2026-10-07 22:46 local (EST5).
  NOW="$(jq -n '"2026-10-08T03:46:00Z" | fromdateiso8601')"
  export TZ=EST5
  export PG_TASK_FOCUS_SWIFTBAR_NOW="$NOW"
  # An executable stand-in: the renderer only offers actions for a binary it can resolve.
  BINP="$TEST_DIR/bin/pg-task-focus"
  mkdir -p "$TEST_DIR/bin"
  printf '#!/bin/sh\nexit 0\n' >"$BINP"
  chmod +x "$BINP"
  export PG_TASK_FOCUS_BIN="$BINP"
  export PG_TASK_FOCUS_ADDR="127.0.0.1:49310"
  unset PG_TASK_FOCUS_SWIFTBAR_WEB_URL PG_TASK_FOCUS_SWIFTBAR_DUE_SOON_MIN PG_TASK_FOCUS_SWIFTBAR_BOOST_MINUTES
  WEB="http://127.0.0.1:49310"
  STATE="$TEST_DIR/state.json"
}

teardown() {
  # A failed test prints the last `run` output (TAP diagnostics on fd 3).
  if [[ -z ${BATS_TEST_COMPLETED:-} ]]; then
    printf '# status=%s output:\n# %s\n' "${status:-?}" "${output//$'\n'/$'\n# '}" >&3
  fi
  rm -rf "$TEST_DIR"
}

# at OFFSET -> ISO-8601 UTC instant NOW+OFFSET seconds, with the daemon's milliseconds.
at() {
  jq -rn --argjson t "$((NOW + $1))" '$t | todate | sub("Z$"; ".000Z")'
}

# state FILTER -> writes the state fixture: an initialized, idle daemon read at NOW,
# with FILTER applied.
state() {
  jq -n --arg read "$(at 0)" '{
    read_at: $read, initialized: true, profile: "normal", version: "v1",
    store: {state: "ok"},
    periods: [{kind: "day", start: "2026-10-07", end: "2026-10-07", zone: "America/New_York",
               today: "2026-10-07", ended: false}],
    tasks: [], next: null, focus: null, dimmed: [], interrupt_stack: [], resume_offer: null
  }' | jq "$1" >"$STATE"
}

# render -> the frame for the current fixture.
render() {
  run "${PLUGIN[@]}" --render <"$STATE"
}

# The fixture cycle builders, as jq function definitions prepended to a filter.
LIB='
def cyc($id; $type; $title; $status; $elapsed; $rem): {
  id: $id, type: $type, title: $title, planned_minutes: 25, boost_minutes: 0, status: $status,
  elapsed_seconds: $elapsed, remaining_seconds: $rem, overtime: ($rem < 0), not_in_profile: false,
  kv: [], alert: {sound: null, reminder_sound: null, repeat_minutes: 5}
};
def dim($id; $type; $title; $elapsed; $sw): cyc($id; $type; $title; "paused"; $elapsed; 600) + {can_switch: $sw};
'

title() { printf '%s\n' "${lines[0]}"; }

# ---------------------------------------------------------------------------
# Title: precedence

@test "idle: the title and a no-cycle row, then the web link" {
  state '.'
  render
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "idle" ]
  [ "${lines[1]}" = "---" ]
  [[ $output == *"No cycle running | color=#888888"* ]]
  [[ $output == *"Open pg-task-focus | href=$WEB"* ]]
}

@test "running: the type and the time left, as mm:ss" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)'
  render
  [ "${lines[0]}" = "deep 12:30" ]
}

@test "running: the time left is moved on by the time since the state was read" {
  state "$LIB"'.read_at = "'"$(at -30)"'" | .focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)'
  render
  [ "${lines[0]}" = "deep 12:00" ]
}

@test "running: an hour or more reads h:mm:ss" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 0; 3725)'
  render
  [ "${lines[0]}" = "deep 1:02:05" ]
}

@test "overtime: a warning icon, a plus sign and the time over" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 1900; -250)'
  render
  [ "${lines[0]}" = ":exclamationmark.triangle.fill: deep +4:10 | color=#cc3333" ]
  [[ $output == *"▶ Deep work · overtime +4:10 | color=#cc3333"* ]]
}

@test "overtime begins when the clock passes the end, not only at the next state" {
  state "$LIB"'.read_at = "'"$(at -60)"'" | .focus = cyc("C1"; "deep"; "Deep work"; "running"; 1490; 10)'
  render
  [ "${lines[0]}" = ":exclamationmark.triangle.fill: deep +0:50 | color=#cc3333" ]
}

@test "paused only: a pause glyph and the most recently paused type" {
  state "$LIB"'.dimmed = [dim("C2"; "email"; "Email"; 300; false), dim("C3"; "deep"; "Deep work"; 900; false)]'
  render
  [ "${lines[0]}" = "⏸ email | color=#888888" ]
}

@test "paused with a resume offer naming it: the title asks to resume" {
  state "$LIB"'.dimmed = [dim("C2"; "deep"; "Deep work"; 300; false)]
    | .resume_offer = {cycle: cyc("C2"; "deep"; "Deep work"; "paused"; 300; 600), action: "resume"}'
  render
  [ "${lines[0]}" = "⏸ deep · Resume? | color=#888888" ]
}

@test "a resume offer for another cycle than the one shown paused: Resume <title>?" {
  state "$LIB"'.dimmed = [dim("C2"; "email"; "Email"; 300; false), dim("C3"; "deep"; "Deep work"; 900; false)]
    | .resume_offer = {cycle: cyc("C3"; "deep"; "Deep work"; "paused"; 900; 600), action: "resume"}'
  render
  [ "${lines[0]}" = "Resume Deep work?" ]
}

@test "an ended period: the exact banner" {
  state '.periods[0].ended = true | .periods[0].banner = "New day: roll over"'
  render
  [ "${lines[0]}" = "New day: roll over" ]
}

@test "an ended week reads its own banner" {
  state '.periods += [{kind: "week", start: "2026-09-28", end: "2026-10-04", zone: "America/New_York", today: "2026-10-07", ended: true, banner: "New week: roll over"}]'
  render
  [ "${lines[0]}" = "New week: roll over" ]
}

@test "next due: the task and the time to it" {
  state '.next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at 1320)"'", due_zone: "America/New_York", status: "open", overdue: false}'
  render
  [ "${lines[0]}" = "Next: Post plan 22m" ]
}

@test "next due: an hour or more reads 1h05m" {
  state '.next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at 3900)"'", due_zone: "America/New_York", status: "open", overdue: false}'
  render
  [ "${lines[0]}" = "Next: Post plan 1h05m" ]
}

@test "next overdue: overdue 12m, never a negative time" {
  state '.next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at -720)"'", due_zone: "America/New_York", status: "open", overdue: true}'
  render
  [ "${lines[0]}" = "Next: Post plan overdue 12m" ]
  [[ ${lines[0]} != *"-"* ]]
}

@test "precedence: read-only beats overtime, overtime beats running, running beats paused" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 1900; -250) | .dimmed = [dim("C2"; "email"; "Email"; 300; true)]
    | .periods[0].ended = true | .periods[0].banner = "New day: roll over"
    | .store = {state: "read_only", reason: "the append fsync failed", since: "'"$(at -5)"'"}'
  render
  [ "${lines[0]}" = ":lock.fill: READ-ONLY | color=#cc3333" ]

  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 1900; -250) | .dimmed = [dim("C2"; "email"; "Email"; 300; true)]'
  render
  [[ ${lines[0]} == ":exclamationmark.triangle.fill: deep +4:10"* ]]

  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 100; 600) | .dimmed = [dim("C2"; "email"; "Email"; 300; true)]'
  render
  [ "${lines[0]}" = "deep 10:00" ]
}

@test "precedence: paused beats an ended period, which beats the next task" {
  state "$LIB"'.dimmed = [dim("C2"; "email"; "Email"; 300; false)] | .periods[0].ended = true | .periods[0].banner = "New day: roll over"'
  render
  [ "${lines[0]}" = "⏸ email | color=#888888" ]

  state '.periods[0].ended = true | .periods[0].banner = "New day: roll over"
    | .next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at 600)"'", due_zone: "America/New_York", status: "open", overdue: false}'
  render
  [ "${lines[0]}" = "New day: roll over" ]
}

# ---------------------------------------------------------------------------
# Title: the next-task suffix while a cycle runs or is paused

@test "suffix: a task due within the due-soon window is appended" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)
    | .next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at 480)"'", due_zone: "America/New_York", status: "open", overdue: false}'
  render
  [ "${lines[0]}" = "deep 12:30 · Post plan 8m" ]
}

@test "suffix: an overdue task reads overdue 12m" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)
    | .next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at -720)"'", due_zone: "America/New_York", status: "open", overdue: true}'
  render
  [ "${lines[0]}" = "deep 12:30 · Post plan overdue 12m" ]
}

@test "suffix: a task due beyond the window is left to the dropdown" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)
    | .next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at 3600)"'", due_zone: "America/New_York", status: "open", overdue: false}'
  render
  [ "${lines[0]}" = "deep 12:30" ]
}

@test "suffix: the window is configurable" {
  export PG_TASK_FOCUS_SWIFTBAR_DUE_SOON_MIN=90
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)
    | .next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at 3600)"'", due_zone: "America/New_York", status: "open", overdue: false}'
  render
  [ "${lines[0]}" = "deep 12:30 · Post plan 1h00m" ]
}

@test "suffix: a paused cycle also carries it" {
  state "$LIB"'.dimmed = [dim("C2"; "email"; "Email"; 300; false)]
    | .next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at 480)"'", due_zone: "America/New_York", status: "open", overdue: false}'
  render
  [ "${lines[0]}" = "⏸ email · Post plan 8m | color=#888888" ]
}

# ---------------------------------------------------------------------------
# Read-only mode (operator ruling 7)

@test "read-only: the dropdown opens with the reason and the restart sentence" {
  state '.store = {state: "read_only", reason: "the append fsync failed", since: "'"$(at -5)"'"}'
  render
  [ "${lines[0]}" = ":lock.fill: READ-ONLY | color=#cc3333" ]
  [ "${lines[1]}" = "---" ]
  [ "${lines[2]}" = "READ-ONLY: the append fsync failed. Restart pg-task-focus to recover | color=#cc3333" ]
}

@test "read-only: no row carries an action except the web link" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)
    | .dimmed = [dim("C2"; "email"; "Email"; 300; true)]
    | .resume_offer = {cycle: cyc("C2"; "email"; "Email"; "paused"; 300; 600), action: "switch"}
    | .next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at 480)"'", due_zone: "America/New_York", status: "open", overdue: false}
    | .store = {state: "read_only", reason: "the append fsync failed", since: "'"$(at -5)"'"}'
  render
  [ "$status" -eq 0 ]
  [[ $output != *"bash="* ]]
  [[ $output == *"Open pg-task-focus | href=$WEB"* ]]
  # The cycle rows are still shown, only without a way to change anything.
  [[ $output == *"▶ Deep work"* ]]
  [[ $output == *"⏸ Email"* ]]
}

# ---------------------------------------------------------------------------
# Dropdown: actions go through the CLI, naming the address and the client

@test "running: pause, stop and boost are direct CLI invocations with address and client" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)'
  render
  [[ $output == *"Pause | bash=$BINP param1=--addr param2=127.0.0.1:49310 param3=--client param4=swiftbar param5=cycle param6=pause param7=C1 terminal=false refresh=false"* ]]
  [[ $output == *"Stop | bash=$BINP param1=--addr param2=127.0.0.1:49310 param3=--client param4=swiftbar param5=cycle param6=stop param7=C1 terminal=false refresh=false"* ]]
  [[ $output == *"Boost +5 min | bash="*"param5=cycle param6=boost param7=C1 param8=--minutes param9=5 terminal=false"* ]]
  [[ $output == *"Boost +10 min"* ]]
  [[ $output == *"Boost +25 min"* ]]
}

@test "boost sizes come from the environment" {
  export PG_TASK_FOCUS_SWIFTBAR_BOOST_MINUTES="15 30"
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)'
  render
  [[ $output == *"Boost +15 min"* ]]
  [[ $output == *"Boost +30 min"* ]]
  [[ $output != *"Boost +5 min"* ]]
}

@test "a dimmed row has a Switch and a Stop action while another cycle runs" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750) | .dimmed = [dim("C2"; "email"; "Email"; 300; true)]'
  render
  [[ $output == *"⏸ Email · paused · 5m elapsed | color=#888888"* ]]
  [[ $output == *"--Switch to this cycle | bash="*"param5=cycle param6=switch param7=C2 terminal=false"* ]]
  [[ $output == *"--Stop | bash="*"param5=cycle param6=stop param7=C2 terminal=false"* ]]
}

@test "a dimmed row the daemon says cannot be switched to has no Switch action" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750) | .dimmed = [dim("C2"; "email"; "Email"; 300; false)]'
  render
  [[ $output != *"param6=switch"* ]]
  [[ $output == *"param6=stop param7=C2"* ]]
}

@test "with nothing running a dimmed row offers Resume (a switch would be refused)" {
  state "$LIB"'.dimmed = [dim("C2"; "email"; "Email"; 300; false)]'
  render
  [[ $output == *"--Resume | bash="*"param5=cycle param6=resume param7=C2 terminal=false"* ]]
  [[ $output != *"param6=switch"* ]]
}

@test "the resume offer acts as a switch while another cycle runs, a resume otherwise" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750) | .dimmed = [dim("C2"; "email"; "Email"; 300; true)]
    | .resume_offer = {cycle: cyc("C2"; "email"; "Email"; "paused"; 300; 600), action: "switch"}'
  render
  [[ $output == *"Resume Email? | bash="*"param5=cycle param6=switch param7=C2"* ]]

  state "$LIB"'.dimmed = [dim("C2"; "email"; "Email"; 300; false)]
    | .resume_offer = {cycle: cyc("C2"; "email"; "Email"; "paused"; 300; 600), action: "resume"}'
  render
  [[ $output == *"Resume Email? | bash="*"param5=cycle param6=resume param7=C2"* ]]
}

@test "complete next task runs task done with the definition name" {
  state '.next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at 1320)"'", due_zone: "America/New_York", status: "open", overdue: false}'
  render
  [[ $output == *"Next: Post plan · due 23:08 EST · in 22m"* ]]
  [[ $output == *"Complete Post plan | bash=$BINP param1=--addr param2=127.0.0.1:49310 param3=--client param4=swiftbar param5=task param6=done param7=post-plan terminal=false refresh=false"* ]]
}

@test "an overdue next task is red in the dropdown" {
  state '.next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at -720)"'", due_zone: "America/New_York", status: "open", overdue: true}'
  render
  [[ $output == *"Next: Post plan · due 22:34 EST · overdue 12m | color=#cc3333"* ]]
}

@test "a value that is not plainly safe leaves the text without an action" {
  state "$LIB"'.focus = cyc("C1 x"; "deep"; "Deep work"; "running"; 600; 750)'
  render
  [[ $output == *$'\nPause\n'* ]]
  [[ $output != *"param7=C1 x"* ]]
}

@test "the address is omitted from actions when none is configured" {
  unset PG_TASK_FOCUS_ADDR
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)'
  render
  [[ $output == *"Pause | bash=$BINP param1=--client param2=swiftbar param3=cycle param4=pause param5=C1 terminal=false refresh=false"* ]]
  [[ $output == *"Open pg-task-focus | href=http://127.0.0.1:49210"* ]]
}

@test "no binary leaves every action as plain text" {
  export PG_TASK_FOCUS_BIN="$TEST_DIR/missing"
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)'
  render
  [ "${lines[0]}" = "deep 12:30" ]
  [[ $output != *"bash="* ]]
}

# ---------------------------------------------------------------------------
# Ended periods: offered nowhere by the plugin, linked only while no cycle is active

@test "an ended period links to the web UI when no cycle is active" {
  state '.periods[0].ended = true | .periods[0].banner = "New day: roll over"'
  render
  [[ $output == *"New day: roll over | href=$WEB"* ]]
  [[ $output != *"bash="* ]]
}

@test "an ended period is not offered while a cycle is active" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750) | .periods[0].ended = true | .periods[0].banner = "New day: roll over"'
  render
  [[ $output == *"New day: roll over (stop the active cycles first) | color=#888888"* ]]
  [[ $output != *"New day: roll over | href"* ]]
}

# ---------------------------------------------------------------------------
# Robustness

@test "a title with a pipe or a newline cannot inject a parameter or a line" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep | bash=/bin/rm"; "running"; 600; 750)
    | .focus.type = "de|ep\nx"'
  render
  [ "${lines[0]}" = "de ep x 12:30" ]
  [[ $output == *"▶ Deep bash=/bin/rm · 12:30 left"* ]]
  [[ $output != *"Deep | bash=/bin/rm"* ]]
}

@test "an uninitialized daemon says so and the title is idle" {
  state '.initialized = false'
  render
  [ "${lines[0]}" = "idle" ]
  [[ $output == *"Not set up yet"* ]]
}

@test "empty or invalid input renders the unreachable frame, exit 0" {
  run bash -c "printf '' | $(printf '%q ' "${PLUGIN[@]}") --render"
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "focus ⚠ | color=#888888" ]
  [[ $output == *"pg-task-focus daemon unreachable"* ]]

  run bash -c "printf 'not json' | $(printf '%q ' "${PLUGIN[@]}") --render"
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "focus ⚠ | color=#888888" ]

  run bash -c "printf '[1,2]' | $(printf '%q ' "${PLUGIN[@]}") --render"
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "focus ⚠ | color=#888888" ]
}

@test "--help works and exits 0" {
  run "${PLUGIN[@]}" --help
  [ "$status" -eq 0 ]
  [[ $output == *"streaming SwiftBar"* ]]
}

# ---------------------------------------------------------------------------
# Streaming: the loop, the separator, the tick, the reconnect

# stub MODE -> a pg-task-focus that records its arguments and plays `status --watch`.
# STUB_STATE is the line it prints; STUB_SLEEP holds the stream open afterwards.
make_stub() {
  STUB="$TEST_DIR/pg-task-focus"
  cat >"$STUB" <<STUBEOF
#!$(command -v bash)
printf '%s\n' "\$*" >>"$TEST_DIR/args"
if [ "\${STUB_NO_LINE:-}" != 1 ]; then
  jq -c . "$TEST_DIR/state.json"
fi
[ -z "\${STUB_SLEEP:-}" ] || sleep "\$STUB_SLEEP"
STUBEOF
  chmod +x "$STUB"
  export PG_TASK_FOCUS_BIN="$STUB"
}

@test "stream: invokes status --watch with the address and the swiftbar client" {
  make_stub
  state '.'
  export PG_TASK_FOCUS_SWIFTBAR_ONCE=1
  run "${PLUGIN[@]}"
  [ "$status" -eq 0 ]
  [ "$(cat "$TEST_DIR/args")" = "--addr 127.0.0.1:49310 --client swiftbar status --watch" ]
}

@test "stream: no separator before the first frame, one between frames, an unreachable frame when the stream ends" {
  make_stub
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)'
  export PG_TASK_FOCUS_SWIFTBAR_ONCE=1
  run "${PLUGIN[@]}"
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "deep 12:30" ]
  # The stream ended at once: exactly the state frame, a separator, the unreachable frame.
  [ "$(grep -c '^~~~$' <<<"$output")" -eq 1 ]
  [[ $output == *$'\n~~~\nfocus ⚠ | color=#888888\n'* ]]
  # No separator opens or closes the output.
  [ "${lines[-1]}" != "~~~" ]
}

@test "stream: a running cycle re-renders the last state on a tick without a new line" {
  make_stub
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)'
  export PG_TASK_FOCUS_SWIFTBAR_ONCE=1 PG_TASK_FOCUS_SWIFTBAR_TICK_S=1 STUB_SLEEP=4
  run "${PLUGIN[@]}"
  [ "$status" -eq 0 ]
  # state frame + at least one tick frame + the unreachable frame = at least two separators
  [ "$(grep -c '^~~~$' <<<"$output")" -ge 2 ]
  [ "$(grep -c '^deep 12:30$' <<<"$output")" -ge 2 ]
}

@test "stream: with no cycle running the state is not re-rendered every second" {
  make_stub
  state '.'
  export PG_TASK_FOCUS_SWIFTBAR_ONCE=1 PG_TASK_FOCUS_SWIFTBAR_TICK_S=1 PG_TASK_FOCUS_SWIFTBAR_IDLE_TICK_S=30 STUB_SLEEP=3
  run "${PLUGIN[@]}"
  [ "$status" -eq 0 ]
  # state frame + the unreachable frame only
  [ "$(grep -c '^~~~$' <<<"$output")" -eq 1 ]
}

@test "stream: a missing client shows the unreachable frame and exits 0" {
  export PG_TASK_FOCUS_BIN="$TEST_DIR/missing" PG_TASK_FOCUS_SWIFTBAR_ONCE=1
  run "${PLUGIN[@]}"
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "focus ⚠ | color=#888888" ]
}

@test "stream: a stream that ends is reconnected after the retry delay" {
  make_stub
  state '.'
  export PG_TASK_FOCUS_SWIFTBAR_RETRY_S=1
  run timeout 4 "${PLUGIN[@]}"
  # timeout stops the loop (124); each connection recorded its arguments
  [ "$status" -eq 124 ] || [ "$status" -eq 0 ]
  [ "$(wc -l <"$TEST_DIR/args")" -ge 2 ]
  [ "$(grep -c '^idle$' <<<"$output")" -ge 2 ]
}

@test "stream: a line split across reads is joined, not dropped" {
  STUB="$TEST_DIR/pg-task-focus"
  cat >"$STUB" <<STUBEOF
#!$(command -v bash)
line=\$(jq -c . "$TEST_DIR/state.json")
printf '%s' "\${line:0:40}"
sleep 2
printf '%s\n' "\${line:40}"
STUBEOF
  chmod +x "$STUB"
  export PG_TASK_FOCUS_BIN="$STUB" PG_TASK_FOCUS_SWIFTBAR_ONCE=1 PG_TASK_FOCUS_SWIFTBAR_TICK_S=1 PG_TASK_FOCUS_SWIFTBAR_IDLE_TICK_S=1
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)'
  run "${PLUGIN[@]}"
  [ "$status" -eq 0 ]
  [[ $output == *"deep 12:30"* ]]
}
