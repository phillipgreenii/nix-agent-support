package effectpolicy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// parseDirImportsOnly parses the import-only AST of every top-level ".go"
// file in dir for which keep (given the bare filename) reports true. It is a
// narrow, build-tag-oblivious replacement for the deprecated
// go/parser.ParseDir (SA1019, deprecated since Go 1.25): unlike ParseDir it
// does not group files by package, but neither guard below relies on package
// grouping — both just want the union of imports across the matched files —
// so this preserves that behavior exactly, including ParseDir's original
// build-tag obliviousness that TestNoDirectHookioImport's doc comment
// explicitly depends on (see its EXCLUSION note).
func parseDirImportsOnly(t *testing.T, fset *token.FileSet, dir string, keep func(name string) bool) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || !keep(name) {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", filepath.Join(dir, name), err)
		}
		files = append(files, f)
	}
	return files
}

// TestNoDirectHookioImport: none of the four spike packages imports
// internal/hookio directly (the port must stay hook-independent). This guard
// only checks DIRECT imports; a transitive reach through cmdparse used to
// exist and is not what this test itself proves absent. As of the
// effect-graph spike's slice 3r, that transitive reach was already narrower
// than it used to be: Redirection (the value type
// cmdparse.ParsedCommand.Redirections used to hold FROM hookio) moved to the
// zero-dependency internal/hooktypes, so none of the four packages reached
// hookio for any TYPE it named. The one remaining edge — cmdparse's own
// import of hookio for `*hookio.HookInput` (LeavesOf/RootLeavesOf's
// parameter) — is RESOLVED as of slice 3ap (tc-8og1): LeavesOf/RootLeavesOf
// moved INTO hookio, removing cmdparse's only import of hookio, so as of this
// slice the four packages no longer reach internal/hookio transitively
// either, not just directly.
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
	notIntegrationTest := func(name string) bool {
		return !strings.HasSuffix(name, "_integration_test.go")
	}
	fset := token.NewFileSet()
	for _, dir := range dirs {
		for _, f := range parseDirImportsOnly(t, fset, dir, notIntegrationTest) {
			name := fset.Position(f.Package).Filename
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
	keepAll := func(string) bool { return true }
	for _, f := range parseDirImportsOnly(t, fset, dir, keepAll) {
		name := fset.Position(f.Package).Filename
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
