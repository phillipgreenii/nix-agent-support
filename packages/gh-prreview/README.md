# gh-prreview - GitHub Pull Request Review Extension

A GitHub CLI extension for managing PR review worktrees efficiently.

**Version 2.0**: Now written in Python with comprehensive test coverage and async GitHub API support for improved performance and maintainability.

## Installation

To verify:

```bash
gh extension list
gh prreview --version
gh prreview --help
```

## Development

This is a Python 3.11+ application built with:

- **Click** - CLI framework
- **Rich** - Beautiful terminal output
- **httpx** - Async HTTP client for GitHub API
- **pytest** - Testing framework with 80%+ coverage

To run tests:

```bash
cd packages/gh-prreview
./check-all.sh  # Runs formatting, linting, type checking, and tests
```

## Commands

### `gh prreview checkout <PR_ID>`

Checkout a pull request as a git worktree for review.

**Features:**

- Creates worktree at `$GH_PRREVIEW_REVIEW_PATH/pr-<PR_ID>` (review path must be configured; see [Configuration](#configuration))
- If worktree already exists at target path, reports it and exits cleanly

**Example:**

```bash
gh prreview checkout 12345
```

### `gh prreview remove <PR_ID>`

Remove a PR review worktree and its local branch (with safety checks).

**Safety Features:**

- Fails if worktree has uncommitted changes (shows full `git status`)
- Shows summary of unpushed commits if they exist
- Does NOT delete local branch if unpushed commits exist
- Only removes branch when it's safe to do so

**Example:**

```bash
gh prreview remove 12345
```

### `gh prreview list-local`

List all locally checked out PR worktrees.

**Shows:**

- PR number
- Branch name
- Worktree path
- Status (clean, uncommitted changes, or unpushed commits count)

**Example:**

```bash
gh prreview list-local

# Output:
# Local PR Worktrees
#  PR  Branch                          Path                                           Status
# ─────────────────────────────────────────────────────────────────────────────────────────────
# 59783  review/pr-59783  ~/pr-reviews/pr-59783  clean
# 60488  review/pr-60488  ~/pr-reviews/pr-60488  uncommitted
```

### `gh prreview list-awaiting`

List PRs awaiting your review based on configured criteria with intelligent status tracking.

**Filtering Criteria:**

- Open (draft PRs excluded by default)
- You are not the author
- You haven't reviewed the latest commit (excludes PRs where you've already reviewed the current state)
- Meets one of:
  - Review requested from you, OR
  - Author is in your watch list, OR
  - Has one of your watch labels

**Options:**

- `--include-draft`: Include draft PRs in results
- `--deep`: Search older PRs (increases limit to 500)
- `--debug`: Enable debug output

**Output Columns:**

- **PR**: PR number
- **Title**: PR title (truncated to 48 chars)
- **Author**: PR author (truncated to 18 chars)
- **Status**: Review status (see below)

**Status Values:**

- `NEW`: You haven't interacted with this PR yet, but it matches your criteria
- `STALE_REVIEW`: You submitted a review, but new commits have been pushed since then
- `PENDING_REVIEW`: You have a pending/draft review that hasn't been submitted yet

**Note:** PRs where you've already reviewed the latest commit are automatically filtered out.

**Examples:**

```bash
# Basic usage
gh prreview list-awaiting

# Include draft PRs
gh prreview list-awaiting --include-draft

# Deep search with drafts
gh prreview list-awaiting --deep --include-draft

# With environment variables
GH_PRREVIEW_WATCH_USERS="alice,bob" gh prreview list-awaiting
```

## Configuration

Configuration can be set via `gh config set` or environment variables. Environment variables take precedence.

### Configuration Keys

| Setting      | gh config               | Environment Variable       | Default      | Description                     |
| ------------ | ----------------------- | -------------------------- | ------------ | ------------------------------- |
| Review Path  | `prreview.review-path`  | `GH_PRREVIEW_REVIEW_PATH`  | _(required)_ | Base path for review worktrees  |
| Watch Labels | `prreview.watch-labels` | `GH_PRREVIEW_WATCH_LABELS` | _(empty)_    | Comma-separated labels to watch |
| Watch Users  | `prreview.watch-users`  | `GH_PRREVIEW_WATCH_USERS`  | _(empty)_    | Comma-separated users to watch  |

### Configuration Examples

**Using gh config:**

```bash
gh config set prreview.review-path ~/pr-reviews
gh config set prreview.watch-labels "needs-review,high-priority"
gh config set prreview.watch-users "alice,bob,charlie"
```

**Using environment variables:**

```bash
export GH_PRREVIEW_REVIEW_PATH=~/pr-reviews
export GH_PRREVIEW_WATCH_LABELS="needs-review,high-priority"
export GH_PRREVIEW_WATCH_USERS="alice,bob,charlie"
```

**Note:** If `gh config set` fails (e.g., due to Nix-managed config), use environment variables instead. Add them to your shell profile for persistence.

## Workflows

### Review Workflow

1. **Find PRs to review:**

   ```bash
   gh prreview list-awaiting
   ```

2. **Checkout a PR:**

   ```bash
   gh prreview checkout 12345
   ```

3. **Review the code in the worktree:**

   ```bash
   cd ~/pr-reviews/pr-12345
   # Review, test, make comments...
   ```

4. **Clean up when done:**
   ```bash
   gh prreview remove 12345
   ```

### Managing Multiple PRs

```bash
# See what you have checked out
gh prreview list-local

# Clean up merged/closed PRs
gh prreview list-local  # Note which ones are MERGED
gh prreview remove 60488  # Remove merged PRs
gh prreview remove 60600
```

### Watching Specific Teams/Labels

If you want to track PRs from specific team members or with specific labels:

```bash
# In your shell profile (~/.zshrc, ~/.bashrc, etc.)
export GH_PRREVIEW_WATCH_USERS="teammate1,teammate2,teammate3"
export GH_PRREVIEW_WATCH_LABELS="needs-review,urgent,security"

# Now list-awaiting will show PRs matching these criteria
gh prreview list-awaiting
```

## Repository Detection

The extension automatically detects the repository:

1. For `list-local`: Uses the first PR worktree's git remote
2. For `list-awaiting`: Tries worktrees first, then current directory's git remote
3. Commands work from any directory once repository is detected

## Error Handling

### Checkout Errors

- **PR not found:** Verify PR number with `gh pr list`
- **Branch doesn't exist:** PR branch may have been deleted
- **Path already exists (different branch):** Use `gh prreview remove <PR_ID>` first
- **Cannot fast-forward:** Worktree has local changes; review manually

### Remove Errors

- **Uncommitted changes:** Shows `git status` output; commit or stash changes first
- **Unpushed commits:** Shows commit summary; branch preserved for safety
- **Worktree not found:** Verify with `gh prreview list-local`

## Requirements

- `gh` (GitHub CLI) - authenticated and configured
- `git` - for worktree management
- Repository access (reads PRs from detected repository)

## License

MIT License
