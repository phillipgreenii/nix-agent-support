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
// what this guards. As of the effect-graph spike's slice 3r, that transitive
// reach is narrower than it used to be: Redirection (the value type
// cmdparse.ParsedCommand.Redirections used to hold FROM hookio) moved to the
// zero-dependency internal/hooktypes, so none of the four packages reaches
// hookio for any TYPE it names anymore. The remaining edge is cmdparse's own
// import of hookio for `*hookio.HookInput` (LeavesOf/RootLeavesOf's
// parameter) — a separate follow-up tied to HookInput.ParsedLeaf/ParsedRoot
// being `any`, not something this guard (or hooktypes existing) fixes.
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

// TestHookTypesImportsNothingInternal is the cheap tightening slice 3r adds
// alongside the move: internal/hooktypes exists PRECISELY so a value type
// (Redirection) can be shared by the hook boundary, the parser, and this
// spike without any of them importing another to name it, so it MUST stay a
// true LEAF package — zero imports of any other internal package, standard
// library only. Unlike TestNoDirectHookioImport (which checks the four spike
// packages don't import hookio), this checks hooktypes doesn't import
// ANYTHING internal, hookio included: a leaf package that quietly grew its
// own internal dependency would defeat the whole point of the move, even
// though it would not trip the hookio-specific guard above.
func TestHookTypesImportsNothingInternal(t *testing.T) {
	dir := filepath.Join("..", "hooktypes")
	fset := token.NewFileSet()
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
				if strings.Contains(p, "/internal/") {
					t.Errorf("%s imports %s, but internal/hooktypes must stay a zero-dependency leaf package", name, p)
				}
			}
		}
	}
}
