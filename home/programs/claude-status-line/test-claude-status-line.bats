#!/usr/bin/env bats

# This suite runs against BOTH build variants of the wrapper:
#   - nerd-font OFF (test-claude-status-line):        CLAUDE_SL_TEST_NERD_FONT unset -> text fallbacks
#   - nerd-font ON  (test-claude-status-line-nerdfont): CLAUDE_SL_TEST_NERD_FONT=1  -> MDI glyphs
# Mode-specific assertions are guarded with nerd_on / nerd_off. Mode-agnostic behavior
# (session, branch fallback, wrapping, width, injection) is asserted unconditionally.

# JSON blob representing a typical Claude Code status line call
TEST_JSON='{"session_id":"abc-123","version":"1.2.3","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus 4.6"},"context_window":{"used_percentage":25}}'

# MDI glyph literals, generated to match scripts.nix. The wrapper emits plane-15 glyphs as
# raw 4-byte UTF-8 sequences (locale-independent). bats may run under a non-UTF-8 locale
# (the nix build sandbox has NO usable locale), where printf '\U...' emits nothing, so we
# compute the same UTF-8 bytes arithmetically here to match byte-for-byte.
_glyph() {
  local cp=$1 esc
  # Build the escape STRING first (\xNN...), then let printf interpret those escapes. A
  # single-pass `printf '\x%02x' N` does NOT work: printf will not re-interpret the hex
  # digits it just substituted.
  esc=$(printf '\\x%02x\\x%02x\\x%02x\\x%02x' \
    $((240 + cp / 262144)) \
    $((128 + (cp / 4096) - (cp / 262144) * 64)) \
    $((128 + (cp / 64) - (cp / 4096) * 64)) \
    $((128 + cp - (cp / 64) * 64)))
  printf '%b' "$esc"
}
GLYPH_REPO=$(_glyph 0xF02A2)
GLYPH_WORKTREE=$(_glyph 0xF024B)
GLYPH_BRANCH=$(_glyph 0xF062C)
GLYPH_THINKING=$(_glyph 0xF0493)
GLYPH_CTX=$(_glyph 0xF035B)
GLYPH_ALERT=$(_glyph 0xF0028)
GLYPH_5H=$(_glyph 0xF0150)
GLYPH_7D=$(_glyph 0xF0679)
# circle-slice fill U+F0A9E .. U+F0AA5 (8 levels)
GLYPH_SLICE1=$(_glyph 0xF0A9E)
GLYPH_SLICE3=$(_glyph 0xF0AA0)
GLYPH_SLICE8=$(_glyph 0xF0AA5)

setup() {
  TEST_DIR=$(mktemp -d)
}

teardown() {
  [ -n "$TEST_DIR" ] && rm -rf "$TEST_DIR"
}

nerd_on() { [ -n "${CLAUDE_SL_TEST_NERD_FONT:-}" ]; }
nerd_off() { [ -z "${CLAUDE_SL_TEST_NERD_FONT:-}" ]; }

# Strip ANSI escape sequences so assertions are readable
strip_ansi() {
  printf '%s' "$1" | sed 's/\x1B\[[0-9;]*m//g'
}

# =====================================================================================
# Model segment (display_name + effort + thinking folded into one segment)
# =====================================================================================

@test "outputs full model display_name" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"Opus 4.6"* ]]
}

@test "model display_name is cyan" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" == *$'\033[36m'*"Opus 4.6"* ]]
}

@test "model folds effort abbreviation in parens (high -> hi)" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"effort":{"level":"high"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"Opus (hi)"* ]]
}

@test "model folds effort abbreviation in parens (xhigh -> xhi)" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"effort":{"level":"xhigh"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"(xhi)"* ]]
}

@test "model omits effort parens when effort absent" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"("* ]]
}

@test "model shows thinking indicator when thinking enabled" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"thinking":{"enabled":true},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  if nerd_on; then
    [[ "$stripped" == *"$GLYPH_THINKING"* ]]
  else
    [[ "$stripped" == *"[thinking]"* ]]
  fi
}

@test "model omits thinking indicator when thinking disabled" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"thinking":{"enabled":false},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"[thinking]"* ]]
  nerd_off || [[ "$stripped" != *"$GLYPH_THINKING"* ]]
}

@test "model omits thinking indicator when thinking absent" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"[thinking]"* ]]
  nerd_off || [[ "$stripped" != *"$GLYPH_THINKING"* ]]
}

# =====================================================================================
# Context segment (simple colored %, + 200k alert)
# =====================================================================================

@test "outputs context usage percentage number" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"25%"* ]]
}

@test "context uses ctx marker (glyph on / text off)" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  if nerd_on; then
    [[ "$stripped" == *"$GLYPH_CTX"* ]]
  else
    [[ "$stripped" == *"ctx:"* ]]
  fi
}

@test "context colored green below 60 percent" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Haiku"},"context_window":{"used_percentage":20}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" == *$'\033[32m'*"20%"* ]]
}

@test "context colored yellow between 60 and 74 percent" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Haiku"},"context_window":{"used_percentage":65}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" == *$'\033[33m'*"65%"* ]]
}

@test "context colored red at 75 percent or above" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Haiku"},"context_window":{"used_percentage":85}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" == *$'\033[31m'*"85%"* ]]
}

@test "context appends red alert when exceeds_200k_tokens true" {
  J='{"session_id":"s1","version":"1.0.0","exceeds_200k_tokens":true,"workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":30}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  if nerd_on; then
    [[ "$stripped" == *"$GLYPH_ALERT"* ]]
  else
    [[ "$stripped" == *"(!)"* ]]
  fi
  # alert is red
  [[ "$output" == *$'\033[31m'* ]]
}

@test "context omits alert when exceeds_200k_tokens false" {
  J='{"session_id":"s1","version":"1.0.0","exceeds_200k_tokens":false,"workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":30}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"(!)"* ]]
  nerd_off || [[ "$stripped" != *"$GLYPH_ALERT"* ]]
}

@test "context omits alert when exceeds_200k_tokens absent" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"(!)"* ]]
  nerd_off || [[ "$stripped" != *"$GLYPH_ALERT"* ]]
}

@test "context segment hidden when used_percentage absent" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus"},"context_window":{}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"%"* ]]
  nerd_off || [[ "$stripped" != *"$GLYPH_CTX"* ]]
}

# =====================================================================================
# Session segments (name is its own segment; id is ALWAYS its own segment)
# =====================================================================================

@test "outputs session_id" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"abc-123"* ]]
}

@test "outputs BOTH session_name and session_id when name present" {
  J='{"session_id":"abc-123","session_name":"my-work","version":"1.2.3","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus 4.6"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"my-work"* ]]
  [[ "$stripped" == *"abc-123"* ]]
}

@test "session_name is bold" {
  J='{"session_id":"abc-123","session_name":"my-work","version":"1.2.3","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus 4.6"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" == *$'\033[1m'*"my-work"* ]]
}

@test "session_name with shell-special characters renders verbatim (no injection)" {
  J='{"session_id":"s1","session_name":"hello $(world) & `friends`","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus 4.6"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *'hello $(world) & `friends`'* ]]
}

@test "session id segment hidden when session_id absent" {
  J='{"version":"1.2.3","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus 4.6"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != "| "* ]]
}

# =====================================================================================
# Location segment (repo + worktree + branch combined, space-separated)
# =====================================================================================

@test "location combines repo, worktree, branch in one segment" {
  J='{"session_id":"s1","version":"1.0.0","worktree":{"name":"my-feature","branch":"feature/foo"},"workspace":{"current_dir":"/tmp/potato","repo":{"owner":"anthropics","name":"claude-code"}},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  # all three appear, and they are NOT separated by ' | ' (same segment)
  [[ "$stripped" == *"anthropics/claude-code"* ]]
  [[ "$stripped" == *"my-feature"* ]]
  [[ "$stripped" == *"feature/foo"* ]]
  [[ "$stripped" != *"anthropics/claude-code | "* ]]
  [[ "$stripped" != *"my-feature | feature/foo"* ]]
}

@test "location repo is dim" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato","repo":{"owner":"anthropics","name":"claude-code"}},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" == *$'\033[2m'*"anthropics/claude-code"* ]]
}

@test "location repo uses repo glyph when nerd on" {
  nerd_on || skip "nerd-font off"
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato","repo":{"owner":"anthropics","name":"claude-code"}},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"$GLYPH_REPO"* ]]
}

@test "location worktree is bold yellow" {
  J='{"session_id":"s1","version":"1.0.0","worktree":{"name":"my-feature"},"workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" == *$'\033[1m'*$'\033[33m'*"my-feature"* || "$output" == *$'\033[33m'*$'\033[1m'*"my-feature"* ]]
  if nerd_on; then
    stripped=$(strip_ansi "$output")
    [[ "$stripped" == *"$GLYPH_WORKTREE"* ]]
  fi
}

@test "location worktree comes ONLY from worktree.name (no git_worktree fallback)" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato","git_worktree":"linked-wt"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"linked-wt"* ]]
}

@test "location branch is green" {
  J='{"session_id":"s1","version":"1.0.0","worktree":{"branch":"feature/bar"},"workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" == *$'\033[32m'*"feature/bar"* ]]
  if nerd_on; then
    stripped=$(strip_ansi "$output")
    [[ "$stripped" == *"$GLYPH_BRANCH"* ]]
  fi
}

@test "location branch has no 'git' prefix" {
  J='{"session_id":"s1","version":"1.0.0","worktree":{"name":"my-wt","branch":"feature/bar"},"workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"feature/bar"* ]]
  [[ "$stripped" != *"git feature/bar"* ]]
}

@test "location segment hidden entirely when repo, worktree and branch all absent" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  # TEST_JSON has no repo, no worktree, no branch (and /tmp/potato is not a git dir)
  [[ "$stripped" != *"/"* ]]
}

@test "location shows only present sub-parts (repo alone)" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato","repo":{"owner":"anthropics","name":"claude-code"}},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"anthropics/claude-code"* ]]
}

@test "location skips repo when only owner present (name empty)" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato","repo":{"owner":"anthropics"}},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"anthropics"* ]]
}

# --- Branch fallback (.git/HEAD, outside a worktree session) ---

@test "branch fallback reads .git/HEAD when worktree.branch absent" {
  mkdir -p "$TEST_DIR/.git"
  printf 'ref: refs/heads/feature/login\n' > "$TEST_DIR/.git/HEAD"
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"'"$TEST_DIR"'"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"feature/login"* ]]
}

@test "branch fallback walks up from a subdirectory to find .git" {
  mkdir -p "$TEST_DIR/.git" "$TEST_DIR/sub/deep"
  printf 'ref: refs/heads/main-line\n' > "$TEST_DIR/.git/HEAD"
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"'"$TEST_DIR/sub/deep"'"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"main-line"* ]]
}

@test "worktree.branch takes precedence over .git/HEAD fallback" {
  mkdir -p "$TEST_DIR/.git"
  printf 'ref: refs/heads/from-head\n' > "$TEST_DIR/.git/HEAD"
  J='{"session_id":"s1","version":"1.0.0","worktree":{"branch":"from-json"},"workspace":{"current_dir":"'"$TEST_DIR"'"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"from-json"* ]]
  [[ "$stripped" != *"from-head"* ]]
}

@test "branch fallback skips on detached HEAD (no ref)" {
  mkdir -p "$TEST_DIR/.git"
  printf '0123456789abcdef0123456789abcdef01234567\n' > "$TEST_DIR/.git/HEAD"
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"'"$TEST_DIR"'"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"0123456789"* ]]
}

@test "branch fallback follows a .git file (gitdir indirection)" {
  mkdir -p "$TEST_DIR/realgit" "$TEST_DIR/checkout"
  printf 'ref: refs/heads/linked-branch\n' > "$TEST_DIR/realgit/HEAD"
  printf 'gitdir: %s\n' "$TEST_DIR/realgit" > "$TEST_DIR/checkout/.git"
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"'"$TEST_DIR/checkout"'"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"linked-branch"* ]]
}

# =====================================================================================
# Vim segment (FIRST)
# =====================================================================================

@test "vim segment is first when present (INSERT green)" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"vim":{"mode":"INSERT"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run env COLUMNS=400 bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == "vim:I"* ]]
  [[ "$output" == *$'\033[32m'"vim:I"* ]]
}

@test "vim NORMAL is cyan" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"vim":{"mode":"NORMAL"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"vim:N"* ]]
  [[ "$output" == *$'\033[36m'"vim:N"* ]]
}

@test "vim VISUAL is yellow" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"vim":{"mode":"VISUAL"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"vim:V"* ]]
  [[ "$output" == *$'\033[33m'"vim:V"* ]]
}

@test "skips vim segment when absent" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"vim:"* ]]
}

# =====================================================================================
# Agent segment (after model, before context)
# =====================================================================================

@test "outputs agent name with @ prefix" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"agent":{"name":"security-reviewer"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"@security-reviewer"* ]]
}

@test "agent appears after model and before context" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"agent":{"name":"secrev"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run env COLUMNS=400 bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  # Opus ... @secrev ... 25%
  [[ "$stripped" == *"Opus"*"@secrev"*"25%"* ]]
}

@test "skips agent segment when absent" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"@"* ]]
}

# =====================================================================================
# Version segment (END, dim)
# =====================================================================================

@test "outputs claude version" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"1.2.3"* ]]
}

@test "version is dim" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" == *$'\033[2m'*"1.2.3"* ]]
}

@test "version is the last segment" {
  run env COLUMNS=400 bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  # 1.2.3 must be after everything else
  [[ "$stripped" == *"25%"*"1.2.3" || "$stripped" == *"1.2.3" ]]
}

@test "skips version segment when absent" {
  J='{"session_id":"abc-123","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus 4.6"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  [ -n "$output" ]
}

# =====================================================================================
# LIMITS segment (5h + 7d combined)
# =====================================================================================

@test "limits segment hidden when rate_limits absent" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"5h"* ]]
  [[ "$stripped" != *"7d"* ]]
  nerd_off || { [[ "$stripped" != *"$GLYPH_5H"* ]] && [[ "$stripped" != *"$GLYPH_7D"* ]]; }
}

@test "limits combines 5h and 7d in one segment" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25},"rate_limits":{"five_hour":{"used_percentage":30},"seven_day":{"used_percentage":40}}}'
  run env COLUMNS=400 bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  if nerd_on; then
    [[ "$stripped" == *"$GLYPH_5H"* ]]
    [[ "$stripped" == *"$GLYPH_7D"* ]]
    # same segment: 5h and 7d not joined by ' | '
    [[ "$stripped" == *"$GLYPH_5H"*"$GLYPH_7D"* ]]
  else
    [[ "$stripped" == *"5h:"* ]]
    [[ "$stripped" == *"7d:"* ]]
    [[ "$stripped" == *"5h:"*"7d:"* ]]
  fi
}

@test "limits nerd-off shows numeric percentage below 80" {
  nerd_off || skip "nerd-font on"
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25},"rate_limits":{"five_hour":{"used_percentage":30},"seven_day":{"used_percentage":40}}}'
  run env COLUMNS=400 bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"5h:30%"* ]]
  [[ "$stripped" == *"7d:40%"* ]]
}

@test "limits nerd-on shows circle-slice fill glyph (no number) below 80" {
  nerd_on || skip "nerd-font off"
  # 5% -> idx=1 -> slice-1; 75% -> idx=8 -> slice-8. Omit context_window so the ONLY possible
  # '%' source is the limits sub-parts; a glyph render must therefore leave no '%' at all.
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus"},"rate_limits":{"five_hour":{"used_percentage":5},"seven_day":{"used_percentage":75}}}'
  run env COLUMNS=400 bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  # 5% -> slice1
  [[ "$stripped" == *"$GLYPH_SLICE1"* ]]
  # 75% -> slice8
  [[ "$stripped" == *"$GLYPH_SLICE8"* ]]
  # glyph render shows NO numeric percentage for these sub-80 values
  [[ "$stripped" != *"%"* ]]
}

@test "limits circle-slice idx maps 25% -> slice3" {
  nerd_on || skip "nerd-font off"
  # 25% -> floor(25/10)+1 = 3 -> slice3
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25},"rate_limits":{"five_hour":{"used_percentage":25}}}'
  run env COLUMNS=400 bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"$GLYPH_SLICE3"* ]]
}

@test "limits default color when at-or-under pace (no yellow)" {
  # used 10% with resets_at far in the future so pace (ptb) is near 0; ptb ~0, used 10 > 0 => yellow?
  # Use a case where used <= ptb: nearly-elapsed block. resets_at = now + 100s of a 18000s block =>
  # ptb = (18000-100)/18000*100 ~= 99. used 10 < 99 => under pace => default color.
  now=$EPOCHSECONDS
  reset=$((now + 100))
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25},"rate_limits":{"five_hour":{"used_percentage":10,"resets_at":'"$reset"'}}}'
  run env COLUMNS=400 bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  # under pace -> the 5h sub-part is not wrapped in YELLOW
  # find the portion; simplest: no yellow SGR immediately around the 5h marker region.
  # Assert the whole limits value is not yellow: there should be no \033[33m in output for a low-usage under-pace case with no other yellow producer.
  [[ "$output" != *$'\033[33m'* ]]
}

@test "limits yellow when burning faster than pace (over pace)" {
  # used 50%, block barely started: resets_at = now + 17900 of 18000 => ptb ~= 0. floor(50) > 0 => yellow.
  now=$EPOCHSECONDS
  reset=$((now + 17900))
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25},"rate_limits":{"five_hour":{"used_percentage":50,"resets_at":'"$reset"'}}}'
  run env COLUMNS=400 bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" == *$'\033[33m'* ]]
}

@test "limits never yellow when resets_at missing (below 80)" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25},"rate_limits":{"five_hour":{"used_percentage":50}}}'
  run env COLUMNS=400 bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" != *$'\033[33m'* ]]
}

@test "limits red number + countdown when >= 80 (5h Hh Mm)" {
  # 90%, resets in ~2h 5m. Pad 30s past the 2h5m boundary so a few seconds of render
  # latency still truncates to "2h 5m" (2h5m30s = 7530s).
  now=$EPOCHSECONDS
  reset=$((now + 7530))
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25},"rate_limits":{"five_hour":{"used_percentage":90,"resets_at":'"$reset"'}}}'
  run env COLUMNS=400 bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"90%"* ]]
  # countdown 2h 5m
  [[ "$stripped" == *"2h 5m"* ]]
  # red
  [[ "$output" == *$'\033[31m'* ]]
}

@test "limits red number without countdown when >= 80 and resets_at missing" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25},"rate_limits":{"five_hour":{"used_percentage":90}}}'
  run env COLUMNS=400 bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"90%"* ]]
  # no parenthesized countdown
  [[ "$stripped" != *"("*"m)"* ]]
}

@test "limits 7d red countdown uses Dd Hh format" {
  # 85%, resets in ~2d 3h. Pad 30m past the 2d3h boundary so a few seconds of render
  # latency still truncates to "2d 3h" (2d3h30m = 185400s).
  now=$EPOCHSECONDS
  reset=$((now + 185400))
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25},"rate_limits":{"seven_day":{"used_percentage":85,"resets_at":'"$reset"'}}}'
  run env COLUMNS=400 bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"85%"* ]]
  [[ "$stripped" == *"2d 3h"* ]]
}

@test "limits shows only the sub-part whose used_percentage is present" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25},"rate_limits":{"seven_day":{"used_percentage":40}}}'
  run env COLUMNS=400 bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  if nerd_on; then
    [[ "$stripped" == *"$GLYPH_7D"* ]]
    [[ "$stripped" != *"$GLYPH_5H"* ]]
  else
    [[ "$stripped" == *"7d:"* ]]
    [[ "$stripped" != *"5h:"* ]]
  fi
}

# =====================================================================================
# Removed segments
# =====================================================================================

@test "no env H/C indicator segment" {
  run bash -c "unset CONTAINED_CLAUDE; echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != "H |"* ]]
  [[ "$stripped" != "C |"* ]]
  [[ "$stripped" != "H"* ]]
}

@test "no output_style segment" {
  J='{"session_id":"s1","version":"1.0.0","workspace":{"current_dir":"/tmp/potato"},"output_style":{"name":"explanatory"},"model":{"display_name":"Opus"},"context_window":{"used_percentage":25}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" != *"style:"* ]]
  [[ "$stripped" != *"explanatory"* ]]
}

# =====================================================================================
# General wrapper behavior
# =====================================================================================

@test "segments are joined with ' | ' separator" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *" | "* ]]
}

@test "exits 0 even when a part produces no output" {
  J='{"workspace":{"current_dir":"/tmp/potato"},"model":{},"context_window":{}}'
  run bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
}

@test "output contains no literal null values" {
  run bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  [[ "$output" != *"null"* ]]
}

# =====================================================================================
# Width-aware wrapping (mode-agnostic)
# =====================================================================================

@test "wide terminal keeps everything on a single line" {
  run env COLUMNS=400 bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 1 ]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *" | "* ]]
}

@test "narrow terminal wraps segments onto multiple lines preserving order" {
  run env COLUMNS=30 CLAUDE_SL_RESERVE=20 bash -c "echo '$TEST_JSON' | claude-status-line"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -gt 1 ]
  # First segment present (session id is first-present in TEST_JSON).
  first=$(strip_ansi "${lines[0]}")
  [[ "$first" == "abc-123"* ]]
  stripped=$(strip_ansi "$output")
  [[ "$stripped" == *"abc-123"* ]]
  [[ "$stripped" == *"Opus 4.6"* ]]
  [[ "$stripped" == *"25%"* ]]
  [[ "$stripped" == *"1.2.3"* ]]
}

@test "oversized component is placed whole on its own line, not split" {
  LONG_NAME="this-is-a-very-long-session-name-far-exceeding-the-budget-xxxxxxxx"
  J='{"session_id":"s1","session_name":"'"$LONG_NAME"'","version":"1.2.3","workspace":{"current_dir":"/tmp/potato"},"model":{"display_name":"Opus 4.6"},"context_window":{"used_percentage":25}}'
  run env COLUMNS=30 CLAUDE_SL_RESERVE=20 bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  found=0
  for line in "${lines[@]}"; do
    [ "$(strip_ansi "$line")" = "$LONG_NAME" ] && found=$((found + 1))
  done
  [ "$found" -eq 1 ]
}

@test "CLAUDE_SL_RESERVE override forces wrapping even on a wide terminal" {
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

# =====================================================================================
# B1 locale-safe width: a glyph-bearing segment must measure width 1 (no over-wrapping)
# even under a non-UTF-8 active locale (env LC_ALL=C).
# =====================================================================================

@test "glyph-bearing segment measures width 1 under LC_ALL=C (no over-wrapping)" {
  nerd_on || skip "nerd-font off (no glyph to measure)"
  # A repo location segment carries one glyph. Under a naive C-locale width count the glyph
  # would measure 10 chars and force a wrap. With the wrapper forcing UTF-8 for the width
  # math, budget is big enough to keep it on one line together with a short neighbor.
  J='{"session_id":"x","workspace":{"current_dir":"/tmp/potato","repo":{"owner":"o","name":"r"}},"model":{"display_name":"M"}}'
  # budget: COLUMNS 40 - reserve 20 = 20. Segments (stripped, UTF-8): "x"(1), glyph+" o/r"(1+4=5), "M"(1).
  # Row1 packs "x | <glyph> o/r | M" = 1+3+5+3+1 = 13 <= 20 -> single line IF glyph counts as 1.
  # Under a C-locale miscount the glyph is 10 => 5 becomes 14 => 1+3+14+3+1=22 > 20 => wraps.
  run env LC_ALL=C COLUMNS=40 CLAUDE_SL_RESERVE=20 bash -c "echo '$J' | claude-status-line"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 1 ]
}
