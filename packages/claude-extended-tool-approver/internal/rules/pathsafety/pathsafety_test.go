package pathsafety

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestPathSafety_ReadInProject_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/home/user/project/foo.go"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Read in project: got %s, want approve", got.Decision)
	}
}

func TestPathSafety_WriteInProject_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Write",
		ToolInput: mustJSON(map[string]string{"file_path": "/home/user/project/foo.go", "content": "x"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Write in project: got %s, want approve", got.Decision)
	}
}

func TestPathSafety_ReadNixStore_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/nix/store/abc123-foo"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Read /nix/store: got %s, want approve (read-only paths support reads)", got.Decision)
	}
}

func TestPathSafety_WriteNixStore_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Write",
		ToolInput: mustJSON(map[string]string{"file_path": "/nix/store/abc123-foo", "content": "x"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("Write /nix/store: got %s, want abstain (read-only path, deferred to claude-code)", got.Decision)
	}
}

func TestPathSafety_WriteUnknownPath_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Write",
		ToolInput: mustJSON(map[string]string{"file_path": "/etc/hosts", "content": "x"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("Write unknown path: got %s, want abstain", got.Decision)
	}
}

func TestPathSafety_ReadUnknownPath_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/usr/bin/ls"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("Read /usr/bin/ls: got %s, want abstain (unknown path)", got.Decision)
	}
}

func TestPathSafety_DeleteInProject_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Delete",
		ToolInput: mustJSON(map[string]string{"file_path": "/home/user/project/foo.go"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Delete in project: got %s, want approve", got.Decision)
	}
}

func TestPathSafety_Bash_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "echo hello"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("Bash: got %s, want abstain", got.Decision)
	}
}

func TestPathSafety_WriteTmp_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Write",
		ToolInput: mustJSON(map[string]string{"file_path": "/tmp/foo.txt", "content": "x"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Write /tmp: got %s, want approve", got.Decision)
	}
}

func TestPathSafety_Name(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	if got := r.Name(); got != "path-safety" {
		t.Errorf("Name() = %q, want path-safety", got)
	}
}

func TestPathSafety_DenyRead_Rejects(t *testing.T) {
	pe := patheval.NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{
		DenyRead: []string{"/Users/phillipg/.ssh"},
	})
	rule := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/Users/phillipg/.ssh/id_rsa"}),
	}
	result := rule.Evaluate(input)
	if result.Decision != hookio.Reject {
		t.Errorf("Decision = %v, want Reject for denyRead path", result.Decision)
	}
}

func TestPathSafety_DenyWrite_Rejects(t *testing.T) {
	pe := patheval.NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{
		DenyWrite: []string{"/Users/phillipg/.ssh"},
	})
	rule := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Edit",
		ToolInput: mustJSON(map[string]string{"file_path": "/Users/phillipg/.ssh/known_hosts"}),
	}
	result := rule.Evaluate(input)
	if result.Decision != hookio.Reject {
		t.Errorf("Decision = %v, want Reject for denyWrite path", result.Decision)
	}
}

func TestPathSafety_DenyWrite_CWD_Rejects(t *testing.T) {
	pe := patheval.NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{
		DenyWrite: []string{"/project/secrets"},
	})
	rule := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Write",
		ToolInput: mustJSON(map[string]string{"file_path": "/project/secrets/key.pem", "content": "x"}),
	}
	result := rule.Evaluate(input)
	if result.Decision != hookio.Reject {
		t.Errorf("Decision = %v, want Reject for denyWrite path under CWD", result.Decision)
	}
}

func TestPathSafety_AllowWrite_Approves(t *testing.T) {
	pe := patheval.NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{
		AllowWrite: []string{"/Users/phillipg/.local/share/contained-claude"},
	})
	rule := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Write",
		ToolInput: mustJSON(map[string]string{"file_path": "/Users/phillipg/.local/share/contained-claude/state.json", "content": "x"}),
	}
	result := rule.Evaluate(input)
	if result.Decision != hookio.Approve {
		t.Errorf("Decision = %v, want Approve for allowWrite path", result.Decision)
	}
}

func TestPathSafety_GlobInProject_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Glob",
		ToolInput: mustJSON(map[string]string{"pattern": "**/*.go", "path": "/home/user/project/src"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Glob in project: got %s (%s), want approve", got.Decision, got.Reason)
	}
}

func TestPathSafety_GlobNoPath_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Glob",
		ToolInput: mustJSON(map[string]string{"pattern": "**/*.go"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Glob no path (defaults to CWD): got %s (%s), want approve", got.Decision, got.Reason)
	}
}

func TestPathSafety_GrepInProject_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Grep",
		ToolInput: mustJSON(map[string]string{"pattern": "TODO", "path": "/home/user/project/src"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Grep in project: got %s (%s), want approve", got.Decision, got.Reason)
	}
}

func TestPathSafety_GrepNoPath_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Grep",
		ToolInput: mustJSON(map[string]string{"pattern": "TODO"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Grep no path (defaults to CWD): got %s (%s), want approve", got.Decision, got.Reason)
	}
}

func TestPathSafety_GlobNixStore_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Glob",
		ToolInput: mustJSON(map[string]string{"pattern": "**/*.nix", "path": "/nix/store/abc123-foo"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Glob /nix/store: got %s (%s), want approve (read-only paths support search)", got.Decision, got.Reason)
	}
}

func TestPathSafety_GlobUnknownPath_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Glob",
		ToolInput: mustJSON(map[string]string{"pattern": "*", "path": "/usr/local/bin"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("Glob /usr/local/bin: got %s, want abstain (unknown path)", got.Decision)
	}
}

func TestPathSafety_GlobDenyRead_Reject(t *testing.T) {
	pe := patheval.NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{
		DenyRead: []string{"/Users/phillipg/.ssh"},
	})
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Glob",
		ToolInput: mustJSON(map[string]string{"pattern": "*", "path": "/Users/phillipg/.ssh"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Reject {
		t.Errorf("Glob deny-read path: got %s, want reject", got.Decision)
	}
}

func TestPathSafety_GrepDenyRead_Reject(t *testing.T) {
	pe := patheval.NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{
		DenyRead: []string{"/Users/phillipg/.ssh"},
	})
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Grep",
		ToolInput: mustJSON(map[string]string{"pattern": "password", "path": "/Users/phillipg/.ssh"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Reject {
		t.Errorf("Grep deny-read path: got %s, want reject", got.Decision)
	}
}

// --- ADR 0041: CETA abstains on agent-config writes ----------------------------
//
// The carve-out lives inside path-safety's Write/Edit/MultiEdit/Delete branch, not
// in a rule ahead of it: Abstain means "continue to the next rule", so an earlier
// rule returning Abstain would be a silent no-op. These tests therefore assert on
// path-safety's own verdict, and the matched cases additionally assert that the
// evaluator still reports the path as writable — that is what proves the carve-out
// (rather than the zone classification) is doing the work.

func writeInput(toolName, path, cwd string) *hookio.HookInput {
	return &hookio.HookInput{
		ToolName:  toolName,
		ToolInput: mustJSON(map[string]string{"file_path": path, "content": "x"}),
		CWD:       cwd,
	}
}

func TestPathSafety_WriteProjectAgentConfig_Abstain(t *testing.T) {
	const project = "/home/user/project"
	// Each case is a path the ADR's decision covers. The four logged rows the ADR
	// cites are the settings.local.json and rules.md shapes.
	cases := []struct {
		name string
		tool string
		path string
	}{
		{"settings.local.json (rows 132474, 39391, 57580)", "Write", project + "/.claude/settings.local.json"},
		{"settings.local.json via Edit", "Edit", project + "/.claude/settings.local.json"},
		{"settings.local.json via MultiEdit", "MultiEdit", project + "/.claude/settings.local.json"},
		{"settings.local.json via Delete", "Delete", project + "/.claude/settings.local.json"},
		{"settings.json", "Write", project + "/.claude/settings.json"},
		{"mcp.json", "Write", project + "/.claude/mcp.json"},
		{".mcp.json", "Write", project + "/.claude/.mcp.json"},
		{"rules.md agent-instruction (row 273301)", "Write", project + "/.claude/rules.md"},
		{"CLAUDE.md agent-instruction", "Write", project + "/.claude/CLAUDE.md"},
		{"nested worktree project (row 273301 shape)", "Write", project + "/.workforests/set/repo/.claude/rules.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pe := patheval.New(project)
			r := New(pe)
			if !pe.Evaluate(tc.path).CanWrite() {
				t.Fatalf("precondition: %s is not in a writable zone, so this case cannot show the carve-out is load-bearing", tc.path)
			}
			got := r.Evaluate(writeInput(tc.tool, tc.path, project))
			if got.Decision != hookio.Abstain {
				t.Errorf("%s %s: got %s (%s), want abstain (ADR 0041 — verdict belongs to claude-code)", tc.tool, tc.path, got.Decision, got.Reason)
			}
		})
	}
}

// The decision is Abstain specifically — not Ask and not Reject. CETA encodes no
// verdict of its own here (ADR 0041's Decision).
func TestPathSafety_WriteAgentConfig_EncodesNoVerdict(t *testing.T) {
	const project = "/home/user/project"
	pe := patheval.New(project)
	r := New(pe)
	got := r.Evaluate(writeInput("Write", project+"/.claude/settings.local.json", project))
	if got.Decision == hookio.Ask || got.Decision == hookio.Reject {
		t.Errorf("agent-config write: got %s, want abstain — CETA must not encode a verdict of its own", got.Decision)
	}
	if got.Decision != hookio.Abstain {
		t.Errorf("agent-config write: got %s, want abstain", got.Decision)
	}
}

// Blast radius: everything under `.claude/` that is agent DATA or a per-artifact
// subdirectory stays approved. ADR 0041's Context names "the memory directories,
// skills, plugins, and transcripts" as the collateral that made a subtree-wide
// denyWrite unusable, so they are explicitly out of scope.
func TestPathSafety_WriteProjectClaudeNonConfig_Approve(t *testing.T) {
	const project = "/home/user/project"
	cases := []struct {
		name string
		path string
	}{
		{"skill under .claude/skills", project + "/.claude/skills/my-skill/SKILL.md"},
		{"skill dir index", project + "/.claude/skills/SKILL.md"},
		{"agent definition", project + "/.claude/agents/reviewer.md"},
		{"slash command", project + "/.claude/commands/deploy.md"},
		{"agent data file directly in .claude", project + "/.claude/scheduled_tasks.lock"},
		{"plugin file", project + "/.claude/plugins/foo/plugin.json"},
		{"ordinary project file named settings.json", project + "/config/settings.json"},
		{"ordinary project markdown", project + "/docs/rules.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pe := patheval.New(project)
			r := New(pe)
			got := r.Evaluate(writeInput("Write", tc.path, project))
			if got.Decision != hookio.Approve {
				t.Errorf("Write %s: got %s (%s), want approve — ADR 0041 covers agent config/instruction only", tc.path, got.Decision, got.Reason)
			}
		})
	}
}

// READS ARE UNAFFECTED (ADR 0041's Decision: "Reads are unaffected — this covers
// writes only"). The Read branch is untouched.
func TestPathSafety_ReadAgentConfig_StillApprove(t *testing.T) {
	const project = "/home/user/project"
	for _, p := range []string{
		project + "/.claude/settings.local.json",
		project + "/.claude/settings.json",
		project + "/.claude/rules.md",
	} {
		pe := patheval.New(project)
		r := New(pe)
		input := &hookio.HookInput{
			ToolName:  "Read",
			ToolInput: mustJSON(map[string]string{"file_path": p}),
			CWD:       project,
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("Read %s: got %s (%s), want approve (reads are unaffected)", p, got.Decision, got.Reason)
		}
	}
}

// User-global `~/.claude/` is the ADR's second scope. It is pinned here whether or
// not code was needed: today the evaluator already classifies most of ~/.claude as
// read-only (so path-safety already abstained), but ~/.claude/plans and
// ~/.claude/projects are read-write, and the $HOME-is-the-project-root case below
// makes the whole of ~/.claude writable — so the carve-out is what guarantees it.
func TestPathSafety_WriteUserGlobalAgentConfig_Abstain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "project")
	for _, p := range []string{
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(home, ".claude", "settings.local.json"),
		filepath.Join(home, ".claude", "CLAUDE.md"),
		filepath.Join(home, ".claude", "rules.md"),
	} {
		pe := patheval.New(project)
		r := New(pe)
		got := r.Evaluate(writeInput("Write", p, project))
		if got.Decision != hookio.Abstain {
			t.Errorf("Write %s: got %s (%s), want abstain (ADR 0041 covers ~/.claude too)", p, got.Decision, got.Reason)
		}
	}
}

// Edge case: $HOME is itself the project root, so `<projectRoot>/**` classifies all
// of ~/.claude as read-write and is evaluated BEFORE the ~/.claude zone block. This
// is the user-global case where the evaluator alone would approve.
func TestPathSafety_WriteUserGlobalAgentConfig_HomeIsProjectRoot_Abstain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pe := patheval.New(home)
	r := New(pe)
	p := filepath.Join(home, ".claude", "settings.local.json")
	if !pe.Evaluate(p).CanWrite() {
		t.Fatalf("precondition: %s is not writable with $HOME as project root, so this case cannot show the carve-out is load-bearing", p)
	}
	got := r.Evaluate(writeInput("Write", p, home))
	if got.Decision != hookio.Abstain {
		t.Errorf("Write %s with $HOME as project root: got %s (%s), want abstain", p, got.Decision, got.Reason)
	}
}

// User-global agent DATA — plans, transcripts and memory — stays writable. ADR 0041's
// Context names the memory directories and transcripts as out of scope, and the
// evaluator already zones ~/.claude/plans and ~/.claude/projects read-write ("Claude
// writes plans and memory").
//
// $HOME is ALSO the project root here, and that is deliberate rather than incidental:
// it makes the whole of ~/.claude writable through the `<projectRoot>/**` zone, which
// Evaluate checks FIRST. Relying on the ~/.claude zone instead would make the test
// depend on where the temp HOME lands — inside a nix build sandbox t.TempDir() sits
// under `/nix/**`, whose read-only zone is checked BEFORE the ~/.claude block, so the
// path would abstain for a reason that has nothing to do with the carve-out and the
// assertion would be masked. Pinning writability up front means an Approve here can
// only mean the carve-out let the path through.
func TestPathSafety_WriteUserGlobalClaudeData_Approve(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, p := range []string{
		filepath.Join(home, ".claude", "projects", "-some-proj", "memory", "MEMORY.md"),
		filepath.Join(home, ".claude", "plans", "some-plan.md"),
		filepath.Join(home, ".claude", "skills", "my-skill", "SKILL.md"),
	} {
		pe := patheval.New(home)
		r := New(pe)
		if !pe.Evaluate(p).CanWrite() {
			t.Fatalf("precondition: %s is not in a writable zone, so an Approve here would not show the carve-out let it through", p)
		}
		got := r.Evaluate(writeInput("Write", p, home))
		if got.Decision != hookio.Approve {
			t.Errorf("Write %s: got %s (%s), want approve — agent data, not agent config", p, got.Decision, got.Reason)
		}
	}
}

// A `~`-prefixed or cwd-relative path names the same file and must match too.
func TestPathSafety_WriteAgentConfig_NonAbsoluteForms_Abstain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "project")
	for _, p := range []string{
		"~/.claude/settings.local.json",
		".claude/settings.local.json",
		"./.claude/rules.md",
		".claude/../.claude/settings.json",
	} {
		pe := patheval.New(project)
		r := New(pe)
		got := r.Evaluate(writeInput("Write", p, project))
		if got.Decision != hookio.Abstain {
			t.Errorf("Write %q: got %s (%s), want abstain", p, got.Decision, got.Reason)
		}
	}
}

// A symlink pointing INTO `.claude` must not slip the write past the check.
func TestPathSafety_WriteAgentConfigViaSymlink_Abstain(t *testing.T) {
	project := t.TempDir()
	claudeDir := filepath.Join(project, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(claudeDir, "settings.local.json")
	if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(project, "my-settings.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	pe := patheval.New(project)
	r := New(pe)
	got := r.Evaluate(writeInput("Write", link, project))
	if got.Decision != hookio.Abstain {
		t.Errorf("Write via symlink to .claude/settings.local.json: got %s (%s), want abstain", got.Decision, got.Reason)
	}
}

// An explicit sandbox.filesystem.denyWrite entry is a decision the user configured;
// Reject still wins over the ADR 0041 abstain. (The carve-out itself does NOT use
// IsDenyWrite — the mechanism cannot express it, per ADR 0041's Context.)
func TestPathSafety_WriteAgentConfig_DenyWriteStillRejects(t *testing.T) {
	const project = "/home/user/project"
	pe := patheval.NewWithCWD(project, project)
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{
		DenyWrite: []string{project + "/.claude"},
	})
	r := New(pe)
	got := r.Evaluate(writeInput("Write", project+"/.claude/settings.local.json", project))
	if got.Decision != hookio.Reject {
		t.Errorf("deny-write agent config: got %s (%s), want reject (explicit user config wins)", got.Decision, got.Reason)
	}
}
