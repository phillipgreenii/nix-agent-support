# WORKSPACE Enforcement Rules + Estate-Hygiene Fixes — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the WORKSPACE design's enforcement premise (spec `pg2-wjt8k` §B.1) _true_ for the acts that must be gated — a direct commit on the **canonical clone's primary branch** by an auto-approving agent, and an immediate `gh pr merge` — and land two low-risk documentation-hygiene fixes discovered alongside.

**Architecture:** Changes to the Go `claude-extended-tool-approver` (CETA) PreToolUse hook plus two doc fixes. (1) Move the git subcommand/`-C` parser into `internal/cmdparse` so a new rule can reuse it without importing another rule. (2) A new `primary-commit` rule: when a `git commit` runs in the **canonical clone** (main working tree, real `.git`) on its **primary branch**, return **`Reject` iff the session is auto-approving (`permission_mode == "bypassPermissions"`)**, else **`Abstain`** — an `Ask` is useless (auto-accepted in bypass) and nags humans, so we block only the sessions that would otherwise silently proceed and leave humans to their normal flow (R-6). Its `PrimaryResolver` is **filesystem-only (no `git` subprocess)** — reads `.git` (dir-vs-file), `.git/HEAD`, `.git/config` — because git subprocesses in the hook caused index/fsmonitor locks and rejected commits in this workspace. (3) `gh pr merge` (immediate) → `Reject`; `gh pr merge --auto` stays `Abstain` (draft-gated). Two doc fixes correct a poisoned marketplace doc and a stale ADR.

**Tech Stack:** Go 1.25 (module `github.com/phillipgreenii/claude-extended-tool-approver`), cobra, `modernc.org/sqlite`; Nix build via **gomod2nix** (no `vendorHash`); packaged under `phillipgreenii-nix-agent-support/packages/claude-extended-tool-approver`.

## Global Constraints

- **Verdict enum** (`internal/hookio/types.go:25-30`): `Approve(0) < Abstain(1) < Ask(2) < Reject(3)`; order is load-bearing (compound-command fold takes the most-restrictive). Identifiers: `hookio.Approve`, `hookio.Abstain`, `hookio.Ask`, `hookio.Reject`.
- **Rule contract** (`internal/hookio/types.go:100-103`): implement `Name() string` and `Evaluate(input *hookio.HookInput) hookio.RuleResult`; return `RuleResult{Decision, Reason, Module: r.Name()}`; `Abstain` when N/A. Tool cwd = `input.CWD`; Bash command = `input.BashCommand()`; **auto-approve signal = `input.PermissionMode`** (`permission_mode`; propagated through the engine at `engine.go:186`). Auto-approving value = `"bypassPermissions"`.
- **Precedence = registration order** in `internal/setup/factory.go` (`NewEngineForCWD`, `eng.RegisterRules(...)`, ~46-69), first-match-wins per leaf; the compound fold then takes the most-restrictive across leaves.
- **The resolver MUST be filesystem-only — NO `git` subprocess.** It reads `.git` (dir vs file), `<root>/.git/HEAD`, and `<root>/.git/config` directly. Rationale: git subprocesses inside a hook that runs on _every_ commit caused index/fsmonitor **locks and rejected commits** in this workspace (cf. open beads `pg2-pi5u1`/`pg2-fnjfs`/`pg2-39rz2`). File reads take no locks and cannot block a commit.
- **Canonical clone = the main working tree** (the real `.git` **directory**), NOT a linked worktree. Detected by filesystem: walk up from the effective dir to the first `.git` entry — a **directory** ⇒ main tree (canonical); a **file** (a worktree's `gitdir:` pointer) ⇒ linked worktree (not canonical).
- **Primary-branch resolution:** read `pgii-integrate-branch.primaryBranch` from `<root>/.git/config` (file read) → else default **`main`**. No `origin/HEAD` lookup, no non-local config scopes (kept subprocess-free).
- **`-C`-aware:** resolve branch context against the effective directory = `input.CWD` after applying each `git -C <path>` (relative joins, absolute wins), not raw `input.CWD`.
- **Verdict is `permission_mode`-based, never `Ask`:** on a detected canonical-primary commit, `Reject` iff `PermissionMode == "bypassPermissions"`, else `Abstain`. (An `Ask` is auto-accepted in bypass — no protection — and nags humans every commit; `Abstain` for humans keeps zero friction, `Reject` blocks the auto-approver.) **Fail-open** on any resolver error (Abstain).
- **Enforcement assumption to confirm empirically (Task 4 Step 5):** a CETA `Reject` (JSON `permissionDecision:"deny"`) actually blocks a tool call in `bypassPermissions` mode. Docs confirm hooks run in that mode and blocking hooks beat allow-rules, but the JSON-deny-vs-bypass precedence is not documented. If the check shows it does _not_ block, escalate CETA's deny path (exit-code-2) — flagged, out of this plan's code scope.
- **Testing (two layers):** stub-`PrimaryResolver` unit tests for rule logic; a real-git **contract test** that builds fixtures with `git` and verifies our _file-format_ assumptions (`.git` dir-vs-file, `.git/HEAD` ref line, `.git/config` section).
- **`nix flake check` needs git for the contract test:** the `claude-extended-tool-approver-go-tests` check (`flake.nix:489-493`) MUST gain `nativeCheckInputs = [ pkgs.git ]` (as sibling `pb-go-tests`/`pg-pr-go-tests` already have).
- **Test command:** `go test ./...` from `packages/claude-extended-tool-approver`. No new external imports ⇒ `gomod2nix.toml` unchanged.
- **Commit convention (repo practice):** every commit body carries `Refs pg2-wjt8k.2.` on the line after the subject, a blank line, then `Co-Authored-By: Claude <noreply@anthropic.com>`.
- **Completion gate** (repo CLAUDE.md): before done, `nix flake check` MUST pass and (if present) `prek run --all-files` / `pre-commit run --all-files` MUST pass; docs updated when rule structure changes.

## Repo layout & landing

Three **independent** landing units (edge-disjoint), each in its own git worktree (R-4), landed via `integrate-branch`. **pg-pr/pb are deliberately untouched** (owned by `pg2-ynhr`).

| unit         | repo                               | tasks                                      |
| ------------ | ---------------------------------- | ------------------------------------------ |
| **A** (core) | `phillipgreenii-nix-agent-support` | Tasks 1–6 (CETA rules + README)            |
| **B**        | `phillipg-nix-repo-base`           | Task 7 (poisoned `claude-marketplaces.md`) |
| **C**        | `phillipgreenii-nix-personal`      | Task 8 (ADR-0027 ghost)                    |

Plan home: `phillipgreenii-nix-agent-support/docs/superpowers/plans/2026-07-24-ws-enforcement-rules.md`.

---

### Task 1: Move git-invocation parsing into `cmdparse`

Relocate the git subcommand extractor from the `git` rule into `internal/cmdparse` (idiomatic home; no rule imports another rule) and extend it to surface `-C` chdir paths. The `git` rule keeps identical behavior.

**Files:**

- Create: `internal/cmdparse/git.go`
- Test: `internal/cmdparse/git_test.go`
- Modify: `internal/rules/git/git.go` (call `cmdparse.GitInvocation` at `:71`; delete local `extractGitSubcommand` `:187-207`; update comment `:160`)

**Interfaces:**

- Produces: `cmdparse.GitInvocation(args []string) (chdirs []string, subcmd string, rest []string)`.

- [ ] **Step 1: Write the failing test**

Create `internal/cmdparse/git_test.go`:

```go
package cmdparse

import (
	"reflect"
	"testing"
)

func TestGitInvocation(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantChdirs []string
		wantSub    string
		wantRest   []string
	}{
		{"plain commit", []string{"commit", "-m", "x"}, nil, "commit", []string{"-m", "x"}},
		{"dash-C", []string{"-C", "/repo", "commit"}, []string{"/repo"}, "commit", []string{}},
		{"chained dash-C", []string{"-C", "a", "-C", "b", "status"}, []string{"a", "b"}, "status", []string{}},
		{"config-injection then commit", []string{"-c", "k=v", "commit"}, nil, "commit", []string{}},
		{"commit with -c flag after subcmd", []string{"commit", "-c", "HEAD~1"}, nil, "commit", []string{"-c", "HEAD~1"}},
		{"no subcommand", []string{"-C", "/repo"}, []string{"/repo"}, "", nil},
		{"commit-tree not commit", []string{"commit-tree", "abc"}, nil, "commit-tree", []string{"abc"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch, sub, rest := GitInvocation(tt.args)
			if !reflect.DeepEqual(ch, tt.wantChdirs) || sub != tt.wantSub || !reflect.DeepEqual(rest, tt.wantRest) {
				t.Errorf("GitInvocation(%v) = (%v,%q,%v), want (%v,%q,%v)", tt.args, ch, sub, rest, tt.wantChdirs, tt.wantSub, tt.wantRest)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/cmdparse/... -run TestGitInvocation` → FAIL (undefined).

- [ ] **Step 3: Implement**

Create `internal/cmdparse/git.go`:

```go
package cmdparse

import "strings"

// GitInvocation parses a git command's pre-subcommand options (the slice AFTER the
// `git` executable), returning the ordered `-C <path>` chdir values, the subcommand
// ("" if none), and the args after it. It consumes the option-arg for
// -C/-c/--git-dir/--work-tree/--namespace exactly as git does.
func GitInvocation(args []string) (chdirs []string, subcmd string, rest []string) {
	i := 0
	for i < len(args) {
		a := args[i]
		switch a {
		case "-C":
			if i+1 < len(args) {
				chdirs = append(chdirs, args[i+1])
			}
			i += 2
			continue
		case "-c", "--git-dir", "--work-tree", "--namespace":
			i += 2
			continue
		default:
			if strings.HasPrefix(a, "-") {
				i++
				continue
			}
			return chdirs, a, args[i+1:]
		}
	}
	return chdirs, "", nil
}
```

- [ ] **Step 4: Run to verify it passes** — `go test ./internal/cmdparse/... -run TestGitInvocation` → PASS.

- [ ] **Step 5: Refactor the git rule**

In `internal/rules/git/git.go`: delete local `extractGitSubcommand` (`:187-207`); change `:71` to `_, subcmd, rest := cmdparse.GitInvocation(pc.Args)`; update the `:160` comment to reference `cmdparse.GitInvocation`. (`cmdparse` already imported.)

- [ ] **Step 6: Run git package tests** — `go test ./internal/rules/git/...` → PASS (behavior unchanged).

- [ ] **Step 7: Commit**

```bash
git add internal/cmdparse/git.go internal/cmdparse/git_test.go internal/rules/git/git.go
git commit -m "refactor(cmdparse): extract GitInvocation (subcommand + -C) for reuse

Refs pg2-wjt8k.2.

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 2: `primary-commit` rule + `PrimaryResolver` (stub-tested logic)

The rule + resolver interface, fully unit-tested with a stub (no real git). Verdict branches on `permission_mode`.

**Files:**

- Create: `internal/rules/primarycommit/primarycommit.go`
- Test: `internal/rules/primarycommit/primarycommit_test.go`

**Interfaces:**

- Produces: `primarycommit.PrimaryResolver` — `IsCanonical(dir string)(bool,error)`, `PrimaryBranch(dir string)(string,error)`, `CurrentBranch(dir string)(string,error)`; `primarycommit.New(resolver PrimaryResolver) *Rule` (`Name()=="primary-commit"`).

- [ ] **Step 1: Write the failing test**

Create `internal/rules/primarycommit/primarycommit_test.go`:

```go
package primarycommit

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

type stubResolver struct {
	canonical     bool
	primary, cur  string
	canonicalErr  error
	priErr, curErr error
	gotDir        string
}

func (s *stubResolver) IsCanonical(dir string) (bool, error) { s.gotDir = dir; return s.canonical, s.canonicalErr }
func (s *stubResolver) PrimaryBranch(string) (string, error) { return s.primary, s.priErr }
func (s *stubResolver) CurrentBranch(string) (string, error) { return s.cur, s.curErr }

func mustJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

func canonMain() *stubResolver { return &stubResolver{canonical: true, primary: "main", cur: "main"} }

func TestPrimaryCommitRule(t *testing.T) {
	tests := []struct {
		name    string
		command string
		tool    string
		mode    string
		res     *stubResolver
		want    hookio.Decision
	}{
		{"bypass: commit on primary", "git commit -m x", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
		{"bypass: commit --amend on primary", "git commit --amend", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
		{"default: commit on primary (no friction)", "git commit -m x", "Bash", "default", canonMain(), hookio.Abstain},
		{"empty mode: commit on primary", "git commit -m x", "Bash", "", canonMain(), hookio.Abstain},
		{"bypass: off primary", "git commit -m x", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: "feat"}, hookio.Abstain},
		{"bypass: linked worktree", "git commit -m x", "Bash", "bypassPermissions", &stubResolver{canonical: false, primary: "main", cur: "main"}, hookio.Abstain},
		{"bypass: detached", "git commit -m x", "Bash", "bypassPermissions", &stubResolver{canonical: true, primary: "main", cur: ""}, hookio.Abstain},
		{"bypass: non-commit git", "git status", "Bash", "bypassPermissions", canonMain(), hookio.Abstain},
		{"bypass: commit-tree", "git commit-tree abc", "Bash", "bypassPermissions", canonMain(), hookio.Abstain},
		{"bypass: non-git bash", "ls -la", "Bash", "bypassPermissions", canonMain(), hookio.Abstain},
		{"bypass: non-bash tool", "", "Read", "bypassPermissions", canonMain(), hookio.Abstain},
		{"bypass: resolver error (fail-open)", "git commit -m x", "Bash", "bypassPermissions", &stubResolver{canonicalErr: errors.New("x")}, hookio.Abstain},
		{"bypass: compound commit && push", "git commit -m x && git push", "Bash", "bypassPermissions", canonMain(), hookio.Reject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New(tt.res)
			in := &hookio.HookInput{ToolName: tt.tool, ToolInput: mustJSON(tt.command), CWD: "/repo", PermissionMode: tt.mode}
			if got := r.Evaluate(in).Decision; got != tt.want {
				t.Errorf("Decision = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPrimaryCommit_DashC_EffectiveDir(t *testing.T) {
	cases := []struct{ cmd, cwd, wantDir string }{
		{"git -C /abs/repo commit", "/cwd", "/abs/repo"},
		{"git -C sub commit", "/cwd", "/cwd/sub"},
		{"git -C a -C b commit", "/cwd", "/cwd/a/b"},
		{"git commit", "/cwd", "/cwd"},
	}
	for _, c := range cases {
		t.Run(c.cmd, func(t *testing.T) {
			s := canonMain()
			New(s).Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(c.cmd), CWD: c.cwd, PermissionMode: "bypassPermissions"})
			if s.gotDir != c.wantDir {
				t.Errorf("effective dir = %q, want %q", s.gotDir, c.wantDir)
			}
		})
	}
}

func TestPrimaryCommit_NilResolver(t *testing.T) {
	got := New(nil).Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON("git commit"), CWD: "/repo", PermissionMode: "bypassPermissions"}).Decision
	if got != hookio.Abstain {
		t.Errorf("Decision = %v, want Abstain", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/rules/primarycommit/...` → FAIL (undefined).

- [ ] **Step 3: Implement**

Create `internal/rules/primarycommit/primarycommit.go`:

```go
// Package primarycommit gates a `git commit` on the PRIMARY branch of the CANONICAL
// clone (the main working tree — the real .git directory, never a linked worktree).
// It returns Reject only when the session is auto-approving (permission_mode ==
// "bypassPermissions"): such a session would silently accept an Ask, so a hard deny is
// the only thing that stops it. Interactive/default sessions get Abstain — no
// every-commit prompt; a human directing a primary commit is permitted (R-6) and left
// to the normal flow. Worktrees, feature branches, non-commit git, and any resolver
// error all Abstain (fail-open; the worktree discipline is the primary control).
package primarycommit

import (
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

const bypassMode = "bypassPermissions"

type PrimaryResolver interface {
	IsCanonical(dir string) (bool, error)   // main working tree (real .git dir), not a worktree
	PrimaryBranch(dir string) (string, error) // .git/config override, else "main"
	CurrentBranch(dir string) (string, error) // "" on detached HEAD
}

type Rule struct{ resolver PrimaryResolver }

func New(resolver PrimaryResolver) *Rule { return &Rule{resolver: resolver} }

func (r *Rule) Name() string { return "primary-commit" }

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	abstain := hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	if input.ToolName != "Bash" {
		return abstain
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return abstain
	}
	for _, pc := range cmdparse.Parse(cmdStr) {
		if !isGit(pc.Executable) {
			continue
		}
		chdirs, subcmd, _ := cmdparse.GitInvocation(pc.Args)
		if subcmd != "commit" {
			continue
		}
		if r.resolver == nil {
			return abstain
		}
		dir := effectiveDir(input.CWD, chdirs)
		canonical, err := r.resolver.IsCanonical(dir)
		if err != nil || !canonical {
			return abstain
		}
		primary, err := r.resolver.PrimaryBranch(dir)
		if err != nil || primary == "" {
			return abstain
		}
		cur, err := r.resolver.CurrentBranch(dir)
		if err != nil || cur == "" || cur != primary {
			return abstain
		}
		// Commit on canonical primary. Block only an auto-approving session (which would
		// otherwise silently accept); trust interactive/default sessions (R-6).
		if input.PermissionMode == bypassMode {
			return hookio.RuleResult{
				Decision: hookio.Reject,
				Reason:   "primary-commit: refusing a commit on the primary branch (" + primary + ") of the canonical clone in an auto-approving session — advancing shared primary requires explicit human direction (R-6); use a feature branch/worktree.",
				Module:   r.Name(),
			}
		}
		return abstain
	}
	return abstain
}

func isGit(exec string) bool { return exec == "git" || filepath.Base(exec) == "git" }

func effectiveDir(cwd string, chdirs []string) string {
	dir := cwd
	for _, c := range chdirs {
		if filepath.IsAbs(c) {
			dir = c
		} else {
			dir = filepath.Join(dir, c)
		}
	}
	return dir
}
```

- [ ] **Step 4: Run to verify it passes** — `go test ./internal/rules/primarycommit/...` → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/rules/primarycommit/primarycommit.go internal/rules/primarycommit/primarycommit_test.go
git commit -m "feat(approver): add primary-commit rule (Reject canonical-primary commit in bypass mode)

Refs pg2-wjt8k.2.

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 3: `FileResolver` (filesystem-only) + factory wiring + real-git contract test

Production resolver via **file reads only** (no git subprocess), register the rule before `git`, and a contract test pinning our file-format assumptions. Add `git` to the go-tests nix check.

**Files:**

- Create: `internal/rules/primarycommit/resolver.go`
- Test: `internal/rules/primarycommit/resolver_test.go`
- Modify: `internal/setup/factory.go` (register before `git.New()`), `flake.nix:489-493` (`nativeCheckInputs`)

**Interfaces:**

- Produces: `primarycommit.NewFileResolver() *FileResolver` satisfying `PrimaryResolver`.

- [ ] **Step 1: Write the failing contract test**

Create `internal/rules/primarycommit/resolver_test.go`:

```go
package primarycommit

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// Contract test: pins our assumptions about the on-disk git file formats the resolver
// reads (.git dir-vs-file, .git/HEAD ref line, .git/config section). Uses real `git`
// only to BUILD fixtures; the resolver itself never shells out. Requires git on PATH.
func TestFileResolver_Contract(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	dir := t.TempDir()
	git := func(d string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", d}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git -C %s %v: %v\n%s", d, args, err, out)
		}
	}
	git(dir, "init", "-q", "-b", "trunk")
	git(dir, "config", "user.email", "t@example.com")
	git(dir, "config", "user.name", "t")

	r := NewFileResolver()

	// Main working tree is canonical (.git is a directory).
	if c, err := r.IsCanonical(dir); err != nil || !c {
		t.Fatalf("IsCanonical(main) = %v, %v; want true", c, err)
	}
	// CurrentBranch reads .git/HEAD — works on an UNBORN branch.
	if b, err := r.CurrentBranch(dir); err != nil || b != "trunk" {
		t.Fatalf("CurrentBranch = %q, %v; want trunk", b, err)
	}
	// No config key -> default main.
	if p, err := r.PrimaryBranch(dir); err != nil || p != "main" {
		t.Fatalf("PrimaryBranch(default) = %q, %v; want main", p, err)
	}
	// Config override wins.
	git(dir, "config", "pgii-integrate-branch.primaryBranch", "mainline")
	if p, _ := r.PrimaryBranch(dir); p != "mainline" {
		t.Fatalf("PrimaryBranch(config) = %q; want mainline", p)
	}
	// A linked worktree is NOT canonical (.git is a gitdir: file).
	git(dir, "commit", "--allow-empty", "-q", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	git(dir, "worktree", "add", "-q", "-b", "feature", wt)
	if c, err := r.IsCanonical(wt); err != nil || c {
		t.Fatalf("IsCanonical(worktree) = %v, %v; want false", c, err)
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/rules/primarycommit/... -run TestFileResolver_Contract` → FAIL (undefined).

- [ ] **Step 3: Implement the file resolver**

Create `internal/rules/primarycommit/resolver.go`:

```go
package primarycommit

import (
	"os"
	"path/filepath"
	"strings"
)

// FileResolver answers branch/tree questions by reading git's on-disk files directly —
// NO git subprocess (avoids index/fsmonitor locks in the hook path).
type FileResolver struct{}

func NewFileResolver() *FileResolver { return &FileResolver{} }

// gitRoot walks up from dir to the first ".git" entry. gitIsDir==true ⇒ main working
// tree (canonical); false ⇒ a linked worktree (.git is a gitdir: file).
func gitRoot(dir string) (root string, gitIsDir bool, found bool) {
	d := dir
	for {
		if info, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d, info.IsDir(), true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false, false
		}
		d = parent
	}
}

func (r *FileResolver) IsCanonical(dir string) (bool, error) {
	_, gitIsDir, found := gitRoot(dir)
	return found && gitIsDir, nil
}

func (r *FileResolver) CurrentBranch(dir string) (string, error) {
	root, gitIsDir, found := gitRoot(dir)
	if !found || !gitIsDir {
		return "", nil
	}
	data, err := os.ReadFile(filepath.Join(root, ".git", "HEAD"))
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(data))
	const pfx = "ref: refs/heads/"
	if strings.HasPrefix(line, pfx) {
		return strings.TrimPrefix(line, pfx), nil // works on an unborn branch too
	}
	return "", nil // raw SHA => detached HEAD
}

func (r *FileResolver) PrimaryBranch(dir string) (string, error) {
	root, gitIsDir, found := gitRoot(dir)
	if found && gitIsDir {
		if v := gitConfigValue(filepath.Join(root, ".git", "config"), "pgii-integrate-branch", "primaryBranch"); v != "" {
			return v, nil
		}
	}
	return "main", nil
}

// gitConfigValue parses a local .git/config for section.key (case-insensitive section/key).
func gitConfigValue(path, section, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	inSection := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inSection = strings.EqualFold(strings.TrimSpace(line[1:len(line)-1]), section)
			continue
		}
		if inSection {
			if i := strings.Index(line, "="); i >= 0 && strings.EqualFold(strings.TrimSpace(line[:i]), key) {
				return strings.TrimSpace(line[i+1:])
			}
		}
	}
	return ""
}
```

- [ ] **Step 4: Register before `git` in the factory**

In `internal/setup/factory.go`, add import `".../internal/rules/primarycommit"` and insert into `RegisterRules(...)` immediately BEFORE `git.New(),`:

```go
		mcp.New(),
		primarycommit.New(primarycommit.NewFileResolver()),
		git.New(),
		gh.New(gh.NewExecResolver()),
```

- [ ] **Step 5: Add git to the go-tests nix check**

In `flake.nix` (~`:489-493`, `claude-extended-tool-approver-go-tests`): add `nativeCheckInputs = [ pkgs.git ];` and correct the "zero net/exec" comment (the contract test builds git fixtures). Mirror `pb-go-tests` `:503` / `pg-pr-go-tests` `:514`.

- [ ] **Step 6: Tests + build + gate**

Run: `go test ./...` → PASS. `go build ./...` → compiles. `nix flake check 2>&1 | rg -i 'error|fail' || echo OK` → no errors.

- [ ] **Step 7: Commit**

```bash
git add internal/rules/primarycommit/resolver.go internal/rules/primarycommit/resolver_test.go internal/setup/factory.go flake.nix
git commit -m "feat(approver): filesystem-only PrimaryResolver + register primary-commit; go-tests get git

Refs pg2-wjt8k.2.

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 4: Engine-level precedence test + sync `buildFullEngine` + empirical bypass check

Prove `primary-commit` wins over `git` (Reject on bypass+primary; git Approves otherwise), keep the integration harness in sync, and confirm the bypass-deny assumption in the real harness.

**Files:**

- Modify: `internal/engine/engine_integration_test.go` (add `primarycommit` to `buildFullEngine` `:28-55`; add precedence test)

- [ ] **Step 1: Add `primary-commit` to `buildFullEngine`**

Insert `primarycommit.New(primarycommit.NewFileResolver())` immediately before the `git.New()` entry (mirror `factory.go`); add the import. (The harness's non-existent test cwd → `IsCanonical` false → Abstain → existing `git commit → Approve` controls still pass.)

- [ ] **Step 2: Write the precedence test**

Add to `engine_integration_test.go` (same package; reuse the file's existing engine constructor / eval entry point / Bash-input helper — names below are placeholders):

```go
type fakePrimaryResolver struct {
	canonical    bool
	primary, cur string
}

func (f fakePrimaryResolver) IsCanonical(string) (bool, error)    { return f.canonical, nil }
func (f fakePrimaryResolver) PrimaryBranch(string) (string, error) { return f.primary, nil }
func (f fakePrimaryResolver) CurrentBranch(string) (string, error) { return f.cur, nil }

func TestPrecedence_PrimaryCommitBeatsGit(t *testing.T) {
	mk := func(cur string) *engine.Engine {
		e := engine.New() // reuse buildFullEngine's constructor
		e.RegisterRules(
			primarycommit.New(fakePrimaryResolver{canonical: true, primary: "main", cur: cur}),
			git.New(),
		)
		return e
	}
	eval := func(e *engine.Engine, cmd, mode string) hookio.RuleResult {
		in := &hookio.HookInput{ToolName: "Bash", ToolInput: mustBashJSON(cmd), CWD: "/repo", PermissionMode: mode}
		return e.EvaluateHook(in) // reuse the existing integration eval entry
	}
	// bypass + on primary -> primary-commit Rejects.
	if r := eval(mk("main"), "git commit -m x", "bypassPermissions"); r.Decision != hookio.Reject || r.Module != "primary-commit" {
		t.Errorf("bypass on-primary = %v/%s; want Reject/primary-commit", r.Decision, r.Module)
	}
	// default + on primary -> primary-commit Abstains, git Approves (no friction).
	if r := eval(mk("main"), "git commit -m x", "default"); r.Decision != hookio.Approve || r.Module != "git" {
		t.Errorf("default on-primary = %v/%s; want Approve/git", r.Decision, r.Module)
	}
	// bypass + off primary -> primary-commit Abstains, git Approves.
	if r := eval(mk("feat"), "git commit -m x", "bypassPermissions"); r.Decision != hookio.Approve || r.Module != "git" {
		t.Errorf("bypass off-primary = %v/%s; want Approve/git", r.Decision, r.Module)
	}
}
```

- [ ] **Step 3: Run to verify** — `go test ./internal/engine/...` → PASS; `go test ./...` → PASS (existing controls unchanged).

- [ ] **Step 4: Commit**

```bash
git add internal/engine/engine_integration_test.go
git commit -m "test(approver): engine precedence (primary-commit beats git in bypass) + harness sync

Refs pg2-wjt8k.2.

Co-Authored-By: Claude <noreply@anthropic.com>"
```

- [ ] **Step 5: MANUAL — confirm Reject blocks in bypass mode (harness assumption)**

This cannot be a Go test (it is Claude Code harness behavior). Once the built approver is on a machine, in a **bypassPermissions** session, attempt a `git commit` on a repo checked out on its primary branch and confirm the tool call is **blocked** (not silently run). Expected: blocked with the primary-commit reason.

- If it blocks → assumption holds; enforcement is real.
- If it does NOT block → CETA's JSON `deny` doesn't override bypass. File a follow-up bead to make CETA's deny path exit non-zero (guaranteed block); the rule logic here is unchanged. Do NOT claim enforcement until this passes.

---

### Task 5: `gh pr merge` (immediate) → `Reject`; `--auto` stays `Abstain`

Immediate `gh pr merge` merges now, bypassing the draft-first landing flow → `Reject`. `--auto` is draft-gated (a PR opens draft; `--auto` can't merge until un-drafted; toggling it refreshes the merge message) → stays `Abstain`.

**Files:**

- Modify: `internal/rules/gh/gh.go` (the `pr merge` branch, ~`:106-119`)
- Test: `internal/rules/gh/gh_test.go` (`TestGH_PrMergeAuto_Abstain` stays; add/adjust the immediate-merge case to expect `Reject`)

- [ ] **Step 1: Update tests**

Keep `TestGH_PrMergeAuto_Abstain` (verdict unchanged). Find the immediate `gh pr merge` (no `--auto`) test assertion (currently expects `Ask`) and change it to `hookio.Reject`; if none exists, add a case `gh pr merge 123` → `Reject`.

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/rules/gh/...` → FAIL on the immediate-merge case (still `Ask`).

- [ ] **Step 3: Change the verdict**

In `internal/rules/gh/gh.go`, the `pr merge` branch:

```go
	if resource == "pr" && subcmd == "merge" {
		if hasFlag(pc.Args, "--auto") {
			// Intentionally Abstain — NOT a bypass. PRs open as draft, so --auto cannot
			// merge until a human un-drafts (the real gate); toggling --auto also refreshes
			// the merge-commit message from the current PR title/body. Do not change to Reject.
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "gh pr merge --auto: allowed (draft-gated; --auto refreshes merge message from PR title/body)",
				Module:   r.Name(),
			}
		}
		return hookio.RuleResult{
			Decision: hookio.Reject,
			Reason:   "gh pr merge (immediate) is prohibited: it merges now, bypassing the draft-first landing flow. Open/keep the PR as draft and use --auto, or merge via the WORKSPACE landing flow.",
			Module:   r.Name(),
		}
	}
```

- [ ] **Step 4: Run to verify it passes** — `go test ./internal/rules/gh/...` → PASS (`--auto` Abstain; immediate Reject; `--auto-merge` unaffected).

- [ ] **Step 5: Commit**

```bash
git add internal/rules/gh/gh.go internal/rules/gh/gh_test.go
git commit -m "feat(approver): reject immediate gh pr merge; keep --auto abstaining (draft-gated)

Refs pg2-wjt8k.2.

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 6: Update the approver README rule catalog

**Files:** Modify `packages/claude-extended-tool-approver/README.md` (the "Rule Modules" section, ~`:88-104`).

- [ ] **Step 1: Document the change**

Insert `primary-commit` at its precedence position (immediately before `git`): _"Reject a `git commit` on the canonical clone's primary branch in an auto-approving (bypassPermissions) session; Abstain otherwise."_ Update the `gh` entry to note `gh pr merge` (immediate) → Reject, `--auto` → Abstain. Don't fix the README's other pre-existing staleness.

- [ ] **Step 2: Verify** — `rg -n 'primary-commit' packages/claude-extended-tool-approver/README.md` → present.

- [ ] **Step 3: Commit**

```bash
git add packages/claude-extended-tool-approver/README.md
git commit -m "docs(approver): document primary-commit + gh pr merge verdicts

Refs pg2-wjt8k.2.

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 7: Doc hygiene — poisoned `claude-marketplaces.md` (Unit B, `phillipg-nix-repo-base`)

The "Always-on rules → SessionStart HOOK plugin" bullet (`docs/claude-marketplaces.md:44-55`) cites a **non-existent** `agent-rules` hook plugin and endorses a removed double-injection anti-pattern (bead `pg2-qewh`). Secondary: option-path drift `...claude.marketplaces.*` → real `...claude-code.marketplaces.*`.

**Files:** Modify `docs/claude-marketplaces.md`.

- [ ] **Step 1: Replace the misleading bullet** (~`:44-55`) with:

```markdown
- **Always-on rules → a SessionStart HOOK plugin.** A SessionStart-hook plugin is the
  only plugin vehicle that is genuinely always-on (a skill body is on-invoke; a
  plugin-root `CLAUDE.md` is inert). Reference the hook command by **bare name**. Real
  examples in this estate: `claude-activity` and `claude-extended-tool-approver` (see
  their `hooks/hooks.json`).

  > Personal always-on _rules_ are NOT delivered this way — they are written to the
  > user-level `~/.claude/CLAUDE.md` (see agent-support
  > `docs/superpowers/specs/2026-06-25-agent-rules-delivery-design.md`). A SessionStart
  > hook that re-injects them double-injects (user `CLAUDE.md` already loads in headless
  > `-p` mode); that anti-pattern was removed in bead `pg2-qewh`. Do not reintroduce it.
```

- [ ] **Step 2: Fix option-path drift** — `rg -n 'programs\.claude\.marketplaces' docs/claude-marketplaces.md`; change each hit to `programs.claude-code.marketplaces`.

- [ ] **Step 3: Verify** — `rg -n 'agent-rules-session-start|agent-rules/hooks' docs/claude-marketplaces.md` → none; `rg -n 'programs\.claude\.marketplaces' docs/claude-marketplaces.md` → none; prettier/pre-commit → PASS.

- [ ] **Step 4: Commit**

```bash
git add docs/claude-marketplaces.md
git commit -m "docs(marketplaces): drop non-existent agent-rules plugin example; fix option paths

Refs pg2-wjt8k.2.

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 8: Doc hygiene — `agent-rules` ghost ADR-0027 (Unit C, `phillipgreenii-nix-personal`)

ADR-0027 is still `Status: Accepted` but describes a removed mechanism; its "Code evidence" points to `phillipgreenii-nix-personal/home/programs/agent-rules/default.nix`, which does not exist. Real module: `phillipgreenii-nix-agent-support/home/programs/agent-rules/default.nix`.

**Files:** Modify `docs/adr/0027-agent-rules-as-nix-package-with-dual-format-delivery.md`, `docs/adr/index.md`.

- [ ] **Step 1: Confirm the ghost** — `ls phillipgreenii-nix-personal/home/programs/agent-rules 2>&1` → "No such file or directory".

- [ ] **Step 2: Supersede + fix pointer** — in ADR-0027: `**Status**: Accepted` → `**Status**: Superseded`; add under it:

```markdown
> **Superseded by** the user-level `~/.claude/CLAUDE.md` delivery — see
> `phillipgreenii-nix-agent-support/docs/superpowers/specs/2026-06-25-agent-rules-delivery-design.md`
> and bead `pg2-qewh` (which removed dual-format generation and the `pgii-link-agent-rules`
> post-checkout hook). Retained for history only.
```

Correct the "Code evidence" path (~`:29`,`:51`) to `phillipgreenii-nix-agent-support/home/programs/agent-rules/default.nix`; note `pgii-link-agent-rules.sh` / `test-post-checkout.bats` were removed.

- [ ] **Step 3: Flip the index** — in `docs/adr/index.md` (ADR-0027 row, ~`:32`): `Accepted` → `Superseded`.

- [ ] **Step 4: Verify** — index row shows `Superseded`; `rg -n 'Status' docs/adr/0027-*.md | head -1` → `Superseded`; prettier → PASS.

- [ ] **Step 5: Commit**

```bash
git add docs/adr/0027-agent-rules-as-nix-package-with-dual-format-delivery.md docs/adr/index.md
git commit -m "docs(adr): mark ADR-0027 superseded; fix dangling agent-rules code-evidence path

Refs pg2-wjt8k.2.

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Self-review (completed)

- **Spec coverage:** §B.1 gated acts → primary-commit `Reject` in bypass (Ask was wrong — auto-accepted in bypass, nags humans); immediate `gh pr merge` `Reject`; `--auto` intentionally Abstain (draft-gated). Two doc landmines cleared. pg-pr/pb enablement excluded (owned by `pg2-ynhr`).
- **Resolver safety:** filesystem-only, no git subprocess (fixes the lock/rejected-commit problem you hit); primary defaults to `main`; canonical = real `.git` directory.
- **Placeholder scan:** only Task 4 Step 2 defers to the integration file's existing constructor/eval/helper names (can't be quoted blind); everything else concrete.
- **Type/name consistency:** `PrimaryResolver{IsCanonical,PrimaryBranch,CurrentBranch}` identical across Tasks 2-4; `cmdparse.GitInvocation` produced T1/consumed T1-2; `hookio.{Approve,Abstain,Ask,Reject}` verbatim; `Name()=="primary-commit"`.
- **Open item (Task 4 Step 5):** the bypass-deny assumption is empirically confirmed at deploy, not asserted in code — the one thing docs don't guarantee.
- **Deliberate limitations:** only `bypassPermissions` is treated as auto-approving (extend the set if other auto modes are used); `--git-dir`/`--work-tree` _flag_ forms aren't `-C`-resolved (rare); fail-open on resolver error.
