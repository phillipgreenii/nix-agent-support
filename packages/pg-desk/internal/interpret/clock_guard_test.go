package interpret

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// Clock guard: no code in this package reads the wall clock except
// SystemClock.Now, so a golden (which injects FixedClock) cannot become
// time-dependent.
func TestNoWallClockOutsideSystemClock(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	scanned := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			allowed := ok && fn.Name.Name == "Now" && fn.Recv != nil && len(fn.Recv.List) == 1 &&
				exprName(fn.Recv.List[0].Type) == "SystemClock"
			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "time" && (sel.Sel.Name == "Now" || sel.Sel.Name == "Since" || sel.Sel.Name == "Until") && !allowed {
					t.Errorf("%s: time.%s outside SystemClock.Now", fset.Position(sel.Pos()), sel.Sel.Name)
				}
				return true
			})
		}
	}
	if scanned < 10 {
		t.Fatalf("scanned only %d files; the guard is not looking at the package", scanned)
	}
}

func exprName(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}
