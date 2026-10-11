package focus

// Behavior-doc conformance of the focus verbs (daily-focus design, "Doc
// conformance"): every invariant id that docs/behavior/pg-desk/focus.md
// defines is cited by a test, and the bead-state table of that doc equals the
// Go table BeadStates row for row.
//
// The behavior docs live outside this Go module (at the repository root), so
// inside the nix build sandbox, which holds only the module's own source
// tree, they are not visible. readOutsideModule turns exactly that case into
// an explicit skip, and nothing else: a missing file in any other
// environment, a normal checkout included, is a failure, so these checks
// cannot go silently green.
//
// Telemetry (D24): test code; it emits and logs nothing.

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// behaviorDocPath is focus.md relative to this module's root.
const behaviorDocPath = "../../docs/behavior/pg-desk/focus.md"

// minDocBytes is the least a focus.md that was really read can weigh; a
// truncated or empty read must not pass as "no invariants, nothing to trace".
const minDocBytes = 4000

// minInvariants is the number of invariants the doc defined when this check
// landed; the doc only grows, so fewer means a truncated or mis-parsed doc.
const minInvariants = 48

// inNixSandbox reports whether the process runs inside a nix build: nix sets
// NIX_BUILD_TOP in the builder environment (it is also set in a dev shell,
// where the doc is visible and the skip never triggers).
func inNixSandbox() bool { return os.Getenv("NIX_BUILD_TOP") != "" }

// missingFileOutcome is the policy for a file outside the module: skip only
// when it is genuinely absent AND the run is inside the nix sandbox.
func missingFileOutcome(err error, sandbox bool) (skip bool) {
	return err != nil && errors.Is(err, fs.ErrNotExist) && sandbox
}

// moduleRootDir walks up from the working directory to this module's own
// go.mod and fails, never skips, when there is none.
func moduleRootDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		if filepath.Dir(dir) == dir {
			t.Fatalf("no go.mod found above %s: the module root walk MUST NOT skip", wd)
		}
	}
}

// readOutsideModule reads a file that lives outside this Go module. See the
// file comment for when it skips.
func readOutsideModule(t *testing.T, what, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err == nil {
		return b
	}
	if missingFileOutcome(err, inNixSandbox()) {
		t.Skipf("%s is not visible from this build (%s): the nix build sandbox holds only this Go module's "+
			"source tree, so this check runs in the repository checkout (commit-time and pre-land hooks)", what, path)
	}
	t.Fatalf("read %s (%s): %v", what, path, err)
	return nil
}

func TestMissingFileOutcomeSkipsOnlyInsideTheNixSandbox(t *testing.T) {
	absent := fmt.Errorf("open x: %w", fs.ErrNotExist)
	for _, tc := range []struct {
		name    string
		err     error
		sandbox bool
		want    bool
	}{
		{"absent in the sandbox", absent, true, true},
		{"absent in a checkout", absent, false, false},
		{"present", nil, true, false},
		{"unreadable in the sandbox", fs.ErrPermission, true, false},
		{"unreadable in a checkout", fs.ErrPermission, false, false},
	} {
		if got := missingFileOutcome(tc.err, tc.sandbox); got != tc.want {
			t.Errorf("%s: skip = %v, want %v", tc.name, got, tc.want)
		}
	}
	t.Setenv("NIX_BUILD_TOP", "")
	if inNixSandbox() {
		t.Error("an empty NIX_BUILD_TOP must not read as the sandbox")
	}
	t.Setenv("NIX_BUILD_TOP", "/build")
	if !inNixSandbox() {
		t.Error("a set NIX_BUILD_TOP must read as the sandbox")
	}
}

func readFocusDoc(t *testing.T) string {
	t.Helper()
	b := readOutsideModule(t, "docs/behavior/pg-desk/focus.md", filepath.Join(moduleRootDir(t), behaviorDocPath))
	if len(b) < minDocBytes {
		t.Fatalf("focus.md is %d bytes, want at least %d: a vacuous read of the behavior doc", len(b), minDocBytes)
	}
	return string(b)
}

// ---- the state table (section 8.3) ----------------------------------------

// splitRow splits one markdown table line into trimmed, unquoted cells; nil
// for a line that is not a table row.
func splitRow(l string) []string {
	l = strings.TrimSpace(l)
	if !strings.HasPrefix(l, "|") || !strings.HasSuffix(l, "|") {
		return nil
	}
	parts := strings.Split(strings.Trim(l, "|"), "|")
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strings.Trim(strings.TrimSpace(p), "`")
	}
	return out
}

// parseStateTable returns the rows of the bead-state table of doc: the table
// whose header is Bead state, Shown as, On strike, On select, Skip reason.
func parseStateTable(doc string) [][]string {
	var rows [][]string
	in := false
	for _, l := range strings.Split(doc, "\n") {
		cells := splitRow(l)
		if len(cells) == 5 && cells[0] == "Bead state" {
			in = true
			continue
		}
		if !in {
			continue
		}
		if len(cells) != 5 {
			break
		}
		if strings.HasPrefix(cells[0], "---") || strings.HasPrefix(cells[0], ":--") {
			continue
		}
		rows = append(rows, cells)
	}
	return rows
}

// diffStateTable compares parsed doc rows with the Go table, row for row, on
// the columns Bead state, Shown as, On strike, On select and Skip reason, and
// returns one line per difference.
func diffStateTable(rows [][]string, table []BeadStateRow) []string {
	var diffs []string
	if len(rows) != len(table) {
		diffs = append(diffs, fmt.Sprintf("doc table has %d rows, BeadStates has %d", len(rows), len(table)))
	}
	columns := []string{"Bead state", "Shown as", "On strike", "On select", "Skip reason"}
	for i := 0; i < len(rows) && i < len(table); i++ {
		r := table[i]
		want := []string{r.Description, r.Shown, r.StrikeEcho, r.SelectEcho, r.SkipReason}
		for j := range want {
			if rows[i][j] != want[j] {
				diffs = append(diffs, fmt.Sprintf("row %d (%s), column %q: doc has %q, BeadStates has %q",
					i+1, r.State, columns[j], rows[i][j], want[j]))
			}
		}
	}
	return diffs
}

// renderStateTable writes table as the markdown table diffStateTable parses.
func renderStateTable(table []BeadStateRow) string {
	var sb strings.Builder
	sb.WriteString("| Bead state | Shown as | On strike | On select | Skip reason |\n")
	sb.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, r := range table {
		fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s |\n", r.Description, r.Shown, r.StrikeEcho, r.SelectEcho, r.SkipReason)
	}
	return sb.String()
}

// TestSection83TableMatchesCode keeps the state-by-outcome table of
// docs/behavior/pg-desk/focus.md (kept as a table for exactly this purpose)
// equal to the Go table BeadStates: Shown as, strike echo, select echo and
// skip reason, one row per row, same words, same order.
func TestSection83TableMatchesCode(t *testing.T) {
	rows := parseStateTable(readFocusDoc(t))
	if len(rows) == 0 {
		t.Fatal("no state table found in focus.md: the header row Bead state | Shown as | On strike | On select | Skip reason is missing")
	}
	for _, d := range diffStateTable(rows, BeadStates) {
		t.Error(d)
	}
}

// The comparator is only worth its green if it turns red on a drift: a
// planted one-word change in any column, a dropped row and an extra row are
// each reported, and an unchanged render is clean.
func TestSection83ComparatorDetectsPlantedDrift(t *testing.T) {
	clean := parseStateTable(renderStateTable(BeadStates))
	if len(clean) != len(BeadStates) {
		t.Fatalf("render/parse round trip lost rows: %d of %d", len(clean), len(BeadStates))
	}
	if d := diffStateTable(clean, BeadStates); len(d) != 0 {
		t.Fatalf("an unchanged table differs: %v", d)
	}
	for col := 0; col < 5; col++ {
		mutated := make([][]string, len(clean))
		for i := range clean {
			mutated[i] = append([]string(nil), clean[i]...)
		}
		mutated[3][col] += "x"
		d := diffStateTable(mutated, BeadStates)
		if len(d) != 1 || !strings.Contains(d[0], "row 4") {
			t.Errorf("column %d: a one-word change is reported as %v, want exactly one difference naming row 4", col, d)
		}
	}
	if d := diffStateTable(clean[1:], BeadStates); len(d) == 0 {
		t.Error("a dropped row went unreported")
	}
	if d := diffStateTable(append(clean, clean[0]), BeadStates); len(d) == 0 {
		t.Error("an extra row went unreported")
	}
}

// ---- invariant traceability ----------------------------------------------

var (
	// invDefinition matches the definition line of an invariant in focus.md.
	invDefinition = regexp.MustCompile(`\*\*INV-FOCUS-(\d+)\.\*\*`)
	// invCitation matches a citation in a comment or a subtest name; the
	// trailing boundary keeps INV-FOCUS-1 from tracing INV-FOCUS-10.
	invCitation = regexp.MustCompile(`INV-FOCUS-(\d+)\b`)
	// invFuncName matches the spelling a Go function name allows.
	invFuncName = regexp.MustCompile(`(?i)INV_?FOCUS_?(\d+)(?:_|$|[A-Z])`)
)

// definedInvariants returns the ids the doc defines, in order of appearance.
func definedInvariants(doc string) []int {
	var ids []int
	for _, m := range invDefinition.FindAllStringSubmatch(doc, -1) {
		n, _ := strconv.Atoi(m[1])
		ids = append(ids, n)
	}
	return ids
}

// citedInvariants scans every *_test.go file under root (testdata and the
// conformance files themselves excepted) for an id in a comment, a Test or
// Fuzz function name, or the name argument of a Run call, and returns each
// cited id with the files citing it.
func citedInvariants(root string, skip map[string]bool) (map[int][]string, int, error) {
	cited := map[int][]string{}
	files := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "vendor" || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") || skip[d.Name()] {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		files++
		rel, _ := filepath.Rel(root, path)
		note := func(text string, re *regexp.Regexp) {
			for _, m := range re.FindAllStringSubmatch(text, -1) {
				n, _ := strconv.Atoi(m[1])
				cited[n] = append(cited[n], rel)
			}
		}
		for _, cg := range f.Comments {
			note(cg.Text(), invCitation)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				if strings.HasPrefix(x.Name.Name, "Test") || strings.HasPrefix(x.Name.Name, "Fuzz") {
					note(x.Name.Name, invFuncName)
				}
			case *ast.CallExpr:
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Run" && len(x.Args) > 0 {
					if lit, ok := x.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s, err := strconv.Unquote(lit.Value); err == nil {
							note(s, invCitation)
						}
					}
				}
			}
			return true
		})
		return nil
	})
	return cited, files, err
}

// untracedInvariants returns the defined ids no test cites, and the cited ids
// the doc does not define (a stale or mistyped citation), each sorted.
func untracedInvariants(defined []int, cited map[int][]string) (untraced, dangling []int) {
	def := map[int]bool{}
	for _, id := range defined {
		def[id] = true
		if len(cited[id]) == 0 {
			untraced = append(untraced, id)
		}
	}
	for id := range cited {
		if !def[id] {
			dangling = append(dangling, id)
		}
	}
	sort.Ints(untraced)
	sort.Ints(dangling)
	return untraced, dangling
}

// TestFocusInvariantsAreTraced fails naming each INV-FOCUS-n id of
// docs/behavior/pg-desk/focus.md that no test name, subtest name or test
// comment in this module cites, and each citation of an id the doc does not
// define.
func TestFocusInvariantsAreTraced(t *testing.T) {
	defined := definedInvariants(readFocusDoc(t))
	seen := map[int]bool{}
	for _, id := range defined {
		if seen[id] {
			t.Errorf("INV-FOCUS-%d is defined twice in focus.md", id)
		}
		seen[id] = true
	}
	if len(seen) < minInvariants {
		t.Fatalf("focus.md defines %d invariants, want at least %d: a vacuous or truncated parse", len(seen), minInvariants)
	}
	for id := 1; id <= len(seen); id++ {
		if !seen[id] {
			t.Errorf("INV-FOCUS-%d is missing: the ids must run 1..%d without a gap", id, len(seen))
		}
	}
	cited, files, err := citedInvariants(moduleRootDir(t), map[string]bool{"doc_conformance_test.go": true})
	if err != nil {
		t.Fatal(err)
	}
	if files < 20 {
		t.Fatalf("scanned only %d test files: a vacuous scan", files)
	}
	untraced, dangling := untracedInvariants(defined, cited)
	for _, id := range untraced {
		t.Errorf("INV-FOCUS-%d is cited by no test name or test comment in this module", id)
	}
	for _, id := range dangling {
		t.Errorf("INV-FOCUS-%d is cited in %v but focus.md does not define it", id, cited[id])
	}
}

// A planted module: the scan reads a comment, a test name, a subtest name and
// a fuzz name, ignores a string that is not a name, does not let INV-FOCUS-1
// trace INV-FOCUS-10, and the check names exactly the gaps.
func TestTraceabilityScanFindsPlantedGaps(t *testing.T) {
	dir := t.TempDir()
	src := `package x

import "testing"

// Traces INV-FOCUS-1 and INV-FOCUS-30.
func TestA(t *testing.T) {
	t.Run("INV-FOCUS-2 subtest", func(t *testing.T) {})
	_ = "INV-FOCUS-4 in a string that is not a name"
}

func TestInvFocus5Spelling(t *testing.T) {}

func FuzzINV_FOCUS_6(f *testing.F) {}
`
	if err := os.WriteFile(filepath.Join(dir, "a_test.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "testdata", "ignored_test.go"), []byte("package x\n// INV-FOCUS-7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cited, files, err := citedInvariants(dir, nil)
	if err != nil || files != 1 {
		t.Fatalf("scan = %d files, err %v", files, err)
	}
	untraced, dangling := untracedInvariants([]int{1, 2, 3, 4, 5, 6, 7, 10}, cited)
	if fmt.Sprint(untraced) != "[3 4 7 10]" {
		t.Errorf("untraced = %v, want [3 4 7 10] (4 is only in a string, 7 only under testdata, 10 only collides with 1)", untraced)
	}
	if fmt.Sprint(dangling) != "[30]" {
		t.Errorf("dangling = %v, want [30]", dangling)
	}
}

// The defined-id parse reads only definition lines, not citations in prose.
func TestDefinedInvariantsReadsDefinitionLinesOnly(t *testing.T) {
	doc := "- **INV-FOCUS-1.** A rule.\nSee INV-FOCUS-9 above.\n- **INV-FOCUS-2.** Another.\n"
	if got := fmt.Sprint(definedInvariants(doc)); got != "[1 2]" {
		t.Errorf("defined = %s, want [1 2]", got)
	}
}
