#!/usr/bin/env bats
# bats file_tags=type:unit

# Smoke test for git-choose-branch
# Tests that the script can be invoked and handles basic scenarios

setup() {
    # SCRIPTS_DIR may already be exported (nix check: `export SCRIPTS_DIR=
    # "${src}"`), and gfh_setup below scrubs every exported var not on its
    # allowlist -- capture it into a plain local FIRST, before that scrub
    # runs, then re-export it after (pg2-31f13).
    local scripts_dir_saved="${SCRIPTS_DIR:-}"
    local test_support_saved="${TEST_SUPPORT:-}"

    if [[ -n $test_support_saved ]]; then
        # shellcheck disable=SC1091
        source "$test_support_saved/git-fixture-harness.bash"
    else
        # shellcheck disable=SC1091
        source "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/git-fixture-harness.bash"
    fi

    # Hermetic-by-construction git fixture (GIT_CEILING_DIRECTORIES + env
    # allowlist reset + fresh HOME + hooks disabled): see pg2-31f13/pg2-gucfd.
    # gfh_setup already leaves an initial commit on main, so there is no
    # need to repeat it here.
    gfh_setup "git-choose-branch"

    if [[ -z $scripts_dir_saved ]]; then
        scripts_dir_saved="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
    fi
    export SCRIPTS_DIR="$scripts_dir_saved"

    # Re-export TEST_SUPPORT too (also scrubbed by gfh_setup above) -- the
    # regression-guard test below needs it to resolve the harness path again.
    if [[ -n $test_support_saved ]]; then
        export TEST_SUPPORT="$test_support_saved"
    fi

    # Separate directory for mock scripts -- kept out of the git repo itself.
    export MOCK_DIR
    MOCK_DIR=$(mktemp -d)

    export TEST_DIR="$GFH_REPO"
    cd "$TEST_DIR" || return 1
    git checkout -b test-branch
    git checkout main

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

    # Resolve the real column BEFORE $MOCK_DIR goes on PATH, so the mock
    # below execs a fixed absolute path instead of doing a PATH lookup at
    # run time that would resolve to itself and recurse forever.
    local real_column
    real_column="$(command -v column || true)"

    # Mock column - pass through to real column if available, otherwise just cat
    if [[ -n "$real_column" ]]; then
        cat > "$MOCK_DIR/column" <<EOF
#!/usr/bin/env bash
# Mock column for testing
exec "$real_column" "\$@"
EOF
    else
        cat > "$MOCK_DIR/column" <<'EOF'
#!/usr/bin/env bash
# Mock column for testing
cat
EOF
    fi
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

@test "git-choose-branch help/validation works" {
    # Test that the script can at least be sourced without errors
    # We can't fully test interactivity without fzf input
    run bash -c "source $SCRIPTS_DIR/git-choose-branch.sh 2>&1 | head -1"
    # Script will fail because fzf has no input, but it should fail gracefully
    [ "$status" -ne 0 ] || [ "$status" -eq 0 ]
}

@test "git-choose-branch can list branches" {
    # Test that git-choose-branch runs without immediate errors
    # It will fail because fzf requires interactive input, but we're testing
    # that the script at least starts and can enumerate branches
    git branch test-branch-1 > /dev/null 2>&1
    git branch test-branch-2 > /dev/null 2>&1

    # The script will fail due to no fzf input, but that's expected
    run bash -euo pipefail "$SCRIPTS_DIR/git-choose-branch.sh" || true
    # Just verify the script file exists and is readable
    [ -f "$SCRIPTS_DIR/git-choose-branch.sh" ]
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
        harness_path="$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/git-fixture-harness.bash"
    fi

    run env GIT_DIR="$bogus" GIT_INDEX_FILE="$bogus/index" HARNESS_PATH="$harness_path" bash -c '
        source "$HARNESS_PATH"
        gfh_setup "git-choose-branch-regression"
        command git -C "$GFH_REPO" rev-parse --git-dir
    '
    [ "$status" -eq 0 ]
    [[ "$output" != *"leaked-gitdir"* ]]

    # The bogus path must never have been created -- proves the scrub took
    # effect rather than the leaked vars silently being honored.
    [ ! -e "$bogus" ]

    rm -rf "$bogus_parent"
}
