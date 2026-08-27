package snapshot

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sortPrimitiveSelectors names the four sort/slices functions this guard
// tracks, keyed by "<package>.<func>" as they appear in a SelectorExpr
// (sort.Slice(x, ...), slices.SortFunc(x, ...), etc.).
var sortPrimitiveSelectors = map[string]bool{
	"sort.Slice":            true,
	"sort.SliceStable":      true,
	"slices.SortFunc":       true,
	"slices.SortStableFunc": true,
}

// noSecondSortAllowlist is every non-test .go file (module-root-relative
// path, forward slashes normalised via filepath.Join at build time) that
// legitimately calls one of the four primitives above, RE-GREPPED from
// source on 2026-08-27 for this bead (pg2-4dz88.7.2) -- not copied from the
// parent bead's own text, which explicitly warns its snapshot of this list
// may have drifted (it had: `internal/prdeps/native.go` carries two more call
// sites than that snapshot named).
//
// internal/sync/snapshotowner.go (sortedInputs) is the one entry that sorts a
// TeamRow/MineRow/PRInput-FAMILY type ([]snapshot.PRInput, by repo+number) --
// but it is upstream of Build, giving per-PR rebuilds a deterministic INPUT
// feed order, not the DISPLAY order (that is CompareTeamRows's job now), so
// it stays a legitimate, allowlisted site rather than a second consumer of
// the comparator's own ordering.
//
// Every other entry sorts a slice type CompareTeamRows has nothing to do with
// (bead IDs, provider entries, language stats, worktree records, PR-dependency
// diagnostics) and predates this bead entirely.
var noSecondSortAllowlist = map[string]bool{
	filepath.Join("internal", "sync", "snapshotowner.go"): true,
	filepath.Join("internal", "auth", "auth.go"):          true,
	filepath.Join("internal", "prdeps", "native.go"):      true,
	filepath.Join("internal", "prdeps", "prdeps.go"):      true,
	filepath.Join("internal", "worktree", "worktree.go"):  true,
	filepath.Join("internal", "enrich", "languages.go"):   true,
	filepath.Join("pkg", "beads", "mergerequest.go"):      true,
	filepath.Join("pkg", "beads", "processingcycle.go"):   true,
}

// noSecondSortOwnFile is the one file THIS bead adds a legitimate sort
// primitive call to: ordering.go's sortTeamRows, the sole place []TeamRow is
// ever sorted.
var noSecondSortOwnFile = filepath.Join("internal", "snapshot", "ordering.go")

// TestNoSecondSortOverRows is the mechanical guard against a future second
// consumer re-sorting []TeamRow / []MineRow / []snapshot.PRInput outside this
// package's own ordering.go (pg2-4dz88.7.2's acceptance criteria).
//
// # Guard shape chosen: coarse, type-blind (bead's option 2)
//
// The bead names two shapes: a go/types-scoped guard that proves an
// expression's element TYPE (via golang.org/x/tools/go/packages, today only a
// transitive dependency -- adopting it would add a new DIRECT dependency and
// a gomod2nix.toml regeneration for a test-only guard), or a coarser guard
// over the four sort-primitive NAMES, blind to what they sort, with a
// complete allowlist of every pre-existing unrelated call site. This test
// picks the SECOND shape: it matches the only existing whole-module
// source-scanning precedent in this repo
// (pkg/provider/vcs/github/chokepoint_test.go's TestGHExecChokePoint and
// stack_readonly_test.go's TestNoGHStackMutatingArgv -- both a plain
// filepath.WalkDir over the module root plus a source/AST scan for a
// forbidden shape), needs no new dependency, and the allowlist above is
// small, stable, and re-derived by grep rather than trusted from prose.
//
// # What this guard CANNOT catch
//
// A hand-rolled reorder that never calls one of the four named primitives --
// internal/snapshot/builder.go's own merged-Mine append (pg2-ew4kf, `out.Mine
// = append(out.Mine, mergedMine...)`) is exactly such a case: it reorders
// MineRow values by appending, not by sorting, so no primitive-name scan will
// ever see it. The doc-comment "single owner" convention (the NeedsAttention
// precedent this bead's own design cites) is the real backstop for that
// residual risk, not this test.
func TestNoSecondSortOverRows(t *testing.T) {
	root := moduleRoot(t)
	var offenders []string
	scanned := 0

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel == noSecondSortOwnFile || noSecondSortAllowlist[rel] {
			return nil
		}

		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", rel, perr)
		}
		scanned++

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			selector := pkgIdent.Name + "." + sel.Sel.Name
			if sortPrimitiveSelectors[selector] {
				pos := fset.Position(call.Pos())
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s(...)", rel, pos.Line, selector))
			}
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk module: %v", walkErr)
	}
	if scanned == 0 {
		t.Fatal("scanned no non-test source files; the guard proved nothing")
	}
	if len(offenders) > 0 {
		t.Fatalf("a sort-primitive call site appeared outside ordering.go and the allowlist:\n  %s\n\n"+
			"If this genuinely orders TeamRow/MineRow/[]snapshot.PRInput for DISPLAY, route it through "+
			"CompareTeamRows (ordering.go) instead. If it sorts a genuinely unrelated slice type, add the "+
			"file to noSecondSortAllowlist in ordering_guard_test.go with a one-line reason.",
			strings.Join(offenders, "\n  "))
	}
}

// moduleRoot walks up from the test's working directory to the directory
// holding go.mod. Mirrors pkg/provider/vcs/github/chokepoint_test.go's helper
// of the same name and purpose; duplicated here (rather than shared) because
// it is a three-line, package-private helper and the two packages must not
// import one another just to share it.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found above the test's working directory")
		}
		dir = parent
	}
}
