package classify

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// classifyImportPath is this package's import path, derived from the module
// path in go.mod so the test survives a module rename.
func classifyImportPath(t *testing.T, moduleRoot string) string {
	t.Helper()
	mod, err := os.ReadFile(filepath.Join(moduleRoot, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for _, line := range strings.Split(string(mod), "\n") {
		if path, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(path) + "/internal/classify"
		}
	}
	t.Fatal("go.mod has no module line")
	return ""
}

// productionImporters lists, as module-relative slash paths, every non-test Go
// file outside this package that imports it.
func productionImporters(t *testing.T, moduleRoot, importPath string) []string {
	t.Helper()
	var found []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "internal/classify" || d.Name() == "testdata" || d.Name() == "vendor" ||
				(rel != "." && strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			if p, err := strconv.Unquote(imp.Path.Value); err == nil && p == importPath {
				found = append(found, rel)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	slices.Sort(found)
	return found
}

// TestClassifyImporterSetIsPinned pins which non-test files import
// internal/classify. The daemon design ("Change kinds") records the decision to
// KEEP the classifier because focus, interpret, pipeline and link use it beyond
// the PR change flow. When a deletion (for example of internal/changes and
// doctor_changeflow.go with the change flow) shrinks the list, or a new caller
// grows it, update the list here in the same change, so retiring the
// classifier stays a conscious step.
func TestClassifyImporterSetIsPinned(t *testing.T) {
	moduleRoot := filepath.Join("..", "..")
	importPath := classifyImportPath(t, moduleRoot)

	// Sorted, as productionImporters returns them. doctor_changeflow.go and
	// the internal/changes files leave with the PR change flow deletion; the rest
	// keep the classifier alive.
	want := []string{
		"cmd/pg-desk/doctor_changeflow.go",
		"cmd/pg-desk/link.go",
		"internal/changes/orphans.go",
		"internal/changes/poll.go",
		"internal/focus/candidates.go",
		"internal/focus/health.go",
		"internal/focus/live.go",
		"internal/focus/rank.go",
		"internal/focus/slot.go",
		"internal/interpret/issue_attention_facts.go",
		"internal/pipeline/entity_change.go",
		"internal/pipeline/feed_row.go",
	}
	got := productionImporters(t, moduleRoot, importPath)
	if !slices.Equal(got, want) {
		t.Errorf("importers of internal/classify changed; update this pin and the daemon design's \"Change kinds\" section deliberately\n got: %q\nwant: %q", got, want)
	}

	// The decision to keep the classifier rests on callers outside the PR
	// change flow; losing them all makes the package a retirement candidate.
	var live int
	for _, f := range got {
		if !strings.HasPrefix(f, "internal/changes/") && f != "cmd/pg-desk/doctor_changeflow.go" {
			live++
		}
	}
	if live == 0 {
		t.Error("internal/classify has no importer outside the PR change flow (internal/changes and doctor_changeflow.go); revisit the keep decision in the daemon design")
	}
}
