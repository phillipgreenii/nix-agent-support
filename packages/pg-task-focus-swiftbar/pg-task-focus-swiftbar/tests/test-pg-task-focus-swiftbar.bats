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
  unset PG_TASK_FOCUS_SWIFTBAR_TICK_S PG_TASK_FOCUS_SWIFTBAR_IDLE_TICK_S
  WEB="http://127.0.0.1:49310"
  STATE="$TEST_DIR/state.json"
  # The title-width setting lives under XDG_STATE_HOME; the Title row re-invokes SELF.
  export XDG_STATE_HOME="$TEST_DIR/xdg-state"
  WIDTH_FILE="$XDG_STATE_HOME/pg-task-focus-swiftbar/title-width"
  export PG_TASK_FOCUS_SWIFTBAR_SELF="$TEST_DIR/bin/pg-task-focus-swiftbar"
}

# set_width WORD -> store WORD (verbatim) as the title-width setting.
set_width() {
  mkdir -p "$(dirname "$WIDTH_FILE")"
  printf '%s\n' "$1" >"$WIDTH_FILE"
}

# no_actions -> succeeds when no row but the Title row carries a bash= action.
no_actions() {
  # (an explicit `return 1`: a negated pipeline never trips bats' errexit)
  if grep -v '^Title: ' <<<"$output" | grep -q 'bash='; then
    return 1
  fi
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

# ttext -> the title line without its colour parameter. The colour of each state
# has its own tests, so the title-text tests do not repeat it.
ttext() { printf '%s' "${lines[0]%% | color=*}"; }

# tcolor -> the colour parameter of the title line (empty when it has none).
tcolor() {
  case ${lines[0]} in
  *" | color="*) printf '%s' "${lines[0]##* | color=}" ;;
  esac
}

# glyph_count LINE -> the number of characters before LINE's first " | ".
glyph_count() {
  jq -Rr 'split(" | ")[0] | length' <<<"$1"
}

# ---------------------------------------------------------------------------
# Title: precedence

@test "idle: the title and a no-cycle row, then the web link" {
  state '.'
  render
  [ "$status" -eq 0 ]
  [ "$(ttext)" = "idle" ]
  [ "${lines[1]}" = "---" ]
  [[ $output == *"No cycle running | color=#888888"* ]]
  [[ $output == *"Open pg-task-focus | href=$WEB"* ]]
}

@test "running: the type and the coarse time left, in whole minutes" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)'
  render
  [ "$(ttext)" = "deep 12m" ]
}

@test "running: the time left is moved on by the time since the state was read" {
  state "$LIB"'.read_at = "'"$(at -30)"'" | .focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)'
  render
  [ "$(ttext)" = "deep 12m" ]

  # 750 s left, read 460 s ago: 290 s left now, which is under 5 min.
  state "$LIB"'.read_at = "'"$(at -460)"'" | .focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)'
  render
  [ "$(ttext)" = "deep <5m" ]
}

@test "running: an hour or more is still whole minutes" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 0; 3725)'
  render
  [ "$(ttext)" = "deep 62m" ]
}

# The coarse clock buckets (the constants block in the jq program): at or above
# 300 s, whole minutes; below it, the next 20 s step above the time left behind a
# "<"; under 20 s, "<20s". Each case is REMAINING_SECONDS=EXPECTED_TEXT.
@test "coarse clock: every bucket edge of the remaining time" {
  local case rem want
  for case in 3600=60m 601=10m 600=10m 359=5m 301=5m 300=5m 299='<5m' 281='<5m' 280='<5m' 279='<4m40s' 261='<4m40s' 260='<4m40s' 259='<4m20s' \
    61='<1m20s' 60='<1m20s' 59='<1m' 41='<1m' 40='<1m' 39='<40s' 21='<40s' 20='<40s' 19='<20s' 1='<20s' 0='<20s'; do
    rem=${case%%=*}
    want=${case#*=}
    state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 0; '"$rem"')'
    render
    if [ "$(ttext)" != "deep $want" ]; then
      printf 'remaining %s: wanted "deep %s", got "%s"\n' "$rem" "$want" "$(ttext)" >&2
      return 1
    fi
  done
}

@test "wide: the cycle row names the cycle and does not repeat the time the title shows" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 0; 299)'
  render
  [ "$(ttext)" = "deep <5m" ]
  [[ $output == *$'\n▶ Deep work\n'* ]]
  [[ $output != *"left"* ]]
}

@test "overtime: a warning icon, a plus sign and the coarse time over" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 1900; -250)'
  render
  [ "$(ttext)" = ":exclamationmark.triangle.fill: deep +<4m20s" ]
  [[ $output == *$'\n▶ Deep work · overtime | color=#cc3333\n'* ]]
}

@test "overtime: the same buckets with a plus sign, at each edge" {
  local case over want
  for case in 3000=+50m 300=+5m 299='+<5m' 20='+<40s' 19='+<20s' 1='+<20s'; do
    over=${case%%=*}
    want=${case#*=}
    state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 1900; -'"$over"')'
    render
    if [ "$(ttext)" != ":exclamationmark.triangle.fill: deep $want" ]; then
      printf 'over %s: wanted "%s", got "%s"\n' "$over" "$want" "$(ttext)" >&2
      return 1
    fi
  done
}

@test "overtime begins when the clock passes the end, not only at the next state" {
  state "$LIB"'.read_at = "'"$(at -60)"'" | .focus = cyc("C1"; "deep"; "Deep work"; "running"; 1490; 10)'
  render
  [ "$(ttext)" = ":exclamationmark.triangle.fill: deep +<1m" ]
}

@test "paused only: a pause glyph and the most recently paused type" {
  state "$LIB"'.dimmed = [dim("C2"; "email"; "Email"; 300; false), dim("C3"; "deep"; "Deep work"; 900; false)]'
  render
  [ "$(ttext)" = "⏸ email" ]
}

@test "paused with a resume offer naming it: the title asks to resume" {
  state "$LIB"'.dimmed = [dim("C2"; "deep"; "Deep work"; 300; false)]
    | .resume_offer = {cycle: cyc("C2"; "deep"; "Deep work"; "paused"; 300; 600), action: "resume"}'
  render
  [ "$(ttext)" = "⏸ deep · Resume?" ]
}

@test "a resume offer for another cycle than the one shown paused: Resume <title>?" {
  state "$LIB"'.dimmed = [dim("C2"; "email"; "Email"; 300; false), dim("C3"; "deep"; "Deep work"; 900; false)]
    | .resume_offer = {cycle: cyc("C3"; "deep"; "Deep work"; "paused"; 900; 600), action: "resume"}'
  render
  [ "$(ttext)" = "Resume Deep work?" ]
}

@test "an ended period: the banner is a dropdown row, never the title" {
  state '.periods[0].ended = true | .periods[0].banner = "New day: roll over"'
  render
  [ "$(ttext)" = "idle" ]
  [[ ${lines[0]} != *"roll over"* ]]
  [[ $output == *$'\nNew day: roll over | href='* ]]
}

@test "an ended week reads its own banner, in the dropdown only" {
  state '.periods += [{kind: "week", start: "2026-09-28", end: "2026-10-04", zone: "America/New_York", today: "2026-10-07", ended: true, banner: "New week: roll over"}]'
  render
  [ "$(ttext)" = "idle" ]
  [[ $output == *$'\nNew week: roll over | href='* ]]
}

@test "the title carries no alert or error text in either width, for any state" {
  local width
  for width in wide narrow; do
    set_width "$width"
    # An ended period with a running cycle, and a read-only daemon with the reason.
    state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)
      | .periods[0].ended = true | .periods[0].banner = "New day: roll over"
      | .store = {state: "read_only", reason: "the append fsync failed", since: "'"$(at -5)"'"}'
    render
    [[ ${lines[0]} != *"roll over"* ]]
    [[ ${lines[0]} != *"READ-ONLY"* ]]
    [[ ${lines[0]} != *"fsync"* ]]
    # The same text is in the dropdown.
    [[ $output == *$'\nNew day: roll over (stop the active cycles first)'* ]]
    [[ $output == *$'\nREAD-ONLY: the append fsync failed.'* ]]
  done
}

@test "next due: the task and the time to it" {
  state '.next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at 1320)"'", due_zone: "America/New_York", status: "open", overdue: false}'
  render
  [ "$(ttext)" = "Next: Post plan 22m" ]
}

@test "next due: an hour or more reads 1h05m" {
  state '.next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at 3900)"'", due_zone: "America/New_York", status: "open", overdue: false}'
  render
  [ "$(ttext)" = "Next: Post plan 1h05m" ]
}

@test "next overdue: overdue 12m, never a negative time" {
  state '.next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at -720)"'", due_zone: "America/New_York", status: "open", overdue: true}'
  render
  [ "$(ttext)" = "Next: Post plan overdue 12m" ]
  [[ ${lines[0]} != *"-"* ]]
}

@test "precedence: overtime beats running, running beats paused; read-only only adds a lock" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 1900; -250) | .dimmed = [dim("C2"; "email"; "Email"; 300; true)]
    | .periods[0].ended = true | .periods[0].banner = "New day: roll over"
    | .store = {state: "read_only", reason: "the append fsync failed", since: "'"$(at -5)"'"}'
  render
  [ "$(ttext)" = ":lock.fill: :exclamationmark.triangle.fill: deep +<4m20s" ]

  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 1900; -250) | .dimmed = [dim("C2"; "email"; "Email"; 300; true)]'
  render
  [ "$(ttext)" = ":exclamationmark.triangle.fill: deep +<4m20s" ]

  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 100; 600) | .dimmed = [dim("C2"; "email"; "Email"; 300; true)]'
  render
  [ "$(ttext)" = "deep 10m" ]
}

@test "precedence: paused beats the next task, and an ended period never reaches the title" {
  state "$LIB"'.dimmed = [dim("C2"; "email"; "Email"; 300; false)] | .periods[0].ended = true | .periods[0].banner = "New day: roll over"'
  render
  [ "$(ttext)" = "⏸ email" ]

  state '.periods[0].ended = true | .periods[0].banner = "New day: roll over"
    | .next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at 600)"'", due_zone: "America/New_York", status: "open", overdue: false}'
  render
  [ "$(ttext)" = "Next: Post plan 10m" ]
}

# ---------------------------------------------------------------------------
# Title: the next-task suffix while a cycle runs or is paused

@test "suffix: a task due within the due-soon window is appended" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)
    | .next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at 480)"'", due_zone: "America/New_York", status: "open", overdue: false}'
  render
  [ "$(ttext)" = "deep 12m · Post plan 8m" ]
}

@test "suffix: an overdue task reads overdue 12m" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)
    | .next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at -720)"'", due_zone: "America/New_York", status: "open", overdue: true}'
  render
  [ "$(ttext)" = "deep 12m · Post plan overdue 12m" ]
}

@test "suffix: a task due beyond the window is left to the dropdown" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)
    | .next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at 3600)"'", due_zone: "America/New_York", status: "open", overdue: false}'
  render
  [ "$(ttext)" = "deep 12m" ]
}

@test "suffix: the window is configurable" {
  export PG_TASK_FOCUS_SWIFTBAR_DUE_SOON_MIN=90
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)
    | .next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at 3600)"'", due_zone: "America/New_York", status: "open", overdue: false}'
  render
  [ "$(ttext)" = "deep 12m · Post plan 1h00m" ]
}

@test "suffix: a paused cycle also carries it" {
  state "$LIB"'.dimmed = [dim("C2"; "email"; "Email"; 300; false)]
    | .next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at 480)"'", due_zone: "America/New_York", status: "open", overdue: false}'
  render
  [ "$(ttext)" = "⏸ email · Post plan 8m" ]
}

# ---------------------------------------------------------------------------
# Read-only mode (operator ruling 7)

@test "read-only: the dropdown opens with the reason and the restart sentence" {
  state '.store = {state: "read_only", reason: "the append fsync failed", since: "'"$(at -5)"'"}'
  render
  # The title is the active task (idle here) behind a lock icon; the reason is a row.
  [ "$(ttext)" = ":lock.fill: idle" ]
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
  no_actions
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
  # The title already says "Next: Post plan 22m": the row adds the clock time only.
  [[ $output == *$'\nNext: Post plan · due 23:08 EST\n'* ]]
  [[ $output != *" · in 22m"* ]]
  [[ $output == *"Complete Post plan | bash=$BINP param1=--addr param2=127.0.0.1:49310 param3=--client param4=swiftbar param5=task param6=done param7=post-plan terminal=false refresh=false"* ]]
}

@test "a next task the title does not show keeps its time in the dropdown row" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)
    | .next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at 3600)"'", due_zone: "America/New_York", status: "open", overdue: false}'
  render
  [[ $output == *$'\nNext: Post plan · due 23:46 EST · in 1h00m\n'* ]]
}

@test "an overdue next task is red in the dropdown" {
  state '.next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at -720)"'", due_zone: "America/New_York", status: "open", overdue: true}'
  render
  [[ $output == *"Next: Post plan · due 22:34 EST | color=#cc3333"* ]]
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
  [ "$(ttext)" = "deep 12m" ]
  no_actions
}

# ---------------------------------------------------------------------------
# Ended periods: offered nowhere by the plugin, linked only while no cycle is active

@test "an ended period links to the web UI when no cycle is active" {
  state '.periods[0].ended = true | .periods[0].banner = "New day: roll over"'
  render
  [[ $output == *"New day: roll over | href=$WEB"* ]]
  no_actions
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
  [ "$(ttext)" = "de ep x 12m" ]
  [[ $output == *"▶ Deep bash=/bin/rm"* ]]
  [[ $output != *"Deep | bash=/bin/rm"* ]]
}

@test "an uninitialized daemon says so and the title is idle" {
  state '.initialized = false'
  render
  [ "$(ttext)" = "idle" ]
  [[ $output == *"Not set up yet"* ]]
}

@test "empty or invalid input renders the unreachable frame, exit 0" {
  run bash -c "printf '' | $(printf '%q ' "${PLUGIN[@]}") --render"
  [ "$status" -eq 0 ]
  [ "$(ttext)" = "focus ⚠" ]
  [[ $output == *"pg-task-focus daemon unreachable"* ]]

  run bash -c "printf 'not json' | $(printf '%q ' "${PLUGIN[@]}") --render"
  [ "$status" -eq 0 ]
  [ "$(ttext)" = "focus ⚠" ]

  run bash -c "printf '[1,2]' | $(printf '%q ' "${PLUGIN[@]}") --render"
  [ "$status" -eq 0 ]
  [ "$(ttext)" = "focus ⚠" ]
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
  [ "$(ttext)" = "deep 12m" ]
  # The stream ended at once: exactly the state frame, a separator, the unreachable frame.
  [ "$(grep -c '^~~~$' <<<"$output")" -eq 1 ]
  [[ $output == *$'\n~~~\nfocus ⚠ | color=#e07000\n'* ]]
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
  [ "$(grep -c '^deep 12m | color=#3a9a4a$' <<<"$output")" -ge 2 ]
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
  [ "$(ttext)" = "focus ⚠" ]
}

@test "stream: a stream that ends is reconnected after the retry delay" {
  make_stub
  state '.'
  export PG_TASK_FOCUS_SWIFTBAR_RETRY_S=1
  run timeout 4 "${PLUGIN[@]}"
  # timeout stops the loop (124); each connection recorded its arguments
  [ "$status" -eq 124 ] || [ "$status" -eq 0 ]
  [ "$(wc -l <"$TEST_DIR/args")" -ge 2 ]
  [ "$(grep -c '^idle | color=#888888$' <<<"$output")" -ge 2 ]
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
  [[ $output == *"deep 12m"* ]]
}

# ---------------------------------------------------------------------------
# Streaming: the tick default, the immediate frame on a state line, the width re-read

@test "stream: the timer tick defaults to 5 s, not every second" {
  make_stub
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)'
  # TICK_S is unset by setup: the default applies. Held open for 3 s, which is
  # inside the first tick, so only the state frame shows (a 1 s tick gave three).
  export PG_TASK_FOCUS_SWIFTBAR_ONCE=1 STUB_SLEEP=3
  run "${PLUGIN[@]}"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^deep 12m | color=#3a9a4a$' <<<"$output")" -eq 1 ]
}

@test "stream: a cycle that runs ticks once the 5 s default has passed" {
  make_stub
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)'
  export PG_TASK_FOCUS_SWIFTBAR_ONCE=1 STUB_SLEEP=7
  run "${PLUGIN[@]}"
  [ "$status" -eq 0 ]
  # the state frame, one tick frame (about 5 s in), then the unreachable frame
  [ "$(grep -c '^deep 12m | color=#3a9a4a$' <<<"$output")" -eq 2 ]
}

@test "stream: a frame caused by a state line is emitted at once, not at the next tick" {
  STUB="$TEST_DIR/pg-task-focus"
  cat >"$STUB" <<STUBEOF
#!$(command -v bash)
jq -c . "$TEST_DIR/state.json"
sleep 1
jq -c . "$TEST_DIR/state2.json"
STUBEOF
  chmod +x "$STUB"
  export PG_TASK_FOCUS_BIN="$STUB" PG_TASK_FOCUS_SWIFTBAR_ONCE=1
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)'
  STATE="$TEST_DIR/state2.json" state '.'
  run "${PLUGIN[@]}"
  [ "$status" -eq 0 ]
  # running frame, the idle frame the second line caused (1 s in, far inside the 5 s tick),
  # the unreachable frame: two separators.
  [[ $output == *$'deep 12m | color=#3a9a4a\n'*$'\n~~~\nidle | color=#888888\n'*$'\n~~~\nfocus ⚠'* ]]
  [ "$(grep -c '^~~~$' <<<"$output")" -eq 2 ]
}

@test "stream: a title-width change shows in the next frame without a restart or a new state line" {
  mkdir -p "$(dirname "$WIDTH_FILE")"
  STUB="$TEST_DIR/pg-task-focus"
  cat >"$STUB" <<STUBEOF
#!$(command -v bash)
printf '%s\n' "\$\$" >>"$TEST_DIR/pids"
jq -c . "$TEST_DIR/state.json"
sleep 1
printf 'narrow\n' >"$WIDTH_FILE"
sleep 3
STUBEOF
  chmod +x "$STUB"
  # Both ticks far away: a frame inside the 4 s the stream is held can only come
  # from the width change.
  export PG_TASK_FOCUS_BIN="$STUB" PG_TASK_FOCUS_SWIFTBAR_ONCE=1 PG_TASK_FOCUS_SWIFTBAR_TICK_S=60 PG_TASK_FOCUS_SWIFTBAR_IDLE_TICK_S=60
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)'
  run "${PLUGIN[@]}"
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "deep 12m | color=#3a9a4a" ]
  [[ $output == *$'\n~~~\n◉ | color=#3a9a4a\n---\ndeep 12m | color=#3a9a4a\n'* ]]
  # one connection: the process was not restarted
  [ "$(wc -l <"$TEST_DIR/pids")" -eq 1 ]
  # wide frame, narrow frame, unreachable frame (narrow now): two separators
  [ "$(grep -c '^~~~$' <<<"$output")" -eq 2 ]
  [[ $output == *$'\n~~~\n⚠ | color=#e07000\n'* ]]
}

# ---------------------------------------------------------------------------
# Title width: the setting, the wide and the narrow title, the Title row

# One fixture per state, for the width checks below.
state_running() { state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)'; }
state_overtime() { state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 1900; -250)'; }
state_paused() { state "$LIB"'.dimmed = [dim("C2"; "email"; "Email"; 300; false)]'; }
state_offer() {
  state "$LIB"'.dimmed = [dim("C2"; "email"; "Email"; 300; false), dim("C3"; "deep"; "Deep work"; 900; false)]
    | .resume_offer = {cycle: cyc("C3"; "deep"; "Deep work"; "paused"; 900; 600), action: "resume"}'
}
state_idle() { state '.'; }
state_next() { state '.next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at 1320)"'", due_zone: "America/New_York", status: "open", overdue: false}'; }
state_readonly() { state '.store = {state: "read_only", reason: "the append fsync failed", since: "'"$(at -5)"'"}'; }

# check_widths STATE-FN GLYPH COLOUR WIDE-TEXT: the wide title is the text in the
# colour; the narrow title is the one glyph in the SAME colour with the wide text
# as the first row of the dropdown.
check_widths() {
  local fn=$1 glyph=$2 colour=$3 text=$4
  "$fn"
  set_width wide
  render
  [ "${lines[0]}" = "$text | color=$colour" ]
  set_width narrow
  render
  [ "${lines[0]}" = "$glyph | color=$colour" ]
  [ "$(glyph_count "${lines[0]}")" -eq 1 ]
  [ "${lines[1]}" = "---" ]
  [ "${lines[2]}" = "$text | color=$colour" ]
}

@test "title width: every state has a wide text and one coloured glyph, the same colour in both" {
  check_widths state_running '◉' '#3a9a4a' 'deep 12m'
  check_widths state_overtime '◉' '#cc3333' ':exclamationmark.triangle.fill: deep +<4m20s'
  check_widths state_paused '◎' '#e0b000' '⏸ email'
  check_widths state_offer '◎' '#e0b000' 'Resume Deep work?'
  check_widths state_idle '○' '#888888' 'idle'
  check_widths state_next '○' '#888888' 'Next: Post plan 22m'
  check_widths state_readonly '⊘' '#cc3333' ':lock.fill: idle'
}

@test "title width: the narrow glyphs differ in shape or colour from state to state" {
  # Overtime and read-only share a red, running and overtime share a ring: the
  # (glyph, colour) pair tells every state apart.
  local fn g
  declare -A seen=()
  for fn in state_running state_overtime state_paused state_idle state_readonly; do
    "$fn"
    set_width narrow
    render
    g="${lines[0]}"
    [ -z "${seen[$g]:-}" ]
    seen[$g]=1
  done
}

@test "title width: the unreachable state follows the width, with its message in the first row" {
  set_width wide
  run bash -c "printf '' | $(printf '%q ' "${PLUGIN[@]}") --render"
  [ "${lines[0]}" = "focus ⚠ | color=#e07000" ]
  set_width narrow
  run bash -c "printf '' | $(printf '%q ' "${PLUGIN[@]}") --render"
  [ "${lines[0]}" = "⚠ | color=#e07000" ]
  [ "$(glyph_count "${lines[0]}")" -eq 1 ]
  [ "${lines[2]}" = "pg-task-focus daemon unreachable | color=#888888" ]
}

@test "title width: a missing client renders unreachable in the narrow form too" {
  set_width narrow
  export PG_TASK_FOCUS_BIN="$TEST_DIR/missing" PG_TASK_FOCUS_SWIFTBAR_ONCE=1
  run "${PLUGIN[@]}"
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "⚠ | color=#e07000" ]
}

@test "title width: no setting file reads as wide and the Title row says so" {
  state_running
  render
  [ "$status" -eq 0 ]
  [ "$(ttext)" = "deep 12m" ]
  [[ $output == *$'\nTitle: wide (click for narrow) | bash='* ]]
}

@test "title width: an empty file, an unrecognised value and a missing state directory each read as wide" {
  state_running
  local value
  for value in "" "bogus" "NARROW" "narrow2" "wide narrow"; do
    set_width "$value"
    render
    [ "$status" -eq 0 ]
    [ "$(ttext)" = "deep 12m" ]
    [[ $output == *"Title: wide (click for narrow)"* ]]
  done
  export XDG_STATE_HOME="$TEST_DIR/never-created/state"
  render
  [ "$status" -eq 0 ]
  [ "$(ttext)" = "deep 12m" ]
}

@test "title width: an unreadable setting reads as wide, without an error" {
  [ "$(id -u)" -ne 0 ] || skip "root reads any file"
  state_running
  set_width narrow
  chmod 000 "$WIDTH_FILE"
  render
  [ "$status" -eq 0 ]
  [ "$(ttext)" = "deep 12m" ]
  chmod 600 "$WIDTH_FILE"
}

@test "title width: narrow is honoured, including surrounding whitespace" {
  state_running
  set_width "  narrow  "
  render
  [ "${lines[0]}" = "◉ | color=#3a9a4a" ]
  [[ $output == *"Title: narrow (click for wide)"* ]]
}

@test "title width: the wide dropdown does not repeat the title's values in a row" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750)
    | .next = {id: "T1", definition: "post-plan", title: "Post plan", due: "'"$(at 480)"'", due_zone: "America/New_York", status: "open", overdue: false}'
  set_width wide
  render
  [ "$(ttext)" = "deep 12m · Post plan 8m" ]
  # neither the remaining time nor the relative due time is in a dropdown row
  [[ $output != *$'\n▶ Deep work · '* ]]
  [[ $output != *"12m left"* ]]
  [[ $output == *$'\nNext: Post plan · due 22:54 EST\n'* ]]
}

@test "title width: the dropdown below the first row is the same in both widths" {
  state "$LIB"'.focus = cyc("C1"; "deep"; "Deep work"; "running"; 600; 750) | .dimmed = [dim("C2"; "email"; "Email"; 300; true)]'
  set_width wide
  render
  # the dropdown rows, less the Title row (which names the width it is in)
  local wide_rows narrow_rows
  wide_rows=$(printf '%s\n' "${lines[@]:1}" | grep -v '^Title: ')
  set_width narrow
  render
  # narrow = the one-glyph title, "---", the detail row, then the wide dropdown
  # (whose own leading "---" is the first row of wide_rows)
  [ "${lines[1]}" = "---" ]
  [ "${lines[2]}" = "deep 12m | color=#3a9a4a" ]
  narrow_rows=$(printf '%s\n' "${lines[@]:3}" | grep -v '^Title: ')
  [ "$narrow_rows" = "$wide_rows" ]
}

@test "Title row: present in every state and width, shows the mode and the action, and ends the dropdown above Refresh" {
  local fn width
  for width in wide narrow; do
    set_width "$width"
    for fn in state_running state_overtime state_paused state_offer state_idle state_next state_readonly; do
      "$fn"
      render
      [ "${lines[-1]}" = "Refresh | refresh=true" ]
      [[ ${lines[-2]} == "Title: $width (click for "* ]]
      [ "${lines[-3]}" = "---" ]
    done
    run bash -c "printf '' | $(printf '%q ' "${PLUGIN[@]}") --render"
    [ "${lines[-1]}" = "Refresh | refresh=true" ]
    [[ ${lines[-2]} == "Title: $width (click for "* ]]
  done
}

@test "Title row: runs this script with --set-title-width and the other width" {
  state_idle
  render
  [[ $output == *$'\nTitle: wide (click for narrow) | bash='"$PG_TASK_FOCUS_SWIFTBAR_SELF"$' param1=--set-title-width param2=narrow terminal=false refresh=true\n'* ]]
  set_width narrow
  render
  [[ $output == *$'\nTitle: narrow (click for wide) | bash='"$PG_TASK_FOCUS_SWIFTBAR_SELF"$' param1=--set-title-width param2=wide terminal=false refresh=true\n'* ]]
}

@test "Title row: a path with whitespace is quoted" {
  export PG_TASK_FOCUS_SWIFTBAR_SELF="$TEST_DIR/Application Support/pg-task-focus-swiftbar"
  state_idle
  render
  [[ $output == *$'\nTitle: wide (click for narrow) | bash="'"$PG_TASK_FOCUS_SWIFTBAR_SELF"'" param1=--set-title-width'* ]]
}

@test "Title row: without the wrapper's path it runs the script itself" {
  unset PG_TASK_FOCUS_SWIFTBAR_SELF
  state_idle
  render
  # The path is the script's own ($0): the artifact under test, or, in the nix
  # build, the unwrapped script behind the runtimeDeps wrapper. Either way it is
  # an executable file that accepts --set-title-width.
  local self
  self=$(sed -n 's/^Title: wide (click for narrow) | bash=\([^ ]*\) param1=--set-title-width .*/\1/p' <<<"$output")
  [ -n "$self" ]
  if [ -x "$self" ]; then
    run "$self" --set-title-width narrow
  else
    run bash "$self" --set-title-width narrow
  fi
  [ "$status" -eq 0 ]
  [ "$(cat "$WIDTH_FILE")" = "narrow" ]
}

@test "Title row: a read-only daemon still offers it (a local setting, not a daemon write)" {
  state_readonly
  render
  [[ $output == *$'\nTitle: wide (click for narrow) | bash='* ]]
}

# ---------------------------------------------------------------------------
# --set-title-width

@test "--set-title-width: writes the setting, prints nothing and never calls the daemon client" {
  make_stub
  run "${PLUGIN[@]}" --set-title-width narrow
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ "$(cat "$WIDTH_FILE")" = "narrow" ]
  [ ! -e "$TEST_DIR/args" ]
  run "${PLUGIN[@]}" --set-title-width wide
  [ "$status" -eq 0 ]
  [ "$(cat "$WIDTH_FILE")" = "wide" ]
}

@test "--set-title-width: leaves no temporary file beside the setting (atomic rename)" {
  run "${PLUGIN[@]}" --set-title-width narrow
  [ "$status" -eq 0 ]
  run "${PLUGIN[@]}" --set-title-width wide
  [ "$status" -eq 0 ]
  [ "$(ls -A "$(dirname "$WIDTH_FILE")")" = "title-width" ]
}

@test "--set-title-width: rejects an unknown or missing value with exit 2 and keeps the setting" {
  set_width narrow
  run "${PLUGIN[@]}" --set-title-width bogus
  [ "$status" -eq 2 ]
  [[ $output == *"must be wide or narrow"* ]]
  [ "$(cat "$WIDTH_FILE")" = "narrow" ]
  run "${PLUGIN[@]}" --set-title-width
  [ "$status" -eq 2 ]
  run "${PLUGIN[@]}" --set-title-width ""
  [ "$status" -eq 2 ]
  [ "$(cat "$WIDTH_FILE")" = "narrow" ]
  [ "$(ls -A "$(dirname "$WIDTH_FILE")")" = "title-width" ]
}

@test "--set-title-width: with XDG_STATE_HOME unset it falls back to HOME/.local/state" {
  unset XDG_STATE_HOME
  run "${PLUGIN[@]}" --set-title-width narrow
  [ "$status" -eq 0 ]
  [ "$(cat "$HOME/.local/state/pg-task-focus-swiftbar/title-width")" = "narrow" ]
  state_running
  render
  [ "${lines[0]}" = "◉ | color=#3a9a4a" ]
}

@test "--set-title-width: an unwritable state directory is reported with exit 1" {
  : >"$TEST_DIR/blocker"
  export XDG_STATE_HOME="$TEST_DIR/blocker"
  run "${PLUGIN[@]}" --set-title-width narrow
  [ "$status" -eq 1 ]
  [[ $output == *"cannot"* ]]
}

@test "--set-title-width: flips the next render, and back" {
  state_running
  render
  [ "${lines[0]}" = "deep 12m | color=#3a9a4a" ]
  run "${PLUGIN[@]}" --set-title-width narrow
  [ "$status" -eq 0 ]
  render
  [ "${lines[0]}" = "◉ | color=#3a9a4a" ]
  run "${PLUGIN[@]}" --set-title-width wide
  [ "$status" -eq 0 ]
  render
  [ "${lines[0]}" = "deep 12m | color=#3a9a4a" ]
}

@test "--help documents the width, the setting file, the switch, the coarse clock and the 5 s tick" {
  run "${PLUGIN[@]}" --help
  [ "$status" -eq 0 ]
  [[ $output == *"--set-title-width"* ]]
  [[ $output == *"title-width"* ]]
  [[ $output == *"wide"* && $output == *"narrow"* ]]
  [[ $output == *"coarse"* ]]
  [[ $output == *"every 5 s while a cycle runs"* ]]
  [[ $output == *"seconds between ticks while a cycle runs (default 5)"* ]]
  # the ended-period prompt is a dropdown row, so it is no longer a title state
  [[ $output != *"New day: roll over"* ]]
}
