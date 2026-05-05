# PR Structure Review Guidelines

For JSON output format, severity levels, field semantics, and tool preference, see common.md.

## Table of Contents

- [Commit Review](#commit-review)
- [PR Metadata Review](#pr-metadata-review)
- [Motivation](#motivation)
- [Changes](#changes)
- [Testing](#testing)
- [Related](#related)

## Commit Review

### What to Evaluate

#### 1. Commit Message Clarity

**Good commit messages:**

- Explain WHAT changed and WHY
- Are clear and descriptive
- Use imperative mood ("add feature" not "added feature")
- Are concise but informative

**Bad commit messages:**

- Vague: "fix", "update", "changes", "wip", "stuff"
- Too brief: "fix bug" (which bug?)
- Too verbose: Multi-paragraph essays
- Unclear: "refactor" (what was refactored and why?)

**Example - Bad**:

```
fix
```

**Example - Good**:

```
fix(auth): prevent null pointer when user session expires

The session cleanup code didn't check for null before accessing
user.id, causing crashes when sessions expired naturally.
```

**Comment when:**

- Messages are vague or uninformative
- The message doesn't explain the change
- Important context is missing

**Example comment**:

```json
{
  "path": null,
  "lines": null,
  "message": "Commit message 'fix' (abc123) is too vague. Should explain what was fixed and why.",
  "severity": "warning"
}
```

#### 2. Commit Message Accuracy

**Accurate messages:**

- Match what the code actually does
- Don't claim features that aren't implemented
- Don't contradict the changes

**Inaccurate messages:**

- Claim to add a feature that's not in the code
- Say "fix" but introduce a new feature
- Describe changes that don't exist

**Example - Inaccurate**:

```
feat(api): add pagination support

# But the code only adds the pagination parameter, doesn't implement pagination
```

**Comment when:**

- The message claims something the code doesn't deliver
- The message contradicts the actual changes
- The type (feat/fix/etc.) doesn't match the changes

**Example comment**:

```json
{
  "path": null,
  "lines": null,
  "message": "Commit 'feat(api): add pagination support' (def456) is misleading. The code only adds the parameter but doesn't implement pagination logic.",
  "severity": "warning"
}
```

#### 3. Conventional Commit Format

**Format**: `type(scope): description`

**Common types**:

- `feat` - New feature
- `fix` - Bug fix
- `docs` - Documentation changes
- `style` - Code style changes (formatting, no logic change)
- `refactor` - Code refactoring (no feature or bug fix)
- `perf` - Performance improvements
- `test` - Adding or updating tests
- `conf` - Configuration changes
- `ci` - CI/CD changes
- `build` - Build system changes
- `chore` - Maintenance tasks

**Good examples**:

```
feat(auth): add password strength validation
fix(api): prevent race condition in user creation
docs(readme): update installation instructions
test(auth): add integration tests for login flow
```

**Bad examples**:

```
Add feature          # Missing type and scope
feat: stuff          # Vague description
FEAT(AUTH): THING    # Wrong case
feat(auth) add thing # Missing colon
```

**Comment when:**

- The format is consistently wrong
- Multiple commits don't follow the convention
- The format makes it hard to understand changes

**Example comment**:

```json
{
  "path": null,
  "lines": null,
  "message": "Several commits don't follow conventional commit format (type(scope): description). Examples: 'Add feature', 'Update code'.",
  "severity": "suggestion"
}
```

**DO NOT comment on:**

- Minor format variations if the message is clear
- Commits that are clear without the format
- Single commits with minor format issues

#### 4. Ticket References

**When to include ticket references:**

- Significant features or changes
- Bug fixes tracked in JIRA
- Work that's part of a sprint or project

**Format**: `Refs: TICKET-ID` in the commit body

**Example**:

```
feat(auth): add two-factor authentication

Implements TOTP-based 2FA with QR code generation and backup codes.

Refs: DE-347
```

**Common ticket patterns**:

- `DE-1234` - Development Engineering
- `PAID-567` - Paid team
- `CI-890` - CI/CD team
- `NO-JIRA` - No ticket (for small changes)

**Comment when:**

- Significant work lacks a ticket reference
- The ticket reference is malformed
- Important context is missing without the ticket

**Example comment**:

```json
{
  "path": null,
  "lines": null,
  "message": "Commit 'feat(auth): add two-factor authentication' (abc123) is significant work but lacks a ticket reference. Add 'Refs: TICKET-ID' to the commit body.",
  "severity": "suggestion"
}
```

**DO NOT comment on:**

- Small fixes or refactors without tickets
- Commits that explicitly say `NO-JIRA`
- Documentation or test-only changes

#### 5. Commit Grouping and Logical Structure

**Good commit structure:**

- Each commit is a logical unit of change
- Commits can be understood independently
- Related changes are grouped together
- Unrelated changes are in separate commits

**Bad commit structure:**

- "WIP" commits that should be squashed
- Mixing unrelated changes in one commit
- Splitting related changes across multiple commits
- "Fix typo" commits after the main commit

**Example - Bad Structure**:

```
1. Add feature X
2. Fix typo in feature X
3. Add tests for feature X
4. Fix another typo
5. Update docs for feature X
```

**Example - Good Structure**:

```
1. feat(api): add feature X with tests and docs
```

**Comment when:**

- Multiple "WIP" or "fix typo" commits should be squashed
- Unrelated changes are mixed in one commit
- The commit history is confusing or hard to review

**Example comment**:

```json
{
  "path": null,
  "lines": null,
  "message": "Commits abc123, def456, and ghi789 should be squashed into a single commit. They all relate to the same feature and include fix-ups.",
  "severity": "suggestion"
}
```

#### 6. Commit Size

**Appropriate commit size:**

- Not too large (hard to review)
- Not too small (too granular)
- Represents a complete, logical change

**Too large:**

- Commits with 50+ files changed
- Multiple features in one commit
- Refactoring + new feature + bug fixes

**Too small:**

- Separate commits for adding and using a function
- Commits that don't work independently
- Excessive granularity (one line per commit)

**Comment when:**

- A commit is unreasonably large and should be split
- Multiple tiny commits should be combined
- The commit structure makes review difficult

**Example comment**:

```json
{
  "path": null,
  "lines": null,
  "message": "Commit abc123 changes 87 files across multiple features. Consider splitting into separate commits: 1) auth changes, 2) API changes, 3) UI changes.",
  "severity": "suggestion"
}
```

### What NOT to Comment On

#### 1. Minor Wording Preferences

If the message is clear, don't comment on minor wording choices.

**Don't say:**

- "Could say 'implement' instead of 'add'"
- "Prefer 'update' over 'change'"
- "Should use past tense" (if imperative is acceptable)

#### 2. Clear and Accurate Messages

If the commit message is clear and matches the code, don't comment.

**Don't say:**

- "This message is good"
- "Clear description"
- "Well-written commit"

#### 3. Single Commit PRs

If the PR has only one commit, don't comment on commit structure unless it's problematic.

#### 4. Established Patterns

If the commit follows the repository's established patterns (even if not conventional commits), don't comment.

## PR Metadata Review

### What to Evaluate

#### 1. PR Title Quality

**Good PR titles:**

- Clearly describe the change
- Follow conventional commit format (if applicable)
- Are concise but informative
- Match the scope of changes

**Bad PR titles:**

- Vague: "Fix bug", "Update code", "Changes"
- Too long: Multi-sentence descriptions
- Misleading: Don't match the actual changes
- All caps or poor formatting

**Example - Bad**:

```
Update stuff
```

**Example - Good**:

```
feat(auth): add password strength validation with configurable rules
```

**Comment when:**

- The title is vague or uninformative
- The title doesn't match the changes
- The title is misleading

**Example comment**:

```json
{
  "path": null,
  "lines": null,
  "message": "PR title 'Update stuff' is too vague. Should clearly describe what was changed, e.g., 'feat(auth): add password strength validation'.",
  "severity": "warning"
}
```

#### 2. PR Description Quality

**Good PR descriptions:**

- Explain the motivation and context
- Describe what changed
- Include testing instructions
- Link to related issues or tickets
- Provide screenshots for UI changes

**Bad PR descriptions:**

- Empty or just the title repeated
- No context or motivation
- No testing instructions
- Missing important information

**Example - Bad**:

```
(empty)
```

**Example - Good**:

```markdown
## Motivation

Users have been creating weak passwords that are easily compromised. This PR adds
password strength validation to prevent common weak passwords.

## Changes

- Added password strength checker with configurable rules
- Integrated with registration and password change flows
- Added user-friendly error messages
- Updated documentation

## Testing

1. Try registering with password "123456" - should be rejected
2. Try "MyP@ssw0rd123" - should be accepted
3. Verify error messages are clear and helpful

## Related

Refs: DE-347
```

**Comment when:**

- The description is empty or minimal
- Important context is missing
- Testing instructions are absent for significant changes
- Screenshots are missing for UI changes

**Example comment**:

```json
{
  "path": null,
  "lines": null,
  "message": "PR description is empty. Should include: 1) Motivation/context, 2) What changed, 3) Testing instructions, 4) Ticket reference.",
  "severity": "warning"
}
```

#### 3. Labels and Metadata

**Appropriate labels:**

- Indicate the type of change (enhancement, bug, documentation)
- Flag special concerns (security, breaking-change, needs-review)
- Identify the team or area (frontend, backend, infrastructure)

**Common labels**:

- `enhancement` - New features
- `bug` - Bug fixes
- `documentation` - Documentation changes
- `security` - Security-related changes
- `breaking-change` - Breaking changes
- `needs-review` - Requires careful review
- `wip` - Work in progress

**Comment when:**

- Important labels are missing (e.g., `breaking-change` for breaking changes)
- Security changes lack the `security` label
- The PR is marked as WIP but seems complete

**Example comment**:

```json
{
  "path": null,
  "lines": null,
  "message": "This PR removes the `email` field from the API response, which is a breaking change. Add the 'breaking-change' label.",
  "severity": "warning"
}
```

**DO NOT comment on:**

- Minor label preferences
- Labels that are team-specific conventions
- Missing labels that aren't critical

#### 4. Commit Organization Within PR

**Good commit organization:**

- Commits tell a logical story
- Each commit builds on the previous
- Commits can be reviewed independently
- The PR is easy to review commit-by-commit

**Bad commit organization:**

- Random order of commits
- Back-and-forth changes (add feature, remove feature, add again)
- Commits that undo previous commits
- Confusing history

**Comment when:**

- The commit order makes the PR hard to review
- Commits contradict each other
- The history is confusing

**Example comment**:

```json
{
  "path": null,
  "lines": null,
  "message": "Commit organization is confusing: commit 3 undoes changes from commit 1. Consider reordering or squashing commits for a clearer history.",
  "severity": "suggestion"
}
```

#### 5. PR Size and Reviewability

**Appropriate PR size:**

- Can be reviewed in a reasonable time (< 30 minutes)
- Focused on a single feature or fix
- Not mixing unrelated changes

**Too large:**

- 1000+ lines changed
- Multiple unrelated features
- Hard to review thoroughly

**Too small:**

- Trivial changes that could be batched
- Excessive granularity

**Comment when:**

- The PR is unreasonably large and should be split
- Multiple unrelated changes should be separate PRs
- The PR is hard to review due to size

**Example comment**:

```json
{
  "path": null,
  "lines": null,
  "message": "This PR changes 2,347 lines across 87 files and includes multiple features. Consider splitting into separate PRs: 1) Auth changes, 2) API changes, 3) UI changes.",
  "severity": "suggestion"
}
```

**DO NOT comment on:**

- Large PRs that are necessarily large (migrations, refactors)
- Size if the PR is well-organized and reviewable
- Small PRs that are appropriately scoped

#### 6. Related PRs and Dependencies

**Clear dependencies:**

- Dependencies are documented
- Related PRs are linked
- Merge order is specified if needed

**Unclear dependencies:**

- PR depends on another but doesn't mention it
- No indication of merge order
- Related work is not linked

**Comment when:**

- The PR depends on another PR but doesn't mention it
- Merge order is important but not documented
- Related work should be linked

**Example comment**:

```json
{
  "path": null,
  "lines": null,
  "message": "This PR depends on #12344 (database migration). Add a note in the description and ensure #12344 is merged first.",
  "severity": "warning"
}
```

#### 7. Draft Status

**Appropriate draft status:**

- Draft PRs are clearly WIP
- Non-draft PRs are ready for review
- Draft status matches the state of the PR

**Inappropriate draft status:**

- Draft PRs that are actually complete
- Non-draft PRs that are clearly WIP
- Confusion about readiness

**Comment when:**

- A draft PR appears complete and ready for review
- A non-draft PR is clearly not ready (failing tests, incomplete features)

**Example comment**:

```json
{
  "path": null,
  "lines": null,
  "message": "This PR is marked as draft but appears complete (all tests passing, feature implemented). Consider marking as ready for review.",
  "severity": "suggestion"
}
```

### What NOT to Comment On

#### 1. Personal Preferences

Don't comment on PR structure preferences that are team-specific or personal.

**Don't say:**

- "I prefer longer descriptions"
- "Should use different labels"
- "Title should be formatted differently" (if it's clear)

#### 2. Well-Structured PRs

If the PR is well-structured, don't comment.

**Don't say:**

- "Good PR description"
- "Clear title"
- "Well-organized"

#### 3. Minor Issues

Don't comment on minor issues that don't affect reviewability.

**Don't say:**

- "Could add one more label"
- "Description could be slightly longer"
- "Title could be more concise" (if it's clear)
