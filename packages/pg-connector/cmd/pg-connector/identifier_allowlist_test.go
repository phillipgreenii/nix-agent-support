package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// This file is pg-connector's OWN copy of the mechanical guard for bead
// pg2-tphcc, mirroring packages/pg-pr/cmd/pg-pr/identifier_allowlist_test.go.
//
// # Why a copy, not a shared widening — bead pg2-h3db9
//
// packages/pg-pr's guard (TestIdentifierAllowlistGuard there) roots itself at
// pgPrModuleRoot, which walks up from the test's working directory to the
// nearest go.mod. That walk is structurally bounded to packages/pg-pr's own
// module: packages/pg-connector has its OWN go.mod (a separate Go module —
// see checks.<system>.pg-connector-go-tests in flake.nix, whose src is
// rooted at ./packages/pg-connector, matching pg-pr-go-tests' own
// ./packages/pg-pr scoping), so pg-pr's test binary never sees this module's
// files at all, in or out of the nix sandbox. Generalizing pgPrModuleRoot to
// "scan multiple known module roots" would not fix this: the nix check that
// runs pg-pr-go-tests builds with only packages/pg-pr in its source tree, so
// packages/pg-connector's testdata is absent from that sandbox regardless of
// what root-scanning logic pg-pr's test binary contains.
//
// The fix mirrors the pattern packages/ccpool/cmd/ccpool/spec_citations_test.go
// already documents for a Go MODULE boundary (as opposed to a same-module
// surface split, which that file's sibling nix check handles instead): each
// Go module gets its own copy of the guard, rooted at its own module root.
// packages/pg-connector's cmd/pg-connector already hosts a module-wide guard
// in this same style — see TestBackendLayoutConvention in
// layout_convention_test.go — so this file lives alongside it.
//
// packages/pg-connector/cmd/pg-connector-pr-github/internal/github/testdata/
// carried fixtures ported over from packages/pg-pr's own testdata/ (bead
// pg2-2j5ac, the pg-pr → pg-connector migration), including the same
// pg2-wb9yb-scrubbed placeholders ("teammate", "review-bot") pg-pr's own
// allowlist documents. That fixture (enriched-prs-single.json) and its
// sibling native-stack-fields.json were deleted as dead surface (bead
// pg2-lh3c4: they backed this backend's own EnrichedPRsProvider
// implementation, which had no design-cited pg-connector consumer) — the
// two placeholders stay allowlisted regardless, since this file's own
// self-tests below (TestIdentifierAllowlistGuardCatchesDeliberateRegression,
// TestIdentifierAllowlistGuardAllowsKnownSafeIdentifiers) use them as this
// module's generic-placeholder convention independent of that one deleted
// fixture. This guard exists so that any future carryover, or anything
// pasted into this module's testdata/ afterward (the concrete near-miss:
// bead pg2-6hkl5's body carries a real ZR repo/PR pair one paste away from
// becoming a fixture here), is checked by something. Like pg-pr-go-tests,
// no separate flake.nix check attribute is needed: mkGoTest never sets
// subPackages, so checks.<system>.pg-connector-go-tests' `go test ./...`
// already exercises this file.
//
// # No checked-in denylist — see the operator ruling this inverts
//
// Same ruling as pg-pr's copy (Phillip, 2026-08-24, bead pg2-tphcc, recorded
// in this repo's CLAUDE.md "Public Repository — No ZipRecruiter Disclosure"):
// no denylist of forbidden tokens, plaintext or hashed, may live in any repo.
// This guard is the same ALLOWLIST inversion pg-pr's copy uses — see that
// file's doc comment for the full rationale (why an allowlist, why testdata/
// is the guarded scope, and the guard's known limitation against free-text
// prose). This file's allowlist below is deliberately NARROWER than pg-pr's:
// it carries only the identifiers pg-connector's own testdata actually uses
// today (confirmed 2026-09-06 by re-running this guard's own regexes against
// every file under packages/pg-connector/**/testdata/). pg-pr's bot/product
// placeholders that do not (yet) appear here — coderabbitai, policy-bot,
// dependabot, alice — are intentionally NOT pre-added; per the ruling's own
// logic, adding an entry before it is needed is exactly the kind of
// speculative list-growth an allowlist is meant to avoid forcing. Add one
// here, with a comment saying why it is safe, the same deliberate way pg-pr's
// list grew.
var allowlistedIdentifiers = map[string]struct{}{
	// the operator's own identity — the one exception the ruling allows.
	"phillipgreenii":            {},
	"phillipg@ziprecruiter.com": {},
	// generic placeholder test identities carried over from pg-pr's own
	// pg2-wb9yb scrub (see that file's doc comment). The production fixture
	// that originally motivated these two (enriched-prs-single.json) was
	// deleted as dead surface (bead pg2-lh3c4), but
	// TestIdentifierAllowlistGuardCatchesDeliberateRegression and
	// TestIdentifierAllowlistGuardAllowsKnownSafeIdentifiers below still
	// exercise them as this module's own generic-placeholder convention —
	// removing them here would just move the "regression" fixture value
	// those self-tests use, not eliminate a real dependency.
	"teammate":   {},
	"review-bot": {},
}

func isAllowlistedIdentifier(id string) bool {
	_, ok := allowlistedIdentifiers[strings.ToLower(strings.TrimSpace(id))]
	return ok
}

// identityFieldPattern, branchLoginPattern and gitTrailerPattern are the same
// three structured-shape detectors pg-pr's copy uses — see that file's doc
// comments for what each matches and why a text scan (not a JSON parse) is
// the established pattern.
var identityFieldPattern = regexp.MustCompile(`"(?:login|author|reviewer|user)"\s*:\s*"([^"]*)"`)

var branchLoginPattern = regexp.MustCompile(`\b([a-z][a-z0-9-]*\.[a-z][a-z0-9-]*)\.[A-Z][A-Z0-9]*-[0-9]+\b`)

var gitTrailerPattern = regexp.MustCompile(`(?m)^\s*(?:Author|Committer):\s*([^<\n]+?)\s*<`)

// scanTextForIdentifiers returns every distinct non-allowlisted
// identifier-shaped token the three patterns above find in text, in sorted
// order. It never inspects a checked-in list of BAD values — see the
// allowlistedIdentifiers doc comment.
func scanTextForIdentifiers(text string) []string {
	seen := map[string]struct{}{}
	add := func(raw string) {
		id := strings.TrimSpace(raw)
		if id == "" || isAllowlistedIdentifier(id) {
			return
		}
		seen[id] = struct{}{}
	}
	for _, m := range identityFieldPattern.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range branchLoginPattern.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range gitTrailerPattern.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	found := make([]string, 0, len(seen))
	for id := range seen {
		found = append(found, id)
	}
	sort.Strings(found)
	return found
}

// underTestdataDir reports whether rel (a path relative to some scan root)
// has a "testdata" path component — same guarded-scope rationale as pg-pr's
// copy (Go's own toolchain treats testdata specially, and it is exactly
// where a captured-API-response fixture lives).
func underTestdataDir(rel string) bool {
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "testdata" {
			return true
		}
	}
	return false
}

// scanTree walks root (ANY directory — a real module root or, in the
// self-tests below, a throwaway t.TempDir()) and returns every distinct
// non-allowlisted identifier-shaped token found in a file under a testdata/
// directory beneath root, plus how many files were scanned. Both the real
// guard (TestIdentifierAllowlistGuard) and its self-tests call this exact
// function, so the self-tests exercise the guard's real logic rather than a
// parallel reimplementation of it.
func scanTree(root string) (violations []string, scannedFiles int, err error) {
	seen := map[string]struct{}{}
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		if !underTestdataDir(rel) {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}
		// A non-UTF-8 file is binary, never a source-of-truth text fixture.
		if !utf8.Valid(b) {
			return nil
		}
		scannedFiles++
		for _, id := range scanTextForIdentifiers(string(b)) {
			key := rel + "\x00" + id
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			violations = append(violations, fmt.Sprintf("%s: %q", rel, id))
		}
		return nil
	})
	sort.Strings(violations)
	return violations, scannedFiles, walkErr
}

// pgConnectorModuleRoot walks up from the test's working directory to the
// directory holding go.mod — mirrors pgPrModuleRoot in
// packages/pg-pr/cmd/pg-pr/identifier_allowlist_test.go and ccpoolModuleRoot
// in packages/ccpool/cmd/ccpool/spec_citations_test.go — so the guard covers
// the whole packages/pg-connector module regardless of which package under
// it runs the test.
func pgConnectorModuleRoot(t *testing.T) string {
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
			t.Fatalf("no go.mod found at or above %q", dir)
		}
		dir = parent
	}
}

// TestIdentifierAllowlistGuard is pg-connector's copy of the mechanical guard
// for bead pg2-tphcc (see this file's package-level doc comment for why a
// copy, not a widening of pg-pr's own test, is the fix — bead pg2-h3db9):
// packages/pg-connector's testdata/ fixtures may not carry a username/login/
// handle-shaped token in a structured identity field unless it is on
// allowlistedIdentifiers.
//
// Guarded scope, ratchet status and known limitation are identical to
// pg-pr's own TestIdentifierAllowlistGuard — see that file's doc comment for
// the full rationale. This copy's walk reaches every testdata/ directory
// under packages/pg-connector, whichever backend or package it lives under
// (cmd/pg-connector-pr-github/internal/github/testdata/,
// cmd/pg-connector/testdata/, pkg/scriptout/conformance/testdata/, and any
// future one) with no code change needed as new ones appear — only
// re-verifying the allowlist still covers what is found.
func TestIdentifierAllowlistGuard(t *testing.T) {
	root := pgConnectorModuleRoot(t)

	violations, scanned, err := scanTree(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}

	// Liveness self-check, run FIRST: the expected violation count is zero,
	// so "found nothing" cannot double as proof the scan ran. Assert the
	// files this invariant exists for are really present and really counted
	// — a rename that moved one out from under the walk, or a future
	// narrowing of underTestdataDir, must FAIL loudly rather than silently
	// reduce coverage to nothing.
	// enriched-prs-single.json/native-stack-fields.json (this guard's
	// original canaries) were deleted as dead surface alongside
	// EnrichedPRsProvider (bead pg2-lh3c4) — these two go:embed'd
	// conformance goldens are real, durable fixtures under testdata/ that
	// take over the liveness-canary role.
	wantFixtures := []string{
		filepath.Join("pkg", "scriptout", "conformance", "testdata", "golden", "response-success.json"),
		filepath.Join("pkg", "scriptout", "conformance", "testdata", "golden", "error.json"),
	}
	for _, want := range wantFixtures {
		if _, statErr := os.Stat(filepath.Join(root, want)); statErr != nil {
			t.Fatalf("expected fixture %s not found under %s — it was renamed or moved "+
				"out from under this guard; update TestIdentifierAllowlistGuard", want, root)
		}
	}
	if scanned < len(wantFixtures) {
		t.Fatalf("guard scanned only %d testdata file(s) under %s; expected at least %d — "+
			"the walk is not reaching the guarded scope", scanned, root, len(wantFixtures))
	}
	t.Logf("scanned %d testdata file(s) under %s", scanned, root)

	for _, v := range violations {
		t.Errorf("non-allowlisted identifier-shaped token in a structured identity "+
			"field: %s\n"+
			"      if this is a real person's login/name, it must NOT be committed to "+
			"this public repo — see this repo's CLAUDE.md \"Public Repository — No "+
			"ZipRecruiter Disclosure\".\n"+
			"      if it is a known-safe placeholder or public bot/product name, add it "+
			"to allowlistedIdentifiers in identifier_allowlist_test.go with a comment "+
			"explaining why it is safe.", v)
	}
}

// TestIdentifierAllowlistGuardCatchesDeliberateRegression proves the
// acceptance criterion: a deliberately introduced non-allowlisted
// identifier-shaped token, in each of the three structured shapes the guard
// recognizes, is flagged red; removing it (restoring the allowlisted
// placeholder) turns the scan clean again. Per this repo's unit-test
// isolation convention, the scenario is generated in a temp directory —
// nothing under packages/pg-connector's real testdata/ is ever touched.
func TestIdentifierAllowlistGuardCatchesDeliberateRegression(t *testing.T) {
	dir := t.TempDir()
	testdataDir := filepath.Join(dir, "testdata")
	if err := os.MkdirAll(testdataDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", testdataDir, err)
	}
	fixture := filepath.Join(testdataDir, "captured.json")

	// Not strictly valid JSON (the trailing git-log block has real newlines
	// inside what would need to be a quoted string) — the scanner is a text
	// scan, not a JSON parser, so that's fine, and it's what lets the git
	// trailer regex's `(?m)^` anchor see a real line start.
	bad := `{
  "author": {"login": "areallyrealcolleague"},
  "headRefName": "another.colleague.PROJ-9999.fix"
}
commit abc123
Author: Some Colleague <some.colleague@example.test>
`
	if err := os.WriteFile(fixture, []byte(bad), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	violations, scanned, err := scanTree(dir)
	if err != nil {
		t.Fatalf("scanning %s: %v", dir, err)
	}
	if scanned != 1 {
		t.Fatalf("expected to scan exactly 1 file, scanned %d", scanned)
	}
	if len(violations) < 3 {
		t.Fatalf("expected all three detection axes (JSON identity field, dotted "+
			"branch-login, git trailer) to fire, got only %d violation(s): %v",
			len(violations), violations)
	}
	joined := strings.Join(violations, " | ")
	for _, want := range []string{"areallyrealcolleague", "another.colleague", "Some Colleague"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected a violation naming %q, got: %v", want, violations)
		}
	}
	t.Logf("caught (as expected): %v", violations)

	// Removing the regression — restoring the allowlisted placeholders this
	// module already uses — must make the scan clean again.
	clean := `{
  "author": {"login": "teammate"},
  "headRefName": "teammate.PROJ-9999.fix"
}
commit abc123
Author: teammate <teammate@example.test>
`
	if err := os.WriteFile(fixture, []byte(clean), 0o644); err != nil {
		t.Fatalf("rewrite fixture: %v", err)
	}
	violations, _, err = scanTree(dir)
	if err != nil {
		t.Fatalf("re-scanning %s: %v", dir, err)
	}
	if len(violations) != 0 {
		t.Fatalf("expected zero violations after removing the regression, got: %v", violations)
	}
}

// TestIdentifierAllowlistGuardAllowsKnownSafeIdentifiers proves the
// operator's own identity and this module's existing generic placeholders
// all pass — none of them should ever force someone to touch
// allowlistedIdentifiers.
func TestIdentifierAllowlistGuardAllowsKnownSafeIdentifiers(t *testing.T) {
	dir := t.TempDir()
	testdataDir := filepath.Join(dir, "testdata")
	if err := os.MkdirAll(testdataDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", testdataDir, err)
	}

	content := `{
  "operator1": {"author": {"login": "phillipgreenii"}},
  "operator2": {"reviewer": "phillipg@ziprecruiter.com"},
  "placeholder1": {"author": "teammate"},
  "placeholder2": {"user": {"login": "review-bot"}},
  "branch": "teammate.PROJ-42.fix"
}
commit def456
Author: phillipgreenii <phillipg@ziprecruiter.com>
`
	if err := os.WriteFile(filepath.Join(testdataDir, "safe.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	violations, scanned, err := scanTree(dir)
	if err != nil {
		t.Fatalf("scanning %s: %v", dir, err)
	}
	if scanned != 1 {
		t.Fatalf("expected to scan exactly 1 file, scanned %d", scanned)
	}
	if len(violations) != 0 {
		t.Fatalf("expected every known-safe identifier to pass, got violations: %v", violations)
	}
}

// TestIdentifierAllowlistGuardIgnoresZrNamingFamily proves the zr/ZR naming
// family used deliberately in bead-id prefixes, flake module names, and
// behavior-doc element ids is never flagged. None of these are values of a
// structured identity field or a dotted branch-login component, so the
// guard's structured-field-only scope already excludes them — this test
// pins that down explicitly rather than leaving it merely implied.
func TestIdentifierAllowlistGuardIgnoresZrNamingFamily(t *testing.T) {
	dir := t.TempDir()
	testdataDir := filepath.Join(dir, "testdata")
	if err := os.MkdirAll(testdataDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", testdataDir, err)
	}

	content := `{
  "module": "pg-pr-zr",
  "envVar": "ZR_TOKEN",
  "behaviorId": "INTF-ZR-014",
  "beadRef": "pg2-zr123",
  "note": "the zr/ZR naming family is a deliberate module/bead-id convention"
}
`
	if err := os.WriteFile(filepath.Join(testdataDir, "zr-family.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	violations, scanned, err := scanTree(dir)
	if err != nil {
		t.Fatalf("scanning %s: %v", dir, err)
	}
	if scanned != 1 {
		t.Fatalf("expected to scan exactly 1 file, scanned %d", scanned)
	}
	if len(violations) != 0 {
		t.Fatalf("the zr/ZR naming family must never be flagged, got violations: %v", violations)
	}
}
