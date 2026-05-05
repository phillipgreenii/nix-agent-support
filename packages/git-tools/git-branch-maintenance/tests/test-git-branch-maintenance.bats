#!/usr/bin/env bats

# Unit tests for git-branch-maintenance
# Tests key functionality without modifying the real repository

setup() {
    if [[ -z ${SCRIPTS_DIR:-} ]]; then
        SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
    fi

    # Create a separate directory for mock scripts (not in the git repo)
    export MOCK_DIR=$(mktemp -d)

    # Create a temporary git repository
    export TEST_DIR=$(mktemp -d)
    cd "$TEST_DIR"
    git init --initial-branch=main
    git config user.email "test@example.com"
    git config user.name "Test User"

    # Create .gitignore to ignore external directories that might be created
    # (e.g., .cursor/ from Cursor IDE, CLAUDE.md symlinks, or other system files)
    echo ".cursor/" > .gitignore
    echo ".DS_Store" >> .gitignore
    echo "CLAUDE.md" >> .gitignore
    git add .gitignore
    git commit -m "Add .gitignore"

    # Create initial commit on main
    echo "test" > test.txt
    git add test.txt
    git commit -m "Initial commit"

    # Create a test branch
    git checkout -b test-branch
    echo "more" >> test.txt
    git commit -am "Test commit"
    git checkout main

    # Create dummy origin/main for tests
    mkdir -p .git/refs/remotes/origin
    git update-ref refs/remotes/origin/main main

    # Create mock GUI tools to prevent windows from opening
    create_mock_gui_tools
}

create_mock_gui_tools() {
    # Mock fzf - return first line of input (non-interactive)
    # If no input, exit with code 1 (simulates cancellation)
    cat > "$MOCK_DIR/fzf" <<'EOF'
#!/usr/bin/env bash
# Mock fzf for testing - returns first line of input or exits if no input
# Read first line with timeout, if empty exit 1 (cancelled)
read -t 1 -r first_line || exit 1
if [ -z "$first_line" ]; then
    exit 1  # Empty input (cancelled)
fi
echo "$first_line"
EOF
    chmod +x "$MOCK_DIR/fzf"

    # Mock column - pass through to real column if available, otherwise just cat
    cat > "$MOCK_DIR/column" <<'EOF'
#!/usr/bin/env bash
# Mock column for testing
if command -v column >/dev/null 2>&1; then
    command column "$@"
else
    cat
fi
EOF
    chmod +x "$MOCK_DIR/column"

    # Find the real git before adding our mock to PATH
    local real_git
    real_git=$(command -v git)

    # Mock git for testing - intercepts fetch commands to avoid network calls
    cat > "$MOCK_DIR/git" <<EOF
#!/usr/bin/env bash
# Mock git for testing - intercepts fetch commands
if [[ "\$1" == "fetch" ]]; then
    # Return success without doing actual network operations
    exit 0
fi
# For all other git commands, use the real git
exec "$real_git" "\$@"
EOF
    chmod +x "$MOCK_DIR/git"

    # Add mocks to PATH
    export PATH="$MOCK_DIR:$PATH"
}

teardown() {
    # Clean up temporary directories
    rm -rf "$TEST_DIR"
    rm -rf "$MOCK_DIR"
}

run_git_branch_maintenance() {
    run bash -euo pipefail "$SCRIPTS_DIR/git-branch-maintenance.sh" "$@"
}

@test "git-branch-maintenance --help shows usage" {
    run_git_branch_maintenance --help
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage:"
}

@test "git-branch-maintenance shows status without operations" {
    run_git_branch_maintenance
    [ "$status" -eq 0 ]
    # Should show status of test-branch
    echo "$output" | grep -q "test-branch"
}

@test "git-branch-maintenance --dry-run doesn't modify branches" {
    # Get initial branch list
    initial_branches=$(git branch | wc -l)

    run_git_branch_maintenance --dry-run --delete-merged
    [ "$status" -eq 0 ]

    # Verify branches weren't modified
    final_branches=$(git branch | wc -l)
    [ "$initial_branches" -eq "$final_branches" ]
}

@test "git-branch-maintenance validates working directory" {
    # Create uncommitted changes
    echo "dirty" > dirty.txt

    run_git_branch_maintenance --ff
    # Should fail due to uncommitted changes
    [ "$status" -ne 0 ]
    echo "$output" | grep -q -i "uncommitted"
}

@test "git-branch-maintenance --force skips validation" {
    # Create uncommitted changes
    echo "dirty" > dirty.txt

    run_git_branch_maintenance --force --dry-run
    # Should succeed with --force
    [ "$status" -eq 0 ]
}

@test "git-branch-maintenance handles specific branches" {
    run_git_branch_maintenance test-branch
    [ "$status" -eq 0 ]
    # Should only show status of test-branch
    echo "$output" | grep -q "test-branch"
}

@test "git-branch-maintenance reads protected branches from git config" {
    # Set up git config for protected branches
    git config --local git-branch-maintenance.protectedBranch "custom-branch"

    # Create and merge a branch that matches the protected name
    git checkout -b custom-branch
    echo "custom" >> test.txt
    git commit -am "Custom commit"
    git checkout main
    git merge custom-branch

    # Try to delete it - should be protected
    run_git_branch_maintenance --delete-merged
    [ "$status" -eq 0 ]
    # Branch should still exist
    git branch | grep -q "custom-branch"
}

@test "git-branch-maintenance accepts --protect-branch flag" {
    # Create and merge a test branch
    git checkout -b feature-branch
    echo "feature" >> test.txt
    git commit -am "Feature commit"
    git checkout main
    git merge feature-branch

    # Try to delete with protection flag
    run_git_branch_maintenance --delete-merged --protect-branch feature-branch
    [ "$status" -eq 0 ]
    # Branch should still exist (protected)
    git branch | grep -q "feature-branch"
}

@test "git-branch-maintenance accepts --protect-worktree flag" {
    # Create a worktree
    mkdir -p /tmp/test-worktree-$$
    git worktree add /tmp/test-worktree-$$ test-branch

    # Merge the branch
    git checkout main
    git merge test-branch

    # Try to delete with worktree protection
    run_git_branch_maintenance --delete-merged --delete-merged-worktrees --protect-worktree /tmp/test-worktree-$$
    [ "$status" -eq 0 ]
    # Worktree should still exist
    [ -d /tmp/test-worktree-$$ ]

    # Cleanup
    git worktree remove /tmp/test-worktree-$$ --force
}

@test "git-branch-maintenance --protect-branch requires argument" {
    run_git_branch_maintenance --protect-branch
    [ "$status" -ne 0 ]
    echo "$output" | grep -q "requires a branch name"
}

@test "git-branch-maintenance --protect-worktree requires argument" {
    run_git_branch_maintenance --protect-worktree
    [ "$status" -ne 0 ]
    echo "$output" | grep -q "requires a path"
}

@test "git-branch-maintenance combines git config and CLI flags" {
    # Set up git config
    git config --local git-branch-maintenance.protectedBranch "config-branch"

    # Create two branches
    git checkout -b config-branch
    echo "config" >> test.txt
    git commit -am "Config commit"
    git checkout main
    git merge config-branch

    git checkout -b cli-branch
    echo "cli" >> test.txt
    git commit -am "CLI commit"
    git checkout main
    git merge cli-branch

    # Try to delete with additional CLI protection
    run_git_branch_maintenance --delete-merged --protect-branch cli-branch
    [ "$status" -eq 0 ]
    # Both branches should still exist
    git branch | grep -q "config-branch"
    git branch | grep -q "cli-branch"
}

@test "git-branch-maintenance cleans up leftover temporary worktree" {
    # Create a leftover temporary worktree from a "previous run"
    git branch tmp-gbm main
    mkdir -p /tmp/test-leftover-worktree-$$
    git worktree add /tmp/test-leftover-worktree-$$ tmp-gbm

    # Run git-branch-maintenance - should clean up the leftover worktree
    run_git_branch_maintenance --dry-run
    [ "$status" -eq 0 ]

    # The leftover worktree should have been cleaned up
    ! git worktree list | grep -q "/tmp/test-leftover-worktree-$$"
}
