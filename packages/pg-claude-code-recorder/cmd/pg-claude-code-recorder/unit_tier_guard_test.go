package main

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
// run-unit-tests hook, pre-land and checks.<system>.pg-claude-code-recorder-go-tests)
// MUST stay fast and hermetic (V-4, docs/adr/0111). A test that starts the
// daemon, runs a binary as a subprocess, builds a Go program or needs a real
// external tool is integration tier: its file MUST carry `//go:build integration`
// (or `faults` / `soak`). Only fast single-process real-tool tests (git, the
// shell listener's bash run) stay unit.

// unitExecAllowed lists programs a unit-tier test may exec by literal name.
var unitExecAllowed = map[string]bool{"git": true, "diff": true, "sh": true, "bash": true}

// goMetaSubcommands are `go` invocations that only query metadata.
var goMetaSubcommands = map[string]bool{"list": true, "env": true, "version": true}

// nonUnitTags are the build tags that keep a file out of the unit tier.
var nonUnitTags = []string{"integration", "faults", "soak"}

// daemonPackages are the package directories (relative to the module package
// root) whose untagged test files MUST NOT start a whole daemon. Other
// packages have their own, unrelated Start methods.
var daemonPackages = map[string]bool{
	"internal/serve":              true,
	"cmd/pg-claude-code-recorder": true,
}

// daemonStarters are helpers that start a whole in-process daemon.
var daemonStarters = map[string]bool{"liveDaemon": true, "RunRow": true}

// integrationMarkers returns one reason per integration-tier construct in src.
// daemonScope turns on the "starts a whole daemon" rule for the file.
func integrationMarkers(name string, src []byte, daemonScope bool) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		return nil, err
	}
	var reasons []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if daemonScope {
			if id, ok := call.Fun.(*ast.Ident); ok && daemonStarters[id.Name] {
				reasons = append(reasons, "starts a whole daemon through "+id.Name+"()")
			}
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if daemonScope {
			if sel.Sel.Name == "Start" && len(call.Args) == 0 {
				reasons = append(reasons, "starts a whole daemon (a zero-argument .Start())")
			}
			if daemonStarters[sel.Sel.Name] {
				reasons = append(reasons, "starts a whole daemon through "+sel.Sel.Name+"()")
			}
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "exec" &&
			(sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext") {
			if r := execReason(call, sel.Sel.Name == "CommandContext"); r != "" {
				reasons = append(reasons, r)
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
		return "builds or runs a Go program (go " + "build/run/vet/test)"
	}
	if unitExecAllowed[prog] {
		return ""
	}
	return fmt.Sprintf("needs the real external tool %q", prog)
}

// carriesNonUnitTag reports whether src's //go:build line requires one of the
// non-unit tags.
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
			// Satisfied only when exactly this tag is on: the file is out of
			// the default build.
			if expr.Eval(func(t string) bool { return t == tag }) &&
				!expr.Eval(func(string) bool { return false }) {
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
		reasons, err := integrationMarkers(path, src, daemonPackages[filepath.ToSlash(filepath.Dir(rel))])
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

// TestUnitTierGuardDetectsIntegrationTest proves the guard fails when an
// integration-shaped test sits in the unit tier, and passes once tagged.
func TestUnitTierGuardDetectsIntegrationTest(t *testing.T) {
	const body = `package x

import ("os/exec"; "testing")

func TestX(t *testing.T) { _ = exec.Command("go", "build", ".") }
`
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "x_test.go"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := unitTierViolations(root)
	if err != nil || len(v) != 1 {
		t.Fatalf("untagged: violations=%v err=%v, want exactly one", v, err)
	}
	if err := os.WriteFile(filepath.Join(root, "x_test.go"), []byte("//go:build integration\n\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, err = unitTierViolations(root); err != nil || len(v) != 0 {
		t.Fatalf("tagged: violations=%v err=%v, want none", v, err)
	}
}

// TestUnitTierGuardDetectsADaemonStartedInTheUnitTier proves the daemon rule
// fires for an untagged test file in a daemon package, for each way a test
// starts a whole daemon, stays quiet for a file outside those packages (their
// Start is another method) and for a tagged file.
func TestUnitTierGuardDetectsADaemonStartedInTheUnitTier(t *testing.T) {
	cases := map[string]string{
		"start":      "func TestX(t *testing.T) { d := open(); _ = d.Start() }",
		"liveDaemon": "func TestX(t *testing.T) { liveDaemon(t) }",
		"runRow":     "func TestX(t *testing.T) { clienttest.RunRow(t, row) }",
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			body := "package x\n\nimport \"testing\"\n\n" + fn + "\n"
			root := t.TempDir()
			dir := filepath.Join(root, "internal", "serve")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "x_test.go")
			if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if v, err := unitTierViolations(root); err != nil || len(v) != 1 {
				t.Fatalf("untagged in a daemon package: violations=%v err=%v, want exactly one", v, err)
			}
			if err := os.WriteFile(file, []byte("//go:build integration\n\n"+body), 0o600); err != nil {
				t.Fatal(err)
			}
			if v, err := unitTierViolations(root); err != nil || len(v) != 0 {
				t.Fatalf("tagged: violations=%v err=%v, want none", v, err)
			}
			other := filepath.Join(root, "internal", "other")
			if err := os.MkdirAll(other, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(other, "x_test.go"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			if v, err := unitTierViolations(root); err != nil || len(v) != 0 {
				t.Fatalf("untagged outside the daemon packages: violations=%v err=%v, want none", v, err)
			}
		})
	}
}
