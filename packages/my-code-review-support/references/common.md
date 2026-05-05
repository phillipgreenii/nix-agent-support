# Code Review Common Reference

This file contains shared information used by all code review agents and commands.

## Tool: my-code-review-support-cli

The `my-code-review-support-cli` CLI tool handles all GitHub interactions for automated PR code reviews.

### What the Tool Handles Automatically

1. **Robot emoji (🤖)** - Appended to all comment bodies when posting
2. **Pending reviews only** - Never submits reviews directly
3. **Deduplication** - Skips comments that already exist in pending review
4. **JSON extraction** - Finds JSON in agent output (handles surrounding text)
5. **GitHub API calls** - All interactions with GitHub

### Available Commands

| Command                                 | Description                                    | Output                                                                 |
| --------------------------------------- | ---------------------------------------------- | ---------------------------------------------------------------------- |
| `setup <PR>`                            | Identify PR, fetch branch, create worktree     | JSON with pr_number, title, branches, url, commit_sha, worktree_path   |
| `files [--base BASE] [--workdir DIR]`   | List changed files with stats                  | JSON with files array (path, additions, deletions)                     |
| `commits [--base BASE] [--workdir DIR]` | List commits with messages                     | JSON with commits array (sha, subject, body, author)                   |
| `pr-info <PR>`                          | Get PR metadata                                | JSON with description, labels, reviewers, checks                       |
| `post <PR>`                             | Parse JSON, deduplicate, create pending review | JSON with comments_posted, duplicates_skipped, pr_level_comments, mode |
| `cleanup <path>`                        | Remove worktree                                | JSON with status                                                       |

### Command Output Examples

**setup output:**

```json
{
  "pr_number": 12345,
  "title": "Add feature X",
  "head_branch": "phillipg.DE-123.feature-x",
  "base_branch": "main",
  "url": "https://github.com/OWNER/REPO/pull/12345",
  "commit_sha": "abc123...",
  "worktree_path": "/tmp/pr-review-12345"
}
```

**files output:**

```json
{
  "files": [{ "path": "src/auth/login.ts", "additions": 45, "deletions": 12 }]
}
```

**commits output:**

```json
{
  "commits": [
    {
      "sha": "abc123",
      "subject": "feat(auth): add validation",
      "body": "Refs: DE-347",
      "author": "John Doe <john@example.com>"
    }
  ]
}
```

**pr-info output:**

```json
{
  "description": "This PR adds...",
  "labels": ["enhancement", "security"],
  "reviewers": ["alice", "bob"],
  "checks": [{ "name": "build", "status": "success", "conclusion": "success" }]
}
```

**post output:**

```json
{
  "comments_posted": 5,
  "duplicates_skipped": 2,
  "pr_level_comments": 1,
  "mode": "created_new"
}
```

## JSON Comment Format

Run `my-code-review-support-cli post --help` for the full JSON input format.

### Field Semantics

| path   | lines          | Scope                   |
| ------ | -------------- | ----------------------- |
| set    | `[N]`          | Single-line comment     |
| set    | `[start, end]` | Multi-line comment      |
| set    | `null`         | File-level comment      |
| `null` | `null`         | PR/branch-level comment |

**Multi-line comment rules:**

- `lines` array with 2 elements: `[start_line, end_line]`
- First element is the start line, second is the end line
- Tool maps to GitHub's `start_line` and `line` parameters

### Severity Levels

- `error` - Bugs, security issues, breaking changes
- `warning` - Performance issues, missing error handling, potential problems
- `suggestion` - Improvements, best practices, test coverage

### Important Notes

- **Do NOT include 🤖 in messages** - the tool appends it automatically
- Output ONLY valid JSON
- If no issues found, output: `{"comments": []}`
- Do NOT include explanatory text outside the JSON structure

## Tool Preference Hierarchy

Always follow this preference order:

1. **`my-code-review-support-cli`** (preferred) - For file lists, commits, PR metadata, posting
2. **`git`** (allowed) - For diffs only: `git diff origin/main...HEAD -- <file>`
3. **`gh`** (FORBIDDEN for reviews) - Never use for posting comments or getting diffs

**Why NOT use `gh api` for diffs:**

- Hits resource limitations on large PRs
- Rate limiting issues
- `git diff` in the worktree is faster and more reliable

## Error Handling

### Setup Failures

If `my-code-review-support-cli setup` fails:

1. Report the error to the user
2. Do NOT proceed with review
3. No cleanup needed (worktree wasn't created)

### Review Failures

If any review phase fails:

1. Run cleanup to remove worktree
2. Report the error to the user
3. Include any partial results if available

### Post Failures

If `my-code-review-support-cli post` fails:

1. Report the error to the user with the JSON output
2. Run cleanup to remove worktree
3. User can manually create a pending review
4. **DO NOT** use `gh pr review` or `gh api` as fallback

### Cleanup Failures

If `my-code-review-support-cli cleanup` fails:

1. Report the error to the user
2. Provide the worktree path for manual cleanup
3. User can run: `git worktree remove <path>`

## Critical Constraints

### For Agents

**NEVER**:

- Use `gh pr review --comment` (submits immediately)
- Use `gh api` to post comments directly
- Use any fallback that posts comments to the PR directly
- Include 🤖 in JSON output (tool adds it)

**ALWAYS**:

- Use `my-code-review-support-cli post` for posting
- Output only valid JSON
- Run cleanup even if previous steps failed

### For Commands

**NEVER**:

- Manually run `git fetch origin <branch>`
- Manually run `git worktree add ...`
- Manually run `rm -rf /tmp/pr-review-...`
- Use `gh api` for PR diffs

**ALWAYS**:

- Use `my-code-review-support-cli` commands for setup, files, commits, pr-info, post, cleanup
- Use `git diff` in worktree for getting file diffs
- Follow the tool preference hierarchy

## PR Identifier Formats

The tool accepts multiple PR identifier formats:

- PR number: `12345` or `#12345`
- PR URL: `https://github.com/OWNER/REPO/pull/12345`
- Branch name: `phillipg.DE-123.feature-x`
- Title search: `Add feature X`

## Implementation Details

- **Language:** Go (Cobra CLI)
- **Prerequisites:** `gh` CLI installed and authenticated
- **Worktree location:** `/tmp/pr-review-<PR_NUMBER>`
- Run `my-code-review-support-cli post --help` for the full JSON input format.
