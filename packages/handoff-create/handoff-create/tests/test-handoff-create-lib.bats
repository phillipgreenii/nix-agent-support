#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Unit tests for handoff-create.bash's functions. No real bd: the one bd
# touchpoint, hc_bd, is replaced by a stub that returns canned `bd show`
# output, so the envelope handling and read-back comparison are exercised on
# BOTH `--json` shapes.

bats_require_minimum_version 1.5.0

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  # shellcheck disable=SC1091
  source "${SCRIPTS_DIR}/handoff-create.bash"
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
}

teardown() {
  rm -rf "$TEST_DIR"
}

# issue_json <type> <title> <first-line> <session> <labels-json> [priority]
issue_json() {
  jq -cn --arg t "$1" --arg ti "$2" --arg fl "$3" --arg s "$4" --argjson l "$5" --argjson p "${6:-0}" \
    '{id:"tst-1", issue_type:$t, title:$ti, priority:$p, description:($fl + "\n\nbody"), metadata:{handed_off_from_session:$s}, labels:$l}'
}

# stub_show <shape> <issue-json>: define hc_bd to print the issue as
# `bd show --json` would, in the "envelope" ({data:[..]}) or "bare" (array) shape.
stub_show() {
  case "$1" in
  envelope) HC_STUB_OUT="$(jq -cn --argjson i "$2" '{data:[$i], schema_version:1}')" ;;
  bare) HC_STUB_OUT="$(jq -cn --argjson i "$2" '[$i]')" ;;
  esac
  export HC_STUB_OUT
  hc_bd() { printf '%s\n' "$HC_STUB_OUT"; }
}

@test "hc_normalize_title adds exactly one Handoff: prefix" {
  [ "$(hc_normalize_title 'finish work')" = "Handoff: finish work" ]
  [ "$(hc_normalize_title 'Handoff: finish work')" = "Handoff: finish work" ]
  [ "$(hc_normalize_title 'Handoff:finish work')" = "Handoff: finish work" ]
  [ "$(hc_normalize_title '  Handoff:   Handoff: x  ')" = "Handoff: x" ]
  [ "$(hc_normalize_title 'Resume: a Handoff: b')" = "Handoff: Resume: a Handoff: b" ]
}

@test "hc_normalize_title rejects an empty subject" {
  run hc_normalize_title "Handoff:"
  [ "$status" -ne 0 ]
  run hc_normalize_title "   "
  [ "$status" -ne 0 ]
}

@test "hc_build_metadata emits the handed_off_from_session JSON, escaped" {
  [ "$(hc_build_metadata 'abc-123')" = '{"handed_off_from_session":"abc-123"}' ]
  [ "$(hc_build_metadata 'a"b' | jq -r .handed_off_from_session)" = 'a"b' ]
}

@test "hc_parse_id accepts one id token and rejects anything else" {
  [ "$(hc_parse_id $'pg2-abc12\n')" = "pg2-abc12" ]
  run hc_parse_id ""
  [ "$status" -ne 0 ]
  run hc_parse_id "two words"
  [ "$status" -ne 0 ]
  run hc_parse_id $'a\nb'
  [ "$status" -ne 0 ]
  run hc_parse_id "Error: nope"
  [ "$status" -ne 0 ]
}

@test "hc_is_unregistered_type_failure matches only the unregistered-type error" {
  # shellcheck disable=SC2034  # HC_ERR and HC_OUT are read by hc_is_unregistered_type_failure
  HC_ERR="Error: validation failed for issue : invalid issue type: handoff"
  # shellcheck disable=SC2034  # as above
  HC_OUT=""
  hc_is_unregistered_type_failure
  # shellcheck disable=SC2034  # read by hc_is_unregistered_type_failure
  HC_ERR="Error: database not found"
  run hc_is_unregistered_type_failure
  [ "$status" -ne 0 ]
}

@test "hc_normalize_show reduces both envelope shapes to the same object" {
  issue="$(issue_json handoff 'Handoff: x' 'Handoff from session s' s '["human"]')"
  env_out="$(jq -cn --argjson i "$issue" '{data:[$i]}' | hc_normalize_show)"
  bare_out="$(jq -cn --argjson i "$issue" '[$i]' | hc_normalize_show)"
  [ "$env_out" = "$bare_out" ]
  [ "$(jq -r .type <<<"$env_out")" = "handoff" ]
  [ "$(jq -r .session <<<"$env_out")" = "s" ]
  [ "$(jq -r .first_line <<<"$env_out")" = "Handoff from session s" ]
}

@test "hc_readback passes on both shapes when everything matches (attended)" {
  issue="$(issue_json handoff 'Handoff: x' 'Handoff from session s' s '["human","auto-session-wrapped"]')"
  for shape in envelope bare; do
    stub_show "$shape" "$issue"
    run --separate-stderr hc_readback tst-1 handoff "Handoff: x" s 1 human auto-session-wrapped
    [ "$status" -eq 0 ]
    [[ $stderr != *MISMATCH* ]]
  done
}

@test "hc_readback passes on both shapes when everything matches (unattended, task fallback)" {
  issue="$(issue_json task 'Handoff: x' 'Handoff from session s' s '[]')"
  for shape in envelope bare; do
    stub_show "$shape" "$issue"
    run --separate-stderr hc_readback tst-1 task "Handoff: x" s 0
    [ "$status" -eq 0 ]
  done
}

@test "hc_readback fails (4) on every kind of disagreement, on both shapes" {
  local -a cases=(
    "type|$(issue_json task 'Handoff: x' 'Handoff from session s' s '[]')|handoff|0"
    "title|$(issue_json handoff 'Handoff: y' 'Handoff from session s' s '[]')|handoff|0"
    "first line|$(issue_json handoff 'Handoff: x' 'Something else' s '[]')|handoff|0"
    "metadata|$(issue_json handoff 'Handoff: x' 'Handoff from session s' other '[]')|handoff|0"
    "priority|$(issue_json handoff 'Handoff: x' 'Handoff from session s' s '[]' 2)|handoff|0"
    "human missing when attended|$(issue_json handoff 'Handoff: x' 'Handoff from session s' s '[]')|handoff|1"
    "human present when unattended|$(issue_json handoff 'Handoff: x' 'Handoff from session s' s '["human"]')|handoff|0"
  )
  local c name issue want_type attended shape
  for c in "${cases[@]}"; do
    IFS='|' read -r name issue want_type attended <<<"$c"
    for shape in envelope bare; do
      stub_show "$shape" "$issue"
      run --separate-stderr hc_readback tst-1 "$want_type" "Handoff: x" s "$attended"
      [ "$status" -eq 4 ] || {
        echo "case '$name' ($shape): expected 4 got $status" >&2
        return 1
      }
      [[ $stderr == *MISMATCH* ]]
    done
  done
}

@test "hc_readback fails (4) when a requested passthrough label is missing" {
  stub_show bare "$(issue_json handoff 'Handoff: x' 'Handoff from session s' s '[]')"
  run --separate-stderr hc_readback tst-1 handoff "Handoff: x" s 0 auto-session-wrapped
  [ "$status" -eq 4 ]
  [[ $stderr == *"label auto-session-wrapped missing"* ]]
}

@test "hc_readback fails (4) on unparseable bd show output" {
  hc_bd() { echo "this is not json"; }
  run --separate-stderr hc_readback tst-1 handoff "Handoff: x" s 0
  [ "$status" -eq 4 ]
  [[ $stderr == *MISMATCH* ]]
}
