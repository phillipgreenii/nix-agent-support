package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The selector-text check in integrationMarkers only sees a qualified
// servetest.<Launcher> call written in the test file itself. It misses a test
// that calls a launcher unqualified from inside package servetest, and a test
// that calls a helper whose body (in a non-test file) builds or starts the
// daemon. This file closes both gaps with a taint pass over the module's
// non-test files: a function is TAINTED when its body execs `go` for anything
// but a metadata query, when it is a servetest launcher, or when it calls a
// tainted function. A test file compiled by a plain `go test ./...` that calls
// a tainted function is an integration-tier test.
//
// Resolution is by name inside the module (go/parser and the module's own
// import paths only; no type information): an unqualified call or a method call
// resolves against the same package directory, and a call qualified by an import
// of an in-module package resolves against that package. A method call matches
// any method of that name in the package, so the pass may over-report (a
// conservative failure that an author fixes by tagging the file or renaming),
// and never under-reports a helper reached by name.

// guardFuncKey identifies a function by package directory (relative to the module
// root, slash-separated) and name.
type guardFuncKey struct{ dir, name string }

// callTarget resolves one call expression, written in a file of package
// directory dir whose in-module imports are aliases, to the guardFuncKey it names.
// ok is false for a call that cannot name a module function (a builtin
// conversion, a call on a call result, an out-of-module package).
func callTarget(call *ast.CallExpr, dir string, aliases map[string]string, pkgName string) (key guardFuncKey, qualifier string, ok bool) {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return guardFuncKey{dir, fn.Name}, "", true
	case *ast.SelectorExpr:
		x, isIdent := fn.X.(*ast.Ident)
		if !isIdent {
			return guardFuncKey{dir, fn.Sel.Name}, "", true // method on an expression
		}
		if target, isImport := aliases[x.Name]; isImport {
			return guardFuncKey{target, fn.Sel.Name}, x.Name, true
		}
		if x.Name == "servetest" && pkgName != "servetest" {
			return guardFuncKey{}, x.Name, false // out-of-module or unresolved qualifier
		}
		return guardFuncKey{dir, fn.Sel.Name}, "", true // method call: match by name in this package
	}
	return guardFuncKey{}, "", false
}

// moduleImportAliases maps each local import name of f that names an in-module
// package to that package's directory relative to the module root.
func moduleImportAliases(f *ast.File, modPath string) map[string]string {
	aliases := map[string]string{}
	if modPath == "" {
		return aliases
	}
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil || !strings.HasPrefix(p, modPath+"/") {
			continue
		}
		dir := strings.TrimPrefix(p, modPath+"/")
		name := path.Base(p)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		aliases[name] = dir
	}
	return aliases
}

// readModulePath returns the module path declared in root/go.mod, or "".
func readModulePath(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

type taintFunc struct {
	key   guardFuncKey
	seed  string
	calls []guardFuncKey
}

// computeTaint returns, for every tainted function of the module's default-build
// non-test files, the reason it is tainted.
func computeTaint(root string) (map[guardFuncKey]string, error) {
	modPath := readModulePath(root)
	var funcs []taintFunc
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !inDefaultBuild(src) {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, src, 0)
		if err != nil {
			return nil // a file the toolchain would reject is not this guard's concern
		}
		rel, _ := filepath.Rel(root, filepath.Dir(p))
		dir := filepath.ToSlash(rel)
		aliases := moduleImportAliases(f, modPath)
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			tf := taintFunc{key: guardFuncKey{dir, fd.Name.Name}}
			if f.Name.Name == "servetest" && servetestLaunchers[fd.Name.Name] {
				tf.seed = "starts the real daemon (a servetest launcher)"
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "exec" &&
						(sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext") {
						r := execReason(call, sel.Sel.Name == "CommandContext")
						if strings.HasPrefix(r, "builds or runs a Go program") && tf.seed == "" {
							tf.seed = "runs `go build` or another Go tool"
						}
					}
				}
				if key, _, ok := callTarget(call, dir, aliases, f.Name.Name); ok {
					tf.calls = append(tf.calls, key)
				}
				return true
			})
			funcs = append(funcs, tf)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	tainted := map[guardFuncKey]string{}
	for _, tf := range funcs {
		if tf.seed != "" {
			tainted[tf.key] = tf.seed
		}
	}
	for changed := true; changed; {
		changed = false
		for _, tf := range funcs {
			if _, done := tainted[tf.key]; done {
				continue
			}
			for _, c := range tf.calls {
				if why, ok := tainted[c]; ok {
					tainted[tf.key] = "calls " + c.name + ", which " + why
					changed = true
					break
				}
			}
		}
	}
	return tainted, nil
}

// helperMarkers returns one reason per tainted function that the test file src
// (in package directory dir, relative to the module root) calls. A qualified
// servetest launcher call is left to integrationMarkers, which already reports it.
func helperMarkers(name string, src []byte, dir, modPath string, tainted map[guardFuncKey]string) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		return nil, err
	}
	aliases := moduleImportAliases(f, modPath)
	seen := map[string]bool{}
	var reasons []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		key, qual, ok := callTarget(call, dir, aliases, f.Name.Name)
		if !ok {
			return true
		}
		why, isTainted := tainted[key]
		if !isTainted {
			return true
		}
		if qual == "servetest" && servetestLaunchers[key.name] {
			return true
		}
		if r := "calls " + key.name + ", which " + why; !seen[r] {
			seen[r] = true
			reasons = append(reasons, r)
		}
		return true
	})
	sort.Strings(reasons)
	return reasons, nil
}

// TestUnitTierGuardFollowsHelpers proves the guard catches a test that reaches the
// daemon launcher unqualified inside package servetest, and a test whose non-test
// helper runs `go build`, against fixture trees under testdata (the walk skips
// testdata, so the fixtures never count against the real tree), and that a helper
// asking `go list` only is clean.
func TestUnitTierGuardFollowsHelpers(t *testing.T) {
	cases := []struct {
		fixture string
		want    string // substring of the single expected violation; "" means none
	}{
		{"unqualified-launcher", "loop_test.go: calls NewDaemon, which starts the real daemon"},
		{"helper-build", "use_test.go: calls Prepare, which calls build, which runs `go build`"},
		{"clean", ""},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			violations, _, err := evaluateUnitTierPurity(materializeFixture(t, tc.fixture), nil)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Join(violations, "\n")
			if tc.want == "" {
				if len(violations) != 0 {
					t.Errorf("clean fixture flagged:\n%s", got)
				}
				return
			}
			if len(violations) != 1 || !strings.Contains(got, tc.want) {
				t.Errorf("violations = %v, want exactly one containing %q", violations, tc.want)
			}
		})
	}
}

// materializeFixture copies testdata/unit-tier-guard/<name> into a temp dir,
// dropping the .txt suffix from each Go file. The fixtures are stored as .go.txt
// so the toolchain and the module's other source walkers (layout convention,
// dependency direction) never see them as module packages.
func materializeFixture(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("testdata", "unit-tier-guard", name)
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, strings.TrimSuffix(rel, ".txt"))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
	if err != nil {
		t.Fatalf("materialize fixture %s: %v", name, err)
	}
	return dst
}
