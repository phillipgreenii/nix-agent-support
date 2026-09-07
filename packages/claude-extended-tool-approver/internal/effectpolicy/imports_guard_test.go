package effectpolicy

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNoDirectHookioImport: none of the four spike packages imports
// internal/hookio directly (the port must stay hook-independent). The
// transitive reach through cmdparse is a known cycle to fix later and is not
// what this guards.
//
// EXCLUSION: any file named "*_integration_test.go" is skipped. That suffix
// is this repo's existing tagged-suite convention (README.md's "Two test
// suites", `//go:build integration`) and slice 3b's agreement harness
// (agreement_integration_test.go) deliberately imports internal/hookio,
// internal/setup and internal/engine — its entire job is bridging the spike
// to the LIVE hookio-based engine for comparison, so it cannot avoid the
// import. go/parser.ParseDir is build-tag-oblivious (the nil filter it used
// to be called with reads every .go file regardless of its `//go:build`
// line), so without this exclusion that one deliberately-tagged file would
// trip the guard on every run, tagged or not. The guard's actual purpose —
// the four packages' DEFAULT build (`go test ./...`, no `-tags integration`)
// stays hookio-independent — is unaffected: excluded files never compile
// into that default build.
func TestNoDirectHookioImport(t *testing.T) {
	dirs := []string{
		filepath.Join("..", "cmddesc"),
		filepath.Join("..", "effectgraph"),
		filepath.Join("..", "evalcontract"),
		".",
	}
	notIntegrationTest := func(f fs.FileInfo) bool {
		return !strings.HasSuffix(f.Name(), "_integration_test.go")
	}
	fset := token.NewFileSet()
	for _, dir := range dirs {
		pkgs, err := parser.ParseDir(fset, dir, notIntegrationTest, parser.ImportsOnly)
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
