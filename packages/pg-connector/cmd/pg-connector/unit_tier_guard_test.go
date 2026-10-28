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
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The unit tier of this module (what the commit-time `run-unit-tests` hook, `pg-hooks
// run pre-land` and `checks.<system>.pg-connector-go-tests` run: a plain `go test
// ./...`, no build tags) MUST stay fast and hermetic (bead pg2-bkwi41, operator ruling
// 2026-10-26: "the integration tests are taking way too long. they should not be
// running as typical unit tests and in typical unit test situations like commit or
// landing").
//
// A test is INTEGRATION tier, and its file MUST carry `//go:build integration`, when it
//
//   - starts the real daemon through the servetest harness (servetest.NewDaemon,
//     StartDaemon, Binary, DaemonPath),
//   - builds or runs a Go program (`go build`, `go run`, `go vet`, `go test -c`),
//   - runs a built binary as a subprocess (an exec.Command whose program is not a
//     literal on the unit allowlist, e.g. a path held in a variable), or
//   - needs a real external tool (bd, dolt, tmux, ccpool).
//
// Everything else stays unit: pure logic, in-process tests against a fake backend
// script, a git fixture repo (git is allowlisted: fast, single process) and
// `go list`/`go env` metadata queries.
//
// Re-executing the test binary (exec.Command(os.Args[0], ...)) is NOT unit tier any
// more (shared command runner ADR in repo x, decision 5; bead pg2-ty6mjm): a test
// that stands in for an external tool MUST use the x runner fake, and a test whose
// subject is a real process MUST live in an integration-tagged file. The existing
// uses are listed by path, each with its reason, in unitReexecAllowed until it is
// migrated or tagged; a new one is a violation.
//
// Integration tests run on demand: `nix build .#checks.<system>.pg-connector-integration-tests`
// or `go test -tags integration ./...` (see README.md, "Integration tier"). Files tagged
// `contract` (real bd, real credentials) are a separate on-demand tier and are excluded
// from the default build the same way.

// unitExecAllowed lists the programs a unit-tier test may exec by literal name.
var unitExecAllowed = map[string]bool{"git": true, "diff": true, "sh": true, "bash": true}

// reexecReason is the reason integrationMarkers reports for an exec of the test binary.
const reexecReason = "re-executes the test binary (os.Args[0]) as a helper process"

// unitReexecAllowed lists, by path relative to the module root, the unit-tier test
// files that still re-execute the test binary, each with the reason it is allowed.
// An entry that no longer matches a re-exec call fails the guard, so the list can
// only shrink.
var unitReexecAllowed = map[string]string{
	"pkg/connectord/store/store_test.go":                            "crash-recovery test kills a re-executed writer mid-transaction; the dead process is the subject, the file also holds unit migration tests",
	"cmd/pg-connector-github/internal/logs/rotation_test.go":        "cross-process rotation test: concurrent client processes are the subject, the file also holds unit rotation tests",
	"cmd/pg-connector-issue-jira/internal/logs/rotation_test.go":    "cross-process rotation test: concurrent client processes are the subject, the file also holds unit rotation tests",
	"cmd/pg-connector-github/internal/sidecar/lock_process_test.go": "cross-process sidecar lock tests: a second process holding the lock is the subject",
	"cmd/pg-connector-github/internal/ops/review/daemon_test.go":    "cross-process sidecar lock test: a second process holding the lock is the subject, the file also holds unit tests",
}

// goMetaSubcommands are `go` invocations that only query metadata, never build.
var goMetaSubcommands = map[string]bool{"list": true, "env": true, "version": true}

// servetestLaunchers are the servetest functions that start (or build) the real daemon.
var servetestLaunchers = map[string]bool{"NewDaemon": true, "StartDaemon": true, "Binary": true, "DaemonPath": true}

// integrationMarkers returns one reason per integration-tier construct in the Go
// source src (a test file named name). An empty result means the file is unit-tier clean.
func integrationMarkers(name string, src []byte) ([]string, error) {
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
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch {
		case pkg.Name == "servetest" && servetestLaunchers[sel.Sel.Name]:
			reasons = append(reasons, "starts the real daemon: servetest."+sel.Sel.Name)
		case pkg.Name == "exec" && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext"):
			if r := execReason(call, sel.Sel.Name == "CommandContext"); r != "" {
				reasons = append(reasons, r)
			}
		}
		return true
	})
	return reasons, nil
}

// execReason classifies one exec.Command(Context) call; "" means unit-tier safe.
func execReason(call *ast.CallExpr, withCtx bool) string {
	i := 0
	if withCtx {
		i = 1
	}
	if len(call.Args) <= i {
		return ""
	}
	prog := call.Args[i]
	if lit, ok := prog.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		name, _ := strconv.Unquote(lit.Value)
		if name == "go" || strings.HasSuffix(name, "/go") {
			if goMetadataOnly(call.Args, i+1) {
				return ""
			}
			return "builds or runs a Go program: exec of go"
		}
		if unitExecAllowed[name] {
			return ""
		}
		return fmt.Sprintf("runs the external program %q", name)
	}
	if idx, ok := prog.(*ast.IndexExpr); ok && isOsArgsZero(idx) {
		return reexecReason
	}
	if goMetadataOnly(call.Args, i+1) {
		return "" // a variable holding the go tool, asked only for metadata
	}
	return "runs a program chosen at run time (a built binary or an external tool)"
}

func isOsArgsZero(idx *ast.IndexExpr) bool {
	sel, ok := idx.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	lit, litOK := idx.Index.(*ast.BasicLit)
	return ok && pkg.Name == "os" && sel.Sel.Name == "Args" && litOK && lit.Value == "0"
}

func goMetadataOnly(args []ast.Expr, i int) bool {
	if i >= len(args) {
		return false
	}
	lit, ok := args[i].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	s, _ := strconv.Unquote(lit.Value)
	return goMetaSubcommands[s]
}

// buildConstraint returns the file's `//go:build` expression, or nil when it has none.
func buildConstraint(src []byte) constraint.Expr {
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			return nil
		}
		if constraint.IsGoBuild(line) {
			expr, err := constraint.Parse(line)
			if err != nil {
				return nil
			}
			return expr
		}
	}
	return nil
}

// inDefaultBuild reports whether a file with this source is compiled by a plain
// `go test ./...` (no -tags): a constraint is evaluated with every tag false except
// the host platform's.
func inDefaultBuild(src []byte) bool {
	expr := buildConstraint(src)
	if expr == nil {
		return true
	}
	return expr.Eval(func(tag string) bool {
		switch tag {
		case runtime.GOOS, runtime.GOARCH, "gc":
			return true
		case "unix":
			return runtime.GOOS == "darwin" || runtime.GOOS == "linux"
		}
		return false
	})
}

// requiresIntegrationTag reports whether the file's constraint names the
// `integration` tag.
func requiresIntegrationTag(src []byte) bool {
	expr := buildConstraint(src)
	return expr != nil && strings.Contains(expr.String(), "integration")
}

// evaluateUnitTierPurity walks root and returns one violation per integration-tier
// construct found in a test file that a plain `go test ./...` compiles, plus the
// number of `integration`-tagged test files it saw (so a caller can prove the guard
// is not vacuous). A re-exec of the test binary is excused only in a file listed in
// reexecAllowed (path relative to root, slash-separated); a listed file that no longer
// re-executes is itself a violation, so the list cannot go stale.
func evaluateUnitTierPurity(root string, reexecAllowed map[string]string) (violations []string, integrationFiles int, err error) {
	used := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !inDefaultBuild(src) && requiresIntegrationTag(src) {
			integrationFiles++
		}
		if !inDefaultBuild(src) {
			return nil
		}
		reasons, err := integrationMarkers(path, src)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		for _, r := range reasons {
			if _, ok := reexecAllowed[filepath.ToSlash(rel)]; ok && r == reexecReason {
				used[filepath.ToSlash(rel)] = true
				continue
			}
			violations = append(violations, fmt.Sprintf(
				"%s: %s; a test like this MUST live in a file tagged //go:build integration (it runs through checks.<system>.pg-connector-integration-tests, not the unit tier)",
				filepath.ToSlash(rel), r,
			))
		}
		return nil
	})
	for p := range reexecAllowed {
		if !used[p] {
			violations = append(violations, fmt.Sprintf("%s: listed in unitReexecAllowed but no longer re-executes the test binary in a unit-tier file; remove the entry", p))
		}
	}
	sort.Strings(violations)
	return violations, integrationFiles, err
}

// TestUnitTierHasNoIntegrationTests fails when a test file compiled by a plain
// `go test ./...` starts the daemon, runs a built binary or Go tool, or needs a real
// external tool. It is the regression guard of the integration/unit split.
func TestUnitTierHasNoIntegrationTests(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	violations, integrationFiles, err := evaluateUnitTierPurity(root, unitReexecAllowed)
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	for _, v := range violations {
		t.Error(v)
	}
	if integrationFiles == 0 {
		t.Errorf("no test file under %s carries //go:build integration: either the integration tier is empty or this guard scans the wrong root", root)
	}
}

// TestUnitTierGuardRecognizesEachIntegrationShape proves the guard rejects every
// shape the criteria name (a negative control per rule) and accepts the unit-tier
// idioms, so the real-tree test above cannot pass vacuously.
func TestUnitTierGuardRecognizesEachIntegrationShape(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		reason string // substring of the expected reason; "" means clean
	}{
		{"daemon via NewDaemon", `d := servetest.NewDaemon(t, servetest.Options{}); _ = d`, "servetest.NewDaemon"},
		{"daemon via StartDaemon", `servetest.StartDaemon(t, servetest.Options{})`, "servetest.StartDaemon"},
		{"daemon binary build", `_ = servetest.Binary(t, "", "")`, "servetest.Binary"},
		{"go build", `exec.Command("go", "build", "-o", "x", ".")`, "exec of go"},
		{"go vet through a context", `exec.CommandContext(ctx, "go", "vet", "./...")`, "exec of go"},
		{"go through a variable, building", `exec.Command(goBin, "build", ".")`, "chosen at run time"},
		{"built binary in a variable", `exec.Command(bin, "--config", cfg)`, "chosen at run time"},
		{"built binary in a field", `exec.CommandContext(ctx, s.Path, "api")`, "chosen at run time"},
		{"real bd", `exec.Command("bd", "init")`, `"bd"`},
		{"real dolt", `exec.Command("dolt", "sql")`, `"dolt"`},
		{"real tmux", `exec.Command("tmux", "ls")`, `"tmux"`},
		{"real ccpool", `exec.Command("ccpool", "status")`, `"ccpool"`},
		{"git fixture is unit", `exec.Command("git", "init")`, ""},
		{"diff is unit", `exec.Command("diff", "-u", "a", "b")`, ""},
		{"sh fake backend is unit", `exec.Command("sh", "-c", "true")`, ""},
		{"helper process re-exec is not unit", `exec.Command(os.Args[0], "-test.run=^TestHelper$")`, "re-executes the test binary"},
		{"go list is metadata", `exec.Command(goBin, "list", "-deps", ".")`, ""},
		{"go env literal is metadata", `exec.Command("go", "env", "GOMOD")`, ""},
		{"a string mentioning the shapes is not a call", `_ = "exec.Command(\"go\", \"build\")"`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "package x\n\nfunc TestX(t *testing.T) {\n\t" + tc.body + "\n}\n"
			reasons, err := integrationMarkers("x_test.go", []byte(src))
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			switch {
			case tc.reason == "" && len(reasons) != 0:
				t.Errorf("unit-tier idiom flagged: %v", reasons)
			case tc.reason != "" && !strings.Contains(strings.Join(reasons, "\n"), tc.reason):
				t.Errorf("reasons %v do not contain %q", reasons, tc.reason)
			}
		})
	}
}

// TestUnitTierGuardHonoursBuildTags proves the walk exempts a file only when its
// constraint keeps it out of a plain `go test ./...`, against a throwaway module tree.
func TestUnitTierGuardHonoursBuildTags(t *testing.T) {
	const marker = "package x\n\nfunc TestX(t *testing.T) { exec.Command(\"go\", \"build\") }\n"
	write := func(root, rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	root := t.TempDir()
	write(root, "a/untagged_test.go", marker)
	write(root, "a/integration_test.go", "//go:build integration\n\n"+marker)
	write(root, "a/contract_test.go", "//go:build contract\n\n"+marker)
	write(root, "a/either_test.go", "//go:build integration || contract\n\n"+marker)
	write(root, "a/negated_test.go", "//go:build !integration\n\n"+marker)
	write(root, "a/testdata/fixture_test.go", marker)

	violations, integrationFiles, err := evaluateUnitTierPurity(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, v := range violations {
		files = append(files, strings.SplitN(v, ":", 2)[0])
	}
	want := []string{"a/negated_test.go", "a/untagged_test.go"}
	if strings.Join(files, ",") != strings.Join(want, ",") {
		t.Errorf("flagged files = %v, want exactly %v (tagged and testdata files are exempt)", files, want)
	}
	if integrationFiles != 2 {
		t.Errorf("integration-tagged files = %d, want 2 (integration_test.go, either_test.go)", integrationFiles)
	}
}

// TestUnitTierGuardReexecAllowlist proves a re-exec of the test binary is a violation
// unless its file is listed by path, that the list excuses nothing else, and that a
// listed file which no longer re-executes is reported (so the list cannot go stale).
func TestUnitTierGuardReexecAllowlist(t *testing.T) {
	const reexec = "package x\n\nfunc TestX(t *testing.T) { exec.Command(os.Args[0], \"-test.run=^TestHelper$\") }\n"
	const reexecAndGo = "package x\n\nfunc TestX(t *testing.T) { exec.Command(os.Args[0]); exec.Command(\"go\", \"build\") }\n"
	const clean = "package x\n\nfunc TestX(t *testing.T) {}\n"
	root := t.TempDir()
	for rel, content := range map[string]string{
		"a/reexec_test.go":      reexec,
		"a/listed_test.go":      reexec,
		"a/mixed_test.go":       reexecAndGo,
		"a/tagged_test.go":      "//go:build integration\n\n" + reexec,
		"a/cleaned_test.go":     clean,
		"a/other_clean_test.go": clean,
	} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	allowed := map[string]string{
		"a/listed_test.go":  "reason",
		"a/mixed_test.go":   "reason",
		"a/cleaned_test.go": "reason",
	}
	violations, _, err := evaluateUnitTierPurity(root, allowed)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(violations, "\n")
	for _, want := range []string{
		"a/reexec_test.go: " + reexecReason,
		"a/mixed_test.go: builds or runs a Go program",
		"a/cleaned_test.go: listed in unitReexecAllowed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("violations do not contain %q:\n%s", want, got)
		}
	}
	if len(violations) != 3 {
		t.Errorf("got %d violations, want exactly 3 (unlisted re-exec, listed file's other violation, stale entry):\n%s", len(violations), got)
	}
}
