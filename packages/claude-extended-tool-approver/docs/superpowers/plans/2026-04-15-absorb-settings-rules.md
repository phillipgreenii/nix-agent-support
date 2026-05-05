# Absorb 8 Settings Rules Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move 8 `settings.local.json` permission rules into the Go rule engine, giving the engine proper path-aware and context-aware control over when to approve.

**Architecture:** Each change modifies one Go rule module (safecmds, buildtools, git, or sqlite3), adds tests, runs `evaluate` to confirm no regressions, removes the settings entry, commits, and closes the bead. Changes are independent and ordered easiest-first.

**Tech Stack:** Go, `jq` for settings.local.json manipulation

**Baseline evaluate metrics (with settings):**

- `Total rows: 29165`
- `Correct: 24882`
- `Misses (settings): 18`
- `Misses (uncaught): 1352`

**Project root:** `~/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver/`

**Settings file:** `~/phillipg_mbp/.claude/settings.local.json`

**Important:** All `go test` and `evaluate` commands must be run with `unset GOEXPERIMENT` prepended (the env var interferes with the Go toolchain).

---

### Task 1: contained-claude (pg2-2xpv)

**Files:**

- Modify: `internal/rules/safecmds/safecmds.go:13-27` (alwaysSafe map)
- Modify: `internal/rules/safecmds/safecmds_test.go` (add test)
- Modify: `~/phillipg_mbp/.claude/settings.local.json` (remove rule)

- [ ] **Step 1: Write the failing test**

Add to `internal/rules/safecmds/safecmds_test.go`:

```go
func TestSafecmds_ContainedClaude_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:       "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "contained-claude --version 2>&1"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("contained-claude --version: got %s, want approve", got.Decision)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && unset GOEXPERIMENT && go test ./internal/rules/safecmds/ -run TestSafecmds_ContainedClaude -v`

Expected: FAIL — `contained-claude` not in `alwaysSafe`

- [ ] **Step 3: Add contained-claude to alwaysSafe**

In `internal/rules/safecmds/safecmds.go`, add `"contained-claude"` to the `alwaysSafe` map. Place it after the `"claude-extended-tool-approver": true, "claude-pretool-hook": true,` line:

```go
	"claude-extended-tool-approver": true, "claude-pretool-hook": true,
	"shellcheck": true, "colima": true, "contained-claude": true,
	"my-code-review-support-cli": true,
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && unset GOEXPERIMENT && go test ./internal/rules/safecmds/ -run TestSafecmds_ContainedClaude -v`

Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && unset GOEXPERIMENT && go test ./...`

Expected: All PASS

- [ ] **Step 6: Run evaluate**

Run: `unset GOEXPERIMENT && claude-extended-tool-approver evaluate --settings=$HOME/phillipg_mbp/.claude/settings.local.json --format=summary 2>&1 | tail -10`

Expected: `Misses (uncaught)` must not increase from 1352. `Misses (settings)` may decrease (good — the Go engine now catches what settings caught).

- [ ] **Step 7: Remove settings rule**

Run: `jq '.permissions.allow |= map(select(. != "Bash(contained-claude:*)"))' ~/phillipg_mbp/.claude/settings.local.json > /tmp/settings-tmp.json && mv /tmp/settings-tmp.json ~/phillipg_mbp/.claude/settings.local.json`

Verify: `jq '.permissions.allow[]' ~/phillipg_mbp/.claude/settings.local.json | grep -c contained-claude` should return 0.

- [ ] **Step 8: Commit**

```bash
cd ~/phillipg_mbp/phillipgreenii-nix-support-apps
git add packages/claude-extended-tool-approver/internal/rules/safecmds/safecmds.go packages/claude-extended-tool-approver/internal/rules/safecmds/safecmds_test.go
git commit -m "feat(safecmds): add contained-claude to alwaysSafe

contained-claude is a Docker-wrapped equivalent of the claude CLI
with identical surface. Add to alwaysSafe alongside existing claude
entry."
```

Note: `settings.local.json` is outside the repo — no need to stage it.

- [ ] **Step 9: Close bead**

Run: `bd close pg2-2xpv --reason="contained-claude added to alwaysSafe, settings rule removed"`

---

### Task 2: bash -n syntax check (pg2-81ei)

**Files:**

- Modify: `internal/rules/safecmds/safecmds.go` (add bash -n handling)
- Modify: `internal/rules/safecmds/safecmds_test.go` (add tests)
- Modify: `~/phillipg_mbp/.claude/settings.local.json` (remove rule)

- [ ] **Step 1: Write the failing tests**

Add to `internal/rules/safecmds/safecmds_test.go`:

```go
func TestSafecmds_BashSyntaxCheck(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"bash -n readable file", "bash -n /home/user/project/script.sh", hookio.Approve},
		{"bash -n readable file with echo", `bash -n /home/user/project/script.sh && echo "OK"`, hookio.Approve},
		{"bash -n nix store file", "bash -n /nix/store/abc123/script.sh", hookio.Approve},
		{"bash -n unknown path", "bash -n /etc/secret.sh", hookio.Abstain},
		{"sh -n readable file", "sh -n /home/user/project/script.sh", hookio.Approve},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
			}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (reason: %s)", got.Decision, tt.want, got.Reason)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && unset GOEXPERIMENT && go test ./internal/rules/safecmds/ -run TestSafecmds_BashSyntaxCheck -v`

Expected: FAIL — `bash` is not in any safe-command list

- [ ] **Step 3: Add bash -n handling**

In `internal/rules/safecmds/safecmds.go`, add a new block after the `// xargs:` block (after line 180) and before the `// jar:` block. Insert it just before the jar check:

```go
		// bash/sh -n: syntax check only, no execution — safe read command
		if (basename == "bash" || basename == "sh") && hasBashSyntaxCheckFlag(pc.Args) {
			fileArgs := extractBashSyntaxCheckFiles(pc.Args)
			if unsafe, path := hasUnsafeReadPath(fileArgs, pe); unsafe {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: " + basename + " -n references unknown path " + path + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			continue
		}
```

Then add these helper functions at the bottom of the file:

```go
// hasBashSyntaxCheckFlag returns true if args contain -n as a standalone flag.
func hasBashSyntaxCheckFlag(args []string) bool {
	for _, a := range args {
		if a == "-n" {
			return true
		}
	}
	return false
}

// extractBashSyntaxCheckFiles extracts file path arguments from bash -n args,
// skipping flags. Returns only path-like arguments for validation.
func extractBashSyntaxCheckFiles(args []string) []string {
	var files []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		files = append(files, a)
	}
	return files
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && unset GOEXPERIMENT && go test ./internal/rules/safecmds/ -run TestSafecmds_BashSyntaxCheck -v`

Expected: All PASS

- [ ] **Step 5: Run full test suite**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && unset GOEXPERIMENT && go test ./...`

Expected: All PASS

- [ ] **Step 6: Run evaluate**

Run: `unset GOEXPERIMENT && claude-extended-tool-approver evaluate --settings=$HOME/phillipg_mbp/.claude/settings.local.json --format=summary 2>&1 | tail -10`

Expected: `Misses (uncaught)` must not increase from baseline.

- [ ] **Step 7: Remove settings rule**

Run: `jq '.permissions.allow |= map(select(. | test("^Bash\\(bash -n ") | not))' ~/phillipg_mbp/.claude/settings.local.json > /tmp/settings-tmp.json && mv /tmp/settings-tmp.json ~/phillipg_mbp/.claude/settings.local.json`

Verify: `jq '.permissions.allow[]' ~/phillipg_mbp/.claude/settings.local.json | grep -c "bash -n"` should return 0.

- [ ] **Step 8: Commit**

```bash
cd ~/phillipg_mbp/phillipgreenii-nix-support-apps
git add packages/claude-extended-tool-approver/internal/rules/safecmds/safecmds.go packages/claude-extended-tool-approver/internal/rules/safecmds/safecmds_test.go
git commit -m "feat(safecmds): add bash/sh -n syntax check as safe read command

bash -n parses syntax without executing — no side effects, no command
substitutions, no traps. Treat the file argument as read-only input."
```

- [ ] **Step 9: Close bead**

Run: `bd close pg2-81ei --reason="bash -n handling added to safecmds, settings rule removed"`

---

### Task 3: cue vet (pg2-vblf)

**Files:**

- Modify: `internal/rules/buildtools/buildtools.go` (add cue vet handling)
- Modify: `internal/rules/buildtools/buildtools_test.go` (add tests)
- Modify: `~/phillipg_mbp/.claude/settings.local.json` (remove rule)

- [ ] **Step 1: Write the failing tests**

Add to `internal/rules/buildtools/buildtools_test.go`:

```go
func TestBuildtools_CueVet(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"cue vet approve", "cue vet ./schemas/ 2>&1", hookio.Approve},
		{"cue vet with path", "cue vet ./common/schemas/", hookio.Approve},
		{"cue export abstain", "cue export ./schemas/", hookio.Abstain},
		{"cue eval abstain", "cue eval ./schemas/", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.command})}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v", got.Decision, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && unset GOEXPERIMENT && go test ./internal/rules/buildtools/ -run TestBuildtools_CueVet -v`

Expected: FAIL — `cue` not handled

- [ ] **Step 3: Add cue vet handling**

In `internal/rules/buildtools/buildtools.go`, add after the `devbox` block (after line 56):

```go
		if basename == "cue" && hasSubcommand(pc.Args, "vet") {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "cue vet is approved (read-only validation)",
				Module:   r.Name(),
			}
		}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && unset GOEXPERIMENT && go test ./internal/rules/buildtools/ -run TestBuildtools_CueVet -v`

Expected: All PASS

- [ ] **Step 5: Run full test suite**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && unset GOEXPERIMENT && go test ./...`

Expected: All PASS

- [ ] **Step 6: Run evaluate**

Run: `unset GOEXPERIMENT && claude-extended-tool-approver evaluate --settings=$HOME/phillipg_mbp/.claude/settings.local.json --format=summary 2>&1 | tail -10`

Expected: `Misses (uncaught)` must not increase from baseline.

- [ ] **Step 7: Remove settings rule**

Run: `jq '.permissions.allow |= map(select(. != "Bash(cue vet:*)"))' ~/phillipg_mbp/.claude/settings.local.json > /tmp/settings-tmp.json && mv /tmp/settings-tmp.json ~/phillipg_mbp/.claude/settings.local.json`

Verify: `jq '.permissions.allow[]' ~/phillipg_mbp/.claude/settings.local.json | grep -c "cue vet"` should return 0.

- [ ] **Step 8: Commit**

```bash
cd ~/phillipg_mbp/phillipgreenii-nix-support-apps
git add packages/claude-extended-tool-approver/internal/rules/buildtools/buildtools.go packages/claude-extended-tool-approver/internal/rules/buildtools/buildtools_test.go
git commit -m "feat(buildtools): add cue vet as approved build tool

cue vet is read-only schema validation, safe to auto-approve."
```

- [ ] **Step 9: Close bead**

Run: `bd close pg2-vblf --reason="cue vet added to buildtools, settings rule removed"`

---

### Task 4: yq verification (pg2-boqw)

**Files:**

- Possibly modify: `~/phillipg_mbp/.claude/settings.local.json` (remove rule)

yq is already handled by safecmds. The sample rows show `hook=(empty)` — the hook wasn't deployed when those decisions were logged. This task is verification only.

- [ ] **Step 1: Run evaluate and check yq-specific results**

Run: `unset GOEXPERIMENT && claude-extended-tool-approver evaluate --settings=$HOME/phillipg_mbp/.claude/settings.local.json --format=json 2>&1 | jq '[.[] | select(.tool_summary | test("^yq "))]'`

If the results show `category: "correct"` for yq rows, the engine already handles them. If they show `category: "miss-caught-by-settings"`, the engine is not matching — investigate why.

- [ ] **Step 2: Check path evaluation for sample paths**

If yq rows are misses, check path evaluation. The sample path is `~/.colima/default/colima.yaml`. Run:

`unset GOEXPERIMENT && claude-extended-tool-approver show 906 1032 1045 --format=json 2>&1 | jq '.[].tool_summary'`

If the path is `PathUnknown` (not in any zone), the abstain is correct — the settings rule was overly broad.

- [ ] **Step 3: Decision and settings removal**

Based on investigate results:

- If yq rows are `correct`: remove the settings rule with `jq '.permissions.allow |= map(select(. != "Bash(yq:*)"))' ~/phillipg_mbp/.claude/settings.local.json > /tmp/settings-tmp.json && mv /tmp/settings-tmp.json ~/phillipg_mbp/.claude/settings.local.json`
- If yq rows are misses due to `PathUnknown`: the settings rule was overly broad. Remove it anyway — abstain is the correct behavior for unknown paths. The rule was hiding the fact that these paths weren't in any zone.
- If yq rows are misses for a different reason: investigate and fix.

- [ ] **Step 4: Commit (if settings rule removed)**

```bash
cd ~/phillipg_mbp/phillipgreenii-nix-support-apps
# Only if Go code changed:
# git add packages/claude-extended-tool-approver/internal/rules/safecmds/safecmds.go
# git commit -m "fix(safecmds): ..."
```

Note: If only the settings.local.json was removed (no Go code changes), no commit needed in the repo.

- [ ] **Step 5: Close bead**

Run: `bd close pg2-boqw --reason="<reason based on findings>"`

---

### Task 5: unzip (pg2-lquv)

**Files:**

- Modify: `internal/rules/safecmds/safecmds.go` (add unzip handling)
- Modify: `internal/rules/safecmds/safecmds_test.go` (add tests)

No settings rule to remove — unzip wasn't in settings.

- [ ] **Step 1: Write the failing tests**

Add to `internal/rules/safecmds/safecmds_test.go`:

```go
func TestSafecmds_Unzip(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"unzip readable archive in writable cwd", "unzip /home/user/project/archive.zip", hookio.Approve},
		{"unzip -d writable dest", "unzip -d /tmp /home/user/project/archive.zip", hookio.Approve},
		{"unzip -d writable dest reversed args", "unzip /home/user/project/archive.zip -d /tmp", hookio.Approve},
		{"unzip -l list only", "unzip -l /home/user/project/archive.zip", hookio.Approve},
		{"unzip -t test only", "unzip -t /home/user/project/archive.zip", hookio.Approve},
		{"unzip -l list from nix store", "unzip -l /nix/store/abc123/archive.zip", hookio.Approve},
		{"unzip unknown archive", "unzip /etc/secret.zip", hookio.Abstain},
		{"unzip -d unknown dest", "unzip -d /etc/somewhere /home/user/project/archive.zip", hookio.Abstain},
		{"unzip readable archive to nix store", "unzip -d /nix/store/abc123 /home/user/project/archive.zip", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
			}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (reason: %s)", got.Decision, tt.want, got.Reason)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && unset GOEXPERIMENT && go test ./internal/rules/safecmds/ -run TestSafecmds_Unzip -v`

Expected: FAIL — `unzip` not handled

- [ ] **Step 3: Add unzip handling**

In `internal/rules/safecmds/safecmds.go`, add after the bash -n block (before the `// jar:` block):

```go
		// unzip: read archive, optionally write to -d destination or cwd
		if basename == "unzip" {
			result := evaluateUnzip(pc.Args, pe, cwd, r.Name())
			if result.Decision != hookio.Approve {
				return result
			}
			continue
		}
```

Then add the helper function at the bottom of the file:

```go
// unzipValueFlags lists unzip flags that consume the next argument.
var unzipValueFlags = map[string]bool{
	"-d": true, "-x": true, "-P": true,
}

// evaluateUnzip handles unzip with archive (read) and destination (write) semantics.
func evaluateUnzip(args []string, pe *patheval.PathEvaluator, cwd string, module string) hookio.RuleResult {
	var archivePath, destDir string
	readOnly := false // -l or -t means list/test only — no extraction

	i := 0
	for i < len(args) {
		a := args[i]
		if a == "-l" || a == "-t" {
			readOnly = true
			i++
			continue
		}
		if a == "-d" && i+1 < len(args) {
			destDir = args[i+1]
			i += 2
			continue
		}
		if unzipValueFlags[a] && i+1 < len(args) {
			i += 2
			continue
		}
		if strings.HasPrefix(a, "-") {
			i++
			continue
		}
		if archivePath == "" {
			archivePath = a
		}
		i++
	}

	// Validate archive path is readable
	if archivePath != "" && looksLikePath(archivePath) {
		if !pe.Evaluate(archivePath).CanRead() {
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "safe-commands: unzip archive references unknown path " + archivePath + " (deferred to claude-code)",
				Module:   module,
			}
		}
	}

	// For read-only operations (-l, -t), no write check needed
	if readOnly {
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: unzip read-only operation", Module: module}
	}

	// Validate write destination
	writeDest := destDir
	if writeDest == "" {
		writeDest = cwd
	}
	if looksLikePath(writeDest) && !pe.Evaluate(writeDest).CanWrite() {
		return hookio.RuleResult{
			Decision: hookio.Abstain,
			Reason:   "safe-commands: unzip destination is not writable " + writeDest + " (deferred to claude-code)",
			Module:   module,
		}
	}

	return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: unzip with known paths", Module: module}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && unset GOEXPERIMENT && go test ./internal/rules/safecmds/ -run TestSafecmds_Unzip -v`

Expected: All PASS

- [ ] **Step 5: Run full test suite**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && unset GOEXPERIMENT && go test ./...`

Expected: All PASS

- [ ] **Step 6: Run evaluate**

Run: `unset GOEXPERIMENT && claude-extended-tool-approver evaluate --settings=$HOME/phillipg_mbp/.claude/settings.local.json --format=summary 2>&1 | tail -10`

Expected: `Misses (uncaught)` must not increase from baseline.

- [ ] **Step 7: Commit**

```bash
cd ~/phillipg_mbp/phillipgreenii-nix-support-apps
git add packages/claude-extended-tool-approver/internal/rules/safecmds/safecmds.go packages/claude-extended-tool-approver/internal/rules/safecmds/safecmds_test.go
git commit -m "feat(safecmds): add unzip with archive read and destination write checks

Validates archive path as readable, output destination as writable.
Supports -d flag for explicit destination, -l/-t for read-only
list/test operations."
```

- [ ] **Step 8: Close bead**

Run: `bd close pg2-lquv --reason="unzip handling added to safecmds with path validation"`

---

### Task 6: chmod investigation (pg2-47gs)

**Files:**

- Possibly modify: `internal/rules/safecmds/safecmds.go`
- Modify: `~/phillipg_mbp/.claude/settings.local.json` (remove rule)

chmod is already in `safeWriteCmds`. The sample rows abstain — need to investigate why.

- [ ] **Step 1: Investigate the specific rows**

Run: `unset GOEXPERIMENT && claude-extended-tool-approver show 4607 26520 --format=json 2>&1 | jq '.[].tool_summary'`

This shows the exact commands. Check if they are compound commands (with `&&` chains).

- [ ] **Step 2: Run evaluate on chmod-specific rows**

Run: `unset GOEXPERIMENT && claude-extended-tool-approver evaluate --settings=$HOME/phillipg_mbp/.claude/settings.local.json --format=json 2>&1 | jq '[.[] | select(.tool_summary | test("^chmod"))]'`

Check the `category` and `hook_decision` fields.

- [ ] **Step 3: Analyze and decide**

Based on the investigation:

- **If compound command causes abstain** (e.g., `chmod +x ... && .../bin/wf`): The current behavior is correct. The engine approves `chmod` but the chained unknown command causes abstain. The settings rule was overly permissive. Remove the settings rule.
- **If path is not recognized as writable**: Fix the path evaluation or add the path zone.

- [ ] **Step 4: Remove settings rule**

Run: `jq '.permissions.allow |= map(select(. != "Bash(chmod:*)"))' ~/phillipg_mbp/.claude/settings.local.json > /tmp/settings-tmp.json && mv /tmp/settings-tmp.json ~/phillipg_mbp/.claude/settings.local.json`

Verify: `jq '.permissions.allow[]' ~/phillipg_mbp/.claude/settings.local.json | grep -c "chmod"` should return 0.

- [ ] **Step 5: Commit (only if Go code changed)**

```bash
cd ~/phillipg_mbp/phillipgreenii-nix-support-apps
# Only if Go code was modified:
# git add packages/claude-extended-tool-approver/internal/rules/safecmds/safecmds.go
# git commit -m "fix(safecmds): ..."
```

- [ ] **Step 6: Close bead**

Run: `bd close pg2-47gs --reason="<reason based on findings>"`

---

### Task 7: git checkout (pg2-55vq)

**Files:**

- Modify: `internal/rules/git/git.go:81-89` (checkout handling)
- Modify: `internal/rules/git/git_test.go` (update existing test, add new tests)
- Modify: `~/phillipg_mbp/.claude/settings.local.json` (remove rule)

- [ ] **Step 1: Update the existing test expectation**

In `internal/rules/git/git_test.go`, change `TestGit_CheckoutDot_Abstain` to expect Approve:

Replace the function name and body:

```go
func TestGit_CheckoutDot_Approve(t *testing.T) {
	approve := []string{
		"git checkout .",
		"git checkout -- .",
	}
	r := New()
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && unset GOEXPERIMENT && go test ./internal/rules/git/ -run TestGit_CheckoutDot_Approve -v`

Expected: FAIL — `git checkout .` currently returns Abstain

- [ ] **Step 3: Change checkout . to Approve**

In `internal/rules/git/git.go`, replace the checkout block (lines 82-89):

Replace:

```go
		if subcmd == "checkout" {
			if hasNonFlagArg(rest, ".") {
				return hookio.RuleResult{Decision: hookio.Abstain, Reason: "git checkout . discards all changes", Module: r.Name()}
			}
			if hasRedirectEnvVar(pc) {
				return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}
			}
			return hookio.RuleResult{Decision: hookio.Approve, Reason: "git checkout", Module: r.Name()}
		}
```

With:

```go
		if subcmd == "checkout" {
			if hasRedirectEnvVar(pc) {
				return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}
			}
			return hookio.RuleResult{Decision: hookio.Approve, Reason: "git checkout", Module: r.Name()}
		}
```

This removes the special-case Abstain for `.` — all checkout variants now Approve (unless redirected context).

- [ ] **Step 4: Run test to verify it passes**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && unset GOEXPERIMENT && go test ./internal/rules/git/ -run TestGit_CheckoutDot_Approve -v`

Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && unset GOEXPERIMENT && go test ./...`

Expected: All PASS

- [ ] **Step 6: Run evaluate**

Run: `unset GOEXPERIMENT && claude-extended-tool-approver evaluate --settings=$HOME/phillipg_mbp/.claude/settings.local.json --format=summary 2>&1 | tail -10`

Expected: `Misses (uncaught)` must not increase. Some `Misses (settings)` may decrease.

**Important:** Also verify that the compound command samples still abstain correctly. Run:

`unset GOEXPERIMENT && claude-extended-tool-approver evaluate --settings=$HOME/phillipg_mbp/.claude/settings.local.json --format=json 2>&1 | jq '[.[] | select(.tool_summary | test("git checkout")) | {tool_summary, category, hook_decision}]'`

Confirm that compound commands with `git clean` or `git stash drop` are NOT approved.

- [ ] **Step 7: Remove settings rule**

Run: `jq '.permissions.allow |= map(select(. != "Bash(git checkout:*)"))' ~/phillipg_mbp/.claude/settings.local.json > /tmp/settings-tmp.json && mv /tmp/settings-tmp.json ~/phillipg_mbp/.claude/settings.local.json`

Verify: `jq '.permissions.allow[]' ~/phillipg_mbp/.claude/settings.local.json | grep -c "git checkout"` should return 0.

- [ ] **Step 8: Commit**

```bash
cd ~/phillipg_mbp/phillipgreenii-nix-support-apps
git add packages/claude-extended-tool-approver/internal/rules/git/git.go packages/claude-extended-tool-approver/internal/rules/git/git_test.go
git commit -m "feat(git): approve git checkout -- . instead of abstaining

git checkout operates on the repo working tree which is always writable.
Claude Code's own safety prompts handle unintended data loss. Compound
commands with git clean/stash drop still correctly abstain via chain
evaluation."
```

- [ ] **Step 9: Close bead**

Run: `bd close pg2-55vq --reason="git checkout . changed from abstain to approve, compound commands still protected by chain evaluation"`

---

### Task 8: sqlite3 investigation (pg2-kdjr)

**Files:**

- Possibly modify: `internal/patheval/evaluator.go` (promote zone to PathReadWrite)
- Possibly modify: `internal/rules/sqlite3/sqlite3.go` (fix parsing)
- Modify: `~/phillipg_mbp/.claude/settings.local.json` (remove rule)

The sqlite3 module exists and classifies queries, but 7 sample rows still abstain. Primary hypothesis: the path `~/.local/share/claude-extended-tool-approver/asks.db` is `PathReadOnly`, so write queries correctly abstain.

- [ ] **Step 1: Investigate the specific rows**

Run: `unset GOEXPERIMENT && claude-extended-tool-approver show 5143 5673 5675 5896 19954 19956 20324 --format=json 2>&1 | jq '.[].tool_summary'`

This shows the exact commands. Note which are SELECTs vs UPDATEs.

- [ ] **Step 2: Run evaluate on sqlite3-specific rows**

Run: `unset GOEXPERIMENT && claude-extended-tool-approver evaluate --settings=$HOME/phillipg_mbp/.claude/settings.local.json --format=json 2>&1 | jq '[.[] | select(.tool_summary | test("^sqlite3")) | {id: .id, tool_summary: .tool_summary, category, hook_decision}]'`

Check the `category` and `hook_decision` fields. If they show `miss-caught-by-settings`, the engine is abstaining and settings catch it.

- [ ] **Step 3: Check path zone**

Run: `unset GOEXPERIMENT && go run ./cmd/claude-extended-tool-approver/ path-check ~/.local/share/claude-extended-tool-approver/asks.db 2>&1` (if this subcommand exists)

Or check the evaluator.go code: `<xdgDataHome>/claude-extended-tool-approver/` maps to `PathReadOnly` (line 312-316 of evaluator.go). UPDATEs need `PathReadWrite` → correct abstain.

- [ ] **Step 4: Decide on fix**

Based on findings:

- **If UPDATEs on PathReadOnly**: The tool-approver's own database is a legitimate write target. Promote `<xdgDataHome>/claude-extended-tool-approver/` from `PathReadOnly` to `PathReadWrite` in `internal/patheval/evaluator.go`.
- **If SELECTs on PathReadOnly are also abstaining**: Query parsing issue — investigate.
- **If path isn't being recognized at all**: Fix path expansion (tilde, XDG).

- [ ] **Step 5: Write failing test for the fix (if Go code changes)**

If promoting the path zone, add a test in `internal/patheval/evaluator_test.go`:

```go
// Test that the tool-approver data dir is read-write
// (it's the tool's own database, legitimate write target)
```

If fixing sqlite3 parsing, add a test in `internal/rules/sqlite3/sqlite3_test.go` with the specific query pattern.

- [ ] **Step 6: Implement the fix**

If promoting path zone, in `internal/patheval/evaluator.go`, change the `claude-extended-tool-approver` block (lines 312-316):

Replace:

```go
		// <xdgDataHome>/claude-extended-tool-approver/**
		extToolApprover := filepath.Join(pe.xdgDataHome, "claude-extended-tool-approver")
		if pathContains(extToolApprover, path) {
			return PathReadOnly
		}
```

With:

```go
		// <xdgDataHome>/claude-extended-tool-approver/**
		// ReadWrite because the tool's own database (asks.db) is a legitimate write target.
		extToolApprover := filepath.Join(pe.xdgDataHome, "claude-extended-tool-approver")
		if pathContains(extToolApprover, path) {
			return PathReadWrite
		}
```

- [ ] **Step 7: Run tests to verify**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver && unset GOEXPERIMENT && go test ./...`

Expected: All PASS

- [ ] **Step 8: Run evaluate**

Run: `unset GOEXPERIMENT && claude-extended-tool-approver evaluate --settings=$HOME/phillipg_mbp/.claude/settings.local.json --format=summary 2>&1 | tail -10`

Expected: `Misses (uncaught)` must not increase. `Misses (settings)` should decrease if the fix works.

- [ ] **Step 9: Remove settings rule**

Run: `jq '.permissions.allow |= map(select(. != "Bash(sqlite3:*)"))' ~/phillipg_mbp/.claude/settings.local.json > /tmp/settings-tmp.json && mv /tmp/settings-tmp.json ~/phillipg_mbp/.claude/settings.local.json`

Verify: `jq '.permissions.allow[]' ~/phillipg_mbp/.claude/settings.local.json | grep -c "sqlite3"` should return 0.

- [ ] **Step 10: Commit**

```bash
cd ~/phillipg_mbp/phillipgreenii-nix-support-apps
git add packages/claude-extended-tool-approver/internal/patheval/evaluator.go packages/claude-extended-tool-approver/internal/rules/sqlite3/sqlite3.go packages/claude-extended-tool-approver/internal/rules/sqlite3/sqlite3_test.go
git commit -m "fix(sqlite3/patheval): promote tool-approver data dir to read-write

The tool-approver's own database (asks.db) is a legitimate write
target. Promoting from PathReadOnly to PathReadWrite allows sqlite3
UPDATE queries to be auto-approved."
```

Note: Only stage files that actually changed.

- [ ] **Step 11: Close bead**

Run: `bd close pg2-kdjr --reason="<reason based on findings>"`
