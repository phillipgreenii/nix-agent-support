package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutputModeFor_DefaultIsJSON(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"pr", "show", "x"})
	// Parsing flags without executing RunE is enough to populate the
	// persistent --output flag's default.
	if err := root.ParseFlags([]string{}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	mode, err := outputModeFor(root)
	if err != nil {
		t.Fatalf("outputModeFor: %v", err)
	}
	if mode != OutputJSON {
		t.Fatalf("mode = %q, want %q", mode, OutputJSON)
	}
}

func TestOutputModeFor_ExplicitHuman(t *testing.T) {
	root := newRootCmd()
	if err := root.ParseFlags([]string{"--output", "human"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	mode, err := outputModeFor(root)
	if err != nil {
		t.Fatalf("outputModeFor: %v", err)
	}
	if mode != OutputHuman {
		t.Fatalf("mode = %q, want %q", mode, OutputHuman)
	}
}

func TestOutputModeFor_InvalidValueIsError(t *testing.T) {
	root := newRootCmd()
	if err := root.ParseFlags([]string{"--output", "yaml"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if _, err := outputModeFor(root); err == nil {
		t.Fatal("expected an error for an unrecognized --output value")
	}
}

func TestRun_InvalidOutputFlag_IsGenericFailure(t *testing.T) {
	// An unrecognized --output value is caught before ever dispatching to
	// a backend, so no config/backend is needed; it is the generic exit-1
	// CLI failure path, matching pr.go's own --disposition validation
	// convention.
	writeConfigFor(t, "backend-unused")

	_, code := executePr(t, []string{"--output", "yaml", "pr", "show", "pr-1"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

func TestRun_InvalidOutputFlag_WriteOp_NoSideEffect(t *testing.T) {
	// The regression this bead fixes [bug A4]: a typo'd --output on a
	// WRITE op (pr categorize mutates state) must be rejected with zero
	// side effects, not after the backend has already run. The fake
	// backend touches sideEffect on every invocation regardless of op —
	// if root's PersistentPreRunE did not catch the bad --output value
	// before Dispatch, this backend would run and the marker file would
	// exist.
	dir := t.TempDir()
	sideEffect := filepath.Join(dir, "backend-was-invoked")
	backendDir := t.TempDir()
	script := filepath.Join(backendDir, "backend-side-effecting")
	content := "#!/bin/sh\ncat >/dev/null\ntouch " + sideEffect + "\ncat <<'FAKE_BACKEND_EOF'\n{\"protocolVersion\":1,\"schemaVersion\":1,\"result\":{\"id\":\"pr-1\",\"category\":\"focus\"}}\nFAKE_BACKEND_EOF\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake backend: %v", err)
	}
	t.Setenv("PATH", backendDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeConfigFor(t, "backend-side-effecting")

	_, code := executePr(t, []string{"--output", "yaml", "pr", "categorize", "pr-1", "--category", "focus"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if _, err := os.Stat(sideEffect); !os.IsNotExist(err) {
		t.Fatalf("backend was invoked despite invalid --output (side-effect marker exists, stat err=%v)", err)
	}
}

func TestFormatSourcesTable_Empty(t *testing.T) {
	got := formatSourcesTable(nil)
	if !strings.Contains(got, "no backends registered") {
		t.Fatalf("formatSourcesTable(nil) = %q", got)
	}
}

func TestFormatSourcesTable_RendersEveryRow(t *testing.T) {
	got := formatSourcesTable([]SourceResult{
		{Source: "backend-a", Status: SourceSucceeded, Count: 3},
		{Source: "backend-b", Status: SourceDegraded, Reason: "bad token"},
	})
	if !strings.Contains(got, "backend-a: succeeded count=3") {
		t.Fatalf("formatSourcesTable = %q, missing succeeded row", got)
	}
	if !strings.Contains(got, "backend-b: degraded (bad token)") {
		t.Fatalf("formatSourcesTable = %q, missing degraded row with reason", got)
	}
}

// TestReportTargetedOutcomeDocCommentsSync guards the four near-identical
// report*TargetedOutcome wrappers (pr.go, issue.go, ci.go, scm.go) — each a
// one-line delegate to writeTargetedResult above, per its own doc comment
// ("the shared write path behind every verb group's own report*
// TargetedOutcome wrapper") — against drifting apart (bead pg2-sxfwd: found
// with issue.go's copy missing the [design: §4.5] tag, ci.go's carrying an
// extra aside, and scm.go's dropping the "or an ambiguous multi-backend
// registration" clause the other three keep). Each doc comment's own first
// word is its function's name (required Go doc convention), so that one
// word is stripped before comparing — the remaining text is otherwise
// compared verbatim, no hand-maintained golden text needed.
func TestReportTargetedOutcomeDocCommentsSync(t *testing.T) {
	wrapperFile := map[string]string{
		"reportPrTargetedOutcome":    "pr.go",
		"reportIssueTargetedOutcome": "issue.go",
		"reportCiTargetedOutcome":    "ci.go",
		"reportScmTargetedOutcome":   "scm.go",
	}

	docFor := func(funcName, file string) string {
		fset := token.NewFileSet()
		astFile, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range astFile.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name.Name != funcName {
				continue
			}
			if fd.Doc == nil {
				t.Fatalf("%s: %s has no doc comment", file, funcName)
			}
			text := fd.Doc.Text()
			if !strings.HasPrefix(text, funcName) {
				t.Fatalf("%s: %s's doc comment does not start with its own name, got %q", file, funcName, text)
			}
			return strings.TrimPrefix(text, funcName)
		}
		t.Fatalf("%s: could not find func %s", file, funcName)
		return ""
	}

	const canonicalFunc = "reportPrTargetedOutcome"
	want := docFor(canonicalFunc, wrapperFile[canonicalFunc])
	for funcName, file := range wrapperFile {
		if funcName == canonicalFunc {
			continue
		}
		if got := docFor(funcName, file); got != want {
			t.Errorf("%s's doc comment (%s) has drifted from %s's (%s):\n--- %s ---\n%s\n--- %s ---\n%s",
				funcName, file, canonicalFunc, wrapperFile[canonicalFunc],
				canonicalFunc, want, funcName, got)
		}
	}
}
