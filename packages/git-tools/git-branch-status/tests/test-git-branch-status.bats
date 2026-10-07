#!/usr/bin/env bats
# bats file_tags=type:unit

# Smoke test for git-branch-status
# Tests that the script runs without errors in basic scenarios

setup() {
    # SCRIPTS_DIR and TEST_SUPPORT may already be exported (nix check: `export
    # SCRIPTS_DIR="${src}"`), and gfh_setup scrubs every exported var not on
    # its allowlist. gfh_save_env/gfh_restore_env carry them across it.
    if [[ -n ${TEST_SUPPORT:-} ]]; then
        # shellcheck disable=SC1091
        source "$TEST_SUPPORT/git-fixture-harness.bash"
    else
        # shellcheck disable=SC1091
        source "$("$(env -u GIT_DIR -u GIT_COMMON_DIR -u GIT_WORK_TREE git -C "$BATS_TEST_DIRNAME" rev-parse --show-toplevel)/tests/support/find-gfh-dir.sh")/git-fixture-harness.bash"
    fi

    gfh_save_env SCRIPTS_DIR TEST_SUPPORT

    # Hermetic-by-construction git fixture (GIT_CEILING_DIRECTORIES + env
    # allowlist reset + fresh HOME + hooks disabled): see pg2-31f13/pg2-gucfd.
    gfh_setup "git-branch-status"

    gfh_restore_env
    if [[ -z ${SCRIPTS_DIR:-} ]]; then
        SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
    fi
    export SCRIPTS_DIR

    # Separate directory for mock scripts -- kept out of the git repo itself.
    export MOCK_DIR
    MOCK_DIR=$(mktemp -d)

    export TEST_DIR="$GFH_REPO"
    cd "$TEST_DIR" || return 1
    echo "test" > test.txt
    git add test.txt
    git commit -m "Initial commit"

    # Create a bare repo to act as "origin"
    gfh_init_bare "$GFH_WORK/origin.git"

    # Add it as a remote
    git remote add origin "$GFH_WORK/origin.git"

    # Push main to origin
    git push -u origin main 2>/dev/null || true

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

    # Add mocks to PATH
    export PATH="$MOCK_DIR:$PATH"
}

teardown() {
    # Clean up temporary directories. gfh_teardown removes GFH_ROOT, which
    # contains TEST_DIR ($GFH_REPO) -- no separate rm -rf "$TEST_DIR" needed.
    rm -rf "$MOCK_DIR"
    gfh_teardown
}

create_mock_git() {
    # Create a mock git script that handles fetch by returning success without network calls
    cat > "$MOCK_DIR/git" <<'EOF'
#!/usr/bin/env bash
# Mock git for testing - intercepts fetch commands
if [[ "$1" == "fetch" ]]; then
    # Return success without doing actual network operations
    exit 0
fi
# For all other git commands, use the real git
exec command git "$@"
EOF
    chmod +x "$MOCK_DIR/git"
    # Prepend MOCK_DIR to PATH so our mock is found first
    export PATH="$MOCK_DIR:$PATH"
}

# Forwards optional arguments to the script under test; callers may pass none.
# shellcheck disable=SC2120
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

@test "regression: a GIT_DIR/GIT_INDEX_FILE leaked into the parent shell before setup is scrubbed, not honored" {
    # Simulates the pg2-67h4y hook-environment leak: GIT_DIR/GIT_INDEX_FILE
    # pointed at a bogus path BEFORE the harness's own setup runs. If the
    # scrub (gfh_reset_env, called by gfh_setup) did not take effect, git
    # would try to operate against/create the bogus path instead of the
    # fixture's own repo.
    local bogus_parent bogus harness_path
    bogus_parent="$(mktemp -d)"
    bogus="$bogus_parent/leaked-gitdir"
    if [[ -n ${TEST_SUPPORT:-} ]]; then
        harness_path="$TEST_SUPPORT/git-fixture-harness.bash"
    else
        harness_path="$("$(env -u GIT_DIR -u GIT_COMMON_DIR -u GIT_WORK_TREE git -C "$BATS_TEST_DIRNAME" rev-parse --show-toplevel)/tests/support/find-gfh-dir.sh")/git-fixture-harness.bash"
    fi

    run env GIT_DIR="$bogus" GIT_INDEX_FILE="$bogus/index" HARNESS_PATH="$harness_path" bash -c '
        source "$HARNESS_PATH"
        gfh_setup "git-branch-status-regression"
        command git -C "$GFH_REPO" rev-parse --git-dir
    '
    [ "$status" -eq 0 ]
    [[ "$output" != *"leaked-gitdir"* ]]

    # The bogus path must never have been created -- proves the scrub took
    # effect rather than the leaked vars silently being honored.
    [ ! -e "$bogus" ]

    rm -rf "$bogus_parent"
}
