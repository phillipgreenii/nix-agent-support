package cmdparse

// TestDeletedRawTextParenMatchersStayDeleted is pg2-813ww's ASSERTION half of
// its "assert done, don't redo" scope note: pg2-hed0a (ADR 0039 step 5a) already
// deleted `matchParen`, `classifyCmdSubstitution` and `classifyBacktickSubstitution`
// — the last raw-text paren matcher outside the seam (I9) and its two callers —
// migrating classifyExpansion to a real parse over the seam. This bead's own
// residue is narrower (the pre-parse shortcut's `$`/backtick-only test, fixed
// in classifyExpansion above) and MUST NOT redo that migration.
//
// This guard makes "stayed deleted" a MECHANICAL fact rather than a claim: it
// scans every Go source file in this package (including this file's own
// siblings, tests included — a raw-text scanner reintroduced in a test would be
// just as much a regression, since the fuzz/property tests are what stand in
// for the deleted invariant per ADR 0039's Enforcement "Fuzz continuity") for a
// top-level declaration of any of the three names, using the same go/parser AST
// approach the ccpool spec-citation guard uses (packages/ccpool/cmd/ccpool/spec_citations_test.go)
// rather than a substring grep — a grep would trip on this file's own doc
// comment and on every historical "DELETED" note in parser.go/shellparse.go
// that legitimately NAMES the three symbols without declaring them.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// deletedRawTextMatcherNames are the three symbols pg2-hed0a deleted. A
// reintroduction under ANY of these names — function, method, var, or const —
// is exactly the regression this guard exists to catch, whichever form it
// takes.
var deletedRawTextMatcherNames = map[string]bool{
	"matchParen":                   true,
	"classifyCmdSubstitution":      true,
	"classifyBacktickSubstitution": true,
}

func TestDeletedRawTextParenMatchersStayDeleted(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(.): %v", err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(".", e.Name())
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parser.ParseFile(%s): %v", path, err)
		}
		checked++
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if deletedRawTextMatcherNames[d.Name.Name] {
					t.Errorf("%s: found a func declaration named %q — matchParen/classifyCmdSubstitution/"+
						"classifyBacktickSubstitution were DELETED by pg2-hed0a (ADR 0039 step 5a) and "+
						"MUST NOT be reintroduced; see LOWERING.md's step 5a section", path, d.Name.Name)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, name := range vs.Names {
						if deletedRawTextMatcherNames[name.Name] {
							t.Errorf("%s: found a declaration named %q — matchParen/classifyCmdSubstitution/"+
								"classifyBacktickSubstitution were DELETED by pg2-hed0a (ADR 0039 step 5a) and "+
								"MUST NOT be reintroduced; see LOWERING.md's step 5a section", path, name.Name)
						}
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatalf("scanned 0 .go files in %s — the guard is vacuous", ".")
	}
	t.Logf("scanned %d .go files in internal/cmdparse for a reintroduced matchParen/"+
		"classifyCmdSubstitution/classifyBacktickSubstitution declaration", checked)
}
