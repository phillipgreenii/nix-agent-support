#!/usr/bin/env bats

# JSON blob representing a typical Claude Code status line call
TEST_JSON='{"session_id":"abc-123","version":"1.2.3","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus 4.6"},"context_window":{"used_percentage":25}}'

setup() {
  TEST_DIR=$(mktemp -d)
}

teardown() {
  [ -n "$TEST_DIR" ] && rm -rf "$TEST_DIR"
}

# Strip ANSI escape sequences so assertions are readable
strip_ansi() {
  printf '%s' "$1" | sed 's/\x1B\[[0-9;]*m//g'
}

@test "outputs model name from JSON" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"Opus 4.6"* ]]
}

@test "outputs context usage percentage" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"ctx:25%"* ]]
}

@test "segments are joined with ' | ' separator" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *" | "* ]]
}

@test "exits 0 even when a part produces no output" {
  EMPTY_JSON='{"workspace":{"current_dir":"/tmp/potato"},"model":{},"context_window":{}}'
  run bash -c "echo '$EMPTY_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
}

@test "output contains no literal null values" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" != *"null"* ]]
}

@test "shows H indicator on host (CONTAINED_CLAUDE unset)" {
  run bash -c "unset CONTAINED_CLAUDE; echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == "H |"* ]]
}

@test "shows C indicator in container (CONTAINED_CLAUDE=1)" {
  run bash -c "export CONTAINED_CLAUDE=1; echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == "C |"* ]]
}

@test "context usage colored green when below 60 percent" {
  LOW_CTX_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Haiku"},"context_window":{"used_percentage":20}}'
  run bash -c "echo '$LOW_CTX_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  # Green ANSI code (\033[32m) should precede ctx
  [[ "$output" == *$'\033[32m'*"ctx:"* ]]
}

@test "context usage colored yellow between 60 and 74 percent" {
  MID_CTX_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Haiku"},"context_window":{"used_percentage":65}}'
  run bash -c "echo '$MID_CTX_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" == *$'\033[33m'*"ctx:"* ]]
}

@test "context usage colored red at 75 percent or above" {
  HIGH_CTX_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Haiku"},"context_window":{"used_percentage":85}}'
  run bash -c "echo '$HIGH_CTX_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" == *$'\033[31m'*"ctx:"* ]]
}

# --- Session segment ---

@test "outputs session_id when session_name absent" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"abc-123"* ]]
}

@test "outputs session_name when present, not session_id" {
  NAMED_JSON='{"session_id":"abc-123","session_name":"my-work","version":"1.2.3","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus 4.6"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$NAMED_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"my-work"* ]]
  [[ "$stripped" != *"abc-123"* ]]
}

@test "session_name with shell-special characters renders verbatim (no injection)" {
  # Guards the single-jq extraction: values are shell-quoted (jq @sh) before eval, so
  # spaces, $(...), backticks and & must be preserved literally and never executed.
  SPECIAL_JSON='{"session_id":"s1","session_name":"hello $(world) & `friends`","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus 4.6"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$SPECIAL_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *'hello $(world) & `friends`'* ]]
}

@test "skips session segment when neither session_id nor session_name present" {
  NO_SESSION_JSON='{"version":"1.2.3","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus 4.6"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$NO_SESSION_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  # Output should not start with a blank segment (no leading " | ")
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != "| "* ]]
}

# --- Worktree segment ---

@test "outputs worktree name from worktree.name" {
  WT_JSON='{"session_id":"s1","version":"1.0.0","worktree":{"name":"my-feature","branch":"feature/foo"},"workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus 4.6"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$WT_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"my-feature"* ]]
}

@test "outputs worktree name from workspace.git_worktree fallback" {
  GWT_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato","git_worktree":"linked-wt"},"model":{"display_name":"Opus 4.6"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$GWT_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"linked-wt"* ]]
}

@test "skips worktree segment when absent" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  # Neither worktree.name nor workspace.git_worktree in TEST_JSON
  [[ "$stripped" != *"/tmp/potato"* ]]
}

# --- Git branch segment ---

@test "outputs branch from worktree.branch without 'git' prefix" {
  BRANCH_JSON='{"session_id":"s1","version":"1.0.0","worktree":{"name":"my-wt","branch":"feature/bar"},"workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus 4.6"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$BRANCH_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"feature/bar"* ]]
  [[ "$stripped" != *"git feature/bar"* ]]
}

@test "skips branch segment when absent" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  # TEST_JSON has no worktree.branch
  [[ "$stripped" != *"git "* ]]
}

# --- Version segment ---

@test "outputs claude version" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"1.2.3"* ]]
}

@test "skips version segment when absent" {
  NO_VER_JSON='{"session_id":"abc-123","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus 4.6"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$NO_VER_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  # Should still exit 0 and produce output (other segments present)
  [ -n "$output" ]
}

# --- Repo segment (workspace.repo) ---

@test "outputs repo owner/name from workspace.repo" {
  REPO_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato","repo":{"host":"github.com","owner":"anthropics","name":"claude-code"}},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$REPO_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"anthropics/claude-code"* ]]
}

@test "repo segment uses magenta" {
  REPO_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato","repo":{"owner":"anthropics","name":"claude-code"}},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$REPO_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" == *$'\033[35m'*"anthropics/claude-code"* ]]
}

@test "skips repo segment when workspace.repo absent" {
  # TEST_JSON has no workspace.repo and no other '/'-producing field, so a correctly
  # skipped repo segment leaves no slash in the output.
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"/"* ]]
}

@test "skips repo segment when only owner present (name empty)" {
  PARTIAL_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato","repo":{"owner":"anthropics"}},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$PARTIAL_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"anthropics"* ]]
}

# --- PR segment (pr.*) ---

@test "outputs PR number from pr.number" {
  PR_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"pr":{"number":1234,"url":"https://github.com/o/r/pull/1234","review_state":"approved"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$PR_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"PR#1234"* ]]
}

@test "PR segment green when approved" {
  PR_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"pr":{"number":1234,"review_state":"approved"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$PR_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" == *$'\033[32m'"PR#1234"* ]]
}

@test "PR segment red when changes_requested" {
  PR_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"pr":{"number":1234,"review_state":"changes_requested"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$PR_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" == *$'\033[31m'"PR#1234"* ]]
}

@test "PR segment yellow when pending" {
  PR_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"pr":{"number":1234,"review_state":"pending"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$PR_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" == *$'\033[33m'"PR#1234"* ]]
}

@test "PR segment dim when draft" {
  PR_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"pr":{"number":1234,"review_state":"draft"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$PR_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" == *$'\033[2m'"PR#1234"* ]]
}

@test "skips PR segment when pr absent" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"PR#"* ]]
}

# --- Mode-flag segments (effort / thinking / output_style / vim / agent) ---

@test "outputs effort level abbreviated (high -> hi)" {
  EFFORT_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"effort":{"level":"high"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$EFFORT_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"eff:hi"* ]]
}

@test "outputs effort level abbreviated (xhigh -> xhi)" {
  EFFORT_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"effort":{"level":"xhigh"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$EFFORT_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"eff:xhi"* ]]
}

@test "skips effort segment when absent" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"eff:"* ]]
}

@test "shows think when thinking enabled" {
  THINK_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"thinking":{"enabled":true},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$THINK_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"think"* ]]
}

@test "skips think when thinking disabled" {
  THINK_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"thinking":{"enabled":false},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$THINK_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"think"* ]]
}

@test "skips think when thinking absent" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"think"* ]]
}

@test "outputs output style name when non-default" {
  STYLE_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"output_style":{"name":"explanatory"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$STYLE_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"style:explanatory"* ]]
}

@test "skips output style when default" {
  STYLE_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"output_style":{"name":"default"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$STYLE_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"style:"* ]]
}

@test "skips output style when absent" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"style:"* ]]
}

@test "outputs vim mode abbreviated (INSERT -> I, green)" {
  VIM_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"vim":{"mode":"INSERT"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$VIM_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"vim:I"* ]]
  [[ "$output" == *$'\033[32m'"vim:I"* ]]
}

@test "outputs vim mode abbreviated (NORMAL -> N)" {
  VIM_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"vim":{"mode":"NORMAL"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$VIM_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"vim:N"* ]]
}

@test "skips vim segment when absent" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"vim:"* ]]
}

@test "outputs agent name with @ prefix" {
  AGENT_JSON='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"agent":{"name":"security-reviewer"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$AGENT_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"@security-reviewer"* ]]
}

@test "skips agent segment when absent" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"@"* ]]
}

# --- Width-aware wrapping ---
# NOTE: claude-status-line is invoked through a pipeline (echo | cmd). A plain shell
# assignment is NOT inherited by a piped command, so COLUMNS/CLAUDE_SL_RESERVE MUST be
# passed via `env` (or as a prefix on the piped command) for the wrapper to see them.

@test "wide terminal keeps everything on a single line" {
  run env COLUMNS=400 bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 1 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *" | "* ]]
}

@test "narrow terminal wraps segments onto multiple lines preserving order" {
  # COLUMNS 30 - reserve 20 = budget 10; every default segment exceeds that once joined.
  run env COLUMNS=30 CLAUDE_SL_RESERVE=20 bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -gt 1 ]
  # First row still begins with the host/container indicator (first part in order).
  first=$(strip_ansi "${lines[0]}")
  [[ "$first" == "H"* ]]
  # No segment dropped: every expected token still present somewhere.
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"abc-123"* ]]
  [[ "$stripped" == *"Opus 4.6"* ]]
  [[ "$stripped" == *"ctx:25%"* ]]
  [[ "$stripped" == *"1.2.3"* ]]
}

@test "oversized component is placed whole on its own line, not split" {
  LONG_NAME="this-is-a-very-long-session-name-far-exceeding-the-budget-xxxxxxxx"
  LONG_JSON='{"session_id":"s1","session_name":"'"$LONG_NAME"'","version":"1.2.3","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus 4.6"},"context_window":{"used_percentage":25}}'
  run env COLUMNS=30 CLAUDE_SL_RESERVE=20 bash -c "echo '$LONG_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  # The long name appears intact on exactly one line by itself (never split apart).
  found=0
  for line in "${lines[@]}"; do
    [ "$(strip_ansi "$line")" = "$LONG_NAME" ] && found=$((found + 1))
  done
  [ "$found" -eq 1 ]
}

@test "CLAUDE_SL_RESERVE override forces wrapping even on a wide terminal" {
  # budget = 400 - 399 = 1, so each segment lands on its own line.
  run env COLUMNS=400 CLAUDE_SL_RESERVE=399 bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -gt 1 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"Opus 4.6"* ]]
}

@test "non-numeric COLUMNS disables wrapping (single line)" {
  run env COLUMNS=abc bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 1 ]
}

@test "unset COLUMNS disables wrapping (single line)" {
  run env -u COLUMNS bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 1 ]
}
