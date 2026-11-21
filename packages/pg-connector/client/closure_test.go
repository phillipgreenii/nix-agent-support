package client

import (
	"context"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// allowedDeps is the closure rule of a contract package: the standard
// library plus these x packages, nothing else. x/exitcode is on the list
// because the exit codes are re-exports of the shared workspace codes.
var allowedDeps = []string{
	"github.com/phillipgreenii/x/cmdrun",
	"github.com/phillipgreenii/x/exitcode",
}

func isStd(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

// TestClosureIsStdlibAndCmdrunOnly fails when any non-test file of the
// package imports something outside the standard library and allowedDeps,
// and when an allowed package itself imports outside the standard library.
func TestClosureIsStdlibAndCmdrunOnly(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if !isStd(p) && !slices.Contains(allowedDeps, p) {
				t.Errorf("%s imports %q: outside the standard library and the allowed list %v", e.Name(), p, allowedDeps)
			}
			seen[p] = true
		}
	}
	for _, p := range allowedDeps {
		if !seen[p] {
			continue
		}
		ensureGOROOT(t)
		pkg, err := build.Import(p, ".", 0)
		if err != nil {
			t.Fatalf("resolve allowed dependency %s: %v", p, err)
		}
		for _, dep := range pkg.Imports {
			if !isStd(dep) {
				t.Errorf("allowed dependency %s imports %q, outside the standard library", p, dep)
			}
		}
	}
}

// ensureGOROOT fills build.Default.GOROOT from `go env GOROOT` when it is empty:
// a test binary built with -trimpath has no compiled-in GOROOT and go test does
// not set GOROOT in the environment, so build.Import could not find the
// standard library.
func ensureGOROOT(t *testing.T) {
	t.Helper()
	if build.Default.GOROOT != "" {
		return
	}
	out, err := exec.CommandContext(context.Background(), "go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatalf("go env GOROOT: %v", err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		t.Fatal("go env GOROOT printed nothing")
	}
	build.Default.GOROOT = root
}

func TestClosureCheckRejectsForeignImport(t *testing.T) {
	if isStd("github.com/spf13/cobra") || !isStd("encoding/json") || !isStd("os") {
		t.Fatal("isStd misclassifies")
	}
	if slices.Contains(allowedDeps, "github.com/spf13/cobra") {
		t.Fatal("cobra must not be allowed")
	}
}
