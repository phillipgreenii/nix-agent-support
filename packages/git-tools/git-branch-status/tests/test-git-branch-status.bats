#!/usr/bin/env bats

# Smoke test for git-branch-status
# Tests that the script runs without errors in basic scenarios

setup() {
    if [[ -z ${SCRIPTS_DIR:-} ]]; then
        SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
    fi

    # Create a temporary git repository
    export TEST_DIR=$(mktemp -d)
    cd "$TEST_DIR"
    git init --initial-branch=main
    git config user.email "test@example.com"
    git config user.name "Test User"
    echo "test" > test.txt
    git add test.txt
    git commit -m "Initial commit"

    # Create a bare repo to act as "origin"
    mkdir -p "$TEST_DIR/origin.git"
    git -C "$TEST_DIR/origin.git" init --bare

    # Add it as a remote
    git remote add origin "$TEST_DIR/origin.git"

    # Push main to origin
    git push -u origin main 2>/dev/null || true

    # Create mock GUI tools to prevent windows from opening
    create_mock_gui_tools
}

create_mock_gui_tools() {
    # Mock fzf - return first line of input (non-interactive)
    # If no input, exit with code 1 (simulates cancellation)
    cat > "$TEST_DIR/fzf" <<'EOF'
#!/usr/bin/env bash
# Mock fzf for testing - returns first line of input or exits if no input
# Read first line with timeout, if empty exit 1 (cancelled)
read -t 1 -r first_line || exit 1
if [ -z "$first_line" ]; then
    exit 1  # Empty input (cancelled)
fi
echo "$first_line"
EOF
    chmod +x "$TEST_DIR/fzf"

    # Mock column - pass through to real column if available, otherwise just cat
    cat > "$TEST_DIR/column" <<'EOF'
#!/usr/bin/env bash
# Mock column for testing
if command -v column >/dev/null 2>&1; then
    command column "$@"
else
    cat
fi
EOF
    chmod +x "$TEST_DIR/column"

    # Add mocks to PATH
    export PATH="$TEST_DIR:$PATH"
}

teardown() {
    # Clean up temporary directory
    rm -rf "$TEST_DIR"
}

create_mock_git() {
    # Create a mock git script that handles fetch by returning success without network calls
    cat > "$TEST_DIR/git" <<'EOF'
#!/usr/bin/env bash
# Mock git for testing - intercepts fetch commands
if [[ "$1" == "fetch" ]]; then
    # Return success without doing actual network operations
    exit 0
fi
# For all other git commands, use the real git
exec command git "$@"
EOF
    chmod +x "$TEST_DIR/git"
    # Prepend TEST_DIR to PATH so our mock is found first
    export PATH="$TEST_DIR:$PATH"
}

run_git_branch_status() {
    run bash -euo pipefail "$SCRIPTS_DIR/git-branch-status.sh" "$@"
}

@test "git-branch-status runs without errors" {
    run_git_branch_status
    [ "$status" -eq 0 ]
}

@test "git-branch-status outputs branch status" {
    run_git_branch_status
    [ "$status" -eq 0 ]
    # Should output at least the main branch
    echo "$output" | grep -q "main"
}
