// Package legacyextract is docket tc-o14i5.1 ("Phase 0: Decisions, freeze,
// oracle")'s packet 0.3: an automated converter that turns legacy
// EvaluateHook/RuleChain-driven table-test cases (internal/engine,
// internal/rules/*) into goldencorpus.Row entries tagged "legacy", per the
// docket design's Phase 0 item 3.
//
// # Scope of this pass — read before trusting the numbers
//
// internal/engine and internal/rules/* together hold ~39k lines of test
// code across 79 files using a wide variety of table shapes (this packet's
// own recount, not the design's ~2,969 heuristic, is the number actually
// used for the 95% bar — see Result.Stats and the generated disposition
// report). Rather than a bespoke parser per file, this converter recognises
// exactly TWO idioms that between them cover the dominant style observed
// across every family sampled (engine, git, nix, kubectl, docker):
//
//   - Idiom A ("batch"): a `for _, v := range <stringSlice>` loop whose body
//     asserts `<expr>.Decision != hookio.<Const>` (t.Error/t.Errorf/
//     t.Fatalf on the mismatch branch) — every element of the slice shares
//     that one expected Decision.
//   - Idiom B ("struct table"): a `for _, v := range <structSlice>` loop
//     over a `[]struct{ ... }` literal whose element type has a string
//     "command-like" field (name matches command|cmd|input, case
//     insensitive) and a `hookio.Decision`-typed field — the classic
//     table-driven-test shape, in EITHER its keyed or positional form.
//
// In both idioms, the loop body is also expected to construct a
// `hookio.HookInput{...}` literal (ToolName + a ToolInput built via
// `mustJSON(map[string]string{...})` or a `hookio.FileToolInput{...}`
// literal) with exactly one field's value tracing back to the loop
// variable (directly, for idiom A, or via a field selector, for idiom B).
// A range loop, or a loop-body HookInput, that does not fit this shape is
// NOT counted as a recognised table (neither numerator nor denominator) —
// it falls to the "unrecognised" remainder named in the disposition
// report, which is the honest accounting this packet's own exit
// criterion asks for ("or list the shortfall explicitly as remainder").
//
// Every row this converter DOES emit sets Mode=default, empty
// Settings/RulesConfig (the corpus schema's own documented "no operator
// configuration" default), CWD/ProjectRoot from a literal `CWD:` field on
// the matched HookInput literal if present, else "/repo" — precedence/
// plan-mode measurement is sibling packet "Phase 0.4"'s scope, not this
// one's.
package legacyextract

import (
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

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/goldencorpus"
)

// declToVerdict is the MANDATORY OLD->NEW mapping this packet's own docket
// binding decision fixes (allow/Approve -> approve; deny/Reject -> reject;
// abstain/NoOpinion -> not-approve; ask/Ask -> not-approve; NULL/unmapped ->
// not-approve). "Abstain" is kept as a fallback alias in case a still-older
// test file predates the ADR 0043 NoOpinion rename.
var declToVerdict = map[string]goldencorpus.Verdict{
	"Approve":   goldencorpus.Approve,
	"Reject":    goldencorpus.Reject,
	"NoOpinion": goldencorpus.NotApprove,
	"Ask":       goldencorpus.NotApprove,
	"Abstain":   goldencorpus.NotApprove,
}

// UnconvertibleRow records one element of a RECOGNISED table (idiom A or B)
// whose command or decision could not be resolved statically (e.g. built
// via fmt.Sprintf, a variable, or a helper call) -- counted in the
// denominator, never the numerator.
type UnconvertibleRow struct {
	File    string
	Func    string
	Reason  string
	Snippet string
}

// UnrecognisedTable records a for-range loop over a literal slice that this
// converter could not classify as idiom A or B at all (neither numerator
// nor denominator -- listed separately in the disposition report as a
// remainder item, distinct from an UnconvertibleRow inside a table this
// converter DID recognise).
type UnrecognisedTable struct {
	File   string
	Func   string
	Reason string
}

// FakeStubFile / TempDirFile record the manual-disposition-carve-out
// inventory (docket design, Phase 0 item 3): files whose tests depend on
// rule-level fakes/stubs, or build tempdir state, per the mechanical hint
// this packet's own packet text supplies (grep for `type [Ff]ake|type
// .*Stub\b` / `t.TempDir\(\)|ioutil.TempDir`). "Family" is the
// internal/rules/<subpackage> (or "engine") directory the file lives under.
type FlaggedFile struct {
	File   string
	Family string
}

// Stats is the packet's own re-counted denominator/numerator (Phase 0 item
// 3's exit criterion), NOT the design's ~2,969 heuristic figure.
type Stats struct {
	FilesScanned      int
	RecognisedTables  int // idiom-A batches + idiom-B struct tables
	Denominator       int // total elements across every RECOGNISED table
	Extracted         int // elements successfully converted to a corpus Row
	UnconvertibleRows int // Denominator - Extracted
}

// Ratio is Extracted/Denominator, the exit-criterion fraction (>= 0.95
// required, else the shortfall is the remainder this packet's disposition
// report lists explicitly).
func (s Stats) Ratio() float64 {
	if s.Denominator == 0 {
		return 0
	}
	return float64(s.Extracted) / float64(s.Denominator)
}

// Result is everything Run produces.
type Result struct {
	Rows               []goldencorpus.Row
	Stats              Stats
	UnconvertibleRows  []UnconvertibleRow
	UnrecognisedTables []UnrecognisedTable
	FakeStubFiles      []FlaggedFile
	TempDirFiles       []FlaggedFile
}

var (
	fakeStubPattern = regexp.MustCompile(`type\s+[Ff]ake\w*|type\s+\w*Stub\b`)
	tempDirPattern  = regexp.MustCompile(`t\.TempDir\(\)|ioutil\.TempDir`)
)

// Run walks repoRoot's internal/engine and internal/rules/** test files and
// extracts every recognised legacy table into corpus rows tagged "legacy".
// repoRoot is the claude-extended-tool-approver module root (the directory
// containing go.mod).
func Run(repoRoot string) (*Result, error) {
	engineDir := filepath.Join(repoRoot, "internal", "engine")
	rulesDir := filepath.Join(repoRoot, "internal", "rules")

	var files []string
	for _, dir := range []string{engineDir, rulesDir} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)

	// Group files by directory (Go package): a table's HookInput-building
	// helper (bashInput/bashJSON-style) may live in a SIBLING file of the
	// same package (e.g. internal/rules/gitdir/tempfixture_test.go defining
	// a helper internal/rules/gitdir/gitdir_test.go's table loop calls), so
	// the call-chain resolver (resolveHookInputChain) needs a per-PACKAGE
	// function index, not a per-file one.
	byDir := map[string][]string{}
	var dirOrder []string
	for _, path := range files {
		dir := filepath.Dir(path)
		if _, seen := byDir[dir]; !seen {
			dirOrder = append(dirOrder, dir)
		}
		byDir[dir] = append(byDir[dir], path)
	}
	sort.Strings(dirOrder)

	res := &Result{}
	fset := token.NewFileSet()
	caseNames := map[string]int{}

	for _, dir := range dirOrder {
		pkgFiles := byDir[dir]
		asts := make(map[string]*ast.File, len(pkgFiles))
		funcsByName := map[string]*ast.FuncDecl{}

		for _, path := range pkgFiles {
			res.Stats.FilesScanned++
			relFamily := familyFor(repoRoot, path)

			src, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("legacyextract: reading %s: %w", path, err)
			}
			if fakeStubPattern.Match(src) {
				res.FakeStubFiles = append(res.FakeStubFiles, FlaggedFile{File: relPath(repoRoot, path), Family: relFamily})
			}
			if tempDirPattern.Match(src) {
				res.TempDirFiles = append(res.TempDirFiles, FlaggedFile{File: relPath(repoRoot, path), Family: relFamily})
			}

			f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
			if err != nil {
				return nil, fmt.Errorf("legacyextract: parsing %s: %w", path, err)
			}
			asts[path] = f
			for _, decl := range f.Decls {
				if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Body != nil {
					funcsByName[fd.Name.Name] = fd
				}
			}
		}

		for _, path := range pkgFiles {
			f := asts[path]
			for _, decl := range f.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil || !strings.HasPrefix(fd.Name.Name, "Test") {
					continue
				}
				extractFunc(fset, relPath(repoRoot, path), fd, funcsByName, res, caseNames)
			}
		}
	}

	res.Stats.UnconvertibleRows = res.Stats.Denominator - res.Stats.Extracted
	sort.Slice(res.Rows, func(i, j int) bool { return res.Rows[i].Case < res.Rows[j].Case })
	return res, nil
}

func familyFor(repoRoot, path string) string {
	rel := relPath(repoRoot, path)
	if strings.HasPrefix(rel, filepath.Join("internal", "engine")) {
		return "engine"
	}
	rel = strings.TrimPrefix(rel, filepath.Join("internal", "rules")+string(filepath.Separator))
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) > 0 {
		return parts[0]
	}
	return "unknown"
}

func relPath(root, path string) string {
	r, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return r
}

// localSliceLits collects every local slice-literal declaration reachable
// anywhere inside fd (including nested t.Run closures), keyed by variable
// name: `name := []T{...}` or `var name = []T{...}`.
func localSliceLits(fd *ast.FuncDecl) map[string]*ast.CompositeLit {
	out := map[string]*ast.CompositeLit{}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			if len(s.Lhs) == 1 && len(s.Rhs) == 1 {
				if id, ok := s.Lhs[0].(*ast.Ident); ok {
					if cl, ok := s.Rhs[0].(*ast.CompositeLit); ok {
						if _, isArr := cl.Type.(*ast.ArrayType); isArr {
							out[id.Name] = cl
						}
					}
				}
			}
		case *ast.GenDecl:
			for _, spec := range s.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
					continue
				}
				if cl, ok := vs.Values[0].(*ast.CompositeLit); ok {
					if _, isArr := cl.Type.(*ast.ArrayType); isArr {
						out[vs.Names[0].Name] = cl
					}
				}
			}
		}
		return true
	})
	return out
}

func extractFunc(fset *token.FileSet, relFile string, fd *ast.FuncDecl, funcsByName map[string]*ast.FuncDecl, res *Result, caseNames map[string]int) {
	slices := localSliceLits(fd)

	ast.Inspect(fd.Body, func(n ast.Node) bool {
		rs, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		var lit *ast.CompositeLit
		switch x := rs.X.(type) {
		case *ast.Ident:
			lit = slices[x.Name]
		case *ast.CompositeLit:
			if _, isArr := x.Type.(*ast.ArrayType); isArr {
				lit = x
			}
		}
		if lit == nil {
			return true
		}
		arr, ok := lit.Type.(*ast.ArrayType)
		if !ok {
			return true
		}

		loopVar := ""
		if id, ok := rs.Value.(*ast.Ident); ok {
			loopVar = id.Name
		}
		if loopVar == "" || loopVar == "_" {
			return true
		}

		switch elt := arr.Elt.(type) {
		case *ast.Ident:
			if elt.Name == "string" {
				processIdiomA(fset, relFile, fd.Name.Name, rs, lit, loopVar, funcsByName, res, caseNames)
			}
		case *ast.StructType:
			processIdiomB(fset, relFile, fd.Name.Name, rs, lit, elt, loopVar, funcsByName, res, caseNames)
		}
		return true
	})
}

// findVerdictConst looks for `<sel>.Decision != hookio.<Const>` (either
// operand order) inside body, guarded by an Error/Errorf/Fatal/Fatalf call
// in the branch taken on mismatch. Returns "" if not found.
func findVerdictConst(body ast.Node) string {
	found := ""
	ast.Inspect(body, func(n ast.Node) bool {
		if found != "" {
			return false
		}
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		bin, ok := ifs.Cond.(*ast.BinaryExpr)
		if !ok || bin.Op != token.NEQ {
			return true
		}
		var constName string
		if isDecisionSelector(bin.X) {
			constName = hookioConstName(bin.Y)
		} else if isDecisionSelector(bin.Y) {
			constName = hookioConstName(bin.X)
		}
		if constName == "" {
			return true
		}
		if !hasFailureCall(ifs.Body) {
			return true
		}
		found = constName
		return false
	})
	return found
}

func isDecisionSelector(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Decision"
}

func hookioConstName(e ast.Expr) string {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != "hookio" {
		return ""
	}
	if _, known := declToVerdict[sel.Sel.Name]; known {
		return sel.Sel.Name
	}
	return ""
}

func hasFailureCall(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Error", "Errorf", "Fatal", "Fatalf":
			found = true
		}
		return true
	})
	return found
}

// hookInputMatch is what resolveHookInputChain found: the ToolName literal
// (if any), a CWD literal (if any), and the recognised tool_input key whose
// value traces to the loop variable (fieldSel == "" for idiom A's bare
// ident case; otherwise the struct field name idiom B selects).
type hookInputMatch struct {
	toolName       string
	toolNameDriver bool // true when the loop variable drives ToolName itself (a non-shell-tool table, e.g. `HookInput{ToolName: tool, ...}`), rather than a tool_input key
	cwd            string
	driverKey      string // the tool_input JSON key driven by the loop variable
	fixedKeys      map[string]string
	ok             bool
}

// varBinding names the value this resolver is currently tracing: either a
// bare identifier (fieldSel == "") or a struct-field selector off it
// (idiom B's `tt.command`-shaped references).
type varBinding struct {
	ident    string
	fieldSel string
}

func (b varBinding) matches(e ast.Expr) bool {
	if b.fieldSel == "" {
		id, ok := e.(*ast.Ident)
		return ok && id.Name == b.ident
	}
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == b.ident && strings.EqualFold(sel.Sel.Name, b.fieldSel)
}

const maxChainDepth = 5

// hookInputLiteral is resolveHookInputChain's entry point: find a
// `hookio.HookInput{...}` literal inside body (following, up to
// maxChainDepth, any single-argument helper-function call whose one
// argument traces the loop variable -- the bashInput(tt.command)/
// bashJSON(cmd)-style indirection this codebase's rule-test packages use
// pervasively alongside the fully-inline mustJSON(map[string]string{...})
// form), and identify which tool_input key the loop variable drives.
func hookInputLiteral(body ast.Node, loopVar, fieldSel string, funcsByName map[string]*ast.FuncDecl) hookInputMatch {
	m := resolveHookInputChain(body, varBinding{ident: loopVar, fieldSel: fieldSel}, funcsByName, 0)
	if m.toolName == "" {
		m.toolName = "Bash"
	}
	return m
}

func resolveHookInputChain(node ast.Node, binding varBinding, funcsByName map[string]*ast.FuncDecl, depth int) hookInputMatch {
	m := hookInputMatch{fixedKeys: map[string]string{}}
	if depth > maxChainDepth || node == nil {
		return m
	}

	// Direct case: a `hookio.HookInput{...}` composite literal is right
	// here in this body.
	ast.Inspect(node, func(n ast.Node) bool {
		if m.ok {
			return false
		}
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := cl.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HookInput" {
			return true
		}
		if xid, ok := sel.X.(*ast.Ident); !ok || xid.Name != "hookio" {
			return true
		}
		var toolInputExpr ast.Expr
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch key.Name {
			case "ToolName":
				if bl, ok := kv.Value.(*ast.BasicLit); ok && bl.Kind == token.STRING {
					m.toolName, _ = strconv.Unquote(bl.Value)
				} else if binding.matches(kv.Value) {
					m.toolNameDriver = true
				}
			case "CWD":
				if bl, ok := kv.Value.(*ast.BasicLit); ok && bl.Kind == token.STRING {
					m.cwd, _ = strconv.Unquote(bl.Value)
				}
			case "ToolInput":
				toolInputExpr = kv.Value
			}
		}
		if toolInputExpr == nil {
			// No ToolInput field at all -- only resolvable when the loop
			// variable instead drives ToolName itself.
			if m.toolNameDriver {
				m.fixedKeys = map[string]string{}
				m.ok = true
			}
			return true
		}
		tm := resolveToolInputChain(toolInputExpr, binding, funcsByName, depth)
		if tm.ok && (tm.driverKey != "" || m.toolNameDriver) {
			m.driverKey, m.fixedKeys, m.ok = tm.driverKey, tm.fixedKeys, true
		}
		return !m.ok
	})
	if m.ok {
		return m
	}

	// Indirect case: no HookInput literal here -- look for a single-arg
	// helper call whose argument traces `binding`, and recurse into the
	// callee with a binding rebound to its own parameter.
	var result hookInputMatch
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok || len(call.Args) == 0 {
			return true
		}
		argIdx := -1
		for i, a := range call.Args {
			if binding.matches(a) {
				argIdx = i
				break
			}
		}
		if argIdx == -1 {
			return true
		}
		callee, ok := funcsByName[id.Name]
		if !ok || callee.Type.Params == nil {
			return true
		}
		paramName := paramNameAt(callee.Type.Params, argIdx)
		if paramName == "" {
			return true
		}
		sub := resolveHookInputChain(callee.Body, varBinding{ident: paramName}, funcsByName, depth+1)
		if sub.ok {
			result = sub
			found = true
			return false
		}
		return true
	})
	if found {
		return result
	}
	return m
}

// toolInputMatch is resolveToolInputChain's result: which key the traced
// binding drives, plus every other key's fixed literal value.
type toolInputMatch struct {
	driverKey string
	fixedKeys map[string]string
	ok        bool
}

// resolveToolInputChain resolves a HookInput's `ToolInput:` field expression
// -- normally `mustJSON(map[string]string{...})` or
// `json.Marshal(hookio.BashToolInput{...})`, either inline or (transitively,
// up to maxChainDepth) behind a helper function like bashJSON(cmd) that does
// the same marshal one level down.
func resolveToolInputChain(expr ast.Expr, binding varBinding, funcsByName map[string]*ast.FuncDecl, depth int) toolInputMatch {
	tm := toolInputMatch{fixedKeys: map[string]string{}}
	if depth > maxChainDepth {
		return tm
	}
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return tm
	}
	arg := call.Args[0]
	if cl, ok := arg.(*ast.CompositeLit); ok {
		switch cl.Type.(type) {
		case *ast.MapType, *ast.SelectorExpr:
			tm.ok = extractKeyedFields(cl.Elts, binding, &tm)
			return tm
		}
	}
	// Indirect: arg is itself a reference to `binding` (or a further
	// call) -- the real marshal happens inside a callee.
	if !binding.matches(arg) {
		return tm
	}
	id, ok := call.Fun.(*ast.Ident)
	if !ok {
		return tm
	}
	callee, ok := funcsByName[id.Name]
	if !ok || callee.Type.Params == nil {
		return tm
	}
	paramName := paramNameAt(callee.Type.Params, 0)
	if paramName == "" {
		return tm
	}
	newBinding := varBinding{ident: paramName}
	// Search the callee body for a marshal-shaped composite literal
	// (map[string]string{...} or a known hookio *ToolInput struct
	// literal) whose field traces the callee's own parameter -- this
	// covers `func bashJSON(cmd string) json.RawMessage { b, _ :=
	// json.Marshal(hookio.BashToolInput{Command: cmd}); return b }`
	// without needing to trace the intermediate `b` assignment/return.
	found := false
	ast.Inspect(callee.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		switch cl.Type.(type) {
		case *ast.MapType, *ast.SelectorExpr:
		default:
			return true
		}
		var candidate toolInputMatch
		candidate.fixedKeys = map[string]string{}
		if extractKeyedFields(cl.Elts, newBinding, &candidate) {
			tm = candidate
			tm.ok = true
			found = true
			return false
		}
		return true
	})
	if found {
		return tm
	}
	// One more level: the callee itself calls a further helper.
	subFound := false
	ast.Inspect(callee.Body, func(n ast.Node) bool {
		if subFound {
			return false
		}
		innerCall, ok := n.(*ast.CallExpr)
		if !ok || len(innerCall.Args) != 1 {
			return true
		}
		sub := resolveToolInputChain(innerCall, newBinding, funcsByName, depth+1)
		if sub.ok {
			tm = sub
			subFound = true
			return false
		}
		return true
	})
	if subFound {
		return tm
	}
	return toolInputMatch{fixedKeys: map[string]string{}}
}

func paramNameAt(fl *ast.FieldList, idx int) string {
	pos := 0
	for _, f := range fl.List {
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		if idx < pos+n {
			if len(f.Names) == 0 {
				return ""
			}
			offset := idx - pos
			if offset < len(f.Names) {
				return f.Names[offset].Name
			}
			return ""
		}
		pos += n
	}
	return ""
}

// extractKeyedFields reports whether every field in elts was STRUCTURALLY
// resolvable (a literal string value, or a value tracing binding) -- it does
// NOT require a driver field to be present (an all-fixed or empty tool_input
// is a legitimate resolution when the loop variable instead drives ToolName
// itself; see hookInputMatch.toolNameDriver).
func extractKeyedFields(elts []ast.Expr, binding varBinding, m *toolInputMatch) bool {
	for _, elt := range elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return false
		}
		var key string
		switch k := kv.Key.(type) {
		case *ast.BasicLit:
			if k.Kind != token.STRING {
				return false
			}
			key, _ = strconv.Unquote(k.Value)
		case *ast.Ident:
			key = jsonKeyFor(k.Name)
		default:
			return false
		}
		if binding.matches(kv.Value) {
			m.driverKey = key
			continue
		}
		bl, ok := kv.Value.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return false
		}
		s, _ := strconv.Unquote(bl.Value)
		m.fixedKeys[key] = s
	}
	return true
}

// jsonKeyFor maps a known Go struct field name (hookio.FileToolInput) to its
// JSON tag; anything unrecognised is lower-cased as a best effort.
func jsonKeyFor(field string) string {
	switch field {
	case "FilePath":
		return "file_path"
	case "Content":
		return "content"
	case "OldString":
		return "old_string"
	case "NewString":
		return "new_string"
	case "Command":
		return "command"
	}
	return strings.ToLower(field)
}

func processIdiomA(fset *token.FileSet, relFile, funcName string, rs *ast.RangeStmt, lit *ast.CompositeLit, loopVar string, funcsByName map[string]*ast.FuncDecl, res *Result, caseNames map[string]int) {
	if len(lit.Elts) == 0 {
		return
	}
	constName := findVerdictConst(rs.Body)
	if constName == "" {
		res.UnrecognisedTables = append(res.UnrecognisedTables, UnrecognisedTable{
			File: relFile, Func: funcName,
			Reason: fmt.Sprintf("string-slice range (%d elements) with no recognised `.Decision != hookio.X` assertion", len(lit.Elts)),
		})
		return
	}
	hm := hookInputLiteral(rs.Body, loopVar, "", funcsByName)
	res.Stats.RecognisedTables++
	for i, elt := range lit.Elts {
		res.Stats.Denominator++
		bl, ok := elt.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			res.UnconvertibleRows = append(res.UnconvertibleRows, UnconvertibleRow{
				File: relFile, Func: funcName, Reason: "batch element is not a string literal",
			})
			continue
		}
		if !hm.ok {
			res.UnconvertibleRows = append(res.UnconvertibleRows, UnconvertibleRow{
				File: relFile, Func: funcName, Reason: "no statically-resolvable hookio.HookInput construction found in loop body",
			})
			continue
		}
		cmd, _ := strconv.Unquote(bl.Value)
		row := buildRow(relFile, funcName, i, "", cmd, declToVerdict[constName], hm)
		row.Case = uniqueCase(row.Case, caseNames)
		res.Rows = append(res.Rows, row)
		res.Stats.Extracted++
	}
}

func processIdiomB(fset *token.FileSet, relFile, funcName string, rs *ast.RangeStmt, lit *ast.CompositeLit, structType *ast.StructType, loopVar string, funcsByName map[string]*ast.FuncDecl, res *Result, caseNames map[string]int) {
	if len(lit.Elts) == 0 {
		return
	}
	commandField, decisionField, nameField := "", "", ""
	fieldOrder := []string{}
	cmdRe := regexp.MustCompile(`(?i)^(command|cmd|input)$`)
	nameRe := regexp.MustCompile(`(?i)^name$`)
	for _, f := range structType.Fields.List {
		if len(f.Names) != 1 {
			fieldOrder = append(fieldOrder, "")
			continue
		}
		fname := f.Names[0].Name
		fieldOrder = append(fieldOrder, fname)
		if id, ok := f.Type.(*ast.Ident); ok && id.Name == "string" {
			if cmdRe.MatchString(fname) && commandField == "" {
				commandField = fname
			}
			if nameRe.MatchString(fname) && nameField == "" {
				nameField = fname
			}
		}
		if sel, ok := f.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Decision" {
			if xid, ok := sel.X.(*ast.Ident); ok && xid.Name == "hookio" {
				decisionField = fname
			}
		}
	}
	if commandField == "" || decisionField == "" {
		res.UnrecognisedTables = append(res.UnrecognisedTables, UnrecognisedTable{
			File: relFile, Func: funcName,
			Reason: fmt.Sprintf("struct-slice range (%d elements) has no string command-like field + hookio.Decision field pair", len(lit.Elts)),
		})
		return
	}
	hm := hookInputLiteral(rs.Body, loopVar, commandField, funcsByName)
	res.Stats.RecognisedTables++
	for i, elt := range lit.Elts {
		res.Stats.Denominator++
		cl, ok := elt.(*ast.CompositeLit)
		if !ok {
			res.UnconvertibleRows = append(res.UnconvertibleRows, UnconvertibleRow{File: relFile, Func: funcName, Reason: "table element is not a composite literal"})
			continue
		}
		fields := map[string]ast.Expr{}
		hasKeys := false
		for _, e := range cl.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				if id, ok := kv.Key.(*ast.Ident); ok {
					fields[id.Name] = kv.Value
					hasKeys = true
				}
			}
		}
		if !hasKeys {
			for idx, e := range cl.Elts {
				if idx < len(fieldOrder) && fieldOrder[idx] != "" {
					fields[fieldOrder[idx]] = e
				}
			}
		}
		cmdExpr, hasCmd := fields[commandField]
		decExpr, hasDec := fields[decisionField]
		if !hasCmd || !hasDec {
			res.UnconvertibleRows = append(res.UnconvertibleRows, UnconvertibleRow{File: relFile, Func: funcName, Reason: "row missing command or decision field value"})
			continue
		}
		cmdLit, ok := cmdExpr.(*ast.BasicLit)
		if !ok || cmdLit.Kind != token.STRING {
			res.UnconvertibleRows = append(res.UnconvertibleRows, UnconvertibleRow{File: relFile, Func: funcName, Reason: "command field is not a string literal"})
			continue
		}
		constName := hookioConstName(decExpr)
		if constName == "" {
			res.UnconvertibleRows = append(res.UnconvertibleRows, UnconvertibleRow{File: relFile, Func: funcName, Reason: "decision field is not a literal hookio.<Const> selector"})
			continue
		}
		if !hm.ok {
			res.UnconvertibleRows = append(res.UnconvertibleRows, UnconvertibleRow{File: relFile, Func: funcName, Reason: "no statically-resolvable hookio.HookInput construction found in loop body"})
			continue
		}
		cmd, _ := strconv.Unquote(cmdLit.Value)
		name := ""
		if nameField != "" {
			if ne, ok := fields[nameField]; ok {
				if nl, ok := ne.(*ast.BasicLit); ok && nl.Kind == token.STRING {
					name, _ = strconv.Unquote(nl.Value)
				}
			}
		}
		row := buildRow(relFile, funcName, i, name, cmd, declToVerdict[constName], hm)
		row.Case = uniqueCase(row.Case, caseNames)
		res.Rows = append(res.Rows, row)
		res.Stats.Extracted++
	}
}

func buildRow(relFile, funcName string, idx int, name, cmd string, verdict goldencorpus.Verdict, hm hookInputMatch) goldencorpus.Row {
	toolInput := map[string]any{}
	for k, v := range hm.fixedKeys {
		toolInput[k] = v
	}
	toolName := hm.toolName
	if hm.toolNameDriver {
		// The loop variable drives ToolName itself (a non-shell-tool table,
		// e.g. `HookInput{ToolName: tool, ToolInput: mustJSON(map[string]string{})}`)
		// -- cmd IS the tool name, not a tool_input value.
		toolName = cmd
	} else {
		driverKey := hm.driverKey
		if driverKey == "" {
			driverKey = "command"
		}
		toolInput[driverKey] = cmd
	}

	cwd := hm.cwd
	if cwd == "" {
		cwd = "/repo"
	}

	c := name
	if c == "" {
		c = fmt.Sprintf("%s_%s_row%d", sanitize(relFile), funcName, idx)
	} else {
		c = sanitize(c)
	}

	family := "unknown"
	if parts := strings.Split(filepath.ToSlash(relFile), "/"); len(parts) > 2 {
		family = parts[2]
	}
	if strings.HasPrefix(filepath.ToSlash(relFile), "internal/engine") {
		family = "engine"
	}

	return goldencorpus.Row{
		Case:      "legacy_" + c,
		Tags:      []string{"legacy", "legacy-" + family},
		ToolInput: goldencorpus.ToolInputFixture{ToolName: toolName, ToolInput: toolInput},
		CWDPathState: goldencorpus.PathStateFixture{
			CWD: cwd,
		},
		Mode:            goldencorpus.ModeDefault,
		ExpectedVerdict: verdict,
		Notes:           fmt.Sprintf("Auto-extracted (tc-o14i5.1.3 legacyextract) from %s:%s (legacy RuleChain-era table test).", relFile, funcName),
	}
}

var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func sanitize(s string) string {
	s = strings.TrimSuffix(s, ".go")
	s = strings.ReplaceAll(s, string(filepath.Separator), "_")
	s = nonAlnum.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	return strings.ToLower(s)
}

func uniqueCase(base string, seen map[string]int) string {
	n := seen[base]
	seen[base] = n + 1
	if n == 0 {
		return base
	}
	return fmt.Sprintf("%s_%d", base, n)
}
