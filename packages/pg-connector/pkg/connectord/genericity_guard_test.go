package connectord

// The import-boundary guard of the shared connector-daemon library (docket
// pg2-959y2, packet H0; rule G3, INV-HOIST-18). It checks every .go file under
// pkg/connectord/ (tests included, any directory named testdata excluded),
// parsed with go/parser: no file imports a path containing /cmd/ or
// /internal/, and none declares package main. The shared library therefore
// never reaches into a command's own packages.
//
// The guard function is evaluateImportBoundary; the committed tree is checked
// by TestGenericityGuardPassesOnTheCommittedTree.

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// evaluateImportBoundary parses every .go file under root (testdata
// directories skipped) and returns one violation string per file that declares
// package main or imports a cmd or internal path, sorted by file.
func evaluateImportBoundary(root string) ([]string, error) {
	var out []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}
		if f.Name.Name == "main" {
			out = append(out, fmt.Sprintf("%s: package main under the shared tree (G3, INV-HOIST-18)", rel))
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				p = imp.Path.Value
			}
			if strings.Contains(p, "/cmd/") || strings.Contains(p, "/internal/") ||
				strings.HasPrefix(p, "cmd/") || strings.HasPrefix(p, "internal/") ||
				strings.HasSuffix(p, "/cmd") || strings.HasSuffix(p, "/internal") {
				out = append(out, fmt.Sprintf("%s: imports %q, a cmd or internal path (G3, INV-HOIST-18)", rel, p))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// writeTree creates the files (slash-separated relative path to content) under
// a temp directory and returns it.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func violations(t *testing.T, files map[string]string) []string {
	t.Helper()
	got, err := evaluateImportBoundary(writeTree(t, files))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func wantViolation(t *testing.T, got []string, parts ...string) {
	t.Helper()
	for _, v := range got {
		ok := true
		for _, p := range parts {
			if !strings.Contains(v, p) {
				ok = false
			}
		}
		if ok {
			return
		}
	}
	t.Errorf("violations %q: none contains all of %q", got, parts)
}

func wantClean(t *testing.T, got []string) {
	t.Helper()
	if len(got) != 0 {
		t.Errorf("violations = %q, want none", got)
	}
}

// The real tree.

func TestGenericityGuardPassesOnTheCommittedTree(t *testing.T) {
	got, err := evaluateImportBoundary(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("the committed tree breaks the import-boundary guard:\n%s", strings.Join(got, "\n"))
	}
}

// Unit tests of the guard function over synthetic trees.

func TestGuardImportBoundary(t *testing.T) {
	for _, p := range []string{"example.test/mod/cmd/tool", "example.test/mod/cmd/tool/internal/x", "example.test/mod/internal/x", "example.test/mod/pkg/internal"} {
		got := violations(t, map[string]string{"store/a.go": "package store\n\nimport _ \"" + p + "\"\n"})
		wantViolation(t, got, "store/a.go", p, "G3")
	}
	wantViolation(t, violations(t, map[string]string{"store/main.go": "package main\n\nfunc main() {}\n"}), "store/main.go", "package main")
	wantClean(t, violations(t, map[string]string{"store/a.go": "package store\n\nimport _ \"example.test/mod/pkg/connectord/store\"\n"}))
}

func TestGuardSkipsTestdata(t *testing.T) {
	wantClean(t, violations(t, map[string]string{
		"store/testdata/fixture.go": "package main\n\nimport _ \"example.test/mod/internal/x\"\n",
		"store/a.go":                "package store\n\nvar V int\n",
	}))
}
