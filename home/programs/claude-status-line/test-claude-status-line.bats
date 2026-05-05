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
