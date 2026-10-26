#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Unit tests for escalation-watchdog.bash: duration parsing, timestamp reading, the
# escalation clock (including its updated_at fallback) and the note.

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  # shellcheck disable=SC1091
  source "${SCRIPTS_DIR}/escalation-watchdog.bash"
}

@test "ew_parse_duration: hours, minutes, seconds, leading zeros are decimal" {
  [ "$(ew_parse_duration 12h)" = 43200 ]
  [ "$(ew_parse_duration 90m)" = 5400 ]
  [ "$(ew_parse_duration 30s)" = 30 ]
  [ "$(ew_parse_duration 0s)" = 0 ]
  [ "$(ew_parse_duration 010m)" = 600 ]
}

@test "ew_parse_duration: rejects everything else" {
  for bad in "" 12 1h30m 1.5h 12H 5d -5m ' 5m' 5m$'\n' h 9999999999h; do
    run ew_parse_duration "$bad"
    [ "$status" -eq 1 ]
  done
}

@test "ew_to_epoch: Z, fractional seconds and +00:00 all read the same" {
  [ "$(ew_to_epoch 2026-10-26T01:59:48Z)" = 1792979988 ]
  [ "$(ew_to_epoch 2026-10-26T01:59:48.123456Z)" = 1792979988 ]
  [ "$(ew_to_epoch 2026-10-26T01:59:48+00:00)" = 1792979988 ]
  run ew_to_epoch "not a time"
  [ "$status" -ne 0 ]
}

@test "ew_now_epoch: the override takes epoch seconds or RFC 3339, otherwise the real clock" {
  ESCALATION_WATCHDOG_NOW=1792979988 run ew_now_epoch
  [ "$output" = 1792979988 ]
  ESCALATION_WATCHDOG_NOW=2026-10-26T01:59:48Z run ew_now_epoch
  [ "$output" = 1792979988 ]
  ESCALATION_WATCHDOG_NOW=garbage run ew_now_epoch
  [ "$status" -ne 0 ]
  unset ESCALATION_WATCHDOG_NOW
  now="$(ew_now_epoch)"
  [ "$now" -gt 1790000000 ]
}

@test "ew_clock: the LATEST 'Added label: escalated' event wins, other events are ignored" {
  history='[
    {"event_type":"created","created_at":"2026-10-20T00:00:00Z"},
    {"event_type":"label_added","comment":"Added label: escalated","created_at":"2026-10-21T00:00:00Z"},
    {"event_type":"label_added","comment":"Added label: foo","created_at":"2026-10-25T00:00:00Z"},
    {"event_type":"label_removed","comment":"Removed label: escalated","created_at":"2026-10-26T00:00:00Z"},
    {"event_type":"label_added","comment":"Added label: escalated","created_at":"2026-10-22T00:00:00Z"}
  ]'
  run ew_clock "$history" "2026-10-26T12:00:00Z"
  [ "$status" -eq 0 ]
  [ "$output" = "$(ew_to_epoch 2026-10-22T00:00:00Z) label_added" ]
}

@test "ew_clock: falls back to updated_at only when no escalated event exists" {
  run ew_clock '[{"event_type":"created","created_at":"2026-10-20T00:00:00Z"}]' "2026-10-26T12:00:00Z"
  [ "$output" = "$(ew_to_epoch 2026-10-26T12:00:00Z) updated_at" ]
  run ew_clock '[]' "2026-10-26T12:00:00Z"
  [ "$output" = "$(ew_to_epoch 2026-10-26T12:00:00Z) updated_at" ]
  run ew_clock 'not json' "2026-10-26T12:00:00Z"
  [ "$output" = "$(ew_to_epoch 2026-10-26T12:00:00Z) updated_at" ]
  run ew_clock '[]' ""
  [ "$status" -eq 1 ]
}

@test "ew_clock: reads the enveloped history shape too" {
  run ew_clock '{"data":[{"event_type":"label_added","comment":"Added label: escalated","created_at":"2026-10-21T00:00:00Z"}]}' ""
  [ "$output" = "$(ew_to_epoch 2026-10-21T00:00:00Z) label_added" ]
}

@test "ew_format_age: hours and minutes, minutes, seconds" {
  [ "$(ew_format_age 46800)" = "13h 0m" ]
  [ "$(ew_format_age 5400)" = "1h 30m" ]
  [ "$(ew_format_age 150)" = "2m" ]
  [ "$(ew_format_age 59)" = "59s" ]
}

@test "ew_last_comment: first line of the newest comment, cut to 160 characters; empty when none" {
  c='[{"text":"old one","created_at":"2026-10-20T00:00:00Z"},{"text":"newest line\nmore","created_at":"2026-10-21T00:00:00Z"}]'
  [ "$(ew_last_comment "$c")" = "newest line" ]
  [ "$(ew_last_comment '[]')" = "" ]
  long="$(printf 'x%.0s' $(seq 1 300))"
  out="$(ew_last_comment "$(jq -cn --arg t "$long" '[{text:$t,created_at:"2026-10-21T00:00:00Z"}]')")"
  [ "${#out}" -eq 160 ]
}

@test "ew_build_note: warning first, then the age, the clock, the last comment and the marker" {
  run ew_build_note 46800 label_added 1792979988 "triager said no"
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "Automation timed out on this bead. The cause may be infrastructure (disk, usage limit, auth), not a decision only you can make. Check the cause first." ]
  [ "${lines[1]}" = "Escalated for 13h 0m (since 2026-10-26T01:59:48Z, measured from the latest 'Added label: escalated' event)." ]
  [ "${lines[2]}" = "Last comment on the bead: triager said no" ]
  run ew_build_note 46800 updated_at 1792979988 ""
  [[ ${lines[1]} == *"the bead's updated_at (no 'Added label: escalated' event was found)"* ]]
  [ "${lines[2]}" = "No comment on the bead records a triager outcome." ]
}
