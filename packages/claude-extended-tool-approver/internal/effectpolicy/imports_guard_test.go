package effectpolicy

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNoDirectHookioImport: none of the four spike packages imports
// internal/hookio directly (the port must stay hook-independent). The
// transitive reach through cmdparse is a known cycle to fix later and is not
// what this guards.
func TestNoDirectHookioImport(t *testing.T) {
	dirs := []string{
		filepath.Join("..", "cmddesc"),
		filepath.Join("..", "effectgraph"),
		filepath.Join("..", "evalcontract"),
		".",
	}
	fset := token.NewFileSet()
	for _, dir := range dirs {
		pkgs, err := parser.ParseDir(fset, dir, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", dir, err)
		}
		for _, pkg := range pkgs {
			for name, f := range pkg.Files {
				for _, imp := range f.Imports {
					p, err := strconv.Unquote(imp.Path.Value)
					if err != nil {
						t.Fatalf("%s: bad import %s", name, imp.Path.Value)
					}
					if strings.HasSuffix(p, "/internal/hookio") {
						t.Errorf("%s imports %s directly", name, p)
					}
				}
			}
		}
	}
}
