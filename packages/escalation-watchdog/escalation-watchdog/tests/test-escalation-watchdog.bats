#!/usr/bin/env bats
# bats file_tags=type:integration
#
# escalation-watchdog against a REAL bd throwaway database AND the real
# hand-to-human (bead pg2-f9x7i). Every test gets its OWN fresh embedded database
# in a temp dir (the watchdog scans every escalated bead, so tests must not share
# one); see hand-to-human's test-support/bd_fixture.bash for the isolation
# invariants. No dolt server is started and the shared tracker is never touched.
#
# The clock is injected, never slept on: ESCALATION_WATCHDOG_NOW is the "now" the
# watchdog sees, built from the real label_added time of the bead
# (`bd history --events`) plus an offset. The few tests that must distinguish two
# escalation times of ONE bead sleep a couple of seconds so the real events differ.

bats_require_minimum_version 1.5.0

if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi
if [[ -z ${TEST_SUPPORT:-} ]]; then
  TEST_SUPPORT="$(cd "${SCRIPTS_DIR}/../../hand-to-human/test-support" && pwd)"
fi
load "$TEST_SUPPORT/bd_fixture"

setup() {
  command -v bd >/dev/null 2>&1 || skip "bd not on PATH"
  command -v hand-to-human >/dev/null 2>&1 || skip "hand-to-human not on PATH"
  hand-to-human --help 2>&1 | grep -q -- '--if-status' || skip "hand-to-human on PATH lacks --if-status (packet 2 of pg2-f9x7i)"
  bd_fixture_setup_file
  bd_fixture_setup
  bd_fixture_assert_isolated
  REAL_H2H="$(command -v hand-to-human)"
  # The builder composes <name>.bash + <name>.sh under strict mode; do the same.
  cat >"$TDIR/run-escalation-watchdog" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "${SCRIPTS_DIR}/escalation-watchdog.bash"
source "${SCRIPTS_DIR}/escalation-watchdog.sh"
WRAPPER
  chmod +x "$TDIR/run-escalation-watchdog"
  W="$TDIR/run-escalation-watchdog"
}

teardown() {
  bd_fixture_teardown_file
}

# new_bead <label>...: create a bead carrying the labels, print its id.
new_bead() {
  local -a args=(create --title "bead $RANDOM" --silent)
  local l
  for l in "$@"; do
    args+=(-l "$l")
  done
  bd "${args[@]}" 2>/dev/null
}

labels_of() { # id -> sorted labels, one per line
  bd_fixture_show_json "$1" | jq -r '(.labels // []) | sort | .[]'
}
notes_of() { bd_fixture_show_json "$1" | jq -r '.notes // ""'; }

# escalated_at <id>: epoch seconds of the LATEST "Added label: escalated" event.
escalated_at() {
  bd history "$1" --events --json |
    jq -r 'map(select(.event_type == "label_added" and .comment == "Added label: escalated") | .created_at | sub("\\.[0-9]+"; "") | fromdateiso8601) | max'
}

# run_wd <now-epoch> <args...>: run the watchdog against the fixture with the clock pinned.
run_wd() {
  local now="$1"
  shift
  ESCALATION_WATCHDOG_NOW="$now" run --separate-stderr "$W" --bd-dir "$TDIR" "$@"
}

@test "isolation guard: the fixture database is the temp embedded one" {
  bd_fixture_assert_isolated
  [ "$PWD" = "$TDIR" ]
}

@test "stale bead: handed off with human + attention + stale-escalation, escalated gone, one note" {
  id="$(new_bead escalated keep-me)"
  t0="$(escalated_at "$id")"
  run_wd $((t0 + 13 * 3600)) --stale-after 12h
  [ "$status" -eq 0 ]
  [[ $output == *"handed-off $id age=13h 0m clock=label_added"* ]]
  [ "$(labels_of "$id")" = $'attention\nhuman\nkeep-me\nstale-escalation' ]
  [[ "$(notes_of "$id")" == "Automation timed out on this bead. The cause may be infrastructure (disk, usage limit, auth), not a decision only you can make. Check the cause first."* ]]
  [[ "$(notes_of "$id")" == *"Escalated for 13h 0m"* ]]
  [[ "$(notes_of "$id")" == *"the latest 'Added label: escalated' event"* ]]
}

@test "fresh bead (escalated for less than --stale-after): untouched" {
  id="$(new_bead escalated)"
  t0="$(escalated_at "$id")"
  run_wd $((t0 + 11 * 3600)) --stale-after 12h
  [ "$status" -eq 0 ]
  [[ $output != *"handed-off"* ]]
  [ "$(labels_of "$id")" = "escalated" ]
  [ "$(notes_of "$id")" = "" ]
}

@test "the threshold is exclusive: exactly --stale-after old is not stale, one second more is" {
  id="$(new_bead escalated)"
  t0="$(escalated_at "$id")"
  run_wd $((t0 + 3600)) --stale-after 1h
  [ "$status" -eq 0 ]
  [ "$(labels_of "$id")" = "escalated" ]
  run_wd $((t0 + 3601)) --stale-after 1h
  [ "$status" -eq 0 ]
  [[ $output == *"handed-off $id"* ]]
}

@test "a stale bead and a fresh bead in one run: only the stale one is handed off" {
  old="$(new_bead escalated)"
  sleep 3
  young="$(new_bead escalated)"
  tb="$(escalated_at "$young")"
  # age(young) == 100s (not stale); age(old) >= 103s (stale).
  run_wd $((tb + 100)) --stale-after 100s --quiet-for 0s
  [ "$status" -eq 0 ]
  [[ $output == *"handed-off $old"* ]]
  [[ $output != *"handed-off $young"* ]]
  [ "$(labels_of "$young")" = "escalated" ]
  [[ $output == *"skipped 1 fresh"* ]]
}

@test "a bead blocked by an open dependency is skipped" {
  blocker="$(new_bead other)"
  id="$(new_bead escalated)"
  bd dep add "$id" "$blocker" >/dev/null 2>&1
  t0="$(escalated_at "$id")"
  run_wd $((t0 + 13 * 3600)) --stale-after 12h
  [ "$status" -eq 0 ]
  [[ $output != *"handed-off"* ]]
  [ "$(labels_of "$id")" = "escalated" ]
}

@test "a deferred bead is skipped" {
  id="$(new_bead escalated)"
  bd defer "$id" >/dev/null 2>&1
  t0="$(escalated_at "$id")"
  run_wd $((t0 + 13 * 3600)) --stale-after 12h
  [ "$status" -eq 0 ]
  [[ $output != *"handed-off"* ]]
  [ "$(labels_of "$id")" = "escalated" ]
}

@test "a claimed (in_progress, assigned) bead is skipped" {
  id="$(new_bead escalated)"
  bd update --claim --actor claimer -- "$id" >/dev/null 2>&1
  t0="$(escalated_at "$id")"
  run_wd $((t0 + 13 * 3600)) --stale-after 12h
  [ "$status" -eq 0 ]
  [[ $output != *"handed-off"* ]]
  [ "$(labels_of "$id")" = "escalated" ]
}

@test "an assigned (still open) bead is skipped" {
  id="$(new_bead escalated)"
  bd update -a someone -- "$id" >/dev/null 2>&1
  t0="$(escalated_at "$id")"
  run_wd $((t0 + 13 * 3600)) --stale-after 12h
  [ "$status" -eq 0 ]
  [[ $output != *"handed-off"* ]]
  [ "$(labels_of "$id")" = "escalated" ]
}

@test "a bead already carrying attention, human-focus, human-focus-required or human is skipped, labels untouched" {
  for hl in attention human-focus human-focus-required human; do
    id="$(new_bead escalated "$hl")"
    t0="$(escalated_at "$id")"
    run_wd $((t0 + 13 * 3600)) --stale-after 12h
    [ "$status" -eq 0 ]
    [[ $output != *"handed-off"* ]]
    [ "$(labels_of "$id")" = "$(printf '%s\n' escalated "$hl" | sort)" ]
  done
}

@test "a bead updated within --quiet-for is skipped (a triager may be working it)" {
  id="$(new_bead escalated)"
  t0="$(escalated_at "$id")"
  # Stale by the clock, but touched moments ago relative to the pinned now.
  run_wd $((t0 + 13 * 3600)) --stale-after 12h --quiet-for 14h
  [ "$status" -eq 0 ]
  [[ $output != *"handed-off"* ]]
  [[ $output == *"1 quiet"* ]]
  [ "$(labels_of "$id")" = "escalated" ]
  run_wd $((t0 + 13 * 3600)) --stale-after 12h --quiet-for 12h
  [[ $output == *"handed-off $id"* ]]
}

@test "no double hand-off: a second run finds nothing and the note is appended once" {
  id="$(new_bead escalated)"
  t0="$(escalated_at "$id")"
  run_wd $((t0 + 13 * 3600)) --stale-after 12h
  [[ $output == *"handed-off $id"* ]]
  notes1="$(notes_of "$id")"
  run_wd $((t0 + 14 * 3600)) --stale-after 12h
  [ "$status" -eq 0 ]
  [[ $output != *"handed-off"* ]]
  [ "$(notes_of "$id")" = "$notes1" ]
  [ "$(labels_of "$id")" = $'attention\nhuman\nstale-escalation' ]
}

@test "re-escalation restarts the clock from the LATEST escalated event" {
  id="$(new_bead escalated)"
  t0="$(escalated_at "$id")"
  run_wd $((t0 + 100)) --stale-after 2s --quiet-for 0s
  [[ $output == *"handed-off $id"* ]]
  # The operator releases it and the triager escalates it again, 3 seconds later.
  sleep 3
  bd update --remove-label human --remove-label attention --remove-label stale-escalation --add-label escalated -- "$id" >/dev/null 2>&1
  t1="$(escalated_at "$id")"
  [ "$t1" -gt "$t0" ]
  # 1s after the re-escalation: the FIRST event is >2s old, the latest is not.
  run_wd $((t1 + 1)) --stale-after 2s --quiet-for 0s
  [ "$status" -eq 0 ]
  [[ $output != *"handed-off"* ]]
  [ "$(labels_of "$id")" = "escalated" ]
  # 3s after: stale again, handed off a second time (a new escalation).
  run_wd $((t1 + 3)) --stale-after 2s --quiet-for 0s
  [[ $output == *"handed-off $id"* ]]
  [ "$(labels_of "$id")" = $'attention\nhuman\nstale-escalation' ]
}

@test "per-tick cap: the default is 3, --max-per-tick overrides, the rest wait for the next tick" {
  ids=()
  for _ in 1 2 3 4 5; do ids+=("$(new_bead escalated)"); done
  t0="$(escalated_at "${ids[0]}")"
  now=$((t0 + 13 * 3600))
  run_wd "$now" --stale-after 12h
  [ "$status" -eq 0 ]
  [ "$(grep -c '^handed-off ' <<<"$output")" -eq 3 ]
  [[ $output == *"2 over the cap"* ]]
  run_wd "$now" --stale-after 12h --max-per-tick 1
  [ "$(grep -c '^handed-off ' <<<"$output")" -eq 1 ]
  run_wd "$now" --stale-after 12h --max-per-tick 10
  [ "$(grep -c '^handed-off ' <<<"$output")" -eq 1 ]
  run_wd "$now" --stale-after 12h
  [ "$(grep -c '^handed-off ' <<<"$output")" -eq 0 ]
}

@test "dry-run reports what it would do and writes nothing" {
  id="$(new_bead escalated)"
  t0="$(escalated_at "$id")"
  events_before="$(bd history "$id" --events --json | jq length)"
  run_wd $((t0 + 13 * 3600)) --stale-after 12h --dry-run
  [ "$status" -eq 0 ]
  [[ $output == *"would-hand-off $id age=13h 0m"* ]]
  [[ $output != *"handed-off $id"* ]]
  [ "$(labels_of "$id")" = "escalated" ]
  [ "$(notes_of "$id")" = "" ]
  [ "$(bd history "$id" --events --json | jq length)" -eq "$events_before" ]
}

@test "the note names the age, the clock used and the last comment" {
  id="$(new_bead escalated)"
  bd comments add "$id" "triager outcome: actions=[]
second line is not quoted" >/dev/null 2>&1
  t0="$(escalated_at "$id")"
  run_wd $((t0 + 30 * 3600 + 5 * 60)) --stale-after 12h
  [[ $output == *"handed-off $id age=30h 5m"* ]]
  n="$(notes_of "$id")"
  [[ $n == *"Escalated for 30h 5m (since "* ]]
  [[ $n == *"Last comment on the bead: triager outcome: actions=[]"* ]]
  [[ $n != *"second line is not quoted"* ]]
  [[ $n == *"Handed off by escalation-watchdog (label stale-escalation)."* ]]
}

@test "guard mismatch (someone claimed it after selection): skipped, not an error, not counted against the cap, nothing written" {
  a="$(new_bead escalated)"
  b="$(new_bead escalated)"
  # A real hand-to-human behind a shim that claims the FIRST bead it is asked about
  # (the race), then runs the real tool unchanged.
  mkdir -p "$TDIR/shim"
  cat >"$TDIR/shim/hand-to-human" <<SHIM
#!/usr/bin/env bash
if [ ! -e "$TDIR/raced" ]; then
  : >"$TDIR/raced"
  bd -C "$TDIR" update -a racer -- "\$1" >/dev/null 2>&1
  echo "\$1" >"$TDIR/raced"
fi
exec "$REAL_H2H" "\$@"
SHIM
  chmod +x "$TDIR/shim/hand-to-human"
  t0="$(escalated_at "$a")"
  PATH="$TDIR/shim:$PATH" run_wd $((t0 + 13 * 3600)) --stale-after 12h --max-per-tick 1
  [ "$status" -eq 0 ]
  raced="$(cat "$TDIR/raced")"
  [[ $output == *"skipped-claimed $raced"* ]]
  [ "$(labels_of "$raced")" = "escalated" ]
  [ "$(notes_of "$raced")" = "" ]
  other="$a"
  [ "$raced" != "$a" ] || other="$b"
  # The cap of 1 was still available for the other bead.
  [[ $output == *"handed-off $other"* ]]
  [ "$(labels_of "$other")" = $'attention\nhuman\nstale-escalation' ]
}

@test "a hand-off that fails is exit 3 and the other beads are still attempted" {
  a="$(new_bead escalated)"
  b="$(new_bead escalated)"
  mkdir -p "$TDIR/shim"
  cat >"$TDIR/shim/hand-to-human" <<SHIM
#!/usr/bin/env bash
if [ ! -e "$TDIR/failed-once" ]; then
  : >"$TDIR/failed-once"
  exit 3
fi
exec "$REAL_H2H" "\$@"
SHIM
  chmod +x "$TDIR/shim/hand-to-human"
  t0="$(escalated_at "$a")"
  PATH="$TDIR/shim:$PATH" run_wd $((t0 + 13 * 3600)) --stale-after 12h
  [ "$status" -eq 3 ]
  [ "$(grep -c '^failed ' <<<"$output")" -eq 1 ]
  [ "$(grep -c '^handed-off ' <<<"$output")" -eq 1 ]
  [ "$a$b" != "" ]
}

@test "an unreachable tracker (no .beads) is exit 4 and nothing is handed off" {
  empty="$(mktemp -d)"
  ESCALATION_WATCHDOG_NOW=1800000000 run --separate-stderr "$W" --bd-dir "$empty" --stale-after 12h
  rm -rf "$empty"
  [ "$status" -eq 4 ]
  [[ $stderr == *"bd ready failed"* ]]
}

# bd_warn_shim: a `bd` first on PATH that prints a warning on stderr, then execs
# the REAL bd (so stdout is the real bd's), as bd does in a tracker without
# beads.role configured.
bd_warn_shim() {
  local real_bd shim_dir
  real_bd="$(command -v bd)"
  shim_dir="$(mktemp -d)"
  cat >"$shim_dir/bd" <<SHIM
#!/usr/bin/env bash
echo "warning: beads.role not configured" >&2
exec "$real_bd" "\$@"
SHIM
  chmod +x "$shim_dir/bd"
  PATH="$shim_dir:$PATH"
  export PATH
}

@test "a bd stderr warning on a successful call does not break the tick: stdout alone is parsed" {
  id="$(new_bead escalated)"
  t0="$(escalated_at "$id")"
  bd_warn_shim
  run_wd $((t0 + 13 * 3600)) --stale-after 12h
  [ "$status" -eq 0 ]
  [[ $output == *"handed-off $id"* ]]
  [[ $stderr != *"could not parse"* ]]
  [ "$(labels_of "$id")" = $'attention\nhuman\nstale-escalation' ]
}

@test "a failing bd ready still reports bd's stderr, exit 4" {
  empty="$(mktemp -d)"
  bd_warn_shim
  ESCALATION_WATCHDOG_NOW=1800000000 run --separate-stderr "$W" --bd-dir "$empty" --stale-after 12h
  rm -rf "$empty"
  [ "$status" -eq 4 ]
  [[ $stderr == *"bd ready failed"* ]]
  [[ $stderr == *"beads.role not configured"* ]]
}

@test "ESCALATION_WATCHDOG_NOW accepts epoch seconds or an RFC 3339 timestamp" {
  id="$(new_bead escalated)"
  t0="$(escalated_at "$id")"
  iso="$(jq -nr --argjson t $((t0 + 13 * 3600)) '$t | todate')"
  run_wd "$iso" --stale-after 12h --dry-run
  [ "$status" -eq 0 ]
  [[ $output == *"would-hand-off $id age=13h 0m"* ]]
}
