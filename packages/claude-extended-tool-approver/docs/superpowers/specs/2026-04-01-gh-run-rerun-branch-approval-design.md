# gh run rerun: Branch-Aware Approval

**Bead**: pg2-8wmn
**Date**: 2026-04-01
**Status**: Design

## Problem

`gh run rerun <run-id>` currently results in **abstain** (falls through to Claude Code's built-in permission prompt) because the gh rule only has static maps for read-only and modifying commands. Rerunning a CI run for the current branch's PR is safe to auto-approve, but rerunning runs for other branches should remain gated.

## Decision

Use **dependency injection** (Strategy pattern) to give the gh rule a `BranchResolver` interface for runtime context lookups. The rule compares the current git branch against the run's branch and approves only on match. On any error or timeout, the rule abstains.

This is the first rule to make network calls. The timeout-to-abstain strategy keeps the hook's fail-safe behavior: if the lookup can't complete, the user still gets prompted.

## Design

### BranchResolver Interface

Defined in `internal/rules/gh/gh.go`:

```go
type BranchResolver interface {
    CurrentBranch(cwd string) (string, error)
    RunBranch(runID string) (string, error)
}
```

- `CurrentBranch` returns the checked-out branch name for the given working directory.
- `RunBranch` returns the `headBranch` of a GitHub Actions workflow run.

### Production Implementation

New file `internal/rules/gh/resolver.go`: `ExecBranchResolver`.

```go
type ExecBranchResolver struct {
    Timeout time.Duration // default 3s
}
```

- `CurrentBranch(cwd)` runs `git -C <cwd> rev-parse --abbrev-ref HEAD` with timeout.
- `RunBranch(runID)` runs `gh run view <runID> --json headBranch -q .headBranch` with timeout.
- Both use `exec.CommandContext` with a `context.WithTimeout` deadline.
- On timeout or any exec error, return `("", error)`.

### Constructor Change

`gh.New()` becomes `gh.New(resolver BranchResolver)`.

- If `resolver` is `nil`, the rerun path always abstains (safe default, simplifies existing tests).

### Evaluation Logic

Inserted in `Evaluate()` before the `readOnlyRun` map check, following the same pattern as the `pr merge` special case:

```
if resource == "run" && subcmd == "rerun":
  1. Extract runID: first positional arg after "rerun" not starting with "--"
  2. If no runID found -> abstain (can't determine association)
  3. If resolver is nil -> abstain
  4. Call resolver.CurrentBranch(input.CWD) -> if error: abstain
  5. Call resolver.RunBranch(runID) -> if error/timeout: abstain
  6. If branches match (case-sensitive) -> approve ("gh run rerun for current branch")
  7. If branches differ -> abstain
```

### Run ID Extraction

The run ID is a positional argument. `gh run rerun` accepts:

- `gh run rerun 12345`
- `gh run rerun 12345 --failed`
- `gh run rerun --failed 12345`

Extraction: scan `pc.Args` after the index of `"rerun"`, return the first element that does not start with `"-"`. If none found, abstain.

### Factory Wiring

In `internal/setup/factory.go`:

```go
// before
gh.New(),

// after
gh.New(gh.NewExecResolver()),
```

`NewExecResolver()` returns an `ExecBranchResolver` with a 3-second default timeout.

## Files Changed

| File                            | Change                                                                                                                        |
| ------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| `internal/rules/gh/gh.go`       | Add `BranchResolver` interface, update `Rule` struct + `New()` constructor, add rerun evaluation block with run ID extraction |
| `internal/rules/gh/resolver.go` | New file: `ExecBranchResolver` with timeout-guarded `git` and `gh` exec calls                                                 |
| `internal/rules/gh/gh_test.go`  | Add stub resolver and test cases for rerun path                                                                               |
| `internal/setup/factory.go`     | Wire `gh.New(gh.NewExecResolver())`                                                                                           |

## Test Plan

Stub resolver for unit tests:

```go
type stubResolver struct {
    currentBranch string
    runBranch     string
    currentErr    error
    runErr        error
}
```

### Test Cases

| Command                       | Stub State                   | Expected                  |
| ----------------------------- | ---------------------------- | ------------------------- |
| `gh run rerun 12345`          | branches match               | approve                   |
| `gh run rerun 12345`          | branches differ              | abstain                   |
| `gh run rerun 12345`          | `CurrentBranch` errors       | abstain                   |
| `gh run rerun 12345`          | `RunBranch` errors (timeout) | abstain                   |
| `gh run rerun --failed 12345` | branches match               | approve (flags before ID) |
| `gh run rerun 12345 --failed` | branches match               | approve (flags after ID)  |
| `gh run rerun` (no ID)        | n/a                          | abstain                   |
| `gh run rerun 12345`          | nil resolver                 | abstain                   |

### Existing Tests

Unaffected. Existing `gh.New()` calls become `gh.New(nil)` — nil resolver means the rerun path abstains, which matches current behavior (abstain on unknown `gh run` subcommands).

## Timeout Strategy

- Default: 3 seconds for both `CurrentBranch` and `RunBranch`
- `CurrentBranch` is local git, should be < 50ms; timeout is a safety net
- `RunBranch` makes a GitHub API call via `gh`; 3s accommodates typical latency
- On timeout: `exec.CommandContext` kills the process, method returns error, rule abstains
- No retries — a single attempt per evaluation
