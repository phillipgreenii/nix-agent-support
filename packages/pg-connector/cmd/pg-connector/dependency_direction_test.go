package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// pgConnectorExecRE matches an `exec.Command`/`exec.CommandContext` call
// whose BINARY argument is a string literal starting with "pg-connector" —
// i.e. a Tier-2 backend (or the Tier-1 umbrella itself) shelling out to
// pg-connector or to another pg-connector-<type>-<backend> binary. Modeled
// directly on this module's existing chokepoint-test convention (see this
// same package's chokepoint_test.go, relocated here from
// cmd/pg-connector-pr-github/internal/github by bead pg2-lh3c4 — ghExecRE)
// — same line-based scanning, same "binary is a string literal in
// the call" shape. Like that precedent, this is line-based: a call whose
// arguments span multiple source lines would not be matched. Every actual
// call site in this module today is single-line.
var pgConnectorExecRE = regexp.MustCompile(`exec\.Command(?:Context)?\(\s*(?:[\w.]+\s*,\s*)?"(pg-connector[\w-]*)"`)

// evaluateCompositionBoundary walks moduleRoot and returns one violation
// string per non-test .go file that execs "pg-connector" or any
// "pg-connector-<type>-<backend>" binary.
//
// This mechanizes the composition boundary (INV-COMP-1): "A Tier-2
// backend never execs pg-connector or another Tier-2 backend binary to
// satisfy its own op; a cross-capability data need is resolved via that
// backend's own direct system access instead" — the doc's own suggested
// enforcement is exactly this: "a mechanical grep for exec.Command/os.exec
// naming pg-connector or another pg-connector-<type>-<backend> binary
// outside a backend's own tests would catch a regression." bead pg2-0vwcc
// fixed the one known offender (cmd/pg-connector-ci-github-actions's
// resolver.go) with a direct in-process resolver and a regression test for
// that ONE call site; this function is the module-wide backstop that
// catches a FUTURE occurrence anywhere in the module, not just at that one
// now-fixed call site.
//
// Test files are excluded from the scan (the design text's own "outside a
// backend's own tests" carve-out) — a test legitimately building and
// exec'ing the real compiled binary to exercise it end-to-end is not the
// undeclared-runtime-dependency problem this rule targets.
func evaluateCompositionBoundary(moduleRoot string) ([]string, error) {
	var violations []string
	err := filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
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
		rel, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if m := pgConnectorExecRE.FindStringSubmatch(line); m != nil {
				violations = append(violations, fmt.Sprintf(
					"%s:%d: execs %q — a Tier-2 backend MUST resolve a cross-capability data need via its own direct system access, never by shelling out to pg-connector or a sibling backend binary (INV-REG-1): %s",
					rel, i+1, m[1], strings.TrimSpace(line),
				))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return violations, nil
}

// TestCompositionBoundaryNoBackendExecsPgConnector is the composition
// boundary's (INV-COMP-1) dependency-direction check: no backend's own
// (non-test) source execs
// pg-connector itself, or another pg-connector-<type>-<backend> binary.
func TestCompositionBoundaryNoBackendExecsPgConnector(t *testing.T) {
	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	violations, err := evaluateCompositionBoundary(moduleRoot)
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

// TestCompositionBoundary_DetectsUmbrellaExec and
// TestCompositionBoundary_DetectsSiblingBackendExec are test-of-a-test
// proofs: bead pg2-0vwcc already fixed the one known violation, so the real
// tree has nothing left to reject — these prove evaluateCompositionBoundary
// actually rejects the two shapes INV-COMP-1 names ("pg-connector or
// another Tier-2 backend binary") rather than only ever passing vacuously.
// Both write a synthetic, never-committed source file to a temp directory.
func TestCompositionBoundary_DetectsUmbrellaExec(t *testing.T) {
	dir := t.TempDir()
	src := `package internal

import (
	"context"
	"os/exec"
)

// resolvePR is a deliberately non-compliant stand-in for the pg2-0vwcc
// defect: shelling out to the Tier-1 umbrella instead of using this
// backend's own direct system access.
func resolvePR(ctx context.Context, id string) error {
	cmd := exec.CommandContext(ctx, "pg-connector", "pr", "show", id)
	return cmd.Run()
}
`
	writeCompositionFixture(t, dir, "cmd/pg-connector-fake-backend/internal/resolver.go", src)

	violations, err := evaluateCompositionBoundary(dir)
	if err != nil {
		t.Fatalf("evaluateCompositionBoundary: %v", err)
	}
	assertContainsViolation(t, violations, `"pg-connector"`)
}

func TestCompositionBoundary_DetectsSiblingBackendExec(t *testing.T) {
	dir := t.TempDir()
	src := `package internal

import "os/exec"

// resolveIssue is a deliberately non-compliant stand-in: shelling out to a
// SIBLING Tier-2 backend binary directly, bypassing the umbrella entirely
// but still violating the same composition boundary.
func resolveIssue(id string) error {
	cmd := exec.Command("pg-connector-issue-beads", "issue", "show", id)
	return cmd.Run()
}
`
	writeCompositionFixture(t, dir, "cmd/pg-connector-fake-backend/internal/resolver.go", src)

	violations, err := evaluateCompositionBoundary(dir)
	if err != nil {
		t.Fatalf("evaluateCompositionBoundary: %v", err)
	}
	assertContainsViolation(t, violations, `"pg-connector-issue-beads"`)
}

// TestCompositionBoundary_AllowsTestFileExec guards the "outside a
// backend's own tests" carve-out: the identical exec call, but in a
// _test.go file, must NOT be flagged.
func TestCompositionBoundary_AllowsTestFileExec(t *testing.T) {
	dir := t.TempDir()
	src := `package internal

import "os/exec"

func TestSomething() {
	cmd := exec.Command("pg-connector", "pr", "show", "1")
	_ = cmd
}
`
	writeCompositionFixture(t, dir, "cmd/pg-connector-fake-backend/internal/resolver_test.go", src)

	violations, err := evaluateCompositionBoundary(dir)
	if err != nil {
		t.Fatalf("evaluateCompositionBoundary: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("evaluateCompositionBoundary flagged a _test.go file, which the design's own carve-out excludes: %v", violations)
	}
}

// scriptoutImportPath is the import path of the wire package whose bare
// string-name wrappers the registry guard below polices.
const scriptoutImportPath = "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"

// bareScriptoutWrappers are the exported string-name entry points that exec
// the bare binary and ignore any registered argv. Every umbrella exec MUST go
// through the Registry (Registry.Invoke / Registry.InvokeCapabilities, which
// pass the registered Target); calling these from the umbrella would silently
// run an instance without its flag, and cacheEnabled would fail OPEN on the
// resulting probe error (bead pg2-h5cmo item 1, INV-REG-4).
var bareScriptoutWrappers = map[string]bool{"Invoke": true, "InvokeCapabilities": true}

// evaluateRegistryBypass parses every non-test .go file directly under dir
// (the umbrella's package directory) and returns one violation per call
// expression scriptout.Invoke(...) / scriptout.InvokeCapabilities(...).
// It is AST-based, so a mention in a comment is not a violation.
func evaluateRegistryBypass(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var violations []string
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		local := ""
		for _, imp := range file.Imports {
			if p, err := strconv.Unquote(imp.Path.Value); err == nil && p == scriptoutImportPath {
				local = "scriptout"
				if imp.Name != nil {
					local = imp.Name.Name
				}
			}
		}
		if local == "" {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == local && bareScriptoutWrappers[sel.Sel.Name] {
				violations = append(violations, fmt.Sprintf(
					"%s:%d: calls scriptout.%s, which execs the bare binary and drops the registered argv — go through Registry.Invoke / Registry.InvokeCapabilities (INV-REG-4)",
					name, fset.Position(call.Pos()).Line, sel.Sel.Name,
				))
			}
			return true
		})
	}
	return violations, nil
}

// TestUmbrellaNeverCallsBareScriptoutWrappers is the registry-bypass guard:
// no non-test file in cmd/pg-connector calls scriptout.Invoke or
// scriptout.InvokeCapabilities.
func TestUmbrellaNeverCallsBareScriptoutWrappers(t *testing.T) {
	violations, err := evaluateRegistryBypass(".")
	if err != nil {
		t.Fatalf("evaluateRegistryBypass: %v", err)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

// TestRegistryBypass_DetectsBareWrapperCalls proves the guard is not
// vacuous: both wrappers are flagged (including under an import alias), a
// comment mention and the Target variants are not.
func TestRegistryBypass_DetectsBareWrapperCalls(t *testing.T) {
	dir := t.TempDir()
	writeCompositionFixture(t, dir, "bad.go", `package main

import (
	"context"

	so "`+scriptoutImportPath+`"
)

func bad(ctx context.Context) {
	_, _ = so.Invoke(ctx, "b", "list", nil, nil)
	_, _ = so.InvokeCapabilities(ctx, "b")
}
`)
	writeCompositionFixture(t, dir, "good.go", `package main

import (
	"context"

	"`+scriptoutImportPath+`"
)

// scriptout.Invoke(ctx, binary) in a comment is not a call.
func good(ctx context.Context, t scriptout.Target) {
	_, _ = scriptout.InvokeTarget(ctx, t, "list", nil, nil)
	_, _ = scriptout.InvokeCapabilitiesTarget(ctx, t)
}
`)
	writeCompositionFixture(t, dir, "bad_test.go", `package main

import "`+scriptoutImportPath+`"

var _, _ = scriptout.Invoke(nil, "b", "x", nil, nil)
`)

	violations, err := evaluateRegistryBypass(dir)
	if err != nil {
		t.Fatalf("evaluateRegistryBypass: %v", err)
	}
	if len(violations) != 2 {
		t.Fatalf("violations = %v, want exactly the 2 calls in bad.go", violations)
	}
	assertContainsViolation(t, violations, "bad.go:10: calls scriptout.Invoke,")
	assertContainsViolation(t, violations, "bad.go:11: calls scriptout.InvokeCapabilities,")
}

func writeCompositionFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func assertContainsViolation(t *testing.T, violations []string, want string) {
	t.Helper()
	for _, v := range violations {
		if strings.Contains(v, want) {
			return
		}
	}
	t.Fatalf("expected a violation containing %q, got: %v", want, violations)
}
