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

// allowlistedIdentifiers is the mechanical guard for bead pg2-tphcc.
//
// This repo's CLAUDE.md forbids disclosing employer/personal identifiers in this
// public flake; pg2-wb9yb (commit 62a9c573) forward-scrubbed packages/pg-pr of
// the ones that had leaked in, and this guard exists so the scrub does not
// regress silently (it already had, once, within a single commit — see that
// commit's own message about internal/sync/broaden_test.go).
//
// # Why an ALLOWLIST, not a denylist
//
// The obvious design — a checked-in list of forbidden tokens (the employer
// name, colleague logins, Jira keys, ...) — was explicitly REJECTED by
// operator ruling (Phillip, 2026-08-24, recorded on pg2-tphcc and in this
// workspace's CLAUDE.md "Workspace Policies"): no such list, plaintext or
// hashed, may live in ANY repo. The ruling widened the goal too: public repos
// should carry no user identifier of any kind except the operator's own.
//
// Inverting the check to an ALLOWLIST satisfies both halves at once. The
// allowlist below is the operator's own already-public identity plus named
// public third-party bot/product accounts that legitimately appear in this
// module (see enrich.go, cirollup.go, sync/revision.go, config.go,
// prview.go's AxisPolicyBot) — none of that discloses anything sensitive, so
// it is safe to commit. Anything ELSE that looks like a login in a
// structured identity field is presumed guilty until added here, which is the
// point: adding a real colleague's login requires a deliberate, reviewable
// diff to this list, rather than a denylist that must be extended with a
// growing catalog of exactly the values it exists to keep out of the repo.
//
// "teammate" and "alice" are not people; they are this module's existing
// placeholder identities. "teammate" is not invented for this guard — it is
// the literal substitution pg2-wb9yb's scrub made, with a jq value-substitution
// program, for a real colleague's GitHub login AND (separately) for a real
// colleague's name embedded in a captured branch name, inside
// pkg/provider/vcs/github/testdata/enriched-prs-single.json. Reusing it here
// instead of inventing a fresh placeholder avoids growing the allowlist for no
// reason. "alice" is internal/prview/testdata/pr-view-full.json's
// hand-authored placeholder author, predating any scrub, following the
// long-standing Alice/Bob naming convention for examples — never a captured
// real value.
var allowlistedIdentifiers = map[string]struct{}{
	// the operator's own identity — the one exception the ruling allows.
	"phillipgreenii":            {},
	"phillipg@ziprecruiter.com": {},
	// public third-party bot/product accounts (see the doc comment above for
	// where each is already named in this module's non-test source).
	"coderabbitai": {},
	"policy-bot":   {},
	"dependabot":   {},
	// generic placeholder test identities — see doc comment above.
	"teammate": {},
	"alice":    {},
}

func isAllowlistedIdentifier(id string) bool {
	_, ok := allowlistedIdentifiers[strings.ToLower(strings.TrimSpace(id))]
	return ok
}

// identityFieldPattern matches a flat JSON string value under one of the four
// identity-bearing key names the redesigned approach names: login, author,
// reviewer, user. This is deliberately a TEXT match, not a JSON parse — see
// the TestIdentifierAllowlistGuard doc comment for why a line-oriented scan is
// the established pattern in this repo. A nested `"author": {"login": "x"}`
// is still caught because `login` is itself one of the four keys and gets its
// own independent match; `"author": {` never matches this pattern on its own
// because the value must start with a `"` immediately after the colon.
var identityFieldPattern = regexp.MustCompile(`"(?:login|author|reviewer|user)"\s*:\s*"([^"]*)"`)

// branchLoginPattern matches a two-part dotted login immediately followed by
// a TICKET-NNN-shaped component — the shape a ZipRecruiter branch name takes
// (`firstname.lastname.TICKET-1234.description`; see this repo's user-level
// CLAUDE.md git-workflow section) and the exact shape pg2-wb9yb's scrub found
// and removed from a real captured branch name
// (`constantin.segarceanu.FINDEV-9345.add-dry-run-table`, again commit
// 62a9c573). Its own scrubbed replacement — a single word before the ticket,
// `teammate.PROJ-1234.add-dry-run-table` — does NOT match: only the two-part
// name.name shape does, because a lone word there is not identity-shaped. The
// captured group is the two-part `word.word` login, not the ticket.
var branchLoginPattern = regexp.MustCompile(`\b([a-z][a-z0-9-]*\.[a-z][a-z0-9-]*)\.[A-Z][A-Z0-9]*-[0-9]+\b`)

// gitTrailerPattern matches a `git log`-style Author:/Committer: trailer. No
// fixture under packages/pg-pr currently captures one — this axis is proven
// only by TestIdentifierAllowlistGuardCatchesDeliberateRegression's synthetic
// fixture, not by anything in the live tree.
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
// has a "testdata" path component. Go's own toolchain treats "testdata" as a
// reserved, special-purpose directory (go build/go vet skip it), and it is
// exactly where a captured-API-response fixture like the one pg2-wb9yb
// scrubbed lives — see TestIdentifierAllowlistGuard's doc comment for why
// this, not "every file in the module," is the guarded scope.
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

// pgPrModuleRoot walks up from the test's working directory to the directory
// holding go.mod — mirrors ccpoolModuleRoot in
// packages/ccpool/cmd/ccpool/spec_citations_test.go — so the guard covers the
// whole packages/pg-pr module regardless of which package under it runs the
// test.
func pgPrModuleRoot(t *testing.T) string {
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

// TestIdentifierAllowlistGuard is the mechanical guard for bead pg2-tphcc:
// packages/pg-pr's testdata/ fixtures may not carry a username/login/handle
// shaped token in a structured identity field unless it is on
// allowlistedIdentifiers.
//
// # GUARDED SCOPE — and why it is testdata/, not the whole module
//
// This test scans only files under a testdata/ directory anywhere beneath the
// module root, not every file in packages/pg-pr. Every _test.go file in this
// module hand-types throwaway logins ("alice", "bob", "zara", "me",
// "claude[bot]", "github-actions", dozens of them across
// pkg/provider/vcs/github/github_test.go and enrich_test.go alone) directly
// in Go string literals. Those are reviewed the same way any other line of
// test code is reviewed, and they are NOT how the actual regression happened
// — scanning them would force the allowlist to grow by one entry every time
// any test author invents a new example name, which is exactly the
// ever-growing list the operator's ruling rejects, without buying real
// protection.
//
// The regression this guard targets is a CAPTURED artifact: pg2-wb9yb's scrub
// (commit 62a9c573) found pkg/provider/vcs/github/testdata/enriched-prs-single.json
// to be a real GitHub GraphQL response pasted in wholesale from a real PR,
// carrying a real colleague's GitHub login and a real colleague's name inside
// a real branch name. testdata/ is exactly where a captured-response fixture
// like that lives — Go's own toolchain treats it specially (go build/go vet
// skip it) — which is why it, not the whole module, is the guarded scope.
//
// # RATCHET, NOT JUDGEMENT
//
// This guard currently covers only packages/pg-pr's testdata/ fixtures. Other
// directories (docs/superpowers/, packages/pa-monitor*,
// packages/claude-extended-tool-approver, docs/adr/, home/programs/,
// packages/pr-pool) are NOT yet scrubbed and are NOT guarded — adding them
// here before they are scrubbed would just make this test permanently red.
// Widen the scope by adding to this test's walk (or, if the widened directory
// sits outside this Go module and is unreachable from a test that walks up to
// go.mod — e.g. home/programs/pg-pr, claude-marketplace/pg-pr — by adding a
// companion nix check mirroring checks.<system>.test-ccpool-surface-spec-citations
// in flake.nix, the same structural split
// packages/ccpool/cmd/ccpool/spec_citations_test.go documents) only AFTER the
// target directory has been scrubbed (tracked: pg2-dssp6, pg2-n3gez,
// pg2-k23s6). This is a ratchet: it only ever widens, never narrows, and the
// next widening is someone else's bead, not a judgement call made here.
//
// # KNOWN LIMITATION
//
// This guard cannot and does not catch a real name embedded in free-text
// PROSE with no structural marker identifying it as a person's name — a
// colleague's name inside a markdown doc paragraph or a code comment, for
// example. It only recognizes the three STRUCTURED shapes named in the
// redesigned approach: a JSON login/author/reviewer/user key's value, a
// dotted `name.name.TICKET-NNN` branch-name component, and a git log
// Author:/Committer: trailer. Free-text prose review remains a manual
// responsibility (see pg2-dssp6, pg2-n3gez for the current remediation's
// manual pass) — this mechanical guard is not sufficient for that class and
// must not be treated as though it were.
func TestIdentifierAllowlistGuard(t *testing.T) {
	root := pgPrModuleRoot(t)

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
	wantFixtures := []string{
		filepath.Join("pkg", "provider", "vcs", "github", "testdata", "enriched-prs-single.json"),
		filepath.Join("internal", "prview", "testdata", "pr-view-full.json"),
		filepath.Join("internal", "prview", "testdata", "pr-view-empty.json"),
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
// nothing under packages/pg-pr's real testdata/ is ever touched.
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

// TestIdentifierAllowlistGuardAllowsKnownSafeIdentifiers proves the operator's
// own identity, every known public bot/product name, and this module's
// existing generic placeholders all pass — none of them should ever force
// someone to touch allowlistedIdentifiers.
func TestIdentifierAllowlistGuardAllowsKnownSafeIdentifiers(t *testing.T) {
	dir := t.TempDir()
	testdataDir := filepath.Join(dir, "testdata")
	if err := os.MkdirAll(testdataDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", testdataDir, err)
	}

	content := `{
  "operator1": {"author": {"login": "phillipgreenii"}},
  "operator2": {"reviewer": "phillipg@ziprecruiter.com"},
  "bot1": {"user": {"login": "coderabbitai"}},
  "bot2": {"user": {"login": "policy-bot"}},
  "bot3": {"user": {"login": "dependabot"}},
  "placeholder1": {"author": "teammate"},
  "placeholder2": {"author": "alice"},
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
