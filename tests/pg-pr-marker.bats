#!/usr/bin/env bats
# Regression guard for the pg-pr PreToolUse agent-marker hook (bead pg2-o3eyk).
#
# Drives require-agent-pr-comment-marker (wrapped onto PATH by the flake's
# test-pg-pr-marker-hook check) with Claude-Code-shaped stdin JSON built via
# `jq -n`, so the marker bytes never need hand-escaping in the payload literal.
#
# Against the pre-fix script these go RED: the CLAUDE_TOOL_NAME gate short-
# circuits every call to exit 0 (so the two refuse cases wrongly pass), and even
# once the tool name is read from stdin the `$'\U0001F916'` glyph never matches
# under the sandbox's C locale (so the marked case is wrongly refused).

# The agent marker (🤖 = U+1F916) as raw UTF-8 bytes — locale-independent.
marker=$'\xf0\x9f\xa4\x96'

run_hook() {
  # Positional form: robust to embedded quotes and raw multibyte bytes in the
  # payload (do NOT nest "$payload" inside a double-quoted bash -c string).
  run bash -c 'printf "%s" "$1" | require-agent-pr-comment-marker' _ "$1"
}

@test "unmarked pg-pr comment add is refused (exit 2)" {
  payload="$(jq -n --arg c "pg-pr comment add 1 --body done" \
    '{tool_name:"Bash",tool_input:{command:$c}}')"
  run_hook "$payload"
  [ "$status" -eq 2 ]
  [[ "$output" == *Refusing* ]]
}

@test "marked gh pr comment is allowed (exit 0)" {
  payload="$(jq -n --arg c "gh pr comment 1 --body 'done ${marker}'" \
    '{tool_name:"Bash",tool_input:{command:$c}}')"
  run_hook "$payload"
  [ "$status" -eq 0 ]
}

@test "unmarked gh api reviews POST is refused (exit 2)" {
  payload="$(jq -n --arg c "gh api repos/o/r/pulls/1/reviews --method POST -f body=lgtm" \
    '{tool_name:"Bash",tool_input:{command:$c}}')"
  run_hook "$payload"
  [ "$status" -eq 2 ]
  [[ "$output" == *Refusing* ]]
}

@test "unrelated Bash command is allowed (exit 0)" {
  payload="$(jq -n --arg c "ls -la" \
    '{tool_name:"Bash",tool_input:{command:$c}}')"
  run_hook "$payload"
  [ "$status" -eq 0 ]
}

@test "non-Bash tool is passed through without inspection (exit 0)" {
  # A write-path command carried by a non-Bash tool must NOT be inspected.
  payload="$(jq -n --arg c "pg-pr comment add 1 --body done" \
    '{tool_name:"Edit",tool_input:{command:$c}}')"
  run_hook "$payload"
  [ "$status" -eq 0 ]
}
