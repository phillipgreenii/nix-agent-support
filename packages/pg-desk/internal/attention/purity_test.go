package attention

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestPackageImportsNoExecOrNetwork enforces the structural half of
// INV-ATTNEVAL-1: the evaluator package (every non-test file) imports neither
// os/exec nor net (nor any net/... package, which would be a network
// capability). Time comes only through the injected Clock, and nothing here
// writes, so those halves are covered by behavior tests.
func TestPackageImportsNoExecOrNetwork(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		scanned++
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, imp := range af.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if path == "os/exec" || path == "net" || strings.HasPrefix(path, "net/") {
				t.Errorf("%s imports %q: the attention evaluator must not exec or open a network connection", f, path)
			}
		}
	}
	if scanned < 4 {
		t.Fatalf("scanned only %d source files; the guard is vacuous", scanned)
	}
}

// TestPackageDoesNotReadTheWallClock bans time.Now in non-test code: all time
// is the injected Clock.
func TestPackageDoesNotReadTheWallClock(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "time.Now(") {
			t.Errorf("%s calls time.Now; use the injected Clock (INV-ATTNEVAL-1)", f)
		}
	}
}
