# kc/kubectl Rule Enhancements + Command-Substitution Allowlist — Implementation Plan (v2, post-review)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce spurious `ASK`/`ABSTAIN` prompts for ZR `kc` (kubectl-wrapper) commands and read-only `$(…)` command substitutions — **without weakening guards on prod/shared clusters or on genuinely consequential operations.**

**Architecture:** Refactor the `kubectl` rule to mirror the `docker` rule — inject `hookio.Evaluator` + `*patheval.PathEvaluator` via `New(eval, pe)`. Introduce a **devxp-scope detector**: exec-family and workspace-mutating verbs auto-approve **only** when the command targets a personal dev workspace (`--ws`/`-n` value `d-<user>…`, and `AWS_PROFILE`/`KC_CLUSTER` not a prod/shared cluster); otherwise they keep today's `Ask`. Exec-family recurses into the inner command with a pod-internal path evaluator (docker-`exec` pattern). Separately widen the shared `safeCmdSubstitutions` classifier in `cmdparse` (git-metadata verbs, `go env` without `-w/-u`, pure utils, and file-readers re-checked against `secretpath.IsSecret`), and approve `prove` in `buildtools`.

**Tech Stack:** Go (standard `testing`), gomod2nix build engine, module `github.com/phillipgreenii/claude-extended-tool-approver`.

## v3 decisions (READ FIRST — they override any residual `Ask` wording below)

1. **The kubectl rule returns ONLY `Approve` or `Abstain` for its OWN decisions — never `Ask`/`Reject`.** Wherever a task below shows `hookio.Ask`, `want ask`, or a `_Ask` test name for a kubectl-rule _own_ outcome (modifying / kubeconfig-modifying, non-dev exec, non-dev scoped verb, mutating rollout, generic fallback), treat it as `hookio.Abstain`. Rationale: `Abstain` defers to the permission mode / settings allow-rules instead of force-prompting. The authoritative classification block in **Task A6** already reflects this.
   - **Exception:** the exec-recursion _result_ (Task A6) is the inner command's real decision from the full chain — it may be any value (`Approve`/`Abstain`/`Ask`/`Reject`). Only the kubectl rule's _own_ terminal returns are constrained to Approve/Abstain. So the A6 test may legitimately assert `Ask` for a _recursed inner_ whose mock/real decision is `Ask`.
   - Existing tests `TestKubectl_Modifying_Ask` and `TestKubectl_KubeconfigModifying_Ask` must be renamed `..._Abstain` and assert `hookio.Abstain` (handled by new Task A1b).
   - The A8 integration case "prod exec NOT approved" asserts `hookio.Abstain` (was `Ask`).
2. **kc logic stays in the base binary for now** (continuing the existing `kc`/buildtools precedent). The proper extraction to structured apply-time config is tracked in bead **`pg2-9cxtr`** — out of scope here.
3. Task order: A1 → **A1b (new)** → A2 (extractOperation) → A3 (scope detector) → A4 (read-only additions) → A5 (scoped sync/workspace) → A6 (exec recursion — the authoritative classification block) → A7 (buildtools prove/yath) → A8 (engine integration) → B1 (substitution allowlist).

## Global Constraints

- Repo: `phillipgreenii-nix-agent-support`; package root: `packages/claude-extended-tool-approver`.
- Decision restrictiveness order (do NOT reorder): `Approve(0) < Abstain(1) < Ask(2) < Reject(3)`. `Approve` is the zero value; every `RuleResult` MUST set `Decision` explicitly.
- Rule dispatch is **first-match-wins** in `engine.Evaluate`; the compound fold in `engine.EvaluateExpression` is **most-restrictive-wins**.
- TDD mandatory: failing test first, watch it fail, minimal implementation, watch it pass, commit. One logical change per commit.
- Per-task test commands MUST include every package whose test binary the change can break. After A1, always run `./internal/engine/` too.
- Completion gates (repo CLAUDE.md): `nix flake check` MUST pass; `prek run --all-files` (or `pre-commit run --all-files`) MUST pass. No Jira ticket in this repo → **omit** the `Refs:` line from commits. No `--no-verify`.
- Work happens in a git worktree, never the canonical clone's primary branch.

## Devxp scope — the security contract (read before A5/A6)

`kc`/`kubectl` can target ANY cluster/namespace, including prod. The user's "the pod is recoverable, no concern what runs/deletes there" model holds **only for a personal dev workspace**. Therefore exec-family (`exec/exe/shell/wsexec`) and workspace-mutating verbs (`sync/syncdev/workspace`) are auto-approved **only when `isDevWorkspaceScope` is true**; otherwise they retain today's `Ask`. This is the linchpin — do not approve any of these unconditionally.

`isDevWorkspaceScope(args, env)` returns true iff **(a)** a `--ws`/`--workspace`/`-n`/`--namespace` value (space or `=` form) begins with `d-` (personal dev-workspace convention, e.g. `d-phillipg01`, `d-phillipgs0-db--sqitch`), **AND (b)** no prod/shared signal is present: `AWS_PROFILE` account (the part before `/`) is not in `{prod,dprod,euprod,build,fastlane,pdx,test}`, and `KC_CLUSTER` (if set) does not name a non-dev cluster (prefix other than `d1-`/`dd1-`).

---

## File Structure

- `internal/rules/kubectl/kubectl.go` — MODIFY. Evaluator+PathEvaluator injection, flag-value-aware `extractOperation`, scope detector, verb maps, exec-recursion, rollout sub-verb handling.
- `internal/rules/kubectl/kubectl_test.go` — MODIFY. `mockEvaluator`, signature fixes, new-behavior tests, migrate the exec/rollout cases whose expected decision changes.
- `internal/setup/factory.go` — MODIFY one line: `kubectl.New(eng, pe)`.
- `internal/engine/engine_integration_test.go` — MODIFY. Fix the second `kubectl.New()` call (line ~50 in `buildFullEngine`); add real-chain kc integration cases.
- `internal/rules/buildtools/buildtools.go` — MODIFY. Add `prove` to `approvedTools`.
- `internal/rules/buildtools/buildtools_test.go` — MODIFY. Add `prove` case.
- `internal/cmdparse/parser.go` — MODIFY. Shared `isSafeSubstitutionCommand`; widened allowlist; imports `secretpath`.
- `internal/cmdparse/parser_test.go` — MODIFY. Cases for the widened allowlist + secret recheck.

**Two independently landable parts:** Part A (kubectl rule + buildtools + integration tests) and Part B (cmdparse substitution). Disjoint packages; can land separately.

**Dropped from v1 (per review):** `slice` (writes files with `-o`; leave as `Ask`), `run-tests` (no `kubectl-run-tests` plugin exists — dead config), `hostname <arg>` (mutating; `hostname` allowed only with no args). `git show/diff/log` stay OUT of the substitution allowlist — their textconv/external-diff RCE is via repo config and a permission hook cannot neutralize it; only git-metadata verbs are added.

---

## PART A — kubectl rule + buildtools + integration tests

### Task A1: Inject Evaluator + PathEvaluator (structural, no behavior change) — fix BOTH call sites

**Files:** Modify `kubectl.go` (struct/`New`), `factory.go` (line ~66), `engine_integration_test.go` (line ~50 in `buildFullEngine`), `kubectl_test.go` (mock + call sites).

**Interfaces produced:** `func New(eval hookio.Evaluator, pe *patheval.PathEvaluator) *Rule`; fields `r.exprEval`, `r.pe`.

- [ ] **Step 1:** In `kubectl_test.go` add the mock (mirror `docker_test.go`) and change every `r := New()` (lines 16,41,60,79,103,116,127) to `r := New(nil, nil)`:

```go
type mockEvaluator struct {
	results       map[string]hookio.RuleResult
	defaultResult hookio.RuleResult
}

func (m *mockEvaluator) EvaluateExpression(expr string, stack []hookio.StackFrame, origin *hookio.HookInput) hookio.RuleResult {
	expr = strings.TrimSpace(expr)
	if r, ok := m.results[expr]; ok {
		return r
	}
	return m.defaultResult
}
```

(add `"strings"` to the test imports.)

- [ ] **Step 2:** `go test ./internal/rules/kubectl/` → FAIL to compile (`too many arguments in call to New`).

- [ ] **Step 3:** In `kubectl.go` change struct + constructor and imports:

```go
import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

type Rule struct {
	exprEval hookio.Evaluator
	pe       *patheval.PathEvaluator
}

func New(eval hookio.Evaluator, pe *patheval.PathEvaluator) *Rule {
	return &Rule{exprEval: eval, pe: pe}
}
```

- [ ] **Step 4:** `internal/setup/factory.go`: `kubectl.New()` → `kubectl.New(eng, pe)`.

- [ ] **Step 5:** `internal/engine/engine_integration_test.go`: in `buildFullEngine`, change `kubectl.New()` → `kubectl.New(eng, pe)` (`eng` and `pe` are already in scope — `docker.New(eng, pe)` is nearby).

- [ ] **Step 6:** `go build ./... && go test ./internal/rules/kubectl/ ./internal/setup/ ./internal/engine/`
      Expected: PASS (behavior unchanged).

- [ ] **Step 7:** Commit: `refactor(kubectl): inject Evaluator+PathEvaluator like docker rule`.

---

### Task A1b: Ask → Abstain for all kubectl own outcomes (foundational)

**Rationale (v3 decision):** the kubectl rule should defer (Abstain) rather than force a prompt (Ask), so the permission mode / settings allow-rules can decide.

- [ ] **Step 1:** In `kubectl_test.go`, rename `TestKubectl_Modifying_Ask` → `TestKubectl_Modifying_Abstain` and `TestKubectl_KubeconfigModifying_Ask` → `TestKubectl_KubeconfigModifying_Abstain`, and change their assertions from `!= hookio.Ask` (want ask) to `!= hookio.Abstain` (want abstain). Keep the same command slices.

- [ ] **Step 2:** `go test ./internal/rules/kubectl/ -run 'Modifying' -v` → FAIL (rule still returns Ask).

- [ ] **Step 3:** In `kubectl.go`, change the two existing `Decision: hookio.Ask, Reason: "modifying kubectl command"` returns to `Decision: hookio.Abstain, Reason: "modifying kubectl command (defer)"`. (After later tasks, the authoritative block in A6 supersedes this; this step keeps the tree green in between.)

- [ ] **Step 4:** `go test ./internal/rules/kubectl/ -v` → PASS.

- [ ] **Step 5:** Commit: `feat(kubectl): abstain (not ask) on modifying commands so mode/settings decide`.

---

### Task A2: Make `extractOperation` flag-value-aware

**Rationale (review arch #4):** today `kubectl -n get delete pod` returns operation `get` (the value of `-n`) → would wrongly approve. New Approve/exec verbs amplify this.

- [ ] **Step 1: Failing test.**

```go
func TestKubectl_FlagValueNotOperation(t *testing.T) {
	r := New(nil, nil)
	// -n's value "get" must NOT be read as the operation; the real op is "delete".
	cmds := []string{
		"kubectl --namespace get delete pod foo",
		"kubectl -n sync delete pod foo",
	}
	for _, cmd := range cmds {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s, want ask (delete is modifying)", cmd, got.Decision)
		}
	}
}
```

- [ ] **Step 2:** `go test ./internal/rules/kubectl/ -run FlagValueNotOperation -v` → FAIL (returns Approve: `get` read-only / would later hit approve maps).

- [ ] **Step 3: Implement.** Replace `extractOperation` with a value-flag-aware version:

```go
// valueFlags consume the following token as their value; that token must not be
// mistaken for the kubectl operation.
var valueFlags = map[string]bool{
	"-n": true, "--namespace": true, "-c": true, "--container": true,
	"-f": true, "--filename": true, "--ws": true, "--workspace": true,
	"--context": true, "--kubeconfig": true, "-o": true, "--output": true,
	"-l": true, "--selector": true,
}

// extractOperation returns the first bare (non-flag, non-flag-value) token
// before any `--`, i.e. the kubectl verb. Returns "" if none.
func extractOperation(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return ""
		}
		if valueFlags[a] {
			i++ // skip the flag's value
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue // bare flag or --flag=value
		}
		return a
	}
	return ""
}
```

- [ ] **Step 4:** `go test ./internal/rules/kubectl/ -v` → PASS (incl. existing tests; `TestKubectl_ReadOnly_Approve` etc. still pass — their flag values don't collide with verbs).

- [ ] **Step 5:** Commit: `fix(kubectl): make extractOperation skip flag values`.

---

### Task A3: Devxp-scope detector

**Files:** `kubectl.go` (+ helpers), `kubectl_test.go`.
**Interfaces produced:** `func isDevWorkspaceScope(args []string, env []cmdparse.EnvAssignment) bool`.

- [ ] **Step 1: Failing test** (pure-function table):

```go
func TestKubectl_IsDevWorkspaceScope(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{"ws d- prefix", "bin/kc exe --ws d-phillipg01 -n mp--ui--customer -c c -- bats", true},
		{"ns d- prefix", "bin/kc exe -n d-phillipgs0-db--sqitch -c c -- shell", true},
		{"ws= form", "bin/kc exe --ws=d-phillipg01 -- bats", true},
		{"aws dev profile + dev ws", "AWS_PROFILE=dev/developers-dev bin/kc exe --ws d-phillipg01 -- bats", true},
		{"no dev signal", "kubectl exec -it pod/foo -- bash", false},
		{"prod namespace", "kubectl exec -n prod pod -- rm -rf /x", false},
		{"prod aws profile overrides dev ws", "AWS_PROFILE=prod/admin bin/kc exe --ws d-phillipg01 -- rm -rf /x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed := cmdparse.Parse(tt.cmd)
			// take the leaf whose executable is kc/kubectl
			var pc cmdparse.ParsedCommand
			for _, p := range parsed {
				if isKubectlExecutable(p.Executable) {
					pc = p
					break
				}
			}
			if got := isDevWorkspaceScope(pc.Args, pc.EnvVars); got != tt.want {
				t.Errorf("%s: got %v want %v", tt.name, got, tt.want)
			}
		})
	}
}
```

(add `"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"` to test imports.)

- [ ] **Step 2:** `go test ./internal/rules/kubectl/ -run IsDevWorkspaceScope` → FAIL (undefined `isDevWorkspaceScope`).

- [ ] **Step 3: Implement.**

```go
// nonDevAWSAccounts are AWS_PROFILE accounts (the part before '/') that name a
// prod/shared cluster; their presence forces a non-dev classification.
var nonDevAWSAccounts = map[string]bool{
	"prod": true, "dprod": true, "euprod": true,
	"build": true, "fastlane": true, "pdx": true, "test": true,
}

// devScopeFlags name a workspace/namespace we can check for the personal-dev prefix.
var devScopeFlags = map[string]bool{
	"--ws": true, "--workspace": true, "-n": true, "--namespace": true,
}

func isPersonalDevName(v string) bool { return strings.HasPrefix(v, "d-") }

// isDevWorkspaceScope reports whether a kc/kubectl invocation targets a personal
// dev workspace (see "Devxp scope" contract).
func isDevWorkspaceScope(args []string, env []cmdparse.EnvAssignment) bool {
	for _, e := range env {
		switch e.Name {
		case "AWS_PROFILE":
			acct := e.Value
			if i := strings.IndexByte(acct, '/'); i >= 0 {
				acct = acct[:i]
			}
			if nonDevAWSAccounts[acct] {
				return false
			}
		case "KC_CLUSTER":
			if e.Value != "" && !strings.HasPrefix(e.Value, "d1-") && !strings.HasPrefix(e.Value, "dd1-") {
				return false
			}
		}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if devScopeFlags[a] && i+1 < len(args) && isPersonalDevName(args[i+1]) {
			return true
		}
		for _, pfx := range []string{"--ws=", "--workspace=", "--namespace="} {
			if strings.HasPrefix(a, pfx) && isPersonalDevName(strings.TrimPrefix(a, pfx)) {
				return true
			}
		}
	}
	return false
}
```

- [ ] **Step 4:** `go test ./internal/rules/kubectl/ -run IsDevWorkspaceScope -v` → PASS.

- [ ] **Step 5:** Commit: `feat(kubectl): add personal dev-workspace scope detector`.

---

### Task A4: Read-only verb additions + rollout status/history

**Interfaces:** extend `readOnlyOperations`; add `rolloutReadOnlySubcommands`, `rolloutSubcommand`.

- [ ] **Step 1: Failing tests.**

```go
func TestKubectl_ReadOnlyAdditions_Approve(t *testing.T) {
	r := New(nil, nil)
	cmds := []string{
		"kubectl events", "kubectl diff -f x.yaml", "kubectl wait --for=condition=Ready pod/foo",
		"bin/kc wslogs -n mp--ui--customer", "bin/kc zrlog -n mp--ui--customer",
		"bin/kc wsfirstpod --ws d-phillipg01",
		"kubectl rollout status deploy/foo", "bin/kc rollout history deploy/foo",
	}
	for _, cmd := range cmds {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestKubectl_RolloutMutating_Ask(t *testing.T) { // regression guard
	r := New(nil, nil)
	for _, cmd := range []string{"kubectl rollout restart deploy/foo", "kubectl rollout undo deploy/foo"} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s, want ask", cmd, got.Decision)
		}
	}
}
```

Also remove `"kubectl rollout restart deployment foo"` from `TestKubectl_Modifying_Ask` (now covered above).

- [ ] **Step 2:** `go test ./internal/rules/kubectl/ -run 'ReadOnlyAdditions|RolloutMutating' -v` → FAIL (new reads return Ask; `rollout status` returns Ask).

- [ ] **Step 3: Implement.** Extend the map; add rollout handling in `Evaluate` (placement in Step-of-A6 final ordering below):

```go
var readOnlyOperations = map[string]bool{
	"get": true, "describe": true, "logs": true, "top": true,
	"cluster-info": true, "config": true, "api-resources": true,
	"api-versions": true, "version": true, "explain": true, "auth": true,
	"events": true, "diff": true, "wait": true,
	"wslogs": true, "zrlog": true, "wsfirstpod": true,
}

var rolloutReadOnlySubcommands = map[string]bool{"status": true, "history": true}

// rolloutSubcommand returns the sub-verb after `rollout` (the second bare token).
func rolloutSubcommand(args []string) string {
	seen := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return ""
		}
		if valueFlags[a] {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		if !seen {
			seen = true
			continue
		}
		return a
	}
	return ""
}
```

Add to `Evaluate` (see A6 for the full ordered block):

```go
		if operation == "rollout" {
			if rolloutReadOnlySubcommands[rolloutSubcommand(pc.Args)] {
				return hookio.RuleResult{Decision: hookio.Approve, Reason: "read-only kubectl command", Module: r.Name()}
			}
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "modifying kubectl command", Module: r.Name()}
		}
```

- [ ] **Step 4:** `go test ./internal/rules/kubectl/ -v` → PASS.

- [ ] **Step 5:** Commit: `feat(kubectl): approve read-only kc plugins, native reads, rollout status/history`.

---

### Task A5: Devxp-scoped approvals for sync/syncdev/workspace

- [ ] **Step 1: Failing tests.**

```go
func TestKubectl_DevxpNative(t *testing.T) {
	r := New(nil, nil)
	approve := []string{
		"AWS_PROFILE=dev/developers-dev bin/kc sync -f mp/ui/customer/layouts/test-runner --ws d-phillipg01",
		"bin/kc workspace list --ws d-phillipg01",
		"bin/kc syncdev --ws d-phillipg01",
	}
	for _, cmd := range approve {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
	// Non-dev scope must NOT be auto-approved.
	ask := []string{
		"bin/kc sync -f x -n prod",
		"AWS_PROFILE=prod/admin bin/kc workspace delete --ws d-phillipg01",
	}
	for _, cmd := range ask {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s, want ask", cmd, got.Decision)
		}
	}
}
```

- [ ] **Step 2:** `go test ./internal/rules/kubectl/ -run DevxpNative -v` → FAIL.

- [ ] **Step 3: Implement.**

```go
// scopedApproveOperations are kc plugin verbs that mutate a dev workspace only;
// auto-approved iff the command targets a personal dev workspace.
var scopedApproveOperations = map[string]bool{
	"sync": true, "syncdev": true, "workspace": true,
}
```

In `Evaluate`:

```go
		if scopedApproveOperations[operation] {
			if isDevWorkspaceScope(pc.Args, pc.EnvVars) {
				return hookio.RuleResult{Decision: hookio.Approve, Reason: "kc dev-workspace command", Module: r.Name()}
			}
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "modifying kubectl command", Module: r.Name()}
		}
```

- [ ] **Step 4:** `go test ./internal/rules/kubectl/ -v` → PASS.

- [ ] **Step 5:** Commit: `feat(kubectl): approve dev-workspace-scoped sync/syncdev/workspace`.

---

### Task A6: Exec-family recursion (dev-scoped only)

**Behavior:** for `exec/exe/shell/wsexec`, if dev-scoped, recurse into the command after `--` through the full rule chain with a pod-internal path evaluator; if there is no inner command → `Abstain`. If NOT dev-scoped → today's `Ask`.

- [ ] **Step 1: Failing tests** (mock evaluator):

```go
func TestKubectl_ExecRecursion(t *testing.T) {
	mockEval := &mockEvaluator{
		results: map[string]hookio.RuleResult{
			"bats":                              {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
			"prove -v t/foo.t":                  {Decision: hookio.Approve, Reason: "ok", Module: "mock"},
			"shell zr-sqitch deploy zr_finance": {Decision: hookio.Ask, Reason: "unknown", Module: "mock"},
		},
		defaultResult: hookio.RuleResult{Decision: hookio.Abstain, Module: "mock"},
	}
	r := New(mockEval, nil)
	tests := []struct {
		name, command string
		want          hookio.Decision
	}{
		{"dev exe safe inner", "bin/kc exe --ws d-phillipg01 -n mp--ui--customer -c test-runner -- bats", hookio.Approve},
		{"dev shell bash -c inner", "bin/kc shell --ws d-phillipg01 -n X -c test-runner -- bash -c 'prove -v t/foo.t'", hookio.Approve},
		{"dev exe sqitch inner asks", "bin/kc exe -n d-phillipgs0-db--sqitch -c sqitch-ui -- shell zr-sqitch deploy zr_finance", hookio.Ask},
		{"NON-dev exec stays ask", "kubectl exec -n prod pod -- rm -rf /var/lib/data", hookio.Ask},
		{"NON-dev exec no ns stays ask", "kubectl exec -it pod/foo -- bash", hookio.Ask},
		{"dev exe no double-dash abstains", "bin/kc exe --ws d-phillipg01 -c test-runner", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.command})}
			if got := r.Evaluate(input); got.Decision != tt.want {
				t.Errorf("cmd %q: got %s want %s (reason %q)", tt.command, got.Decision, tt.want, got.Reason)
			}
		})
	}
}
```

Also remove `"kubectl exec -it pod/foo -- bash"` from `TestKubectl_Modifying_Ask` (its expected decision is still `Ask` here, but via the non-dev exec branch, not the generic modifying branch — keep the assertion in `TestKubectl_ExecRecursion` where it is meaningful; leaving it in `Modifying_Ask` would still pass but is redundant).

- [ ] **Step 2:** `go test ./internal/rules/kubectl/ -run ExecRecursion -v` → FAIL.

- [ ] **Step 3: Implement.** Add the exec set + helpers, and assemble the FULL ordered classification block in `Evaluate`'s loop (this supersedes the individual snippets above — this is the authoritative body after `operation := extractOperation(pc.Args)`):

```go
var execOperations = map[string]bool{"exec": true, "exe": true, "shell": true, "wsexec": true}
```

Inside `for _, pc := range parsed { if !isKubectlExecutable(pc.Executable) { continue }`:

```go
		operation := extractOperation(pc.Args)
		if operation == "" {
			return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
		}
		if execOperations[operation] {
			if isDevWorkspaceScope(pc.Args, pc.EnvVars) {
				return r.evaluateExec(pc.Args, input)
			}
			return hookio.RuleResult{Decision: hookio.Abstain, Reason: "non-dev kubectl exec (defer to mode/settings)", Module: r.Name()}
		}
		if operation == "rollout" {
			if rolloutReadOnlySubcommands[rolloutSubcommand(pc.Args)] {
				return hookio.RuleResult{Decision: hookio.Approve, Reason: "read-only kubectl command", Module: r.Name()}
			}
			return hookio.RuleResult{Decision: hookio.Abstain, Reason: "modifying kubectl command (defer)", Module: r.Name()}
		}
		if scopedApproveOperations[operation] {
			if isDevWorkspaceScope(pc.Args, pc.EnvVars) {
				return hookio.RuleResult{Decision: hookio.Approve, Reason: "kc dev-workspace command", Module: r.Name()}
			}
			return hookio.RuleResult{Decision: hookio.Abstain, Reason: "non-dev kc command (defer)", Module: r.Name()}
		}
		if readOnlyOperations[operation] {
			return hookio.RuleResult{Decision: hookio.Approve, Reason: "read-only kubectl command", Module: r.Name()}
		}
		return hookio.RuleResult{Decision: hookio.Abstain, Reason: "modifying kubectl command (defer)", Module: r.Name()}
```

Helpers (patterned on docker's `evaluateExec`/`extractInnerCommand`):

```go
func (r *Rule) evaluateExec(args []string, input *hookio.HookInput) hookio.RuleResult {
	inner := innerAfterDoubleDash(args)
	if len(inner) == 0 {
		return hookio.RuleResult{Decision: hookio.Abstain, Reason: "kc exec without inner command", Module: r.Name()}
	}
	if r.exprEval == nil {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	innerExpr := extractInnerCommand(inner)
	outerExpr := strings.Join(strings.Fields(strings.Join(args, " ")), " ")
	stack := []hookio.StackFrame{{RuleName: r.Name(), Command: "kc exec", Expression: outerExpr}}
	scoped := *input
	if r.pe != nil {
		scoped.PathEval = r.pe.WithMounts([]patheval.Mount{}) // pod-internal paths
	}
	return r.exprEval.EvaluateExpression(innerExpr, stack, &scoped)
}

func innerAfterDoubleDash(args []string) []string {
	for i, a := range args {
		if a == "--" {
			return args[i+1:]
		}
	}
	return nil
}

func extractInnerCommand(cmdArgs []string) string {
	if len(cmdArgs) >= 3 && (cmdArgs[0] == "bash" || cmdArgs[0] == "sh") && cmdArgs[1] == "-c" {
		return strings.Join(cmdArgs[2:], " ")
	}
	return strings.Join(cmdArgs, " ")
}
```

- [ ] **Step 4:** `go test ./internal/rules/kubectl/ -v && go build ./... && go vet ./internal/rules/kubectl/` → PASS/clean.

- [ ] **Step 5:** Commit: `feat(kubectl): recurse into dev-scoped exec-family inner command`.

---

### Task A7: Approve `prove` (and `yath`) in buildtools

**Rationale:** test-runner rows (`kc shell -- bash -c 'prove …'`) only stop prompting once the inner `prove` resolves to Approve.

- [ ] **Step 1: Failing test** in `buildtools_test.go` (match existing table style):

```go
func TestBuildTools_Prove(t *testing.T) {
	r := New()
	for _, cmd := range []string{"prove -v t/foo.t", "mp/ui/customer/bin/devxp/prove t/bar.t", "yath test"} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})}
		if got := r.Evaluate(input); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s want approve", cmd, got.Decision)
		}
	}
}
```

(If `buildtools_test.go` lacks `mustJSON`, copy the 4-line helper from `kubectl_test.go`.)

- [ ] **Step 2:** `go test ./internal/rules/buildtools/ -run Prove -v` → FAIL.

- [ ] **Step 3: Implement.** Add to `approvedTools`: `"prove": true, "yath": true,`.

- [ ] **Step 4:** `go test ./internal/rules/buildtools/ -v` → PASS.

- [ ] **Step 5:** Commit: `feat(buildtools): approve prove/yath test runners`.

---

### Task A8: Engine-level integration tests (real chain)

**Rationale (all reviewers):** unit tests mock the evaluator; nothing proves preemption doesn't happen, that the sqitch guard survives end-to-end, or that prod-exec is not approved.

- [ ] **Step 1: Add cases** to `internal/engine/engine_integration_test.go` driving `eng.EvaluateHook(...)` (use the existing helper/harness in that file; CWD = a project root the harness already sets up). Add a test:

```go
func TestIntegration_KcRules(t *testing.T) {
	eng := buildFullEngine(t) // existing helper
	cases := []struct {
		name, cmd string
		want      hookio.Decision
	}{
		{"dev exe bats approve", "AWS_PROFILE=dev/developers-dev bin/kc exe --ws d-phillipg01 -n mp--ui--customer -c test-runner -- bats", hookio.Approve},
		{"dev shell prove approve", "AWS_PROFILE=dev/developers-dev bin/kc shell --ws d-phillipg01 -n X -c test-runner -- bash -c 'prove -v t/foo.t'", hookio.Approve},
		{"dev sqitch guard prompts", "bin/kc exe -n d-phillipgs0-db--sqitch -c sqitch-ui -- shell zr-sqitch deploy zr_finance", hookio.Abstain},
		{"prod exec NOT approved", "kubectl exec -n prod pod -- rm -rf /var/lib/data", hookio.Ask},
		{"kc get approve", "bin/kc get pods -n mp--ui--customer", hookio.Approve},
		{"rollout restart ask", "kubectl rollout restart deploy/foo", hookio.Ask},
		{"compound cd+export+exe", "cd " + projectRootForTest(t) + " && export PATH=/x && AWS_PROFILE=dev/developers-dev bin/kc exe --ws d-phillipg01 -c c -- bats", hookio.Approve},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := &hookio.HookInput{ToolName: "Bash", CWD: projectRootForTest(t), ToolInput: mustJSON(map[string]string{"command": tc.cmd})}
			if got := eng.EvaluateHook(in); got.Decision != tc.want {
				t.Errorf("%s: got %s want %s (%s)", tc.name, got.Decision, tc.want, got.Reason)
			}
		})
	}
}
```

**Implementer note:** align `buildFullEngine`, `projectRootForTest`, and `mustJSON` to whatever the existing file actually names them (read the file first; adapt helper names). The `compound` and `dev shell prove` cases depend on A7 having landed (`prove` approved) and on `bats` being approved by buildtools (it already is).

- [ ] **Step 2:** `go test ./internal/engine/ -run TestIntegration_KcRules -v`. Expected initially: the sqitch/prod/rollout cases should already pass after A2–A6; the `dev shell prove` case passes only after A7. Fix any helper-name mismatches until green.

- [ ] **Step 3:** Commit: `test(engine): real-chain integration coverage for kc rules`.

---

## PART B — command-substitution allowlist (broader + secret recheck)

### Task B1: Widen `safeCmdSubstitutions` with git-metadata verbs, guarded `go env`, and secret-rechecked readers

**Files:** `cmdparse/parser.go` (imports `secretpath`; new `isSafeSubstitutionCommand`), `cmdparse/parser_test.go`.

- [ ] **Step 1: Failing tests** — add to the `TestHasUnsafeCommandSubstitution` table (`want==false` means safe):

```go
		// pure utils + guarded go env + git metadata (safe → false)
		{"$(go env GOMODCACHE)", false},
		{"$(git rev-parse --show-toplevel)", false},
		{"$(git symbolic-ref --short HEAD)", false},
		{"$(git merge-base main HEAD)", false},
		{"$(uname -m)", false},
		{"$(readlink -f /x)", false},
		{"`git rev-parse HEAD`", false},
		// file-readers, secret-rechecked (non-secret path → safe)
		{"$(cat VERSION)", false},
		{"$(grep -c foo bar.txt)", false},
		{"$(head -1 go.mod)", false},
		// file-readers on SECRET paths → unsafe (guard preserved)
		{"$(cat .env)", true},
		{"$(cat secrets/prod.yaml)", true},
		{"$(cat ~/.ssh/id_rsa)", true},
		// mutating / RCE forms stay unsafe
		{"$(go env -w GOPROXY=https://evil)", true},
		{"$(go build ./...)", true},
		{"$(git push origin main)", true},
		{"$(git show HEAD)", true},   // excluded: textconv/external-diff RCE
		{"$(git diff)", true},        // excluded
		{"$(find . -delete)", true},
```

- [ ] **Step 2:** `go test ./internal/cmdparse/ -run TestHasUnsafeCommandSubstitution -v` → FAIL (the safe ones currently classify unsafe).

- [ ] **Step 3: Implement.** Add `import "github.com/phillipgreenii/claude-extended-tool-approver/internal/secretpath"`. Replace the `safeCmdSubstitutions` map and add the helper:

```go
// safeCmdSubstitutions: commands that never mutate and never read a file, safe
// inside $(...) regardless of arguments.
var safeCmdSubstitutions = map[string]bool{
	"mktemp": true, "date": true, "whoami": true, "id": true,
	"pwd": true, "basename": true, "dirname": true,
	"readlink": true, "realpath": true, "uname": true,
	"echo": true, "printf": true,
}

// fileReaderSubstitutions: read-only readers whose PATH ARGS must be re-checked
// against secretpath so a $(cat .env) still forces a prompt.
var fileReaderSubstitutions = map[string]bool{
	"cat": true, "grep": true, "head": true, "tail": true, "wc": true, "ls": true,
}

// gitReadSubcommands: git subcommands that only read metadata (no diff/show/log —
// those honor textconv/external-diff, an RCE surface a hook cannot neutralize).
var gitReadSubcommands = map[string]bool{
	"rev-parse": true, "rev-list": true, "symbolic-ref": true,
	"merge-base": true, "describe": true, "status": true,
}

func isSafeSubstitutionCommand(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	cmd := tokens[0]
	if safeCmdSubstitutions[cmd] {
		return true
	}
	if cmd == "hostname" && len(tokens) == 1 { // bare hostname reads; `hostname X` sets it
		return true
	}
	if cmd == "go" && len(tokens) >= 2 && tokens[1] == "env" {
		for _, t := range tokens[2:] { // go env -w/-u mutate persistent config
			if t == "-w" || t == "-u" {
				return false
			}
		}
		return true
	}
	if cmd == "git" && len(tokens) >= 2 && gitReadSubcommands[tokens[1]] {
		return true
	}
	if fileReaderSubstitutions[cmd] {
		for _, t := range tokens[1:] {
			if strings.HasPrefix(t, "-") {
				continue
			}
			if secretpath.IsSecret(t) {
				return false // reading a secret → force a prompt
			}
		}
		return true
	}
	return false
}
```

Then in BOTH `classifyCmdSubstitution` and `classifyBacktickSubstitution` replace `if safeCmdSubstitutions[tokens[0]] {` … with `if isSafeSubstitutionCommand(tokens) {`.

- [ ] **Step 4:** `go test ./internal/cmdparse/ -v` → PASS (new + existing; composition cases like `$(mktemp)$(curl evil)` still `true`).

- [ ] **Step 5: Cross-rule coverage** — add one `envvars` case proving the env-prefix path is intentional. In `internal/rules/envvars/envvars_test.go`, assert `FOO=$(git rev-parse HEAD) make` no longer abstains on the assignment (whatever the rule's approve/abstain contract is — read the file and add the matching assertion).

- [ ] **Step 6:** `go test ./internal/cmdparse/ ./internal/rules/envvars/ ./internal/engine/ -v` → PASS.

- [ ] **Step 7:** Commit: `feat(cmdparse): widen safe command-substitutions (git-metadata, go env, secret-rechecked readers)`.

---

## Final Verification (after all tasks)

- [ ] `go test ./...` → all PASS. `go vet ./...` → clean.
- [ ] Replay sanity (read-only) against the real log exemplars:
  - `301185` (`AWS_PROFILE=dev/… kc sync … --ws d-phillipg01`) → Approve
  - `301065`/`300181` (`kc shell … -- bash -c '…prove…'`, dev scope) → Approve (needs A7)
  - `17136`/`31347`/`38192` (`kc exe -n d-…-sqitch … -- shell zr-sqitch deploy`) → Abstain (still prompts — guard preserved)
  - a synthetic `kubectl exec -n prod … -- rm -rf /x` → Ask (NOT approved)
  - `299713`/`299002` (`$(go env …)`, `$(git rev-parse …)`) → no substitution-driven Abstain; `$(cat .env)` still prompts
- [ ] `nix flake check` passes; `prek run --all-files` (or `pre-commit`) passes.
- [ ] Update/close `bd` beads.

## Deferred (future bead)

- `kc cp` (local-path pathsafety + pod-side scope) and mutating `rollout` devxp-approval — need the scope detector (now built in A3) extended to `cp`'s `pod:/path` argument grammar; land separately.

## Self-Review Notes

- Ordering in `Evaluate` loop: `operation==""` → exec-family (dev-gated) → rollout (sub-verb) → scoped-approve (dev-gated) → read-only → else Ask. Verified disjoint verb sets.
- `extractOperation` and `rolloutSubcommand` both use `valueFlags` to skip flag values.
- Scope detector is the most heuristic component; its table test (A3) plus the prod-exec integration case (A8) pin the security-critical branches.
- Substitution: git `show/diff/log` deliberately excluded (textconv RCE); `go env` gated on `-w/-u`; readers gated by `secretpath.IsSecret`; `hostname` only bare.
