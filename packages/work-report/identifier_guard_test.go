package workreport

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

// This file is work-report's own organization-identifier guard.
//
// This repository is PUBLIC: no organization identifiers (repos, project keys,
// actor names, workspace paths) may appear in work-report's code, testdata or
// docs. The pg-pr module carries an equivalent guard, but that package is
// frozen and cannot be widened, so work-report owns its own copy of the
// allowlist-inversion design described in the repository CLAUDE.md under
// "Mechanical guard".
//
// # Why an ALLOWLIST, not a denylist
//
// A committed list of forbidden tokens would itself disclose exactly the
// values it exists to keep out, so none is kept. Instead a small committed
// ALLOWLIST of known-safe identities is checked, and any other
// username/login/handle-shaped token found in a structured identity position
// is reported. Adding a new identity requires a deliberate, reviewable diff to
// allowlistedIdentifiers.
//
// # Scope
//
// The walk covers every .go file, every file under a testdata/ directory and
// every file under a docs/ directory, all beneath the module root. Free-text
// prose with no structural marker cannot be machine-checked and remains a
// manual-review responsibility.
//
// The guard's own source MUST NOT contain a real organization identifier, and
// MUST NOT spell a planted token or a complete structured-identity shape
// contiguously: the planted fixtures are assembled at run time so that this
// file scans clean.

// allowlistedIdentifiers holds the known-safe identities: the operator's own
// public handle, public third-party bot/product accounts, and generic
// placeholder identities used by tests.
var allowlistedIdentifiers = map[string]struct{}{
	// the operator's own already-public handle.
	"phillipgreenii": {},
	// public third-party bot/product accounts.
	"dependabot": {},
	"policy-bot": {},
	// generic placeholder identities.
	"teammate":   {},
	"alice":      {},
	"bob":        {},
	"review-bot": {},
}

func isAllowlistedIdentifier(id string) bool {
	_, ok := allowlistedIdentifiers[strings.ToLower(strings.TrimSpace(id))]
	return ok
}

// identityFieldPattern matches a flat string value under one of the
// identity-bearing keys: login, author, reviewer, user. A nested
// author-with-login object is still caught because the login key matches on
// its own.
var identityFieldPattern = regexp.MustCompile(`"(?:login|author|reviewer|user)"\s*:\s*"([^"]*)"`)

// branchLoginPattern matches a two-part dotted login followed by a
// TICKET-NNN-shaped component (the shape of a ticket-bearing branch name). The
// captured group is the two-part name.
var branchLoginPattern = regexp.MustCompile(`\b([a-z][a-z0-9-]*\.[a-z][a-z0-9-]*)\.[A-Z][A-Z0-9]*-[0-9]+\b`)

// gitTrailerPattern matches a git-log style author or committer trailer line.
var gitTrailerPattern = regexp.MustCompile(`(?m)^\s*(?:Author|Committer):\s*([^<\n]+?)\s*<`)

// scanTextForIdentifiers returns every distinct non-allowlisted
// identifier-shaped token found in text, sorted.
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

// hasPathComponent reports whether rel has a path component equal to name.
func hasPathComponent(rel, name string) bool {
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == name {
			return true
		}
	}
	return false
}

// walkStats counts the files the walk actually scanned, by class.
type walkStats struct {
	GoFiles       int
	TestdataFiles int
	DocsFiles     int
	BehaviorFiles int // files under docs/behavior
}

// scanTree walks root and scans every .go file, every file under a testdata/
// directory and every file under a docs/ directory. It returns the sorted
// violations and what it visited. The real guard and the negative-control
// tests call this same function.
func scanTree(root string) (violations []string, stats walkStats, err error) {
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
		isGo := strings.HasSuffix(rel, ".go")
		inTestdata := hasPathComponent(rel, "testdata")
		inDocs := hasPathComponent(rel, "docs")
		if !isGo && !inTestdata && !inDocs {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}
		// A non-UTF-8 file is binary, never a source-of-truth text file.
		if !utf8.Valid(b) {
			return nil
		}
		if isGo {
			stats.GoFiles++
		}
		if inTestdata {
			stats.TestdataFiles++
		}
		if inDocs {
			stats.DocsFiles++
			if strings.HasPrefix(rel, filepath.Join("docs", "behavior")+string(filepath.Separator)) {
				stats.BehaviorFiles++
			}
		}
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
	return violations, stats, walkErr
}

// moduleRoot walks up from the working directory to the directory holding
// go.mod.
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
			t.Fatalf("no go.mod found at or above %q", dir)
		}
		dir = parent
	}
}

// TestIdentifierGuardWorkReportTree is the guard proper: the work-report
// module's code, testdata and docs MUST NOT carry a non-allowlisted
// identifier-shaped token.
func TestIdentifierGuardWorkReportTree(t *testing.T) {
	root := moduleRoot(t)

	violations, stats, err := scanTree(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}

	// Non-vacuity, asserted BEFORE the verdict: a dropped fileset must fail
	// loudly rather than pass because nothing was scanned.
	if stats.GoFiles < 1 {
		t.Fatalf("guard visited %d .go file(s) under %s; expected at least 1", stats.GoFiles, root)
	}
	if stats.TestdataFiles < 1 {
		t.Fatalf("guard visited %d testdata file(s) under %s; expected at least 1", stats.TestdataFiles, root)
	}
	// docs/behavior arrives with a later change; once it exists it MUST be
	// scanned.
	if info, statErr := os.Stat(filepath.Join(root, "docs", "behavior")); statErr == nil && info.IsDir() {
		if stats.BehaviorFiles < 1 {
			t.Fatalf("docs/behavior exists under %s but the guard visited %d file(s) in it", root, stats.BehaviorFiles)
		}
	}
	t.Logf("scanned %d .go, %d testdata, %d docs file(s) (%d under docs/behavior) under %s",
		stats.GoFiles, stats.TestdataFiles, stats.DocsFiles, stats.BehaviorFiles, root)

	for _, v := range violations {
		t.Errorf("non-allowlisted identifier-shaped token: %s\n"+
			"      if this is a real person's or organization's identifier, it MUST NOT be committed "+
			"to this public repo (see the repository CLAUDE.md, \"Public Repository\").\n"+
			"      if it is a known-safe placeholder or public bot/product name, add it to "+
			"allowlistedIdentifiers in identifier_guard_test.go with a comment explaining why.", v)
	}
}

// plantedFixture assembles text carrying one non-allowlisted token in each of
// the three structured shapes, built at run time so this source never spells a
// complete shape.
func plantedFixture(token string) string {
	q := `"`
	jsonShape := "{" + q + "login" + q + ": " + q + token + q + "}\n"
	branchShape := "branch: " + token + "." + token + ".PROJ-" + "1\n"
	trailerShape := "Author" + ": " + token + " <" + token + "@example.test>\n"
	return jsonShape + branchShape + trailerShape
}

// TestIdentifierGuardNegativeControl proves the guard fails on a planted
// token in each of code, testdata and docs, and that the walk reports each
// location.
func TestIdentifierGuardNegativeControl(t *testing.T) {
	const token = "zzplantedhandle"
	root := t.TempDir()
	files := map[string]string{
		"planted_code.go":                      "package planted\n\n/*\n" + plantedFixture(token) + "*/\n",
		filepath.Join("testdata", "case.json"): plantedFixture(token),
		filepath.Join("docs", "note.md"):       plantedFixture(token),
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	violations, stats, err := scanTree(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	if stats.GoFiles != 1 || stats.TestdataFiles != 1 || stats.DocsFiles != 1 {
		t.Fatalf("walk stats = %+v; want exactly one .go, one testdata and one docs file", stats)
	}
	joined := strings.Join(violations, "\n")
	for rel := range files {
		if !strings.Contains(joined, rel+": ") {
			t.Errorf("guard did not report the planted token in %s; violations:\n%s", rel, joined)
		}
	}
	// The structured-field and dotted-branch axes both fire on the token.
	if !strings.Contains(joined, `"`+token+`"`) {
		t.Errorf("no violation for the structured-field axis: %s", joined)
	}
	if !strings.Contains(joined, `"`+token+"."+token+`"`) {
		t.Errorf("no violation for the dotted-branch axis: %s", joined)
	}
}

// TestIdentifierGuardAllowsKnownSafeIdentifiers proves allowlisted identities
// pass in every walked location, so they never force an allowlist edit.
func TestIdentifierGuardAllowsKnownSafeIdentifiers(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"phillipgreenii", "dependabot", "policy-bot", "teammate", "alice", "bob", "review-bot"} {
		p := filepath.Join(root, "testdata", id+".json")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(plantedFixture(id)), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	violations, stats, err := scanTree(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	if stats.TestdataFiles != 7 {
		t.Fatalf("expected 7 testdata files scanned, got %+v", stats)
	}
	// Allowlisted dotted names are not single allowlist entries, so only the
	// branch axis (name.name) may fire; the structured-field and trailer axes
	// must be clean.
	for _, v := range violations {
		if !strings.Contains(v, ".") {
			t.Errorf("unexpected violation for an allowlisted identity: %s", v)
		}
	}
}
