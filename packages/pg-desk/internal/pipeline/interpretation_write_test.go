package pipeline

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// INV-REINTERPRET-6 and 14: the hydration's interpretation write is guarded by
// facts identity (content_hash), never by the entity version. Several writers
// bump the version while keeping the facts and write no interpretation: a
// deactivation (changes poll's deactivateIf), a local reconcile (reconcileOne)
// and a removed feed row (commitRemovedFeedRow). Each lands in the gap between
// the entity commit and the interpretation write (the beforeFinish seam) and
// the interpretation MUST still be written, with the handle's stamp.
func TestFinishEntityChange_VersionBumpWithTheSameFactsStillWritesTheInterpretation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		active bool
		kinds  []string
	}{
		{"deactivation", false, nil},
		{"local reconcile", true, []string{"reconcile"}},
		{"removed feed row", false, []string{"removed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gerr error
			res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", nil)
			p := ecPipeline(t, &res, &gerr)
			p.store.SetInterpStamp("v1/abc")
			bumped := false
			p.seams.beforeFinish = func() error {
				e, found, err := p.store.GetEntity(ecRepo, "issue", "i-1")
				if err != nil || !found {
					t.Fatalf("GetEntity found=%v err=%v", found, err)
				}
				if _, err := p.store.WriteEntityStateWithLog(store.Entity{
					Repo: ecRepo, EntityType: "issue", EntityID: "i-1",
					Facts: e.Facts, AsOf: e.AsOf, ContentHash: e.ContentHash, HeadSHA: e.HeadSHA,
				}, e.Version, "2026-09-16T01:00:00Z", tc.active, tc.kinds, "pg-desk", "2026-09-16T01:00:00Z"); err != nil {
					t.Fatalf("version bump with the same facts: %v", err)
				}
				bumped = true
				return nil
			}
			mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})
			if !bumped {
				t.Fatal("the seam did not run")
			}
			got, found, err := p.store.GetInterpretation(ecRepo, "issue", "i-1")
			if err != nil || !found {
				t.Fatalf("interpretation found=%v err=%v: a version bump alone must not skip the write", found, err)
			}
			if got.InterpVersion != "v1/abc" {
				t.Fatalf("stamp = %q, want the handle's", got.InterpVersion)
			}
		})
	}
}

// A hydration overtaken by one with DIFFERENT facts is skipped, and the newer
// hydration's row stays.
func TestFinishEntityChange_LateWriteWithDifferentFactsIsSkipped(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", nil)
	p := ecPipeline(t, &res, &gerr)
	p.seams.beforeFinish = func() error {
		e, _, _ := p.store.GetEntity(ecRepo, "issue", "i-1")
		if _, err := p.store.WriteEntityStateWithLog(store.Entity{
			Repo: ecRepo, EntityType: "issue", EntityID: "i-1",
			Facts: e.Facts, AsOf: e.AsOf, ContentHash: "a-newer-hydration", HeadSHA: e.HeadSHA,
		}, e.Version, "2026-09-16T01:00:00Z", true, nil, "pg-desk", "2026-09-16T01:00:00Z"); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})
	if _, found, _ := p.store.GetInterpretation(ecRepo, "issue", "i-1"); found {
		t.Fatal("a late write with different facts was written")
	}
}

// INV-REINTERPRET-14: a hydration through a handle with no stamp still writes
// (NULL) and the write helper does not fail.
func TestWriteInterpretation_HandleWithoutStampWritesNull(t *testing.T) {
	var gerr error
	res := ecIssue(t, "open", "me", "2026-09-16T00:00:00Z", nil)
	p := ecPipeline(t, &res, &gerr)
	mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})
	got, found, err := p.store.GetInterpretation(ecRepo, "issue", "i-1")
	if err != nil || !found || got.InterpVersion != "" {
		t.Fatalf("row = %+v found=%v err=%v, want a NULL-stamped row", got, found, err)
	}
}

// INV-REINTERPRET-1: Store.WriteInterpretation is the only caller of the SQL
// upsert. In production code (every non-test file of the module) nothing calls
// UpsertInterpretation, the schema-dual seeding writer, and the shared SQL
// helper is called from WriteInterpretation alone. Test files are skipped,
// including external test packages, so a test may still seed with the former.
func TestOnlyWriteInterpretationReachesTheInterpretationUpsert(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	var callers []string
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		scanned++
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var name string
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					name = fun.Sel.Name
				case *ast.Ident:
					name = fun.Name
				}
				if name == "UpsertInterpretation" || name == "upsertInterpretationNew" {
					callers = append(callers, name+" in "+filepath.ToSlash(path)+" ("+fn.Name.Name+")")
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 50 {
		t.Fatalf("scanned only %d production files: the scan is mis-scoped", scanned)
	}
	var want []string
	for _, c := range callers {
		// The definition of UpsertInterpretation calls the helper; WriteInterpretation
		// calls it too; the ForTest seeding helpers (testing_seed.go, named and
		// guarded as test-only) call UpsertInterpretation. Nothing else may.
		if strings.Contains(c, "internal/store/testing_seed.go (Seed") {
			continue
		}
		if strings.HasPrefix(c, "upsertInterpretationNew in ../../internal/store/interpretation.go (UpsertInterpretation)") ||
			strings.HasPrefix(c, "upsertInterpretationNew in ../../internal/store/interpretation_write.go (WriteInterpretation)") {
			continue
		}
		want = append(want, c)
	}
	if len(want) != 0 {
		t.Fatalf("production code reaches the interpretation upsert outside WriteInterpretation: %v", want)
	}
}
