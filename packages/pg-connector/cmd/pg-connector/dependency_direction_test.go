package main

import (
	"fmt"
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

// dfWordSegmentRE matches a df-<word> path segment (e.g. df-survey,
// df-attention, df-categorize) anywhere in an import path — either at the
// very start of the path or immediately after a "/" separator, so it never
// matches a token that merely CONTAINS "df-" mid-word (e.g. it would not
// match "asdf-word").
var dfWordSegmentRE = regexp.MustCompile(`(^|/)df-[\w-]+`)

// attentionZeroImportTargets returns the file paths within moduleRoot that
// the attention capability's zero-import dependency-direction guard scans:
// pkg/schema/attention.go itself, plus every file under
// pkg/provider/attention/ — the attention capability's shape/interface
// package MUST have zero imports of, or references to, any
// daily-focus/df-survey package or type. Either path is skipped, not an
// error, if it does not exist (e.g. a synthetic fixture exercising only
// one of the two).
func attentionZeroImportTargets(moduleRoot string) ([]string, error) {
	var targets []string

	schemaFile := filepath.Join(moduleRoot, "pkg", "schema", "attention.go")
	if _, err := os.Stat(schemaFile); err == nil {
		targets = append(targets, schemaFile)
	}

	providerDir := filepath.Join(moduleRoot, "pkg", "provider", "attention")
	if _, err := os.Stat(providerDir); err == nil {
		err := filepath.WalkDir(providerDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(d.Name(), ".go") {
				targets = append(targets, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	return targets, nil
}

// evaluateAttentionZeroImport parses every target file
// (attentionZeroImportTargets) and returns one violation string per import
// whose path either contains the substring "daily-focus" or matches a
// df-<word> path segment (e.g. df-survey, df-attention, df-categorize).
//
// This mechanizes the SECOND half of the module's own dependency-direction
// CI check: evaluateCompositionBoundary above enforces only the
// composition-boundary/exec-scan half (no exec.Command/os/exec of
// pg-connector or a sibling backend binary) — it does not scan for
// daily-focus/df-survey imports at all. This function is net-new work, not
// a restatement of that existing guard.
func evaluateAttentionZeroImport(moduleRoot string) ([]string, error) {
	targets, err := attentionZeroImportTargets(moduleRoot)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	var violations []string
	for _, path := range targets {
		rel, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)

		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		for _, imp := range file.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if strings.Contains(importPath, "daily-focus") || dfWordSegmentRE.MatchString(importPath) {
				violations = append(violations, fmt.Sprintf(
					"%s: imports %q — the attention capability's shape/interface package MUST have zero imports of, or references to, any daily-focus/df-survey package or type",
					rel, importPath,
				))
			}
		}
	}
	return violations, nil
}

// TestAttentionZeroImport is the module's own attention zero-import
// dependency-direction check: pkg/schema/attention.go and every file under
// pkg/provider/attention/ have zero imports of, or references to, any
// daily-focus/df-survey package or type. Against the current, real tree
// this passes vacuously (no such package exists yet to import) — the
// fixture tests below prove evaluateAttentionZeroImport actually rejects a
// real violation rather than only ever passing vacuously.
func TestAttentionZeroImport(t *testing.T) {
	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	violations, err := evaluateAttentionZeroImport(moduleRoot)
	if err != nil {
		t.Fatalf("evaluateAttentionZeroImport: %v", err)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

// TestAttentionZeroImport_DetectsDailyFocusSubstring is a test-of-a-test:
// it writes a synthetic, never-committed pkg/provider/attention file
// importing a package whose path contains "daily-focus", and asserts
// evaluateAttentionZeroImport rejects it.
func TestAttentionZeroImport_DetectsDailyFocusSubstring(t *testing.T) {
	dir := t.TempDir()
	src := `package attention

import (
	"context"

	"github.com/example/daily-focus/pkg/survey"
)

func doThing(ctx context.Context) survey.Item { return survey.Item{} }
`
	writeCompositionFixture(t, dir, "pkg/provider/attention/iface.go", src)

	violations, err := evaluateAttentionZeroImport(dir)
	if err != nil {
		t.Fatalf("evaluateAttentionZeroImport: %v", err)
	}
	assertContainsViolation(t, violations, "daily-focus")
}

// TestAttentionZeroImport_DetectsDfWordSegment is the same test-of-a-test
// shape for the OTHER half of the rule: an import path matching a
// df-<word> path segment (e.g. df-survey), not containing "daily-focus" at
// all.
func TestAttentionZeroImport_DetectsDfWordSegment(t *testing.T) {
	dir := t.TempDir()
	src := `package attention

import "github.com/example/df-survey/pkg/types"

var _ = types.Foo{}
`
	writeCompositionFixture(t, dir, "pkg/provider/attention/dispatch.go", src)

	violations, err := evaluateAttentionZeroImport(dir)
	if err != nil {
		t.Fatalf("evaluateAttentionZeroImport: %v", err)
	}
	assertContainsViolation(t, violations, "df-survey")
}

// TestAttentionZeroImport_ScansSchemaFileToo proves the check actually
// walks pkg/schema/attention.go itself, not only pkg/provider/attention/.
func TestAttentionZeroImport_ScansSchemaFileToo(t *testing.T) {
	dir := t.TempDir()
	src := `package schema

import "github.com/example/daily-focus/pkg/survey"

var _ = survey.Item{}
`
	writeCompositionFixture(t, dir, "pkg/schema/attention.go", src)

	violations, err := evaluateAttentionZeroImport(dir)
	if err != nil {
		t.Fatalf("evaluateAttentionZeroImport: %v", err)
	}
	assertContainsViolation(t, violations, "daily-focus")
}

// TestAttentionZeroImport_AllowsCleanImports guards against the opposite
// failure mode (a checker that flags everything): an ordinary import of
// this module's own shared schema package must produce zero violations.
func TestAttentionZeroImport_AllowsCleanImports(t *testing.T) {
	dir := t.TempDir()
	src := `package attention

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

func doThing(ctx context.Context) schema.AttentionItem { return schema.AttentionItem{} }
`
	writeCompositionFixture(t, dir, "pkg/provider/attention/iface.go", src)

	violations, err := evaluateAttentionZeroImport(dir)
	if err != nil {
		t.Fatalf("evaluateAttentionZeroImport: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("evaluateAttentionZeroImport flagged clean imports: %v", violations)
	}
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
