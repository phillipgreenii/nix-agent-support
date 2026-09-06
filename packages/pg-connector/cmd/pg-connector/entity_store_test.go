package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// entityKindTokens is the fixed set of entity-kind names this design
// currently has (pkg/schema's PR/Issue/CIRun/WorktreeInfo/BranchInfo —
// design §2's entity model). "worktree" and "branch" are both scm-capability
// entities [design: §4.7]; "cirun"/"ci" both mean the ci capability's run
// entity — either spelling is accepted so a field/type named either
// "CIRuns" or "Runs" (of a "ciRun"-ish value type) is recognized.
var entityKindTokens = map[string]string{
	"pr":       "pr",
	"issue":    "issue",
	"ci":       "ci",
	"cirun":    "ci",
	"scm":      "scm",
	"worktree": "scm",
	"branch":   "scm",
}

// entityKindOf heuristically maps a Go identifier (a struct field name, or
// a map value type's identifier) to one of entityKindTokens, by lower-
// casing, stripping a trailing plural "s" and a common
// state/info/data/result suffix, and looking up the remainder. It returns
// ("", false) when the identifier does not name a recognized entity kind.
//
// This is deliberately a NAME heuristic, not a type-identity check (no
// go/types resolution against pkg/schema's actual declarations): design
// §8's acceptance criterion — "no package ... defines a persistent store
// keyed by more than one entity type's IDs together" — has no single
// canonical marker in the language (no interface, no annotation) for "this
// map field represents one entity kind's keyed collection," so a full
// type-checker pass would still bottom out in a name-based judgment call
// at the value-type level. Accepted scope limitation, matching how sibling
// design gaps were handled: documented here rather than skipped silently.
func entityKindOf(ident string) (string, bool) {
	lower := strings.ToLower(ident)
	lower = strings.TrimSuffix(lower, "state")
	lower = strings.TrimSuffix(lower, "info")
	lower = strings.TrimSuffix(lower, "data")
	lower = strings.TrimSuffix(lower, "result")
	lower = strings.TrimSuffix(lower, "s")
	if kind, ok := entityKindTokens[lower]; ok {
		return kind, true
	}
	return "", false
}

// hasJSONTag reports whether tag (an ast.BasicLit's raw string-literal
// Value, backticks included) carries a `json:` struct tag — the signal
// this module's one existing persistent store (cmd/pg-connector-pr-github/
// internal/store.go's storeFile) actually uses for its own on-disk shape.
// Requiring this tag is a deliberate proxy for "this field is meant to be
// serialized to a persistent file," distinguishing an actually-persisted
// store field from an incidental in-memory map that merely happens to be
// named after an entity kind.
func hasJSONTag(tag *ast.BasicLit) bool {
	if tag == nil {
		return false
	}
	return strings.Contains(tag.Value, "json:")
}

// mapValueTypeName returns the trailing identifier of a map value type
// expression — e.g. "prState" for map[string]prState, "prState" for
// map[string]*prState, "PR" for map[string]schema.PR — or "" if it cannot
// be resolved to a simple named type.
func mapValueTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return mapValueTypeName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	default:
		return ""
	}
}

// evaluateEntityStoreIsolation parses every .go file directly in dir (no
// recursion — dir is expected to be one package outside any backend's own
// internal/ tree) and returns one violation string per struct type that
// defines two or more JSON-tagged map fields whose field name or map-value
// type name resolve (via entityKindOf) to two or more DISTINCT entity
// kinds — the mechanical form of design §8's "no package ... defines a
// persistent store keyed by more than one entity type's IDs together."
func evaluateEntityStoreIsolation(dir string) ([]string, error) {
	entries, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var violations []string
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		rel := filepath.Base(path)
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				kinds := map[string][]string{} // kind -> field names carrying it
				for _, f := range st.Fields.List {
					mt, ok := f.Type.(*ast.MapType)
					if !ok {
						continue
					}
					if !hasJSONTag(f.Tag) {
						continue
					}
					var fieldName string
					if len(f.Names) > 0 {
						fieldName = f.Names[0].Name
					}
					if kind, ok := entityKindOf(fieldName); ok {
						kinds[kind] = append(kinds[kind], fieldName)
						continue
					}
					if kind, ok := entityKindOf(mapValueTypeName(mt.Value)); ok {
						kinds[kind] = append(kinds[kind], fieldName)
					}
				}
				if len(kinds) >= 2 {
					var parts []string
					for kind, fields := range kinds {
						parts = append(parts, fmt.Sprintf("%s (%s)", kind, strings.Join(fields, ",")))
					}
					violations = append(violations, fmt.Sprintf(
						"%s: type %q is a persistent store combining %d distinct entity kinds in one struct — %s — split into one store per entity type [design: §8]",
						rel, ts.Name.Name, len(kinds), strings.Join(parts, "; "),
					))
				}
			}
		}
	}
	return violations, nil
}

// entityStoreScanDirs returns every directory this module's shared surface
// (outside any single backend's own internal/) — pkg/schema, pkg/provider
// and its subpackages, pkg/scriptout and its subpackages, plus every
// cmd/<binary>/ TOP-LEVEL directory itself (not recursed into internal/,
// which design §8's own acceptance criterion explicitly exempts: "outside
// a single backend's own internal/").
func entityStoreScanDirs(moduleRoot string) ([]string, error) {
	var dirs []string
	seen := map[string]bool{}
	add := func(dir string) {
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}

	err := filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case rel == ".":
			return nil
		case rel == ".git" || d.Name() == "testdata" || d.Name() == "vendor":
			return fs.SkipDir
		case rel == "pkg" || strings.HasPrefix(rel, "pkg/"):
			// pkg/schema, pkg/provider(+subpackages), pkg/scriptout
			// (+subpackages) are all shared surface — descend and add
			// every directory that actually holds .go files.
			if hasGoFiles(path) {
				add(rel)
			}
			return nil
		case rel == "cmd":
			return nil
		case strings.HasPrefix(rel, "cmd/"):
			segments := strings.Split(rel, "/")
			if len(segments) == 2 {
				// cmd/<binary> itself: the binary's own top-level
				// package. In scope (design §8 exempts only "a single
				// backend's own internal/", not its top-level package).
				add(rel)
			}
			// Do not descend into a backend's own subdirectories at
			// all: internal/ is exempted by design, and check 1
			// (evaluateLayoutConvention) already forbids any
			// non-internal subdirectory from existing in the first
			// place, so there is nothing else under cmd/<binary>/ for
			// this check to reach.
			return fs.SkipDir
		default:
			return nil
		}
	})
	if err != nil {
		return nil, err
	}
	return dirs, nil
}

func hasGoFiles(dir string) bool {
	matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
	return err == nil && len(matches) > 0
}

// TestNoCrossConnectorEntityStore is design §8's cross-entity-store check:
// no package outside a single backend's own internal/ defines a persistent
// store keyed by more than one entity type's IDs together.
func TestNoCrossConnectorEntityStore(t *testing.T) {
	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	dirs, err := entityStoreScanDirs(moduleRoot)
	if err != nil {
		t.Fatalf("entityStoreScanDirs: %v", err)
	}
	if len(dirs) == 0 {
		t.Fatal("entityStoreScanDirs found no directories to scan — module layout has drifted")
	}
	for _, dir := range dirs {
		violations, err := evaluateEntityStoreIsolation(filepath.Join(moduleRoot, dir))
		if err != nil {
			t.Fatalf("evaluateEntityStoreIsolation(%s): %v", dir, err)
		}
		for _, v := range violations {
			t.Errorf("%s/%s", dir, v)
		}
	}
}

// TestNoCrossConnectorEntityStore_DetectsCombinedStore is a test-of-a-test:
// design §8 has nothing to reject in the current, already-compliant tree
// (its one real persistent store, cmd/pg-connector-pr-github/internal/
// store.go's storeFile, is correctly scoped to PR ids only AND lives inside
// a backend's own internal/, so it is out of this check's scope for two
// independent reasons). This proves evaluateEntityStoreIsolation actually
// rejects a struct combining two entity kinds' JSON-tagged map fields,
// modeled directly on that real storeFile's own shape plus a second,
// synthetic issue-keyed map field. Written to a temp directory — never the
// real tree.
func TestNoCrossConnectorEntityStore_DetectsCombinedStore(t *testing.T) {
	dir := t.TempDir()
	src := `package internal

type prState struct {
	Category string ` + "`json:\"category,omitempty\"`" + `
}

type issueState struct {
	Status string ` + "`json:\"status,omitempty\"`" + `
}

// storeFile is a deliberately non-compliant stand-in: a single persistent
// store keyed by BOTH pr and issue entity ids together.
type storeFile struct {
	PRs    map[string]prState    ` + "`json:\"prs\"`" + `
	Issues map[string]issueState ` + "`json:\"issues\"`" + `
}
`
	writeCompositionFixture(t, dir, "store.go", src)

	violations, err := evaluateEntityStoreIsolation(dir)
	if err != nil {
		t.Fatalf("evaluateEntityStoreIsolation: %v", err)
	}
	if len(violations) == 0 {
		t.Fatal("evaluateEntityStoreIsolation did not flag storeFile combining pr and issue kinds")
	}
	found := false
	for _, v := range violations {
		if strings.Contains(v, "storeFile") && strings.Contains(v, "pr") && strings.Contains(v, "issue") {
			found = true
		}
	}
	if !found {
		t.Fatalf("evaluateEntityStoreIsolation's violations did not identify storeFile/pr/issue specifically: %v", violations)
	}
}

// TestNoCrossConnectorEntityStore_AllowsSingleKindStore guards the
// opposite failure mode: a store keyed by exactly ONE entity kind (modeled
// on the real storeFile) must NOT be flagged, matching the real tree's own
// compliant store shape.
func TestNoCrossConnectorEntityStore_AllowsSingleKindStore(t *testing.T) {
	dir := t.TempDir()
	src := `package internal

type prState struct {
	Category string ` + "`json:\"category,omitempty\"`" + `
}

type storeFile struct {
	PRs map[string]prState ` + "`json:\"prs\"`" + `
}
`
	writeCompositionFixture(t, dir, "store.go", src)

	violations, err := evaluateEntityStoreIsolation(dir)
	if err != nil {
		t.Fatalf("evaluateEntityStoreIsolation: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("evaluateEntityStoreIsolation flagged a single-entity-kind store: %v", violations)
	}
}
