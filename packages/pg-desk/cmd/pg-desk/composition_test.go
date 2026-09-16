package main

// composition_test.go is the chokepoint test enforcing this docket's D10
// composition rule (docs/behavior/pg-desk/README.md "Composition rule";
// docket pg2-2j5ac.32 design's "Purpose and placement"): packages/pg-desk
// execs no binary other than pg-connector and the operator-configured
// browser opener — never gh, bd, pjira, or any other tool, directly or
// transitively.
//
// Shape mirrors packages/pg-connector/cmd/pg-connector/chokepoint_test.go's
// TestGHExecChokePoint (module-wide walk to the nearest go.mod, plus a
// choke-point-file allowlist), generalized from "ban one named binary" to
// "allow only one named binary literal (pg-connector) anywhere in the
// module; every other literal exec.Command/exec.CommandContext binary name
// is a violation." The configured browser (open.chrome_bin) is exempt by
// construction rather than by name: it is never a Go string literal in
// this module — always a variable read from config — so a call site that
// execs it (execLiteralRE only matches a literal first argument) is
// invisible to this regex regardless of which browser is configured. No
// exec.Command call of any kind exists in this packet yet (every
// subcommand is a stub — see stub.go), so this test currently passes
// vacuously; it exists now so the very first real exec call, in a later
// packet, is checked by something from day one.
import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// execLiteralRE matches an exec.Command/exec.CommandContext call whose
// FIRST argument is a Go string literal — i.e. a hardcoded binary name —
// and captures that literal. A call whose first argument is a variable or
// field expression (e.g. cfg.Open.ChromeBin) does not match: the pattern
// requires the argument to start with `"` immediately after the opening
// paren (optionally preceded by a context argument).
var execLiteralRE = regexp.MustCompile(`exec\.Command(?:Context)?\(\s*(?:[\w.]+\s*,\s*)?"([^"]+)"`)

// allowedLiteralBinaries is the ONLY literal binary name this module may
// exec directly, per D10. Anything else — gh, bd, pjira, a hardcoded
// browser name, or any other tool — is a violation.
var allowedLiteralBinaries = map[string]bool{
	"pg-connector": true,
}

// scanForCompositionViolations walks root and returns every line
// (relative-path:line: text) whose exec.Command(Context)? call names a
// literal binary not on allowedLiteralBinaries, plus how many .go files
// were scanned. Factored out so both the real guard
// (TestCompositionChokePoint) and its self-test
// (TestCompositionChokePointCatchesDeliberateRegression) exercise the same
// logic rather than a parallel reimplementation of it.
func scanForCompositionViolations(root string) (violations []string, scannedFiles int, err error) {
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}
		scannedFiles++
		for i, line := range strings.Split(string(src), "\n") {
			for _, m := range execLiteralRE.FindAllStringSubmatch(line, -1) {
				bin := m[1]
				if !allowedLiteralBinaries[bin] {
					violations = append(violations, fmt.Sprintf("%s:%d: execs %q directly: %s",
						rel, i+1, bin, strings.TrimSpace(line)))
				}
			}
		}
		return nil
	})
	sort.Strings(violations)
	return violations, scannedFiles, walkErr
}

// moduleRoot walks up from the test's own working directory to the nearest
// go.mod. Mirrors packages/pg-connector/cmd/pg-connector/chokepoint_test.go's
// moduleRoot — same shape, independent copy per this module's own go.mod
// (see identifier_allowlist_test.go's package doc comment for why each Go
// module in this repo carries its own copy of this kind of guard).
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found above the test's working directory")
		}
		dir = parent
	}
}

// TestCompositionChokePoint is the real guard: packages/pg-desk's whole
// module may not exec any literal binary other than "pg-connector".
func TestCompositionChokePoint(t *testing.T) {
	root := moduleRoot(t)

	violations, scanned, err := scanForCompositionViolations(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}

	// Liveness self-check: prove the walk actually reached this package's
	// own .go files, so "found nothing" cannot double as proof the scan
	// never ran (this packet's stub subcommand files are a fixed, known
	// lower bound).
	const minExpectedFiles = 14 // one stub file per subcommand this packet adds
	if scanned < minExpectedFiles {
		t.Fatalf("guard scanned only %d .go file(s) under %s; expected at least %d — "+
			"the walk is not reaching the guarded scope", scanned, root, minExpectedFiles)
	}
	t.Logf("scanned %d .go file(s) under %s", scanned, root)

	if len(violations) > 0 {
		t.Fatalf("composition rule violated (D10): packages/pg-desk may exec only "+
			"pg-connector and the operator-configured browser (never gh, bd, pjira, or any "+
			"other literal binary), directly or transitively:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestCompositionChokePointCatchesDeliberateRegression proves the
// acceptance criterion "no non-pg-connector/browser exec anywhere in the
// package (verified by the chokepoint test, not just asserted)": a
// deliberately introduced literal exec of a forbidden binary is flagged,
// and removing it (or switching to the allowed pg-connector literal)
// clears the scan. Per this repo's unit-test isolation convention, the
// scenario is generated in a temp directory — nothing under
// packages/pg-desk's real source is ever touched.
func TestCompositionChokePointCatchesDeliberateRegression(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	target := filepath.Join(dir, "offender.go")

	bad := "package fixture\n\nimport \"os/exec\"\n\nfunc f() {\n\texec.Command(\"bd\", \"list\")\n}\n"
	if err := os.WriteFile(target, []byte(bad), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	violations, scanned, err := scanForCompositionViolations(dir)
	if err != nil {
		t.Fatalf("scanning %s: %v", dir, err)
	}
	if scanned != 1 {
		t.Fatalf("expected to scan exactly 1 file, scanned %d", scanned)
	}
	if len(violations) != 1 {
		t.Fatalf("expected exactly 1 violation, got %d: %v", len(violations), violations)
	}
	if !strings.Contains(violations[0], `"bd"`) {
		t.Fatalf("expected a violation naming %q, got: %v", "bd", violations)
	}
	t.Logf("caught (as expected): %v", violations)

	// Switching to the allowed literal must make the scan clean again.
	clean := "package fixture\n\nimport \"os/exec\"\n\nfunc f() {\n\texec.Command(\"pg-connector\", \"list\")\n}\n"
	if err := os.WriteFile(target, []byte(clean), 0o644); err != nil {
		t.Fatalf("rewrite fixture: %v", err)
	}
	violations, _, err = scanForCompositionViolations(dir)
	if err != nil {
		t.Fatalf("re-scanning %s: %v", dir, err)
	}
	if len(violations) != 0 {
		t.Fatalf("expected zero violations after switching to the allowed literal, got: %v", violations)
	}
}

// TestCompositionChokePointAllowsConfigDrivenBrowserExec proves the
// composition rule's other half: an exec.Command call whose binary comes
// from a variable (the shape a config-driven browser-open call takes,
// e.g. exec.Command(cfg.Open.ChromeBin, url)) is never flagged, because it
// is not a literal.
func TestCompositionChokePointAllowsConfigDrivenBrowserExec(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	content := "package fixture\n\nimport \"os/exec\"\n\n" +
		"func openBrowser(bin, url string) error {\n" +
		"\treturn exec.Command(bin, url).Run()\n" +
		"}\n"
	if err := os.WriteFile(filepath.Join(dir, "open.go"), []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	violations, scanned, err := scanForCompositionViolations(dir)
	if err != nil {
		t.Fatalf("scanning %s: %v", dir, err)
	}
	if scanned != 1 {
		t.Fatalf("expected to scan exactly 1 file, scanned %d", scanned)
	}
	if len(violations) != 0 {
		t.Fatalf("a config-driven (non-literal) exec.Command binary must never be flagged, got: %v", violations)
	}
}
