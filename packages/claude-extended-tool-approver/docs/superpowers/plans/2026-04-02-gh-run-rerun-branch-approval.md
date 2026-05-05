# gh run rerun Branch-Aware Approval — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Auto-approve `gh run rerun` when the workflow run belongs to the current git branch; abstain otherwise.

**Architecture:** Inject a `BranchResolver` interface into the gh rule via constructor. Production resolver shells out to `git` and `gh` with a 3-second timeout. On any error or timeout, the rule abstains (safe default).

**Tech Stack:** Go 1.24, `os/exec`, `context.WithTimeout`

**Spec:** `docs/superpowers/specs/2026-04-01-gh-run-rerun-branch-approval-design.md`
**Bead:** pg2-8wmn

---

## File Structure

| File                            | Responsibility                                                                                                                                |
| ------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/rules/gh/gh.go`       | `BranchResolver` interface, `Rule` struct with resolver field, `New(resolver)` constructor, rerun evaluation logic with `extractRunID` helper |
| `internal/rules/gh/resolver.go` | `ExecBranchResolver` production implementation with timeout-guarded `git` and `gh` exec calls, `NewExecResolver()` constructor                |
| `internal/rules/gh/gh_test.go`  | `stubResolver` type, rerun test cases, updated existing tests to use `New(nil)`                                                               |
| `internal/setup/factory.go`     | Wire `gh.New(gh.NewExecResolver())`                                                                                                           |

---

### Task 1: Add BranchResolver Interface and Update Constructor

**Files:**

- Modify: `internal/rules/gh/gh.go`
- Modify: `internal/rules/gh/gh_test.go`

- [ ] **Step 1: Update `Rule` struct and constructor in `gh.go`**

Add the `BranchResolver` interface and update the `Rule` struct to hold a resolver. Update `New()` to accept a `BranchResolver` parameter. No evaluation logic changes yet.

In `internal/rules/gh/gh.go`, add the interface above the `Rule` struct, update the struct to store the resolver, and change `New()`:

```go
// BranchResolver looks up branch context for runtime decisions.
type BranchResolver interface {
	CurrentBranch(cwd string) (string, error)
	RunBranch(runID string) (string, error)
}

type Rule struct {
	resolver BranchResolver
}

func New(resolver BranchResolver) *Rule {
	return &Rule{resolver: resolver}
}
```

- [ ] **Step 2: Update all existing test calls to `New(nil)`**

In `internal/rules/gh/gh_test.go`, every call to `New()` becomes `New(nil)`. There are 6 occurrences across the test functions: `TestGH_ReadOnly_Approve`, `TestGH_Modifying_Ask`, `TestGH_PrMerge_Ask`, `TestGH_PrMergeAuto_Abstain`, `TestGH_PrMergeAutoMerge_Ask`, `TestGH_NonGh_Abstain`, `TestGH_NonBash_Abstain`, `TestGH_Name`.

Replace every `r := New()` with `r := New(nil)`.

- [ ] **Step 3: Run tests to verify nothing broke**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && go test ./internal/rules/gh/...
```

Expected: All existing tests pass (nil resolver has no effect on existing paths).

- [ ] **Step 4: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps && git add packages/claude-extended-tool-approver/internal/rules/gh/gh.go packages/claude-extended-tool-approver/internal/rules/gh/gh_test.go && git commit -m "refactor(gh): add BranchResolver interface and accept in constructor

Refs: pg2-8wmn"
```

---

### Task 2: Add Rerun Evaluation Logic with Tests (TDD)

**Files:**

- Modify: `internal/rules/gh/gh.go`
- Modify: `internal/rules/gh/gh_test.go`

- [ ] **Step 1: Add stub resolver and all rerun test cases in `gh_test.go`**

Add the following to `gh_test.go`:

```go
import (
	"errors"
	// ... existing imports
)

type stubResolver struct {
	currentBranch string
	runBranch     string
	currentErr    error
	runErr        error
}

func (s *stubResolver) CurrentBranch(cwd string) (string, error) {
	return s.currentBranch, s.currentErr
}

func (s *stubResolver) RunBranch(runID string) (string, error) {
	return s.runBranch, s.runErr
}

func TestGH_RunRerun(t *testing.T) {
	errFailed := errors.New("simulated failure")

	tests := []struct {
		name     string
		cmd      string
		resolver BranchResolver
		want     hookio.Decision
	}{
		{
			name:     "branches match",
			cmd:      "gh run rerun 12345",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "feature-x"},
			want:     hookio.Approve,
		},
		{
			name:     "branches differ",
			cmd:      "gh run rerun 12345",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "main"},
			want:     hookio.Abstain,
		},
		{
			name:     "current branch error",
			cmd:      "gh run rerun 12345",
			resolver: &stubResolver{currentErr: errFailed},
			want:     hookio.Abstain,
		},
		{
			name:     "run branch error (timeout)",
			cmd:      "gh run rerun 12345",
			resolver: &stubResolver{currentBranch: "feature-x", runErr: errFailed},
			want:     hookio.Abstain,
		},
		{
			name:     "flags before run ID",
			cmd:      "gh run rerun --failed 12345",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "feature-x"},
			want:     hookio.Approve,
		},
		{
			name:     "flags after run ID",
			cmd:      "gh run rerun 12345 --failed",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "feature-x"},
			want:     hookio.Approve,
		},
		{
			name:     "no run ID",
			cmd:      "gh run rerun",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "feature-x"},
			want:     hookio.Abstain,
		},
		{
			name:     "nil resolver",
			cmd:      "gh run rerun 12345",
			resolver: nil,
			want:     hookio.Abstain,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New(tt.resolver)
			input := &hookio.HookInput{
				ToolName:  "Bash",
				ToolInput: mustJSON(map[string]string{"command": tt.cmd}),
				CWD:       "/tmp/test-repo",
			}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("cmd %q: got %s, want %s (reason: %s)", tt.cmd, got.Decision, tt.want, got.Reason)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify the new tests fail**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && go test ./internal/rules/gh/... -run TestGH_RunRerun -v
```

Expected: Compilation succeeds but the "branches match" tests fail with `got abstain, want approve` (since the rerun logic doesn't exist yet — it falls through to the default abstain).

- [ ] **Step 3: Add `extractRunID` helper and rerun evaluation block in `gh.go`**

Add the `extractRunID` helper function:

```go
// extractRunID returns the first positional (non-flag) argument after the
// "rerun" subcommand in a gh run rerun invocation. Returns "" if not found.
func extractRunID(args []string) string {
	// args layout: ["run", "rerun", ...rest]
	// Find "rerun" index and scan after it for first non-flag arg.
	rerunIdx := -1
	for i, a := range args {
		if a == "rerun" {
			rerunIdx = i
			break
		}
	}
	if rerunIdx < 0 {
		return ""
	}
	for _, a := range args[rerunIdx+1:] {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}
```

Add `"strings"` to the imports.

Insert the rerun evaluation block in `Evaluate()`, just before the `readOnlyRun` map check (before the `if readOnlyRun[subcmd] && resource == "run"` block). This follows the same pattern as the existing `pr merge` special case:

```go
		if resource == "run" && subcmd == "rerun" {
			runID := extractRunID(pc.Args)
			if runID == "" || r.resolver == nil {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "gh run rerun: cannot determine branch association",
					Module:   r.Name(),
				}
			}
			currentBranch, err := r.resolver.CurrentBranch(input.CWD)
			if err != nil {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "gh run rerun: cannot determine current branch",
					Module:   r.Name(),
				}
			}
			runBranch, err := r.resolver.RunBranch(runID)
			if err != nil {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "gh run rerun: cannot determine run branch",
					Module:   r.Name(),
				}
			}
			if currentBranch == runBranch {
				return hookio.RuleResult{
					Decision: hookio.Approve,
					Reason:   "gh run rerun for current branch",
					Module:   r.Name(),
				}
			}
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "gh run rerun for different branch",
				Module:   r.Name(),
			}
		}
```

- [ ] **Step 4: Run all gh tests to verify they pass**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && go test ./internal/rules/gh/... -v
```

Expected: All tests pass — both existing and new rerun tests.

- [ ] **Step 5: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps && git add packages/claude-extended-tool-approver/internal/rules/gh/gh.go packages/claude-extended-tool-approver/internal/rules/gh/gh_test.go && git commit -m "feat(gh): add branch-aware approval for gh run rerun

Approve gh run rerun when the run's branch matches the current git
branch. Abstain on mismatch, missing run ID, or resolver errors.

Refs: pg2-8wmn"
```

---

### Task 3: Add ExecBranchResolver Production Implementation

**Files:**

- Create: `internal/rules/gh/resolver.go`

- [ ] **Step 1: Create `resolver.go` with `ExecBranchResolver`**

Create `internal/rules/gh/resolver.go`:

```go
package gh

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

const defaultResolverTimeout = 3 * time.Second

// ExecBranchResolver resolves branch names by shelling out to git and gh.
type ExecBranchResolver struct {
	Timeout time.Duration
}

// NewExecResolver returns an ExecBranchResolver with the default 3s timeout.
func NewExecResolver() *ExecBranchResolver {
	return &ExecBranchResolver{Timeout: defaultResolverTimeout}
}

// CurrentBranch returns the checked-out branch for the given working directory.
func (r *ExecBranchResolver) CurrentBranch(cwd string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), r.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// RunBranch returns the headBranch of a GitHub Actions workflow run.
func (r *ExecBranchResolver) RunBranch(runID string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), r.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "run", "view", runID, "--json", "headBranch", "-q", ".headBranch")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
```

- [ ] **Step 2: Verify compilation**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && go build ./...
```

Expected: Clean compilation, no errors.

- [ ] **Step 3: Run all gh tests to ensure nothing broke**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && go test ./internal/rules/gh/... -v
```

Expected: All tests pass (resolver.go isn't used by tests — they use stubResolver).

- [ ] **Step 4: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps && git add packages/claude-extended-tool-approver/internal/rules/gh/resolver.go && git commit -m "feat(gh): add ExecBranchResolver with timeout-guarded git/gh lookups

Production BranchResolver that shells out to git and gh with a 3-second
timeout. Returns error on timeout, which the rule treats as abstain.

Refs: pg2-8wmn"
```

---

### Task 4: Wire ExecBranchResolver in Factory

**Files:**

- Modify: `internal/setup/factory.go`

- [ ] **Step 1: Update factory to pass resolver to gh rule**

In `internal/setup/factory.go`, change the `gh.New()` call in `RegisterRules`:

Replace:

```go
		gh.New(),
```

With:

```go
		gh.New(gh.NewExecResolver()),
```

No import changes needed — `gh` is already imported.

- [ ] **Step 2: Verify full build**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && go build ./...
```

Expected: Clean compilation.

- [ ] **Step 3: Run full test suite**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && go test ./...
```

Expected: All tests pass.

- [ ] **Step 4: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps && git add packages/claude-extended-tool-approver/internal/setup/factory.go && git commit -m "feat(gh): wire ExecBranchResolver into factory

Refs: pg2-8wmn"
```

---

### Task 5: Run Pre-Commit Checks and Validate

**Files:** None (validation only)

- [ ] **Step 1: Run pre-commit hooks**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps && pre-commit run --all-files
```

Expected: All checks pass.

- [ ] **Step 2: Run full test suite one more time**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && go test ./... -v
```

Expected: All tests pass.
