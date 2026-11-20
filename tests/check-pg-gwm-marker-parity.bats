#!/usr/bin/env bats
# bats file_tags=type:unit
# The pg-gwm exception marker must be spelled identically in the always-on rules text and in
# the tool approver's pggwmworktree rule (global rule V-3: the real script, real repo files,
# plus mutated fixtures as negative controls).

bats_require_minimum_version 1.5.0

REPO_ROOT="$BATS_TEST_DIRNAME/.."
SCRIPT="$REPO_ROOT/scripts/check-pg-gwm-marker-parity.sh"
RULES="$REPO_ROOT/home/programs/agent-rules/pgii-agent-rules.md"
RULE_GO="$REPO_ROOT/packages/claude-extended-tool-approver/internal/rules/pggwmworktree/rule.go"

@test "real rules text and real hook agree on PG_GWM_EXCEPTION" {
  run "$SCRIPT" "$RULES" "$RULE_GO"
  [ "$status" -eq 0 ]
  [ "$output" = "ok: PG_GWM_EXCEPTION" ]
}

@test "a renamed marker in the rules text fails" {
  sed 's/PG_GWM_EXCEPTION/PG_GWM_EXCUSE/g' "$RULES" >"$BATS_TEST_TMPDIR/rules.md"
  run "$SCRIPT" "$BATS_TEST_TMPDIR/rules.md" "$RULE_GO"
  [ "$status" -ne 0 ]
}

@test "a renamed marker in the hook fails" {
  sed 's/^const ExceptionEnv = .*/const ExceptionEnv = "PG_GWM_EXCUSE"/' "$RULE_GO" >"$BATS_TEST_TMPDIR/rule.go"
  run "$SCRIPT" "$RULES" "$BATS_TEST_TMPDIR/rule.go"
  [ "$status" -ne 0 ]
}

@test "dropping a prohibited route from the rules text fails" {
  sed 's#bin/worktree#bin/wt#g' "$RULES" >"$BATS_TEST_TMPDIR/rules.md"
  run "$SCRIPT" "$BATS_TEST_TMPDIR/rules.md" "$RULE_GO"
  [ "$status" -ne 0 ]
}
