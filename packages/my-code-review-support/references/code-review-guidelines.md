# Code Review Guidelines

For JSON output format, severity levels, field semantics, and tool preference, see common.md.

This reference documents how to review code changes for bugs, security issues, performance problems, and other technical concerns.

## Reading Files for Context

When reviewing a file, you may need the full file content for context:

```bash
# Read the full file at HEAD
cat <file_path>

# Or use the Read tool
Read(path="<worktree_path>/<file_path>")
```

**When to read full files:**

- Understanding the broader context of a change
- Checking how a function is called elsewhere
- Verifying imports and dependencies
- Understanding class/module structure

**When NOT to read full files:**

- The diff provides sufficient context
- Simple, isolated changes
- Performance-critical reviews (read only what's needed)

## What to Look For

### 1. Bugs and Logic Errors

**Examples:**

- Off-by-one errors in loops
- Incorrect conditional logic
- Null/undefined reference errors
- Type mismatches
- Incorrect API usage
- Race conditions
- Deadlocks

**Comment when:**

- The code will produce incorrect results
- The logic doesn't match the stated intent
- Edge cases are not handled

**Example comment:**

```json
{
  "path": "src/utils/pagination.ts",
  "lines": [23],
  "message": "Off-by-one error: should be `i < total` not `i <= total` to avoid index out of bounds.",
  "severity": "error"
}
```

### 2. Security Vulnerabilities

**Examples:**

- SQL injection
- XSS vulnerabilities
- CSRF vulnerabilities
- Insecure data storage
- Hardcoded secrets
- Insufficient input validation
- Authentication/authorization bypasses
- Insecure cryptography

**Comment when:**

- User input is not sanitized
- Sensitive data is logged or exposed
- Security best practices are violated

**Example comment:**

```json
{
  "path": "src/api/users.ts",
  "lines": [45],
  "message": "Potential SQL injection: user input `req.body.email` is not sanitized before query. Use parameterized queries or an ORM.",
  "severity": "error"
}
```

### 3. Performance Issues

**Examples:**

- N+1 queries
- Inefficient algorithms (O(n²) when O(n) is possible)
- Unnecessary loops or iterations
- Large data structures in memory
- Blocking I/O in hot paths
- Missing indexes
- Unnecessary re-renders (React)

**Comment when:**

- The performance impact is significant
- There's a clear, better alternative
- The code will cause user-facing slowness

**Example comment:**

```json
{
  "path": "src/services/analytics.ts",
  "lines": [67, 72],
  "message": "N+1 query: fetching user details in a loop. Consider using a single query with JOIN or batch loading.",
  "severity": "warning"
}
```

### 4. Missing Error Handling

**Examples:**

- Unhandled promise rejections
- Missing try-catch blocks
- No error logging
- Silent failures
- Incorrect error propagation

**Comment when:**

- Errors could crash the application
- Failures are not logged or reported
- Users would see cryptic errors

**Example comment:**

```json
{
  "path": "src/api/payment.ts",
  "lines": [34, 42],
  "message": "Missing error handling: async function can throw but no try-catch wrapper. Add error handling and log failures.",
  "severity": "warning"
}
```

### 5. Incorrect API Usage

**Examples:**

- Deprecated API usage
- Incorrect function signatures
- Missing required parameters
- Violating API contracts
- Misusing library functions

**Comment when:**

- The API is used incorrectly
- There's a better API for the use case
- The code will break with future updates

**Example comment:**

```json
{
  "path": "src/components/DatePicker.tsx",
  "lines": [28],
  "message": "Incorrect API usage: `moment()` is deprecated. Use `dayjs()` or native `Date` API instead.",
  "severity": "warning"
}
```

### 6. Breaking Changes

**Examples:**

- Changing public API signatures
- Removing exported functions
- Changing database schema without migration
- Modifying configuration formats
- Changing environment variable names

**Comment when:**

- The change will break existing code
- Migration path is not documented
- Backwards compatibility is broken

**Example comment:**

```json
{
  "path": "src/api/v1/users.ts",
  "lines": [15],
  "message": "Breaking change: removing `email` field from response. This will break existing API clients. Consider deprecation period or API versioning.",
  "severity": "error"
}
```

### 7. Style and Convention Violations

**Only comment on violations specific to this codebase**, not general style preferences.

**Examples:**

- Violating established naming conventions
- Inconsistent formatting (if not auto-formatted)
- Missing required documentation
- Violating architectural patterns

**Comment when:**

- The code violates documented conventions
- Inconsistency will confuse other developers
- Required documentation is missing

**Example comment:**

```json
{
  "path": "src/services/emailService.ts",
  "lines": [12],
  "message": "Naming convention: service classes should use PascalCase. Rename to `EmailService`.",
  "severity": "suggestion"
}
```

### 8. Missing or Incorrect Documentation

**Examples:**

- Public APIs without JSDoc/docstrings
- Complex logic without comments
- Incorrect or outdated comments
- Missing README updates

**Comment when:**

- Public APIs lack documentation
- Complex logic needs explanation
- Comments contradict the code

**Example comment:**

```json
{
  "path": "src/utils/crypto.ts",
  "lines": null,
  "message": "Missing documentation: public function `encryptData` should have JSDoc explaining parameters, return value, and potential errors.",
  "severity": "suggestion"
}
```

### 9. Test Coverage Gaps

**Examples:**

- New features without tests
- Critical paths untested
- Edge cases not covered
- Missing integration tests

**Comment when:**

- Critical functionality lacks tests
- Edge cases are not tested
- Test coverage significantly decreased

**Example comment:**

```json
{
  "path": null,
  "lines": null,
  "message": "Missing tests: new authentication flow in `src/auth/login.ts` should have unit and integration tests.",
  "severity": "suggestion"
}
```

## What NOT to Comment On

### 1. Good Practices

If the code follows best practices, don't comment. Assume good practices are the baseline.

**Don't say:**

- "Good use of async/await"
- "Nice error handling"
- "Well-structured code"

### 2. Praise or Compliments

Reviews are for identifying problems, not praising. Save praise for human reviewers.

**Don't say:**

- "Great work!"
- "This is really clean"
- "Love this approach"

### 3. Minor Style Preferences

Don't comment on style choices that don't violate conventions.

**Don't say:**

- "I prefer single quotes" (if both are acceptable)
- "This could be on one line" (if multi-line is fine)
- "I would name this differently" (if the name is clear)

### 4. Working Code That's Different

If the code works correctly but you would write it differently, don't comment.

**Don't say:**

- "I would use a for loop instead of map"
- "You could use a ternary here"
- "This could be more concise"

**Exception**: If your alternative is significantly better (performance, readability, maintainability), suggest it as a `suggestion` severity.

## Comment Guidelines

### Be Specific

**Bad**: "This function has issues"

**Good**: "This function doesn't handle the case where `user` is null on line 23, which will cause a runtime error"

### Suggest a Fix

When possible, suggest how to fix the problem.

**Example**:

```json
{
  "path": "src/api/auth.ts",
  "lines": [45],
  "message": "Password is logged in plaintext. Remove the log statement or redact the password: `logger.info('Login attempt', { email, password: '[REDACTED]' })`.",
  "severity": "error"
}
```

### Keep It Concise

Comments should be actionable and to the point.

**Bad**: "I noticed that this function is doing a lot of things and it might be hard to maintain in the future because it's handling both validation and database operations which violates the single responsibility principle..."

**Good**: "This function handles both validation and database operations. Consider splitting into `validateUser()` and `saveUser()` for better maintainability."

### Use Appropriate Severity

- **error**: Must be fixed (bugs, security, breaking changes)
- **warning**: Should be fixed (performance, missing error handling)
- **suggestion**: Nice to have (improvements, best practices, test coverage)
