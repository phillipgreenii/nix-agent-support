#!/usr/bin/env bats
# bats file_tags=type:unit
# Regression guard for the handoff selection filter of /pb:process-handoff.
#
# The command selects handoffs client-side (type `handoff`, or a title starting `Handoff`)
# because the `handoff` issue type is not registered in every tracker. The jq filter lives in the
# command markdown as `SEL='...'`; this test EXTRACTS it from there (never retyped) and runs it
# over a fixture in both `bd --json` shapes (the bare array and the {"data":[..]} envelope).
#
# Environment:
#   PBPH_CMD  path to the command markdown; set by the flake check, and defaults to the file in
#             this checkout when unset (the commit-time bats hook)
#   jq, awk on PATH

setup() {
  PBPH_CMD="${PBPH_CMD:-$BATS_TEST_DIRNAME/../claude-marketplace/pb/commands/process-handoff.md}"
  [ -f "$PBPH_CMD" ] || { echo "PBPH_CMD does not exist: $PBPH_CMD" >&2; return 1; }
}

# The single `SEL='...'` line from the command file, with the shell quoting removed.
sel_filter() {
  local line
  line="$(awk '/^SEL=\x27/ { print; exit }' "$PBPH_CMD")"
  [ -n "$line" ] || return 1
  line="${line#SEL=\'}"
  printf '%s' "${line%\'}"
}

# A fixture covering every branch of the filter. Fields mirror `bd ready --json`.
fixture_array() {
  cat <<'JSON'
[
  {"id":"a-1","title":"Anything at all","issue_type":"handoff","status":"open"},
  {"id":"a-2","title":"Handoff: carry over the work","issue_type":"task","status":"open","assignee":"someone"},
  {"id":"a-3","title":"HANDOFF - shouted","issue_type":"task","status":"open"},
  {"id":"a-4","title":"Hand-off: hyphenated","issue_type":"task","status":"open"},
  {"id":"a-5","title":"handoff","issue_type":"task","status":"open"},
  {"id":"x-1","title":"Resume: an ordinary work bead","issue_type":"task","status":"open"},
  {"id":"x-2","title":"Handoffs are not selected by a longer word","issue_type":"task","status":"open"},
  {"id":"x-3","title":"Not a Handoff: the word is not first","issue_type":"task","status":"open"},
  {"id":"x-4","title":"Handoff: a molecule template","issue_type":"task","status":"open","is_template":true},
  {"id":"x-5","title":"NEXT SESSION: legacy form, deliberately not selected","issue_type":"task","status":"open"}
]
JSON
}

run_filter() {
  local shape="$1" filter
  filter="$(sel_filter)"
  if [ "$shape" = envelope ]; then
    fixture_array | jq '{schema_version: 1, data: .}' | jq -r "$filter"
  else
    fixture_array | jq -r "$filter"
  fi
}

@test "the command file carries exactly one SEL filter" {
  [ "$(awk '/^SEL=\x27/ { n++ } END { print n + 0 }' "$PBPH_CMD")" = 1 ]
  [ -n "$(sel_filter)" ]
}

@test "bare array: selects type handoff and every title form starting Handoff, in order" {
  run run_filter bare
  [ "$status" -eq 0 ]
  [ "$(printf '%s\n' "$output" | cut -f1 | paste -sd, -)" = "a-1,a-2,a-3,a-4,a-5" ]
}

@test "envelope: selects the same beads as the bare array" {
  run run_filter envelope
  [ "$status" -eq 0 ]
  [ "$(printf '%s\n' "$output" | cut -f1 | paste -sd, -)" = "a-1,a-2,a-3,a-4,a-5" ]
}

@test "output carries id, status, assignee and title, tab separated" {
  run run_filter bare
  [ "$status" -eq 0 ]
  [ "$(printf '%s\n' "$output" | sed -n 2p)" = "$(printf 'a-2\topen\tsomeone\tHandoff: carry over the work')" ]
  # an absent assignee prints as an empty field, not "null"
  [ "$(printf '%s\n' "$output" | sed -n 3p | cut -f3)" = "" ]
}

@test "ordinary work beads, longer words, templates and legacy title forms are not selected" {
  run run_filter bare
  [ "$status" -eq 0 ]
  for id in x-1 x-2 x-3 x-4 x-5; do
    if printf '%s\n' "$output" | cut -f1 | grep -qx "$id"; then
      echo "unexpectedly selected $id" >&2
      return 1
    fi
  done
}

@test "negative control: a filter without the title test selects only the typed handoff" {
  # Proves the fixture can tell the title branch from the type branch: if this passed with the
  # real filter the tests above would be vacuous.
  run bash -c "$(declare -f fixture_array); fixture_array | jq -r '(if type==\"object\" and has(\"data\") then .data else . end) | .[] | select(.issue_type==\"handoff\") | .id'"
  [ "$status" -eq 0 ]
  [ "$output" = "a-1" ]
}
