#!/usr/bin/env bats
# Regression guard for the pg-pr PreToolUse agent-marker hook
# (beads pg2-o3eyk wired+de-locale-fragile'd it; pg2-dumx fixed the enforcement
# contract + the marker it checks).
#
# Drives require-agent-pr-comment-marker (wrapped onto PATH by the flake's
# test-pg-pr-marker-hook check) with Claude-Code-shaped stdin JSON built via
# `jq -n`, so the marker bytes never need hand-escaping in the payload literal.
#
# Enforcement contract (pg2-dumx): the pg-pr CLI auto-stamps every body it posts
# via internal/marker.Stamp, so a `pg-pr comment add` / `pg-pr review post` /
# `pg-pr review submit` command legitimately carries NO marker and MUST be
# allowed unconditionally. The hook exists to catch the BYPASS: a direct
# `gh pr comment` / `gh api ...reviews...POST` of an AI-authored body, which is
# refused unless it carries the pg-pr marker. The accepted marker is
# marker.IsOurs: the canonical invisible HTML marker `<!-- pg-pr -->` OR the
# legacy visible glyph (🤖). The glyph is matched by raw UTF-8 bytes under
# LC_ALL=C (the `$'\U…'` form degrades to a literal string in a C locale).

# The legacy agent glyph (🤖 = U+1F916) as raw UTF-8 bytes — locale-independent.
glyph=$'\xf0\x9f\xa4\x96'
# The canonical invisible marker (ASCII, locale-safe).
htmlmarker='<!-- pg-pr -->'

run_hook() {
  # Positional form: robust to embedded quotes and raw multibyte bytes in the
  # payload (do NOT nest "$payload" inside a double-quoted bash -c string).
  run bash -c 'printf "%s" "$1" | require-agent-pr-comment-marker' _ "$1"
}

@test "unmarked pg-pr comment add is allowed (trusted CLI auto-stamps, exit 0)" {
  payload="$(jq -n --arg c "pg-pr comment add 1 --body done" \
    '{tool_name:"Bash",tool_input:{command:$c}}')"
  run_hook "$payload"
  [ "$status" -eq 0 ]
}

@test "unmarked pg-pr review submit is allowed (trusted CLI auto-stamps, exit 0)" {
  payload="$(jq -n --arg c "pg-pr review submit 1 --event COMMENT" \
    '{tool_name:"Bash",tool_input:{command:$c}}')"
  run_hook "$payload"
  [ "$status" -eq 0 ]
}

@test "unmarked pg-pr review post is allowed (trusted CLI auto-stamps, exit 0)" {
  payload="$(jq -n --arg c "pg-pr review post 1 --body lgtm" \
    '{tool_name:"Bash",tool_input:{command:$c}}')"
  run_hook "$payload"
  [ "$status" -eq 0 ]
}

@test "unmarked direct gh pr comment is refused (bypass, exit 2)" {
  payload="$(jq -n --arg c "gh pr comment 1 --body 'done'" \
    '{tool_name:"Bash",tool_input:{command:$c}}')"
  run_hook "$payload"
  [ "$status" -eq 2 ]
  [[ "$output" == *Refusing* ]]
}

@test "unmarked direct gh api reviews POST is refused (bypass, exit 2)" {
  payload="$(jq -n --arg c "gh api repos/o/r/pulls/1/reviews --method POST -f body=lgtm" \
    '{tool_name:"Bash",tool_input:{command:$c}}')"
  run_hook "$payload"
  [ "$status" -eq 2 ]
  [[ "$output" == *Refusing* ]]
}

@test "gh pr comment carrying the visible glyph marker is allowed (exit 0)" {
  payload="$(jq -n --arg c "gh pr comment 1 --body 'done ${glyph}'" \
    '{tool_name:"Bash",tool_input:{command:$c}}')"
  run_hook "$payload"
  [ "$status" -eq 0 ]
}

@test "gh pr comment carrying the invisible HTML marker is allowed (exit 0)" {
  payload="$(jq -n --arg c "gh pr comment 1 --body '${htmlmarker} done'" \
    '{tool_name:"Bash",tool_input:{command:$c}}')"
  run_hook "$payload"
  [ "$status" -eq 0 ]
}

@test "gh api reviews POST carrying the HTML marker is allowed (exit 0)" {
  payload="$(jq -n --arg c "gh api repos/o/r/pulls/1/reviews --method POST -f body='${htmlmarker} lgtm'" \
    '{tool_name:"Bash",tool_input:{command:$c}}')"
  run_hook "$payload"
  [ "$status" -eq 0 ]
}

@test "chained pg-pr && unmarked gh bypass is still refused (exit 2)" {
  # Whole-command substring match: naming a pg-pr subcommand does not launder a
  # direct gh bypass in the same command line. (Inverse false-negative — a stray
  # marker elsewhere in the line allowing an unmarked gh body — is an inherent
  # limit of substring matching, acknowledged as defense-in-depth in the hook.)
  payload="$(jq -n --arg c "pg-pr sync && gh pr comment 1 --body evil" \
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
  # An unmarked direct-gh bypass carried by a non-Bash tool must NOT be inspected.
  payload="$(jq -n --arg c "gh pr comment 1 --body 'done'" \
    '{tool_name:"Edit",tool_input:{command:$c}}')"
  run_hook "$payload"
  [ "$status" -eq 0 ]
}
