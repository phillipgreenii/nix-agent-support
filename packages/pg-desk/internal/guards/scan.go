// Package guards holds module-local guard checks for pg-desk. The scanning
// logic lives here, in a non-test file, so the planted-violation self-tests
// can drive it against synthetic package trees.
//
// G5: pg-desk MUST NOT contain decision logic. Work-kind and dedup-key
// literals live in pg-decider only, and pg-desk never executes a tracker
// write verb. The vocabulary allowed in pg-desk production code is `focus`,
// `focus_selected`, `source_type` and `source_id`.
package guards

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// bannedLiterals are the string-literal substrings pg-desk production code
// MUST NOT contain (G5).
var bannedLiterals = []string{"focus-item", "dedup_key"}

// writeVerbs are the connector issue verbs pg-desk MUST NOT pass at the argv
// level (`issue create|update|comment|close`). Read verbs (`issue show`,
// `issue list`, `issue deps`, `issue children`, `issue refresh`) stay legal.
var writeVerbs = map[string]bool{
	"create":  true,
	"update":  true,
	"comment": true,
	"close":   true,
}

// PreCutoverAllowlist is the ONE allowlist of retiring pre-cutover files
// (slash-separated, relative to the module root) that legitimately write to
// the tracker. Each entry is deleted by the change-flow cutover release
// (bead pg2-2j5ac.52.22, which removes internal/sync); the guard fails on a
// stale entry so the deletion cannot be forgotten.
//
//   - internal/sync/connector.go: argv carries `issue create` and
//     `issue update`.
//
// The allowlist is exactly the set of non-test files the scanner reported on
// the tree at the time the guard landed; nothing else is allowlisted.
var PreCutoverAllowlist = []string{
	"internal/sync/connector.go", // deleted by the change-flow cutover release
}

// The single documented exemption: the rank's candidacy exclusion reads the
// bead metadata key `dedup_key` (a read-only check, not dedup logic). The
// literal `dedup_key` is permitted ONLY as the value of the constant
// MetaDedupKey declared in internal/focus/exclusions.go. The exemption is one
// (file, constant) pair checked by name, never a file-wide or package-wide
// allowance: a second occurrence of the literal in that file still fails.
const (
	exemptFile     = "internal/focus/exclusions.go"
	exemptConst    = "MetaDedupKey"
	exemptConstVal = "dedup_key"
)

// guardPkgDir is this package's own directory (relative to the module root);
// the scan excludes it because its scanner and fixtures necessarily contain
// the banned literals.
const guardPkgDir = "internal/guards"

// Violation is one forbidden construct in pg-desk production code.
type Violation struct {
	File   string // path relative to the scanned root, slash-separated
	Line   int
	Detail string
}

// ScanResult is the outcome of ScanDecisionLogic.
type ScanResult struct {
	Violations []Violation // reported violations, allowlisted files excluded
	Allowed    []Violation // violations suppressed by the allowlist
	GoFiles    int         // non-test .go files scanned
}

// ScanDecisionLogic inspects every non-test .go file under root (the pg-desk
// module root), excluding internal/guards, testdata, vendor and .git
// directories, and reports G5 violations: banned literals, an exec of `bd`,
// and connector write verbs at the argv level. It walks the AST so a comment
// that merely mentions a banned word never trips it. Files named in allow
// (slash-separated, relative to root) have their violations moved to
// ScanResult.Allowed instead of Violations.
func ScanDecisionLogic(root string, allow []string) (ScanResult, error) {
	var res ScanResult
	allowed := map[string]bool{}
	for _, a := range allow {
		allowed[a] = true
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "vendor":
				return filepath.SkipDir
			}
			if rel == guardPkgDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		res.GoFiles++
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}
		for _, v := range scanFile(fset, f, rel) {
			if allowed[rel] {
				res.Allowed = append(res.Allowed, v)
			} else {
				res.Violations = append(res.Violations, v)
			}
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	sortViolations(res.Violations)
	sortViolations(res.Allowed)
	return res, nil
}

func sortViolations(vs []Violation) {
	sort.SliceStable(vs, func(i, j int) bool {
		if vs[i].File != vs[j].File {
			return vs[i].File < vs[j].File
		}
		return vs[i].Line < vs[j].Line
	})
}

// strLit returns the unquoted value of a string literal expression.
func strLit(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(bl.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// exemptLiterals returns the single literal node exempt from the banned-literal
// rule: the value of const MetaDedupKey in internal/focus/exclusions.go.
func exemptLiterals(f *ast.File, rel string) map[*ast.BasicLit]bool {
	out := map[*ast.BasicLit]bool{}
	if rel != exemptFile {
		return out
	}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != exemptConst || len(vs.Values) != 1 {
				continue
			}
			bl, ok := vs.Values[0].(*ast.BasicLit)
			if !ok {
				continue
			}
			if s, ok := strLit(bl); ok && s == exemptConstVal {
				out[bl] = true
			}
		}
	}
	return out
}

func scanFile(fset *token.FileSet, f *ast.File, rel string) []Violation {
	var out []Violation
	exempt := exemptLiterals(f, rel)
	add := func(pos token.Pos, format string, args ...any) {
		out = append(out, Violation{File: rel, Line: fset.Position(pos).Line, Detail: fmt.Sprintf(format, args...)})
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.ImportSpec:
			return false // import paths are not string constants of the program
		case *ast.BasicLit:
			if exempt[x] {
				return true
			}
			if s, ok := strLit(x); ok {
				for _, b := range bannedLiterals {
					if strings.Contains(s, b) {
						add(x.Pos(), "string literal %q contains banned literal %q (G5: decision logic lives in pg-decider only)", s, b)
					}
				}
			}
		case *ast.CallExpr:
			checkArgv(x.Args, add)
		case *ast.CompositeLit:
			checkArgv(x.Elts, add)
		}
		return true
	})
	return out
}

// checkArgv inspects the elements of a call's arguments or of a composite
// literal as an argv: a literal "bd" is an exec of bd, and adjacent literals
// "issue", <write verb> are a connector write verb.
func checkArgv(elts []ast.Expr, add func(token.Pos, string, ...any)) {
	for i, e := range elts {
		s, ok := strLit(e)
		if !ok {
			continue
		}
		if s == "bd" {
			add(e.Pos(), `argv carries "bd" (G5: pg-desk MUST NOT exec bd)`)
		}
		if s == "issue" && i+1 < len(elts) {
			if verb, ok := strLit(elts[i+1]); ok && writeVerbs[verb] {
				add(e.Pos(), `argv carries "issue", %q (G5: pg-desk MUST NOT execute a tracker write verb)`, verb)
			}
		}
	}
}

// FindModuleRoot walks up from start to the directory holding go.mod (the
// module's own go.mod; NEVER flake.nix, which is absent inside the nix build
// and would make the guard skip). It returns "" when none is found.
func FindModuleRoot(start string) string {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
