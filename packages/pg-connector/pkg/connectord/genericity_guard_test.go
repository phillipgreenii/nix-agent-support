package connectord

// The genericity guard of the shared connector-daemon library (docket
// pg2-959y2, packet H0). It turns "the shared library stays generic" into
// three mechanical checks over every .go file under pkg/connectord/ (tests
// included, any directory named testdata excluded), parsed with go/parser:
//
//	G1  a known-word ratchet: identifiers, string literals and comments are
//	    split into tokens, and a token on the closed forbidden list may occur
//	    in a file only as often as the committed baseline allows. A count above
//	    the baseline fails; a count below it fails with "lower the baseline to
//	    N"; a file or token with no entry has a baseline of zero (INV-HOIST-2).
//	G2  no string-literal comparison in the generic packages (engine, feed,
//	    feedop, query, groupkit, groupsvc, changemetrics; non-test files): an
//	    ==, !=, case or strings.HasPrefix/HasSuffix/Contains against a string
//	    literal, and a string literal in a const or var initialiser, a map key
//	    or a composite literal, fails unless the literal is on the committed
//	    protocol list (INV-HOIST-3, INV-HOIST-24).
//	G3  the import boundary: no file imports a path containing /cmd/ or
//	    /internal/, and none declares package main (INV-HOIST-18).
//
// The baseline is testdata/genericity_allowlist.json. Its entries are
// (file, token, max) with file relative to pkg/connectord/. The guard function
// is evaluateGenericity; the committed tree is checked by
// TestGenericityGuardPassesOnTheCommittedTree.
//
// REPORT MODE. The counts in the baseline are never typed by hand. After an
// intended change of a count (a packet that lowers it, or one that edits a
// baselined file), regenerate the file from the tree with
//
//	PG_CONNECTORD_GENERICITY_REPORT=1 go test ./pkg/connectord -run TestGenericityGuardReport
//
// run from the module root, and commit the result. Report mode keeps the class
// and why of an entry that already exists, writes a class and a why for an
// entry it creates, and drops entries whose count has fallen to zero. A second
// run changes nothing (TestGenericityReportReproducesTheCommittedBaseline).
//
// THE GUARD'S OWN FILE. This file is inside the scanned tree, and its word
// lists and unit-test fixtures contain the forbidden words, so it is baselined
// through report mode like any other file; it has entries in the baseline of
// class legacy. Editing this file changes its counts: re-run report mode.

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

const (
	allowlistPath = "testdata/genericity_allowlist.json"
	reportEnv     = "PG_CONNECTORD_GENERICITY_REPORT"
)

// Allowlist mirrors the JSON of the baseline file.
type Allowlist struct {
	Version  int          `json:"version"`
	Baseline []AllowEntry `json:"baseline"`
}

// AllowEntry allows Max occurrences of Token in File (relative to the root).
type AllowEntry struct {
	File  string `json:"file"`
	Token string `json:"token"`
	Max   int    `json:"max"`
	Class string `json:"class"`
	Why   string `json:"why"`
}

// The classes an entry carries.
const (
	classOpaqueColumn = "opaque-column"
	classLegacy       = "legacy"
)

// Class A (identifiers and string literals) and class B (comments): the closed
// lists of known words. A whole-token word matches a token of the split text; a
// substring word matches inside the lower-cased text; a phrase matches inside
// the lower-cased, whitespace-normalised text. The words left out on purpose
// (ordinary database or Go senses) are not listed.
var (
	wholeWordsA = []string{
		"pr", "prs", "ci", "gh", "bd", "jira", "pjira", "jql", "bead", "beads", "issue", "issues", "epic",
		"sprint", "owner", "repo", "repos", "draft", "review", "reviews", "workflow", "sha", "mergeable",
		"rollup", "graphql",
	}
	substringWordsA = []string{"github", "graphql", "pjira", "pullrequest", "pull_request", "mergeable"}
	wholeWordsB     = []string{
		"pr", "prs", "ci", "gh", "bd", "jira", "pjira", "jql", "bead", "beads", "issue", "issues", "epic",
		"sprint", "owner", "repo",
	}
	substringWordsB = []string{"github", "graphql", "pjira"}
	phraseWords     = []string{"pull request"}
)

// genericPackages are the directories (under the root) whose non-test files rule
// G2 covers.
var genericPackages = []string{"engine", "feed", "feedop", "query", "groupkit", "groupsvc", "changemetrics"}

// protocolList is the committed list of literals the generic packages may
// compare to or declare: the membership field prefix, the origin words, the
// result words, the wire change words, the label placeholders, the JSON word
// null and the empty string.
var protocolList = map[string]bool{
	"query:":  true,
	"query":   true,
	"refresh": true,
	"write":   true,
	"read":    true,
	"settled": true,
	"failed":  true,
	"timeout": true,
	"added":   true,
	"removed": true,
	"changed": true,
	"other":   true,
	"unknown": true,
	"null":    true,
	"":        true,
}

// splitTokens splits s into lower-cased tokens on non-alphanumeric characters
// and on camel-case and acronym boundaries. Digits do not split: sha256 is one
// token.
func splitTokens(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if len(cur) > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			// An acronym's plural s (PRs, IDs) stays with the acronym.
			if nextLower && runes[i+1] == 's' && (i+2 >= len(runes) || !unicode.IsLower(runes[i+2])) {
				nextLower = false
			}
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

// countWords counts, per word of the given lists, its occurrences in text: the
// larger of the whole-token count, the substring count and the phrase count, so
// one occurrence matched two ways is counted once.
func countWords(text string, whole, substring, phrases []string) map[string]int {
	counts := map[string]int{}
	tokens := splitTokens(text)
	lower := strings.ToLower(text)
	normalised := strings.Join(strings.Fields(lower), " ")
	for _, w := range whole {
		n := 0
		for _, tok := range tokens {
			if tok == w {
				n++
			}
		}
		if n > counts[w] {
			counts[w] = n
		}
	}
	for _, w := range substring {
		if n := strings.Count(lower, w); n > counts[w] {
			counts[w] = n
		}
	}
	for _, w := range phrases {
		if n := strings.Count(normalised, w); n > counts[w] {
			counts[w] = n
		}
	}
	return counts
}

// fileScan is what one file contributes: its per-token counts and its G2 and
// G3 violations.
type fileScan struct {
	counts map[string]int
	g2     []string
	g3     []string
}

func (s *fileScan) add(counts map[string]int) {
	for w, n := range counts {
		if n > 0 {
			s.counts[w] += n
		}
	}
}

func isDirective(comment string) bool {
	return strings.HasPrefix(comment, "//go:") || strings.HasPrefix(comment, "// +build") || strings.HasPrefix(comment, "//+build")
}

func stringValue(lit *ast.BasicLit) string {
	if v, err := strconv.Unquote(lit.Value); err == nil {
		return v
	}
	return lit.Value
}

func isStringLit(e ast.Expr) (*ast.BasicLit, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return nil, false
	}
	return lit, true
}

func scanFile(fset *token.FileSet, f *ast.File, rel string) *fileScan {
	scan := &fileScan{counts: map[string]int{}}

	// G3: package main and the import boundary.
	if f.Name.Name == "main" {
		scan.g3 = append(scan.g3, fmt.Sprintf("%s: package main under the shared tree (G3, INV-HOIST-18)", rel))
	}
	skipLits := map[*ast.BasicLit]bool{}
	for _, imp := range f.Imports {
		skipLits[imp.Path] = true
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			p = imp.Path.Value
		}
		if strings.Contains(p, "/cmd/") || strings.Contains(p, "/internal/") ||
			strings.HasPrefix(p, "cmd/") || strings.HasPrefix(p, "internal/") ||
			strings.HasSuffix(p, "/cmd") || strings.HasSuffix(p, "/internal") {
			scan.g3 = append(scan.g3, fmt.Sprintf("%s: imports %q, a cmd or internal path (G3, INV-HOIST-18)", rel, p))
		}
	}

	// G1: identifiers, string literals and comments.
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			scan.add(countWords(x.Name, wholeWordsA, substringWordsA, phraseWords))
		case *ast.BasicLit:
			if x.Kind == token.STRING && !skipLits[x] {
				scan.add(countWords(stringValue(x), wholeWordsA, substringWordsA, phraseWords))
			}
		}
		return true
	})
	for _, group := range f.Comments {
		for _, c := range group.List {
			if isDirective(c.Text) {
				continue
			}
			scan.add(countWords(c.Text, wholeWordsB, substringWordsB, phraseWords))
		}
	}

	// G2: only the non-test files of the generic packages.
	if isGenericFile(rel) {
		scan.g2 = g2Violations(fset, f, rel)
	}
	return scan
}

func isGenericFile(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return false
	}
	top, _, found := strings.Cut(rel, "/")
	if !found {
		return false
	}
	for _, p := range genericPackages {
		if top == p {
			return true
		}
	}
	return false
}

// g2Violations reports every string literal of the file in a compared or
// declared position that is not on the protocol list.
func g2Violations(fset *token.FileSet, f *ast.File, rel string) []string {
	var out []string
	seen := map[token.Pos]bool{}
	check := func(e ast.Expr, what string) {
		lit, ok := isStringLit(e)
		if !ok || seen[lit.Pos()] {
			return
		}
		seen[lit.Pos()] = true
		if protocolList[stringValue(lit)] {
			return
		}
		out = append(out, fmt.Sprintf("%s:%d: %s with string literal %s outside the protocol list (G2, INV-HOIST-24)",
			rel, fset.Position(lit.Pos()).Line, what, lit.Value))
	}
	checkAll := func(root ast.Node, what string) {
		ast.Inspect(root, func(n ast.Node) bool {
			if e, ok := n.(*ast.BasicLit); ok {
				check(e, what)
			}
			return true
		})
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BinaryExpr:
			if x.Op == token.EQL || x.Op == token.NEQ {
				check(x.X, "comparison")
				check(x.Y, "comparison")
			}
		case *ast.CaseClause:
			for _, e := range x.List {
				check(e, "case")
			}
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "strings" {
					switch sel.Sel.Name {
					case "HasPrefix", "HasSuffix", "Contains":
						for _, a := range x.Args {
							check(a, "strings."+sel.Sel.Name)
						}
					}
				}
			}
		case *ast.GenDecl:
			if x.Tok == token.CONST || x.Tok == token.VAR {
				for _, spec := range x.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, v := range vs.Values {
							checkAll(v, "declaration")
						}
					}
				}
			}
		case *ast.CompositeLit:
			for _, e := range x.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					check(kv.Key, "composite literal key")
					check(kv.Value, "composite literal")
				} else {
					check(e, "composite literal")
				}
			}
		}
		return true
	})
	return out
}

// scanTree parses every .go file under root (testdata directories skipped) and
// returns the scans keyed by the slash-separated path relative to root.
func scanTree(root string) (map[string]*fileScan, error) {
	scans := map[string]*fileScan{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}
		scans[rel] = scanFile(fset, f, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return scans, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// evaluateGenericity runs rules G1 to G3 over the tree at root against the
// baseline and returns one violation string per problem.
func evaluateGenericity(root string, allow Allowlist) ([]string, error) {
	scans, err := scanTree(root)
	if err != nil {
		return nil, err
	}
	var out []string

	// G1.
	type key struct{ file, token string }
	maxOf := map[key]int{}
	entryDirs := map[string]bool{}
	for _, e := range allow.Baseline {
		k := key{e.File, e.Token}
		if _, dup := maxOf[k]; dup {
			out = append(out, fmt.Sprintf("baseline: duplicate entry for %s token %q", e.File, e.Token))
		}
		maxOf[k] = e.Max
		if top, _, found := strings.Cut(e.File, "/"); found {
			entryDirs[top] = true
		}
	}
	for _, file := range sortedKeys(scans) {
		counts := scans[file].counts
		for _, tok := range sortedKeys(counts) {
			n, max := counts[tok], maxOf[key{file, tok}]
			if n <= max {
				continue
			}
			msg := fmt.Sprintf("%s: token %q occurs %d times, baseline allows %d (G1, INV-HOIST-2)", file, tok, n, max)
			if top, _, found := strings.Cut(file, "/"); found && !entryDirs[top] {
				msg += fmt.Sprintf("; no baseline entry for `%s/`", top)
			}
			out = append(out, msg)
		}
	}
	for _, e := range allow.Baseline {
		n := 0
		if s, ok := scans[e.File]; ok {
			n = s.counts[e.Token]
		}
		if n < e.Max {
			out = append(out, fmt.Sprintf("%s: token %q occurs %d times, baseline says %d: lower the baseline to %d (G1, INV-HOIST-2)", e.File, e.Token, n, e.Max, n))
		}
	}

	// G2 and G3.
	for _, file := range sortedKeys(scans) {
		out = append(out, scans[file].g2...)
		out = append(out, scans[file].g3...)
	}
	return out, nil
}

// classOf picks the class report mode writes for an entry it creates: a
// non-test file of the store naming the shipped sha and mergeable columns or
// their Go fields is opaque-column, anything else is legacy.
func classOf(file, tok string) (class, why string) {
	isTest := strings.HasSuffix(file, "_test.go")
	if strings.HasPrefix(file, "store/") && !isTest && (tok == "sha" || tok == "mergeable") {
		return classOpaqueColumn, "shipped column or Go field name (head_sha, base_sha, last_known_mergeable); permanent until a later rename rung"
	}
	if isTest {
		return classLegacy, "fixture or word list of an existing test file"
	}
	return classLegacy, "pre-existing code that the hoist does not neutralise"
}

// buildAllowlist is report mode: the baseline that matches the tree at root. An
// entry of prior keeps its class and why; a new entry gets both from classOf;
// a token with a count of zero gets no entry.
func buildAllowlist(root string, prior Allowlist) (Allowlist, error) {
	scans, err := scanTree(root)
	if err != nil {
		return Allowlist{}, err
	}
	type key struct{ file, token string }
	old := map[key]AllowEntry{}
	for _, e := range prior.Baseline {
		old[key{e.File, e.Token}] = e
	}
	out := Allowlist{Version: 1, Baseline: []AllowEntry{}}
	for _, file := range sortedKeys(scans) {
		for _, tok := range sortedKeys(scans[file].counts) {
			n := scans[file].counts[tok]
			if n == 0 {
				continue
			}
			e := AllowEntry{File: file, Token: tok, Max: n}
			if prev, ok := old[key{file, tok}]; ok {
				e.Class, e.Why = prev.Class, prev.Why
			} else {
				e.Class, e.Why = classOf(file, tok)
			}
			out.Baseline = append(out.Baseline, e)
		}
	}
	return out, nil
}

func readAllowlist(path string) (Allowlist, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Allowlist{}, err
	}
	var a Allowlist
	if err := json.Unmarshal(raw, &a); err != nil {
		return Allowlist{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return a, nil
}

func marshalAllowlist(a Allowlist) ([]byte, error) {
	raw, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// writeTree creates the files (slash-separated relative path to content) under
// a temp directory and returns it.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func violations(t *testing.T, files map[string]string, allow Allowlist) []string {
	t.Helper()
	got, err := evaluateGenericity(writeTree(t, files), allow)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func wantViolation(t *testing.T, got []string, parts ...string) {
	t.Helper()
	for _, v := range got {
		ok := true
		for _, p := range parts {
			if !strings.Contains(v, p) {
				ok = false
			}
		}
		if ok {
			return
		}
	}
	t.Errorf("violations %q: none contains all of %q", got, parts)
}

func wantClean(t *testing.T, got []string) {
	t.Helper()
	if len(got) != 0 {
		t.Errorf("violations = %q, want none", got)
	}
}

// The real tree.

func TestGenericityGuardPassesOnTheCommittedTree(t *testing.T) {
	allow, err := readAllowlist(allowlistPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := evaluateGenericity(".", allow)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("the committed tree breaks the genericity guard (re-run report mode only for an intended count change):\n%s", strings.Join(got, "\n"))
	}
}

func TestGenericityBaselineAndProtocolListAreCommitted(t *testing.T) {
	allow, err := readAllowlist(allowlistPath)
	if err != nil {
		t.Fatal(err)
	}
	if allow.Version != 1 || len(allow.Baseline) == 0 {
		t.Fatalf("baseline = version %d with %d entries, want version 1 and entries", allow.Version, len(allow.Baseline))
	}
	for _, e := range allow.Baseline {
		if e.File == "" || e.Token == "" || e.Max <= 0 || e.Why == "" || (e.Class != classLegacy && e.Class != classOpaqueColumn) {
			t.Errorf("malformed baseline entry %+v", e)
		}
	}
	for _, w := range []string{"query:", "query", "refresh", "write", "read", "settled", "failed", "timeout", "added", "removed", "changed", "other", "unknown", "null", ""} {
		if !protocolList[w] {
			t.Errorf("protocol list lacks %q", w)
		}
	}
}

// TestGenericityReportReproducesTheCommittedBaseline is the "a second run of
// report mode changes nothing" check: the committed file is the fixed point of
// report mode over the current tree.
func TestGenericityReportReproducesTheCommittedBaseline(t *testing.T) {
	committed, err := readAllowlist(allowlistPath)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := buildAllowlist(".", committed)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rebuilt, committed) {
		want, _ := marshalAllowlist(rebuilt)
		t.Errorf("report mode would change the baseline: re-run it (see the header comment), it would write:\n%s", want)
	}
}

// TestGenericityGuardReport is report mode: with the trigger environment
// variable set it rewrites the baseline file from the tree; without it the test
// skips.
func TestGenericityGuardReport(t *testing.T) {
	if os.Getenv(reportEnv) == "" {
		t.Skipf("report mode runs only with %s=1", reportEnv)
	}
	prior, err := readAllowlist(allowlistPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	got, err := buildAllowlist(".", prior)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := marshalAllowlist(got)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(allowlistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(allowlistPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Unit tests of the guard function over synthetic trees.

func TestSplitTokens(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"PRSchemaVersion", []string{"pr", "schema", "version"}},
		{"ci_changed", []string{"ci", "changed"}},
		{"sha256", []string{"sha256"}},
		{"HeadSHA", []string{"head", "sha"}},
		{"Priority", []string{"priority"}},
		{"head_sha base-sha", []string{"head", "sha", "base", "sha"}},
		{"sha256Sum", []string{"sha256", "sum"}},
		{"IDs", []string{"ids"}},
		{"PRs", []string{"prs"}},
		{"PRsFoo", []string{"prs", "foo"}},
		{"PRSet", []string{"pr", "set"}},
	}
	for _, c := range cases {
		if got := splitTokens(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitTokens(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGuardPassesACleanFile(t *testing.T) {
	wantClean(t, violations(t, map[string]string{
		"store/clean.go": "package store\n\n// Clean is a clean declaration.\nfunc Clean() string { return \"value\" }\n",
	}, Allowlist{}))
}

func TestGuardFlagsEveryWholeWordAsIdentifierStringAndComment(t *testing.T) {
	for _, w := range wholeWordsA {
		got := violations(t, map[string]string{"store/a.go": "package store\n\nvar " + w + " int\n"}, Allowlist{})
		wantViolation(t, got, "store/a.go", `token "`+w+`"`)
		got = violations(t, map[string]string{"store/a.go": "package store\n\nvar V = \"x " + w + " y\"\n"}, Allowlist{})
		wantViolation(t, got, "store/a.go", `token "`+w+`"`)
	}
	for _, w := range wholeWordsB {
		got := violations(t, map[string]string{"store/a.go": "package store\n\n// about " + w + " here\nvar V int\n"}, Allowlist{})
		wantViolation(t, got, "store/a.go", `token "`+w+`"`)
	}
	// Class B has fewer words than class A: a class A only word is fine in a comment.
	wantClean(t, violations(t, map[string]string{"store/a.go": "package store\n\n// the workflow of a sha\nvar V int\n"}, Allowlist{}))
}

func TestGuardFlagsSubstringWordsAndThePhrase(t *testing.T) {
	cases := map[string]string{
		"identifier github":   "package store\n\nvar githubClient int\n",
		"identifier pullreq":  "package store\n\nvar PullRequest int\n",
		"string pull_request": "package store\n\nvar V = \"pull_request\"\n",
		"string phrase":       "package store\n\nvar V = \"a Pull Request\"\n",
		"comment phrase":      "package store\n\n// the pull\n// request\n/* a pull   request */\nvar V int\n",
		"comment graphql":     "package store\n\n// a graphql call\nvar V int\n",
		"identifier pjira":    "package store\n\nvar viaPjira int\n",
	}
	for name, src := range cases {
		if got := violations(t, map[string]string{"store/a.go": src}, Allowlist{}); len(got) == 0 {
			t.Errorf("%s: no violation", name)
		}
	}
}

func TestGuardPassesTheFalsePositiveWords(t *testing.T) {
	for _, w := range []string{"process", "prune", "Priority", "circuit", "sha256", "checks", "ticket", "head", "base", "commit", "branch", "merge"} {
		src := "package store\n\n// " + w + " in a comment\nvar " + w + "Value = \"" + w + "\"\n"
		wantClean(t, violations(t, map[string]string{"store/a.go": src}, Allowlist{}))
	}
}

func TestGuardPassesAnImportPathContainingGithubCom(t *testing.T) {
	src := "package store\n\nimport _ \"github.com/example/lib\"\n"
	wantClean(t, violations(t, map[string]string{"store/a.go": src}, Allowlist{}))
}

func TestGuardSkipsTestdataAndDirectives(t *testing.T) {
	got := violations(t, map[string]string{
		"store/testdata/fixture.go": "package fixture\n\nvar pr int\n",
		"store/a.go":                "//go:build linux\n\npackage store\n\n//go:generate echo ci\nvar V int\n",
	}, Allowlist{})
	wantClean(t, got)
}

func TestGuardRatchetFailsAboveAndBelowTheBaseline(t *testing.T) {
	files := map[string]string{"store/a.go": "package store\n\nvar a, b = \"sha\", \"sha\"\n"}
	allow := func(max int) Allowlist {
		return Allowlist{Version: 1, Baseline: []AllowEntry{{File: "store/a.go", Token: "sha", Max: max, Class: classLegacy, Why: "x"}}}
	}
	wantClean(t, violations(t, files, allow(2)))
	wantViolation(t, violations(t, files, allow(1)), "store/a.go", `"sha"`, "occurs 2 times", "allows 1")
	wantViolation(t, violations(t, files, allow(3)), "store/a.go", `"sha"`, "lower the baseline to 2")
	wantViolation(t, violations(t, files, Allowlist{}), "store/a.go", `"sha"`, "allows 0")
}

func TestGuardBaselineEntryForAMissingFileMustBeLowered(t *testing.T) {
	allow := Allowlist{Version: 1, Baseline: []AllowEntry{{File: "store/gone.go", Token: "sha", Max: 1}}}
	wantViolation(t, violations(t, map[string]string{"store/a.go": "package store\n"}, allow), "store/gone.go", "lower the baseline to 0")
}

func TestGuardFlagsDuplicateBaselineEntries(t *testing.T) {
	e := AllowEntry{File: "store/a.go", Token: "sha", Max: 1}
	got := violations(t, map[string]string{"store/a.go": "package store\n\nvar V = \"sha\"\n"}, Allowlist{Baseline: []AllowEntry{e, e}})
	wantViolation(t, got, "duplicate entry")
}

func TestGuardNewFileUnderEngineHasNoBaselineEntry(t *testing.T) {
	files := map[string]string{"engine/e.go": "package engine\n\nvar owner int\n"}
	got := violations(t, files, Allowlist{Version: 1, Baseline: []AllowEntry{{File: "store/a.go", Token: "sha", Max: 0}}})
	wantViolation(t, got, "engine/e.go", `"owner"`, "no baseline entry for `engine/`")
	// A file of a directory that has other entries gets the plain message.
	files = map[string]string{"store/b.go": "package store\n\nvar owner int\n"}
	got = violations(t, files, Allowlist{Version: 1, Baseline: []AllowEntry{{File: "store/a.go", Token: "sha", Max: 0}}})
	wantViolation(t, got, "store/b.go", `"owner"`, "allows 0")
}

func TestGuardImportBoundary(t *testing.T) {
	for _, p := range []string{"example.test/mod/cmd/tool", "example.test/mod/cmd/tool/internal/x", "example.test/mod/internal/x", "example.test/mod/pkg/internal"} {
		got := violations(t, map[string]string{"store/a.go": "package store\n\nimport _ \"" + p + "\"\n"}, Allowlist{})
		wantViolation(t, got, "store/a.go", p, "G3")
	}
	wantViolation(t, violations(t, map[string]string{"store/main.go": "package main\n\nfunc main() {}\n"}, Allowlist{}), "store/main.go", "package main")
	wantClean(t, violations(t, map[string]string{"store/a.go": "package store\n\nimport _ \"example.test/mod/pkg/connectord/store\"\n"}, Allowlist{}))
}

func TestGuardLiteralComparisonRuleG2(t *testing.T) {
	bad := map[string]string{
		"equality":        "package engine\n\nfunc F(k string) bool { return k == \"example-kind\" }\n",
		"inequality":      "package feed\n\nfunc F(k string) bool { return \"example-kind\" != k }\n",
		"case":            "package query\n\nfunc F(k string) int {\n\tswitch k {\n\tcase \"example-kind\":\n\t\treturn 1\n\t}\n\treturn 0\n}\n",
		"prefix":          "package feedop\n\nimport \"strings\"\n\nfunc F(k string) bool { return strings.HasPrefix(k, \"example-\") }\n",
		"suffix":          "package groupkit\n\nimport \"strings\"\n\nfunc F(k string) bool { return strings.HasSuffix(k, \"-example\") }\n",
		"contains":        "package groupsvc\n\nimport \"strings\"\n\nfunc F(k string) bool { return strings.Contains(k, \"example\") }\n",
		"const":           "package changemetrics\n\nconst Kind = \"example-kind\"\n",
		"var":             "package engine\n\nvar Kind = \"example-kind\"\n",
		"map key":         "package engine\n\nvar M = map[string]int{\"example-kind\": 1}\n",
		"composite value": "package engine\n\nvar S = []string{\"example-kind\"}\n",
	}
	for name, src := range bad {
		dir := strings.TrimPrefix(strings.SplitN(src, "\n", 2)[0], "package ")
		got := violations(t, map[string]string{dir + "/x.go": src}, Allowlist{})
		wantViolation(t, got, dir+"/x.go", "G2", "example")
		if len(got) == 0 {
			t.Errorf("%s: no violation", name)
		}
	}
	good := "package engine\n\nimport \"strings\"\n\nconst Origin = \"refresh\"\n\nfunc F(k, raw string) bool {\n\tswitch k {\n\tcase \"added\", \"removed\", \"changed\":\n\t\treturn true\n\t}\n\treturn k == \"\" || raw == \"null\" || strings.HasPrefix(k, \"query:\") || k == \"other\" || k == \"unknown\"\n}\n"
	wantClean(t, violations(t, map[string]string{"engine/x.go": good}, Allowlist{}))
	// Not covered: a test file of a generic package, and a package outside the list.
	wantClean(t, violations(t, map[string]string{
		"engine/x_test.go": "package engine\n\nvar Kind = \"example-kind\"\n",
		"store/x.go":       "package store\n\nvar Kind = \"example-kind\"\n",
	}, Allowlist{}))
}

// TestGenericityGuardNamesTheRealFileAndToken pins the message shape the
// docket's packets rely on: the file, then the token.
func TestGenericityGuardNamesTheRealFileAndToken(t *testing.T) {
	got := violations(t, map[string]string{"engine/e.go": "package engine\n\nvar jira int\n"}, Allowlist{})
	if len(got) != 1 || !strings.HasPrefix(got[0], "engine/e.go: token \"jira\"") {
		t.Errorf("violations = %q, want one starting with the file and the token", got)
	}
}
