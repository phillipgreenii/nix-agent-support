#!/usr/bin/env bats
# bats file_tags=type:integration
# Conformance: pb:drain-one step 2 UNDERSTAND surfaces a bead's comments (plain `bd show` or
# `--include-comments`; `--json` alone omits them), tells the worker to follow a TRIAGER-ANSWER:
# comment, and pb:drain-stuck refuses a re-escalation after one unless the comment states
# ANSWER-DOES-NOT-APPLY. Each check runs on the real tree and on a mutated copy that must fail.
#
# Environment: PBD_PB_DIR  path to the pb plugin root (holds skills/)

setup() {
  : "${PBD_PB_DIR:?}"
  work="$BATS_TEST_TMPDIR/pb"
  cp -R "$PBD_PB_DIR" "$work"
  chmod -R u+w "$work"
}

check_one() {
  local f="$1/skills/drain-one/SKILL.md"
  grep -qF -- '--include-comments' "$f" || { echo "drain-one: no --include-comments"; return 1; }
  grep -qF -- 'TRIAGER-ANSWER:' "$f" || { echo "drain-one: no TRIAGER-ANSWER"; return 1; }
  grep -qF -- 'binding instructions' "$f" || { echo "drain-one: not binding"; return 1; }
}

check_stuck() {
  local f="$1/skills/drain-stuck/SKILL.md"
  grep -qF -- 'TRIAGER-ANSWER:' "$f" || { echo "drain-stuck: no TRIAGER-ANSWER"; return 1; }
  grep -qF -- 'ANSWER-DOES-NOT-APPLY:' "$f" || { echo "drain-stuck: no ANSWER-DOES-NOT-APPLY"; return 1; }
}

@test "drain-one UNDERSTAND surfaces and follows TRIAGER-ANSWER" {
  run check_one "$work"
  [ "$status" -eq 0 ]
}

@test "drain-one check bites when the comment read is removed" {
  perl -pi -e 's/--include-comments/REMOVED/g' "$work/skills/drain-one/SKILL.md"
  run check_one "$work"
  [ "$status" -ne 0 ]
}

@test "drain-stuck requires a reason to re-escalate after a TRIAGER-ANSWER" {
  run check_stuck "$work"
  [ "$status" -eq 0 ]
}

@test "drain-stuck check bites when the reason line is removed" {
  perl -pi -e 's/ANSWER-DOES-NOT-APPLY:/REMOVED:/g' "$work/skills/drain-stuck/SKILL.md"
  run check_stuck "$work"
  [ "$status" -ne 0 ]
}
