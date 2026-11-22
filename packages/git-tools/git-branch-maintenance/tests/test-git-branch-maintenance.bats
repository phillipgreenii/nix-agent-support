#!/usr/bin/env bats
# bats file_tags=type:unit

# Unit tests for git-branch-maintenance
# Tests key functionality without modifying the real repository

setup() {
    # SCRIPTS_DIR and TEST_SUPPORT may already be exported (nix check: `export
    # SCRIPTS_DIR="${src}"`), and gfh_setup scrubs every exported var not on
    # its allowlist. gfh_save_env/gfh_restore_env carry them across it.
    # GFH_LIB is the one required file (tc-4ehow); nix package checks
    # export it.
    local gfh_lib="${GFH_LIB:-}"
    if [[ -z $gfh_lib ]]; then
      echo "GFH_LIB is not set: commit via the run-unit-tests hook, run under the workspace .envrc (direnv, or direnv exec <workspace-root> ...), or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
      return 1
    fi
    # shellcheck disable=SC1090,SC1091
    source "$gfh_lib"

    gfh_save_env SCRIPTS_DIR TEST_SUPPORT GFH_LIB

    # Hermetic-by-construction git fixture (GIT_CEILING_DIRECTORIES + env
    # allowlist reset + fresh HOME + hooks disabled): see pg2-31f13/pg2-gucfd.
    # This replaces the old bare `git init`/`git config` in a plain mktemp
    # dir, which had no protection at all against a leaked GIT_DIR family env
    # var from a linked-worktree commit hook (pg2-67h4y).
    gfh_setup "git-branch-maintenance"

    gfh_restore_env
    if [[ -z ${SCRIPTS_DIR:-} ]]; then
        SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
    fi
    export SCRIPTS_DIR

    # Create a separate directory for mock scripts (not in the git repo)
    export MOCK_DIR
    MOCK_DIR=$(mktemp -d)

    export TEST_DIR="$GFH_REPO"
    cd "$TEST_DIR" || return 1

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

    # Find the real git before adding our mock to PATH. Exported so tests
    # that need to install their own git mock later (after $MOCK_DIR/git is
    # already on PATH) can still reach the real binary instead of
    # recursively re-invoking whatever mock is currently active.
    export REAL_GIT
    REAL_GIT=$(command -v git)

    # Mock git for testing - intercepts fetch commands to avoid network calls
    cat > "$MOCK_DIR/git" <<EOF
#!/usr/bin/env bash
# Mock git for testing - intercepts fetch commands
if [[ "\$1" == "fetch" ]]; then
    # Return success without doing actual network operations
    exit 0
fi
# For all other git commands, use the real git
exec "$REAL_GIT" "\$@"
EOF
    chmod +x "$MOCK_DIR/git"

    # Add mocks to PATH
    export PATH="$MOCK_DIR:$PATH"
}

teardown() {
    # Clean up temporary directories. gfh_teardown removes GFH_ROOT, which
    # contains TEST_DIR ($GFH_REPO) -- no separate rm -rf "$TEST_DIR" needed.
    rm -rf "$MOCK_DIR"
    gfh_teardown
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
    # Create a worktree under the fixture's work dir ($GFH_WORK is the
    # GIT_CEILING_DIRECTORIES boundary), in its physical form so the literal
    # path matches what git registers.
    local wt
    wt="$(cd "$GFH_WORK" && pwd -P)/test-worktree"
    git worktree add "$wt" test-branch

    # Merge the branch
    git checkout main
    git merge test-branch

    # Try to delete with worktree protection
    run_git_branch_maintenance --delete-merged --delete-merged-worktrees --protect-worktree "$wt"
    [ "$status" -eq 0 ]
    # Worktree should still exist
    [ -d "$wt" ]

    # Cleanup
    git worktree remove "$wt" --force
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
    local wt
    wt="$(cd "$GFH_WORK" && pwd -P)/test-leftover-worktree"
    git worktree add "$wt" tmp-gbm

    # Run git-branch-maintenance - should clean up the leftover worktree
    run_git_branch_maintenance --dry-run
    [ "$status" -eq 0 ]

    # The leftover worktree should have been cleaned up
    ! git worktree list | grep -qF "$wt"
}

@test "git-branch-maintenance never proposes deleting the repo's own primary worktree" {
    # Check out test-branch directly in the PRIMARY worktree (this TEST_DIR
    # is the main working tree - there is no linked worktree involved here).
    git checkout test-branch

    # Make origin/main point at the same commit, so test-branch shows as
    # merged (the scenario from the bug report: the primary checkout sits on
    # a branch that shows merged into origin/main).
    git update-ref refs/remotes/origin/main test-branch

    # Dry-run must not claim it would delete the primary worktree/branch.
    run_git_branch_maintenance --dry-run --delete-merged --delete-merged-worktrees
    [ "$status" -eq 0 ]
    branch_block=$(echo "$output" | grep -A 1 "^Branch: test-branch$")
    echo "$branch_block" | grep -q "skip"
    [[ $branch_block != *"Would delete branch and worktree"* ]]

    # A real (non-dry-run) invocation must not attempt to remove the primary
    # worktree either - this directory, and the branch, must remain intact.
    run_git_branch_maintenance --delete-merged --delete-merged-worktrees
    [ "$status" -eq 0 ]
    [ -d "$TEST_DIR" ]
    [ "$(git rev-parse --abbrev-ref HEAD)" = "test-branch" ]
    git branch | grep -q "test-branch"
}

@test "git-branch-maintenance still protects explicit worktrees and still deletes ordinary ones" {
    # Two branches at the same commit as main, so both are trivially
    # "merged" into origin/main without needing extra commits/merges.
    git branch protected-branch main
    git branch ordinary-branch main

    # git canonicalizes worktree paths when it registers them (e.g. resolving
    # /tmp to /private/tmp on macOS), and get_branch_worktree() reports that
    # canonical form back. Build the paths already-canonical (via the
    # fixture work dir resolved with `pwd -P`) so the literal string passed to
    # --protect-worktree is guaranteed to match what the script compares it
    # against - a plain "/tmp/..." literal would not.
    protected_wt="$(cd "$GFH_WORK" && pwd -P)/protected-worktree"
    git worktree add "$protected_wt" protected-branch

    ordinary_wt="$(cd "$GFH_WORK" && pwd -P)/ordinary-worktree"
    git worktree add "$ordinary_wt" ordinary-branch

    run_git_branch_maintenance --delete-merged --delete-merged-worktrees --protect-worktree "$protected_wt"
    [ "$status" -eq 0 ]

    # Explicitly protected worktree/branch (existing behavior) is unaffected
    # by adding implicit primary-worktree protection.
    [ -d "$protected_wt" ]
    git branch | grep -q "protected-branch"

    # An ordinary, unprotected worktree must still be deleted - implicit
    # primary-worktree protection must not over-broaden to other worktrees.
    [ ! -d "$ordinary_wt" ]
    remaining_branches=$(git branch)
    [[ $remaining_branches != *"ordinary-branch"* ]]

    # Safety-net cleanup in case an assertion above caught a regression.
    git worktree remove "$protected_wt" --force 2>/dev/null || true
    git worktree remove "$ordinary_wt" --force 2>/dev/null || true
}

@test "git-branch-maintenance protects a worktree configured with a raw non-canonicalized path" {
    # Two branches at the same commit as main, so both are trivially
    # "merged" into origin/main without needing extra commits/merges.
    git branch protected-branch main
    git branch ordinary-branch main

    # Deliberately DO NOT canonicalize this path (contrast with the previous
    # test's `cd "$GFH_WORK" && pwd -P`). On macOS, mktemp -d returns a
    # path under /var/folders/... which is itself a symlink chain to
    # /private/var/folders/...; git canonicalizes worktree paths when it
    # registers them, so get_branch_worktree() reports back the resolved
    # /private/... form. A raw, un-resolved --protect-worktree path must
    # still match - is_protected_worktree() must canonicalize the configured
    # path(s), not just compare the literal strings.
    protected_wt="$GFH_WORK/protected-worktree-raw"
    git worktree add "$protected_wt" protected-branch

    ordinary_wt="$GFH_WORK/ordinary-worktree-raw"
    git worktree add "$ordinary_wt" ordinary-branch

    run_git_branch_maintenance --delete-merged --delete-merged-worktrees --protect-worktree "$protected_wt"
    [ "$status" -eq 0 ]

    # The worktree/branch protected via a raw, non-canonical path must still
    # be protected.
    [ -d "$protected_wt" ]
    git branch | grep -q "protected-branch"

    # An ordinary, unprotected worktree must still be deleted - the
    # canonicalization fix must not over-broaden protection.
    [ ! -d "$ordinary_wt" ]
    remaining_branches=$(git branch)
    [[ $remaining_branches != *"ordinary-branch"* ]]

    # Safety-net cleanup in case an assertion above caught a regression.
    git worktree remove "$protected_wt" --force 2>/dev/null || true
    git worktree remove "$ordinary_wt" --force 2>/dev/null || true
}

@test "git-branch-maintenance reports failure instead of claiming success when worktree removal fails" {
    # Create a linked worktree for test-branch. Canonicalize the path up
    # front (see the comment in the previous test) so the mock below matches
    # on exactly the path the script will actually pass to
    # `git worktree remove`.
    fail_wt="$(cd "$GFH_WORK" && pwd -P)/worktree-remove-fail"
    git worktree add "$fail_wt" test-branch

    # Make origin/main point at test-branch's commit so it shows as merged
    # (fast-forwarding local main would not do this - origin/main would
    # stay behind, exactly as pinned at setup time).
    git update-ref refs/remotes/origin/main test-branch

    # Install a git mock that specifically refuses to remove THIS worktree
    # (leaving every other git invocation, including the tmp-gbm cleanup
    # worktree, to the real git) so the failure is deterministic.
    cat > "$MOCK_DIR/git" <<EOF
#!/usr/bin/env bash
if [[ "\$1" == "fetch" ]]; then
    exit 0
fi
if [[ "\$1" == "worktree" && "\$2" == "remove" && "\$3" == "$fail_wt" ]]; then
    echo "mock: refusing to remove worktree" >&2
    exit 1
fi
exec "$REAL_GIT" "\$@"
EOF
    chmod +x "$MOCK_DIR/git"

    run_git_branch_maintenance --delete-merged --delete-merged-worktrees
    [ "$status" -eq 0 ]

    # The failure must be reported, not silently claimed as a successful
    # deletion.
    echo "$output" | grep -q -i "fail"
    [[ $output != *"Deleted (with worktree)"* ]]

    # Since the worktree removal failed, the branch must NOT have been
    # deleted either (it is only deleted after a successful removal).
    git branch | grep -q "test-branch"

    # Cleanup with the real git, bypassing the failing mock.
    "$REAL_GIT" worktree remove "$fail_wt" --force 2>/dev/null || rm -rf "$fail_wt"
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
    harness_path="${GFH_LIB:-}"

    run env GIT_DIR="$bogus" GIT_INDEX_FILE="$bogus/index" HARNESS_PATH="$harness_path" bash -c '
        source "$HARNESS_PATH"
        gfh_setup "git-branch-maintenance-regression"
        command git -C "$GFH_REPO" rev-parse --git-dir
    '
    [ "$status" -eq 0 ]
    # The reported git-dir must be the fixture'"'"'s own .git, never the leaked path.
    [[ "$output" != *"leaked-gitdir"* ]]

    # The bogus path must never have been created -- proves the scrub took
    # effect rather than the leaked vars silently being honored.
    [ ! -e "$bogus" ]

    rm -rf "$bogus_parent"
}

# The script's own cleanup of its scratch branch lets git print
# "Deleted branch tmp-gbm (was ...)" on stdout in every mode; that is not a
# report about the user's branches, so the "no Deleted wording" checks below
# ignore it.
output_without_tmp_cleanup() {
    echo "$output" | grep -v '^Deleted branch tmp-gbm'
}

@test "dry-run: a merged branch in a linked worktree is skipped, not reported as deleted (no --delete-merged-worktrees)" {
    # Same commit as main, so trivially merged into origin/main.
    git branch merged-wt-branch main
    wt="$(cd "$GFH_WORK" && pwd -P)/dry-run-skip-worktree"
    git worktree add "$wt" merged-wt-branch

    run_git_branch_maintenance --dry-run --delete-merged
    [ "$status" -eq 0 ]

    # The per-branch dry-run line says skip, and the status line is the real
    # path's has-worktree status.
    branch_block=$(echo "$output" | grep -A 3 "^Branch: merged-wt-branch$")
    echo "$branch_block" | grep -q "Would skip (has worktree)"
    echo "$branch_block" | grep -q "Has worktree, use --delete-merged-worktrees to remove"

    # Neither the branch nor its worktree appears under any deleted or
    # would-delete summary heading or total.
    summary=$(echo "$output" | sed -n '/Summary:/,$p')
    [[ $summary != *"merged-wt-branch"* ]]
    [[ $summary != *"$wt"* ]]
    [[ $output != *"Would delete branches"* ]]
    [[ $output != *"Would delete worktrees"* ]]
    [[ $output != *"would be deleted"* ]]
    [[ $(output_without_tmp_cleanup) != *"Deleted"* ]]

    # Nothing was changed.
    [ -d "$wt" ]
    git branch | grep -q "merged-wt-branch"

    git worktree remove "$wt" --force 2>/dev/null || true
}

@test "dry-run: a merged branch in a protected worktree is skipped, not reported as deleted (--delete-merged-worktrees)" {
    git branch merged-protected-branch main
    wt="$(cd "$GFH_WORK" && pwd -P)/dry-run-protected-worktree"
    git worktree add "$wt" merged-protected-branch
    git config --local --add git-branch-maintenance.protectedWorktree "$wt"

    run_git_branch_maintenance --dry-run --delete-merged --delete-merged-worktrees
    [ "$status" -eq 0 ]

    branch_block=$(echo "$output" | grep -A 3 "^Branch: merged-protected-branch$")
    echo "$branch_block" | grep -q "Would skip (has worktree)"
    echo "$branch_block" | grep -q "Has worktree"

    summary=$(echo "$output" | sed -n '/Summary:/,$p')
    [[ $summary != *"merged-protected-branch"* ]]
    [[ $summary != *"$wt"* ]]
    [[ $output != *"Would delete branches"* ]]
    [[ $output != *"Would delete worktrees"* ]]
    [[ $output != *"would be deleted"* ]]
    [[ $(output_without_tmp_cleanup) != *"Deleted"* ]]

    [ -d "$wt" ]
    git branch | grep -q "merged-protected-branch"

    git worktree remove "$wt" --force 2>/dev/null || true
}

@test "dry-run: a merged branch without a worktree is listed under Would delete and counted, with no Deleted wording" {
    git branch merged-plain-branch main

    run_git_branch_maintenance --dry-run --delete-merged
    [ "$status" -eq 0 ]

    # Listed under a Would delete heading and counted in a would-delete total.
    echo "$output" | grep -q "Would delete branches:"
    echo "$output" | sed -n '/Would delete branches:/,/^$/p' | grep -q "merged-plain-branch"
    echo "$output" | grep -q "1 branch(es) would be deleted"

    # The per-branch status line reads Would delete, not the raw status token.
    branch_block=$(echo "$output" | grep -A 3 "^Branch: merged-plain-branch$")
    echo "$branch_block" | grep -q "Would delete"
    [[ $branch_block != *"would-deleted"* ]]

    # No line of a dry run reads Deleted, and no worktree total appears
    # since no worktree is involved.
    [[ $(output_without_tmp_cleanup) != *"Deleted"* ]]
    [[ $output != *"worktree(s)"* ]]

    # Nothing was changed.
    git branch | grep -q "merged-plain-branch"
}

@test "dry-run: a merged branch deletable with its worktree is listed under Would delete branches and worktrees" {
    git branch merged-del-wt-branch main
    wt="$(cd "$GFH_WORK" && pwd -P)/dry-run-delete-worktree"
    git worktree add "$wt" merged-del-wt-branch

    run_git_branch_maintenance --dry-run --delete-merged --delete-merged-worktrees
    [ "$status" -eq 0 ]

    echo "$output" | sed -n '/Would delete branches:/,/^$/p' | grep -q "merged-del-wt-branch"
    echo "$output" | sed -n '/Would delete worktrees:/,/^$/p' | grep -qF "$wt"
    echo "$output" | grep -q "1 branch(es) would be deleted"
    echo "$output" | grep -q "1 worktree(s) would be deleted"
    branch_block=$(echo "$output" | grep -A 3 "^Branch: merged-del-wt-branch$")
    echo "$branch_block" | grep -q "Would delete (with worktree)"
    [[ $(output_without_tmp_cleanup) != *"Deleted"* ]]

    [ -d "$wt" ]
    git branch | grep -q "merged-del-wt-branch"

    git worktree remove "$wt" --force 2>/dev/null || true
}

@test "a refused git fetch is tolerated: warns and still deletes locally merged branches" {
    git branch merged-after-refused-fetch main

    cat > "$MOCK_DIR/git" <<MOCK
#!/usr/bin/env bash
if [[ "\$1" == "fetch" ]]; then
    echo "pg-origin-guard: refused (no-human)" >&2
    exit 1
fi
exec "$REAL_GIT" "\$@"
MOCK
    chmod +x "$MOCK_DIR/git"

    run_git_branch_maintenance --delete-merged
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "git fetch failed"
    ! git branch | grep -q "merged-after-refused-fetch"
}

# Install a counting git shim ahead of the real git: every invocation's argv
# is appended to $GIT_CALL_LOG (fetch is still short-circuited like the setup
# mock). Returns the log path in GIT_CALL_LOG.
install_counting_git() {
    export GIT_CALL_LOG="$MOCK_DIR/git-calls.log"
    : > "$GIT_CALL_LOG"
    cat > "$MOCK_DIR/git" <<MOCK
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "$GIT_CALL_LOG"
if [[ "\$1" == "fetch" ]]; then
    exit 0
fi
exec "$REAL_GIT" "\$@"
MOCK
    chmod +x "$MOCK_DIR/git"
}

@test "the merged set and worktree list are computed once per run regardless of branch count" {
    local i
    for i in 1 2 3 4 5 6 7 8; do
        git branch "merged-$i" main
    done
    git branch -f unmerged-1 test-branch
    install_counting_git

    run_git_branch_maintenance --delete-merged
    [ "$status" -eq 0 ]

    # Behaviour: merged branches deleted, the unmerged one kept
    [ -z "$(git branch --list merged-1)" ]
    git branch | grep -q "unmerged-1"

    local merged_calls worktree_calls
    merged_calls=$(grep -cE '^(branch|for-each-ref) .*--merged' "$GIT_CALL_LOG" || true)
    worktree_calls=$(grep -cE '^worktree list' "$GIT_CALL_LOG" || true)
    [ "$merged_calls" -eq 1 ]
    [ "$worktree_calls" -eq 1 ]
}

@test "a stale branch fast-forwarded in the same run is deleted as merged" {
    # behind: base moves ahead of this branch
    git branch behind main
    git checkout -q -b advance main
    echo more > more.txt && git add more.txt && git commit -qm advance
    git update-ref refs/remotes/origin/main advance
    git checkout -q main
    install_counting_git

    run_git_branch_maintenance --ff --delete-merged
    [ "$status" -eq 0 ]
    [ -z "$(git branch --list behind)" ]
    echo "$output" | grep -q "• behind"
}

# The temporary-worktree root is overridable so no test can ever touch a real
# /tmp/git-branch-maintenance-* directory on the machine running the suite.
use_private_tmp_root() {
    export GBM_TMP_ROOT="$MOCK_DIR/tmp-root"
    mkdir -p "$GBM_TMP_ROOT"
}

@test "git worktree add stderr reaches the output when the temporary worktree cannot be created" {
    use_private_tmp_root
    cat > "$MOCK_DIR/git" <<MOCK
#!/usr/bin/env bash
if [[ "\$1" == "fetch" ]]; then exit 0; fi
if [[ "\$1" == "worktree" && "\$2" == "add" ]]; then
    echo "fatal: simulated no space left on device" >&2
    exit 128
fi
exec "$REAL_GIT" "\$@"
MOCK
    chmod +x "$MOCK_DIR/git"

    run_git_branch_maintenance
    [ "$status" -ne 0 ]
    echo "$output" | grep -q "simulated no space left on device"
    echo "$output" | grep -q "git worktree add"
}

@test "git branch -f stderr reaches the output when the temporary branch cannot be created" {
    use_private_tmp_root
    cat > "$MOCK_DIR/git" <<MOCK
#!/usr/bin/env bash
if [[ "\$1" == "fetch" ]]; then exit 0; fi
if [[ "\$1" == "branch" && "\$2" == "-f" ]]; then
    echo "fatal: simulated ref lock failure" >&2
    exit 128
fi
exec "$REAL_GIT" "\$@"
MOCK
    chmod +x "$MOCK_DIR/git"

    run_git_branch_maintenance
    [ "$status" -ne 0 ]
    echo "$output" | grep -q "simulated ref lock failure"
    echo "$output" | grep -q "git branch -f"
}

@test "orphaned temporary worktree directories are removed at start, and only those" {
    use_private_tmp_root
    local root="$GBM_TMP_ROOT"
    local old_stamp="202001010000"

    # Orphan: matching name, not registered, old
    mkdir -p "$root/git-branch-maintenance-111/sub"
    echo data > "$root/git-branch-maintenance-111/sub/file"
    touch -t "$old_stamp" "$root/git-branch-maintenance-111"

    # Too young: matching name, not registered, fresh
    mkdir -p "$root/git-branch-maintenance-222"

    # Old but not our name
    mkdir -p "$root/other-dir-333"
    touch -t "$old_stamp" "$root/other-dir-333"

    # Old, matching, but a still-registered worktree of this repo
    git worktree add -q --detach "$root/git-branch-maintenance-444" main
    touch -t "$old_stamp" "$root/git-branch-maintenance-444"

    # Old, matching, a linked worktree of ANOTHER repository whose gitdir exists
    local other="$MOCK_DIR/other-repo"
    gfh_init_repo "$other" "gbm-other"
    git -C "$other" commit -q --allow-empty -m other
    git -C "$other" worktree add -q --detach "$root/git-branch-maintenance-555"
    touch -t "$old_stamp" "$root/git-branch-maintenance-555"

    # Old, matching, but a symlink: never followed or removed
    mkdir -p "$MOCK_DIR/symlink-target"
    ln -s "$MOCK_DIR/symlink-target" "$root/git-branch-maintenance-666"

    run_git_branch_maintenance --dry-run
    [ "$status" -eq 0 ]
    [ -d "$root/git-branch-maintenance-111" ]
    echo "$output" | grep -q "Would remove orphaned.*git-branch-maintenance-111"

    run_git_branch_maintenance
    [ "$status" -eq 0 ]

    [ ! -e "$root/git-branch-maintenance-111" ]
    [ -d "$root/git-branch-maintenance-222" ]
    [ -d "$root/other-dir-333" ]
    [ -d "$root/git-branch-maintenance-444" ]
    [ -d "$root/git-branch-maintenance-555" ]
    [ -L "$root/git-branch-maintenance-666" ]
    [ -d "$MOCK_DIR/symlink-target" ]
    echo "$output" | grep -q "Removing orphaned.*git-branch-maintenance-111"
}

@test "the orphan age threshold is configurable" {
    use_private_tmp_root
    mkdir -p "$GBM_TMP_ROOT/git-branch-maintenance-777"
    export GBM_ORPHAN_MIN_AGE_MIN=0
    # mtime is "now"; make it clearly older than 0 minutes
    touch -t 202001010000 "$GBM_TMP_ROOT/git-branch-maintenance-777"
    run_git_branch_maintenance
    [ "$status" -eq 0 ]
    [ ! -e "$GBM_TMP_ROOT/git-branch-maintenance-777" ]
}
