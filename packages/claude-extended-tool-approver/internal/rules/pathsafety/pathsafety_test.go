package pathsafety

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
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
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
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
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
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
	got := hookio.Verdict(r.Evaluate(input))
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
	result := hookio.Verdict(rule.Evaluate(input))
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
	result := hookio.Verdict(rule.Evaluate(input))
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
	result := hookio.Verdict(rule.Evaluate(input))
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
	result := hookio.Verdict(rule.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
			got := hookio.Verdict(r.Evaluate(writeInput(tc.tool, tc.path, project)))
			if got.Decision != hookio.NoOpinion {
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
	got := hookio.Verdict(r.Evaluate(writeInput("Write", project+"/.claude/settings.local.json", project)))
	if got.Decision == hookio.Ask || got.Decision == hookio.Reject {
		t.Errorf("agent-config write: got %s, want abstain — CETA must not encode a verdict of its own", got.Decision)
	}
	if got.Decision != hookio.NoOpinion {
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
			got := hookio.Verdict(r.Evaluate(writeInput("Write", tc.path, project)))
			if got.Decision != hookio.Approve {
				t.Errorf("Write %s: got %s (%s), want approve — ADR 0041 covers agent config/instruction only", tc.path, got.Decision, got.Reason)
			}
		})
	}
}

// CASE FOLDING (pg2-2ng80). isAgentConfigPath originally folded case in ONE of its
// three parts — the `.md` extension — and compared the directory name and the config
// basenames exactly. On this machine that made the control bypassable rather than
// merely imprecise: the home volume is APFS and case-INSENSITIVE, verified by
// creating `.claude/settings.local.json` and reading the same bytes back through
// `.CLAUDE/settings.local.json` and `.claude/Settings.Local.json`. So a case-varied
// spelling named the SAME real agent-config file, matched nothing, fell through to
// the CanWrite() approve, and was auto-approved.
//
// MATCHING AND NON-MATCHING SHAPES SHARE ONE TABLE ON PURPOSE. The fix has two
// halves that pull in opposite directions — make the predicate case-insensitive,
// and keep it bounded to the IMMEDIATE children of `.claude` — and each is easy to
// satisfy while regressing the other (folding by prefix-matching `.claude` anywhere
// in the path would pass every Abstain row here and silently start blocking the
// memory directories, skills, plugins and transcripts ADR 0041's Context keeps in
// scope for writing). Splitting these rows into two tests would let a later reader
// fix one and break the other. Every `want: Approve` row is therefore a case-varied
// path exactly ONE level deeper than `.claude`, or a directory that merely resembles
// `.claude`.
func TestPathSafety_WriteAgentConfig_CaseFolding(t *testing.T) {
	const project = "/home/user/project"
	cases := []struct {
		name string
		path string
		want hookio.Decision
	}{
		// --- part 1 of the predicate: the PARENT DIRECTORY name ---
		{"dir .CLAUDE + config basename", project + "/.CLAUDE/settings.local.json", hookio.NoOpinion},
		{"dir .CLAUDE + one level deeper", project + "/.CLAUDE/skills/x.md", hookio.Approve},
		{"dir .Claude + config basename", project + "/.Claude/settings.json", hookio.NoOpinion},
		{"dir .Claude + one level deeper", project + "/.Claude/plugins/foo/plugin.json", hookio.Approve},

		// --- part 2 of the predicate: the config BASENAME set ---
		{"basename Settings.Local.json", project + "/.claude/Settings.Local.json", hookio.NoOpinion},
		{"basename SETTINGS.JSON", project + "/.claude/SETTINGS.JSON", hookio.NoOpinion},
		{"basename MCP.json", project + "/.claude/MCP.json", hookio.NoOpinion},
		{"basename .MCP.JSON", project + "/.claude/.MCP.JSON", hookio.NoOpinion},
		{"basename SETTINGS.JSON one level deeper", project + "/.claude/agents/SETTINGS.JSON", hookio.Approve},

		// --- part 3 of the predicate: the `.md` EXTENSION (folded before the fix too;
		// pinned here so the three parts are asserted side by side and cannot drift) ---
		{"ext RULES.MD", project + "/.claude/RULES.MD", hookio.NoOpinion},
		{"ext Claude.Md", project + "/.claude/Claude.Md", hookio.NoOpinion},
		{"ext DEPLOY.MD one level deeper", project + "/.claude/commands/DEPLOY.MD", hookio.Approve},

		// --- every part varied at once ---
		{"all-caps dir and config basename", project + "/.CLAUDE/SETTINGS.LOCAL.JSON", hookio.NoOpinion},
		{"all-caps dir and instruction basename", project + "/.CLAUDE/CLAUDE.MD", hookio.NoOpinion},
		{"all-caps dir, one level deeper", project + "/.CLAUDE/SKILLS/MY-SKILL/SKILL.MD", hookio.Approve},

		// --- folding MUST NOT turn into fuzzy matching: a directory that merely
		// resembles `.claude` is still a different directory ---
		{"dir claude without the dot", project + "/claude/settings.json", hookio.Approve},
		{"dir .claudex", project + "/.claudex/settings.json", hookio.Approve},
		{"dir .claude.bak", project + "/.claude.bak/settings.json", hookio.Approve},
	}
	for _, tc := range cases {
		for _, tool := range []string{"Write", "Edit", "MultiEdit", "Delete"} {
			t.Run(tc.name+"/"+tool, func(t *testing.T) {
				pe := patheval.New(project)
				r := New(pe)
				// Both directions need this precondition: an Abstain only proves the
				// carve-out fired if the zone would otherwise have approved, and an
				// Approve only proves the fold did not widen if the zone permits it.
				if !pe.Evaluate(tc.path).CanWrite() {
					t.Fatalf("precondition: %s is not in a writable zone, so this case cannot distinguish the carve-out from zone classification", tc.path)
				}
				got := hookio.Verdict(r.Evaluate(writeInput(tool, tc.path, project)))
				if got.Decision != tc.want {
					t.Errorf("%s %s: got %s (%s), want %s", tool, tc.path, got.Decision, got.Reason, tc.want)
				}
			})
		}
	}
}

// CRITERION: all three parts of the predicate AGREE on case handling. Asserted
// directly on the predicate (not through Evaluate) so the agreement is visible as
// one property rather than inferred from end-to-end verdicts, and with the bounded
// cases in the SAME table for the reason given above.
func TestIsAgentConfigPath_AllThreePartsFoldCase(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"baseline, all lowercase", "/p/.claude/settings.json", true},
		{"part 1 varied: parent dir", "/p/.CLAUDE/settings.json", true},
		{"part 2 varied: config basename", "/p/.claude/SETTINGS.JSON", true},
		{"part 3 varied: .md extension", "/p/.claude/rules.MD", true},
		{"all three varied at once", "/p/.CLAUDE/CLAUDE.MD", true},

		// The depth-1 bound and the exact-name requirement survive the fold.
		{"bound: one level deeper, case varied", "/p/.CLAUDE/skills/SKILL.MD", false},
		{"bound: dir merely resembles .claude", "/p/.claudex/settings.json", false},
		{"bound: non-config, non-md basename", "/p/.claude/scheduled_tasks.lock", false},
		{"empty path", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAgentConfigPath(tc.path); got != tc.want {
				t.Errorf("isAgentConfigPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// THE FOLD PRIMITIVE IS strings.EqualFold, NOT strings.ToLower — and that is a
// correctness requirement, not a style choice. EqualFold implements Unicode simple
// case FOLDING; ToLower implements simple case MAPPING; the two disagree, and the
// disagreement is reachable on this filesystem. Verified on this machine's APFS
// volume: after `echo real > .claude/settings.local.json`,
// `cat .claude/ſettings.local.json` (U+017F LATIN SMALL LETTER LONG S) prints
// `real` — the filesystem folds `ſ` to `s`. EqualFold matches that spelling; ToLower
// leaves the `ſ` untouched and MISSES it, which is the pg2-2ng80 bypass one codepoint
// over: a path that names the genuine agent-config file, matches nothing, falls
// through to CanWrite() and is auto-approved.
//
// This test fails if the predicate is ever "simplified" to a ToLower/lowercased-key
// form.
func TestPathSafety_WriteAgentConfig_FoldsNotMerelyLowercases(t *testing.T) {
	const project = "/home/user/project"
	const canonical = "settings.local.json"
	const witness = "ſettings.local.json" // ſettings.local.json

	// Pin what makes this witness decisive, so a Go stdlib change cannot turn the
	// test into a silent tautology.
	if !strings.EqualFold(witness, canonical) {
		t.Fatalf("premise: EqualFold(%q, %q) must be true for this witness to exercise the fold", witness, canonical)
	}
	if strings.ToLower(witness) == canonical {
		t.Fatalf("premise: ToLower(%q) must NOT equal %q, or the witness cannot distinguish folding from lowercasing", witness, canonical)
	}

	p := project + "/.claude/" + witness
	pe := patheval.New(project)
	r := New(pe)
	if !pe.Evaluate(p).CanWrite() {
		t.Fatalf("precondition: %s is not in a writable zone, so this case cannot distinguish the carve-out from zone classification", p)
	}
	got := hookio.Verdict(r.Evaluate(writeInput("Write", p, project)))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("Write %s: got %s (%s), want abstain — the predicate must FOLD case (strings.EqualFold), not merely lowercase", p, got.Decision, got.Reason)
	}
}

// READS ARE UNAFFECTED (ADR 0041's Decision: "Reads are unaffected — this covers
// writes only"). The Read branch is untouched — including by the pg2-2ng80 case
// fold, so the case-varied spellings are listed here too.
func TestPathSafety_ReadAgentConfig_StillApprove(t *testing.T) {
	const project = "/home/user/project"
	for _, p := range []string{
		project + "/.claude/settings.local.json",
		project + "/.claude/settings.json",
		project + "/.claude/rules.md",
		project + "/.CLAUDE/settings.local.json",
		project + "/.Claude/settings.json",
		project + "/.claude/Settings.Local.json",
		project + "/.claude/RULES.MD",
	} {
		pe := patheval.New(project)
		r := New(pe)
		input := &hookio.HookInput{
			ToolName:  "Read",
			ToolInput: mustJSON(map[string]string{"file_path": p}),
			CWD:       project,
		}
		got := hookio.Verdict(r.Evaluate(input))
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
		got := hookio.Verdict(r.Evaluate(writeInput("Write", p, project)))
		if got.Decision != hookio.NoOpinion {
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
	got := hookio.Verdict(r.Evaluate(writeInput("Write", p, home)))
	if got.Decision != hookio.NoOpinion {
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
		got := hookio.Verdict(r.Evaluate(writeInput("Write", p, home)))
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
		got := hookio.Verdict(r.Evaluate(writeInput("Write", p, project)))
		if got.Decision != hookio.NoOpinion {
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
	got := hookio.Verdict(r.Evaluate(writeInput("Write", link, project)))
	if got.Decision != hookio.NoOpinion {
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
	got := hookio.Verdict(r.Evaluate(writeInput("Write", project+"/.claude/settings.local.json", project)))
	if got.Decision != hookio.Reject {
		t.Errorf("deny-write agent config: got %s (%s), want reject (explicit user config wins)", got.Decision, got.Reason)
	}
}

// TestADR0044_PathSafety_RefusedSites is the per-rule half of pg2-qxe85's census for
// path-safety: the three sites pg2-d0ja3 left as ErrNotApplicable now REFUSE.
//
// Path-safety is the highest-value rule in the remainder, because it is the one whose
// not-applicable was most easily mistaken for an exhaustion: it runs EARLY (before mcp and
// every Bash rule), it is the only rule that classifies a file-tool path at all, and its
// declines therefore reached the loop exhaustion with nobody having said anything. A
// consumer reading "no rule modelled this path" where the truth is "the evaluator declined
// to clear it" is the approval-widening misreading ADR 0044 exists to stop.
//
// The assertion is a RELATION, not a hardcoded verdict: refused, with a floor no weaker
// than NoOpinion, attributed to this rule, and still matching ErrNotApplicable so an
// un-upgraded consumer keeps working.
func TestADR0044_PathSafety_RefusedSites(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	tests := []struct {
		site      string
		tool      string
		toolInput map[string]string
	}{
		{
			site: "Read of a path the evaluator will not clear", tool: "Read",
			toolInput: map[string]string{"file_path": "/usr/bin/ls"},
		},
		{
			site: "Write to a path the evaluator will not clear", tool: "Write",
			toolInput: map[string]string{"file_path": "/etc/hosts", "content": "x"},
		},
		{
			site: "search over a path the evaluator will not clear", tool: "Glob",
			toolInput: map[string]string{"pattern": "*", "path": "/usr/local/bin"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.site, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: tt.tool, ToolInput: mustJSON(tt.toolInput), CWD: "/home/user/project"}
			res, err := r.Evaluate(input)
			if !errors.Is(err, hookio.ErrRefused) {
				t.Fatalf("%s: err=%v res=%+v, want ErrRefused — a declined path reported as not-applicable reads as an EXHAUSTION", tt.site, err, res)
			}
			if res.Decision < hookio.NoOpinion {
				t.Errorf("%s: floor is %s, weaker than NoOpinion", tt.site, res.Decision)
			}
			if res.Reason == "" || res.Module != r.Name() {
				t.Errorf("%s: floor = %+v, want a reasoned refusal attributed to %q", tt.site, res, r.Name())
			}
			if !errors.Is(err, hookio.ErrNotApplicable) {
				t.Errorf("%s: refusal does not match ErrNotApplicable; the engine would file it as a FAILURE", tt.site)
			}
		})
	}
}

// TestADR0044_PathSafety_AgentConfigSiteStaysTerminal is the boundary this conversion must
// not cross, and it is asserted separately because the two shapes look alike in a diff.
//
// ADR 0041 requires the agent-config write branch to STOP the chain: a refusal continues
// it, so converting that site would let a later rule act on a write ADR 0041 exists to
// keep un-approved. It stays a terminal NoOpinion with a nil error — the ONE site in the
// ruleset with that shape.
func TestADR0044_PathSafety_AgentConfigSiteStaysTerminal(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Write",
		ToolInput: mustJSON(map[string]string{"file_path": "/home/user/project/.claude/settings.local.json", "content": "{}"}),
		CWD:       "/home/user/project",
	}
	res, err := r.Evaluate(input)
	if err != nil {
		t.Fatalf("agent-config write returned err=%v; ADR 0041 requires a TERMINAL verdict, and any error (refusal included) continues the chain", err)
	}
	if res.Decision != hookio.NoOpinion {
		t.Errorf("agent-config write = %s, want abstain (ADR 0041)", res.Decision)
	}
}
