package unittier

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
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

// The unit tier (a plain `go test ./...`, no build tags: the commit-time
// run-unit-tests hook, pre-land and checks.<system>.pg-router-ccpool-handler-go-tests) MUST
// stay fast and hermetic (V-4). A test that re-executes the test binary, starts
// a handler binary, drives the real ccpool or tmux or builds a Go program is integration tier: its file MUST
// carry `//go:build integration`, and it runs in
// checks.<system>.pg-router-handlers-integration-tests. Only fast single-process
// real-tool tests (git, a /bin/sh stub handler) stay unit.

// unitExecAllowed lists programs a unit-tier test may exec by literal name.
var unitExecAllowed = map[string]bool{"git": true, "diff": true, "sh": true, "bash": true, "true": true, "ps": true}

// goMetaSubcommands are `go` invocations that only query metadata.
var goMetaSubcommands = map[string]bool{"list": true, "env": true, "version": true}

// nonUnitTags are the build tags that keep a file out of the unit tier.
var nonUnitTags = []string{"integration", "smoke"}

// launcherNames are the helpers that start the router binary or a re-executed
// test-binary helper process.
var launcherNames = map[string]bool{
	"buildBinary":             true,
	"runAdapterProcess":       true,
	"runWithClosedStdoutPipe": true,
	"newRealSandbox":          true,
}

// integrationMarkers returns one reason per integration-tier construct in src.
func integrationMarkers(name string, src []byte) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		return nil, err
	}
	var reasons []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.IndexExpr:
			if sel, ok := x.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "Args" {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "os" {
					if lit, ok := x.Index.(*ast.BasicLit); ok && lit.Value == "0" {
						reasons = append(reasons, "re-executes the test binary (os.Args[0])")
					}
				}
			}
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok && launcherNames[id.Name] {
				reasons = append(reasons, "calls the process launcher "+id.Name+"()")
			}
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if launcherNames[sel.Sel.Name] {
				reasons = append(reasons, "calls the process launcher "+sel.Sel.Name+"()")
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "exec" &&
				(sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext") {
				if r := execReason(x, sel.Sel.Name == "CommandContext"); r != "" {
					reasons = append(reasons, r)
				}
			}
		}
		return true
	})
	return reasons, nil
}

func execReason(call *ast.CallExpr, withCtx bool) string {
	i := 0
	if withCtx {
		i = 1
	}
	if len(call.Args) <= i {
		return ""
	}
	lit, ok := call.Args[i].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "runs a program held in a variable (a built binary or the test binary) as a subprocess"
	}
	prog, _ := strconv.Unquote(lit.Value)
	if prog == "go" || strings.HasSuffix(prog, "/go") {
		if len(call.Args) > i+1 {
			if sub, ok := call.Args[i+1].(*ast.BasicLit); ok {
				if s, _ := strconv.Unquote(sub.Value); goMetaSubcommands[s] {
					return ""
				}
			}
		}
		return "builds or runs a Go program (go build/run/vet/test)"
	}
	if unitExecAllowed[filepath.Base(prog)] {
		return ""
	}
	return fmt.Sprintf("needs the real external tool %q", prog)
}

// carriesNonUnitTag reports whether src's //go:build line keeps the file out of
// the unit build by requiring one of the non-unit tags (an OS tag alongside it,
// as in `integration && unix`, does not matter).
func carriesNonUnitTag(src []byte) bool {
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if !constraint.IsGoBuild(line) {
			if strings.HasPrefix(line, "package ") {
				return false
			}
			continue
		}
		expr, err := constraint.Parse(line)
		if err != nil {
			return false
		}
		for _, tag := range nonUnitTags {
			osTag := func(t string) bool { return t == "unix" || t == "darwin" || t == "linux" }
			if expr.Eval(func(t string) bool { return t == tag || osTag(t) }) &&
				!expr.Eval(osTag) {
				return true
			}
		}
		return false
	}
	return false
}

// unitTierViolations scans every *_test.go under root and returns the untagged
// ones that carry an integration marker.
func unitTierViolations(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if carriesNonUnitTag(src) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		reasons, err := integrationMarkers(path, src)
		if err != nil {
			return err
		}
		for _, r := range reasons {
			out = append(out, rel+": "+r)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func TestUnitTierHasNoIntegrationTests(t *testing.T) {
	v, err := unitTierViolations("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range v {
		t.Errorf("unit-tier test is integration tier; tag its file //go:build integration: %s", line)
	}
}

// TestUnitTierGuardDetectsIntegrationTests proves the guard fails for each
// integration-shaped construct planted in an untagged file, and passes once the
// file is tagged.
func TestUnitTierGuardDetectsIntegrationTests(t *testing.T) {
	cases := map[string]string{
		"reexec":   `func TestX(t *testing.T) { _ = exec.Command(os.Args[0], "-test.run=^H$") }`,
		"goBuild":  `func TestX(t *testing.T) { _ = exec.Command("go", "build", ".") }`,
		"variable": `func TestX(t *testing.T) { bin := "x"; _ = exec.Command(bin) }`,
		"tool":     `func TestX(t *testing.T) { _ = exec.Command("bd", "init") }`,
		"launcher": `func TestX(t *testing.T) { _ = runAdapterProcess(t) }`,
		"ccpool":   `func TestX(t *testing.T) { _ = exec.Command("ccpool", "list") }`,
		"tmux":     `func TestX(t *testing.T) { _ = exec.Command("tmux", "ls") }`,
		"sandbox":  `func TestX(t *testing.T) { _ = newRealSandbox(t) }`,
		"binary":   `func TestX(t *testing.T) { _ = buildBinary(t, "x") }`,
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			body := "package x\n\nimport (\"os\"; \"os/exec\"; \"testing\")\n\nvar _ = os.Args\nvar _ = exec.Command\n\n" + fn + "\n"
			root := t.TempDir()
			file := filepath.Join(root, "x_test.go")
			if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if v, err := unitTierViolations(root); err != nil || len(v) == 0 {
				t.Fatalf("untagged: violations=%v err=%v, want at least one", v, err)
			}
			if err := os.WriteFile(file, []byte("//go:build integration && unix\n\n"+body), 0o600); err != nil {
				t.Fatal(err)
			}
			if v, err := unitTierViolations(root); err != nil || len(v) != 0 {
				t.Fatalf("tagged: violations=%v err=%v, want none", v, err)
			}
		})
	}
}

// TestUnitTierGuardAllowsFastRealTools proves the guard stays quiet for the
// single-process real tools the unit tier may use.
func TestUnitTierGuardAllowsFastRealTools(t *testing.T) {
	const body = `package x

import ("os/exec"; "testing")

func TestX(t *testing.T) {
	_ = exec.Command("git", "init")
	_ = exec.Command("/bin/sh", "-c", "true")
	_ = exec.Command("go", "list", "./...")
}
`
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "x_test.go"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, err := unitTierViolations(root); err != nil || len(v) != 0 {
		t.Fatalf("violations=%v err=%v, want none", v, err)
	}
}
