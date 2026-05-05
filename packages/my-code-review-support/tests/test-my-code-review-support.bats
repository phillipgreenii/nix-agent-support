#!/usr/bin/env bats

# Structural validation tests for the my-code-review-support plugin content package

SHARE_DIR="${MY_CODE_REVIEW_SUPPORT_SHARE:-}"

setup() {
  if [ -z "$SHARE_DIR" ]; then
    # If not set explicitly, try to find it from the package
    if command -v my-code-review-support-cli &>/dev/null; then
      SHARE_DIR="$(dirname "$(dirname "$(command -v my-code-review-support-cli)")")/share/my-code-review-support"
    fi
  fi

  if [ -z "$SHARE_DIR" ] || [ ! -d "$SHARE_DIR" ]; then
    skip "MY_CODE_REVIEW_SUPPORT_SHARE not set and package not installed"
  fi
}

# --- Skill files ---

@test "skills/perform-draft-review-pr/SKILL.md exists" {
  [ -f "$SHARE_DIR/skills/perform-draft-review-pr/SKILL.md" ]
}

@test "skills/check-my-pr/SKILL.md exists" {
  [ -f "$SHARE_DIR/skills/check-my-pr/SKILL.md" ]
}

# --- Agent files ---

@test "agents/review-orchestrator.md exists" {
  [ -f "$SHARE_DIR/agents/review-orchestrator.md" ]
}

@test "agents/review-code-changes.md exists" {
  [ -f "$SHARE_DIR/agents/review-code-changes.md" ]
}

@test "agents/review-pr-structure.md exists" {
  [ -f "$SHARE_DIR/agents/review-pr-structure.md" ]
}

@test "agents/gather-pr-feedback.md exists" {
  [ -f "$SHARE_DIR/agents/gather-pr-feedback.md" ]
}

@test "agents/review-pr-feedback.md exists" {
  [ -f "$SHARE_DIR/agents/review-pr-feedback.md" ]
}

@test "agents/review-jira-alignment.md exists" {
  [ -f "$SHARE_DIR/agents/review-jira-alignment.md" ]
}

# --- Reference files ---

@test "references/common.md exists" {
  [ -f "$SHARE_DIR/references/common.md" ]
}

@test "references/code-review-guidelines.md exists" {
  [ -f "$SHARE_DIR/references/code-review-guidelines.md" ]
}

@test "references/pr-structure-guidelines.md exists" {
  [ -f "$SHARE_DIR/references/pr-structure-guidelines.md" ]
}

@test "references/feedback-bead-conventions.md exists" {
  [ -f "$SHARE_DIR/references/feedback-bead-conventions.md" ]
}

# --- No old name references ---

@test "agents do not reference claude-code-review" {
  ! grep -r "claude-code-review" "$SHARE_DIR/agents/"
}

@test "skills do not reference claude-code-review" {
  ! grep -r "claude-code-review" "$SHARE_DIR/skills/"
}

@test "references do not reference claude-code-review" {
  ! grep -r "claude-code-review" "$SHARE_DIR/references/"
}

@test "no .cursor references in plugin content" {
  ! grep -r "\.cursor" "$SHARE_DIR/" --include="*.md"
}

# --- Cross-reference validation ---

@test "review-orchestrator.md references common.md" {
  grep -q "references/common.md" "$SHARE_DIR/agents/review-orchestrator.md"
}

@test "review-orchestrator.md includes summary report format" {
  grep -q "Summary Report Format" "$SHARE_DIR/agents/review-orchestrator.md"
}

@test "review-code-changes.md references common.md" {
  grep -q "references/common.md" "$SHARE_DIR/agents/review-code-changes.md"
}

@test "review-code-changes.md references code-review-guidelines.md" {
  grep -q "references/code-review-guidelines.md" "$SHARE_DIR/agents/review-code-changes.md"
}

@test "review-pr-structure.md references common.md" {
  grep -q "references/common.md" "$SHARE_DIR/agents/review-pr-structure.md"
}

@test "review-pr-structure.md references pr-structure-guidelines.md" {
  grep -q "references/pr-structure-guidelines.md" "$SHARE_DIR/agents/review-pr-structure.md"
}

@test "gather-pr-feedback.md references feedback-bead-conventions.md" {
  grep -q "references/feedback-bead-conventions.md" "$SHARE_DIR/agents/gather-pr-feedback.md"
}

@test "review-pr-feedback.md references feedback-bead-conventions.md" {
  grep -q "references/feedback-bead-conventions.md" "$SHARE_DIR/agents/review-pr-feedback.md"
}

@test "common.md references my-code-review-support-cli post --help" {
  grep -q "my-code-review-support-cli post --help" "$SHARE_DIR/references/common.md"
}

# --- Skill frontmatter validation ---

@test "perform-draft-review-pr SKILL.md has name frontmatter" {
  grep -q "^name:" "$SHARE_DIR/skills/perform-draft-review-pr/SKILL.md"
}

@test "perform-draft-review-pr SKILL.md has description frontmatter" {
  grep -q "^description:" "$SHARE_DIR/skills/perform-draft-review-pr/SKILL.md"
}

@test "check-my-pr SKILL.md has name frontmatter" {
  grep -q "^name:" "$SHARE_DIR/skills/check-my-pr/SKILL.md"
}

@test "check-my-pr SKILL.md has description frontmatter" {
  grep -q "^description:" "$SHARE_DIR/skills/check-my-pr/SKILL.md"
}

# --- Agent frontmatter validation ---

@test "review-code-changes.md uses sonnet model" {
  grep -q "^model: sonnet" "$SHARE_DIR/agents/review-code-changes.md"
}

@test "review-pr-structure.md uses sonnet model" {
  grep -q "^model: sonnet" "$SHARE_DIR/agents/review-pr-structure.md"
}
