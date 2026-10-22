#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Unit tests for hand-to-human.bash: the canonical label names and the argv of
# the one update call.

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
  # shellcheck disable=SC1091
  source "${SCRIPTS_DIR}/hand-to-human.bash"
}

@test "the single WRITE label per role is the documented one" {
  [ "$HTH_HUMAN_LABEL" = human ]
  [ "$HTH_ATTENTION_LABEL" = attention ]
  [ "$HTH_ESCALATED_LABEL" = escalated ]
}

@test "hth_remove_labels: escalated first, extras after, de-duplicated" {
  run hth_remove_labels a b a escalated
  [ "$status" -eq 0 ]
  [ "$output" = $'escalated\na\nb' ]
}

@test "hth_update_args: both adds precede every removal, actor last" {
  run hth_update_args sess-1 escalated x
  [ "$status" -eq 0 ]
  [ "$output" = $'--add-label\nhuman\n--add-label\nattention\n--remove-label\nescalated\n--remove-label\nx\n--actor\nsess-1' ]
}

@test "hth_update_args: no actor means no --actor" {
  run hth_update_args "" escalated
  [ "$output" = $'--add-label\nhuman\n--add-label\nattention\n--remove-label\nescalated' ]
}
