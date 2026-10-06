// Package guards holds repository-level guard checks for the pg-decider
// decision flow. The scanning logic lives here, in a non-test file, so its
// self-test can drive it against synthetic package trees.
package guards

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// pgPrModule is the module path of packages/pg-pr, the package the decision
// flow MUST NOT depend on (design guard G8).
const pgPrModule = "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr"

// Violation is one forbidden dependency on packages/pg-pr.
type Violation struct {
	File   string // path relative to the scanned root, slash-separated
	Detail string
}

// ScanResult is the outcome of ScanForPgPr.
type ScanResult struct {
	Violations []Violation
	GoFiles    int // .go files whose imports were inspected
	GoMods     int // go.mod files whose directives were inspected
}

// ScanForPgPr inspects every .go file's import declarations (imports-only
// parse) and every go.mod's require and replace directives under each of the
// given directories (relative to root), and reports any that name the
// packages/pg-pr module path or directory. Comments and string literals that
// merely mention pg-pr are not violations. A missing directory is an error so
// a typo cannot turn the guard into a silent pass.
func ScanForPgPr(root string, dirs []string) (ScanResult, error) {
	var res ScanResult
	fset := token.NewFileSet()
	for _, dir := range dirs {
		base := filepath.Join(root, filepath.FromSlash(dir))
		if st, err := os.Stat(base); err != nil || !st.IsDir() {
			return res, fmt.Errorf("guarded directory %s: not a readable directory (%v)", dir, err)
		}
		err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			rel, relErr := filepath.Rel(root, p)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			switch {
			case d.Name() == "go.mod":
				res.GoMods++
				data, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				for _, detail := range goModViolations(string(data)) {
					res.Violations = append(res.Violations, Violation{File: rel, Detail: detail})
				}
			case strings.HasSuffix(d.Name(), ".go"):
				res.GoFiles++
				f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
				if err != nil {
					return fmt.Errorf("parse imports of %s: %w", rel, err)
				}
				for _, imp := range f.Imports {
					ip, err := strconv.Unquote(imp.Path.Value)
					if err != nil {
						return fmt.Errorf("%s: bad import literal %s: %w", rel, imp.Path.Value, err)
					}
					if namesPgPr(ip) {
						res.Violations = append(res.Violations, Violation{
							File:   rel,
							Detail: fmt.Sprintf("imports %q (G8: the flow MUST NOT depend on packages/pg-pr)", ip),
						})
					}
				}
			}
			return nil
		})
		if err != nil {
			return res, err
		}
	}
	return res, nil
}

// namesPgPr reports whether an import path, module path or directory token
// refers to the packages/pg-pr module or directory.
func namesPgPr(tok string) bool {
	if tok == pgPrModule || strings.HasPrefix(tok, pgPrModule+"/") {
		return true
	}
	segs := strings.Split(path.Clean(tok), "/")
	for i := 0; i+1 < len(segs); i++ {
		if segs[i] == "packages" && segs[i+1] == "pg-pr" {
			return true
		}
	}
	// A relative directory reference such as ../pg-pr in a replace directive.
	return len(segs) >= 2 && (segs[0] == "." || segs[0] == "..") && segs[len(segs)-1] == "pg-pr"
}

// goModViolations returns a detail line for every require or replace
// directive in a go.mod body that names packages/pg-pr. Comments are
// stripped; only tokens of require and replace directives (single-line or
// inside a block) are examined.
func goModViolations(body string) []string {
	var out []string
	block := "" // directive of the enclosing "( ... )" block, if any
	for i, line := range strings.Split(body, "\n") {
		if c := strings.Index(line, "//"); c >= 0 {
			line = line[:c]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		var directive string
		var toks []string
		switch {
		case block != "":
			if fields[0] == ")" {
				block = ""
				continue
			}
			directive, toks = block, fields
		case len(fields) == 2 && fields[1] == "(":
			block = fields[0]
			continue
		default:
			directive, toks = fields[0], fields[1:]
		}
		if directive != "require" && directive != "replace" {
			continue
		}
		for _, tok := range toks {
			if namesPgPr(tok) {
				out = append(out, fmt.Sprintf("line %d: %s directive names %q (G8: the flow MUST NOT depend on packages/pg-pr)", i+1, directive, tok))
				break
			}
		}
	}
	return out
}
