package proto

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// dormantNNames are the Go spellings of the RETIRED Directory.dormant_n proto
// field (field 7, retired by ADR 0024 R8): the generated struct field and its
// generated accessor.
var dormantNNames = map[string]bool{"DormantN": true, "GetDormantN": true}

// idleNNames are the Go spellings of the idle count dormant_n must be folded
// into.
var idleNNames = map[string]bool{"IdleN": true, "GetIdleN": true}

// TestDormantNIsOnlyEverFoldedIntoIdle is the ADR 0024 R8 guard for bead
// pg2-vsrxf.
//
// `Directory.dormant_n` is RETIRED: nothing in this module WRITES it, so every
// current daemon leaves it at 0 forever. A surface that reads it as a STANDALONE
// count therefore prints a permanent zero AND silently loses the sessions that
// now land in blocked_n / idle_n — which is exactly how `info path:` came to
// render three usage-limit-blocked sessions as "0 working, 0 idle, 0 dormant".
//
// The invariant this locks in, mechanically, over the whole module:
//
//	Every read of DormantN / GetDormantN in hand-written code MUST sit inside an
//	addition whose other operand reads IdleN / GetIdleN — i.e. dormant_n may only
//	ever be FOLDED INTO IDLE, never reported, bucketed, or compared on its own.
//
// This is deliberately a scan rather than an assertion about today's known call
// sites: a NEW served surface that starts reading dormant_n standalone fails this
// test without anyone remembering to extend a list.
//
// Excluded: generated protobuf code (`*.pb.go`, which necessarily declares the
// field and its accessor) and `*_test.go` files (a test may legitimately
// construct dormant_n to exercise version skew with an older daemon).
func TestDormantNIsOnlyEverFoldedIntoIdle(t *testing.T) {
	root := moduleRoot(t)

	total := 0
	var violations []string

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
		if !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") ||
			strings.HasSuffix(name, ".pb.go") {
			return nil
		}

		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		folded := foldedDormantPositions(f)
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok || !dormantNNames[id.Name] {
				return true
			}
			total++
			if folded[id.Pos()] {
				return true
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			violations = append(violations,
				fmt.Sprintf("%s:%d reads %s", rel, fset.Position(id.Pos()).Line, id.Name))
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("scanning %s: %v", root, walkErr)
	}

	// Self-check: the scanner must actually be looking at the module. Today the
	// legitimate fold sites are internal/proto/from_proto.go (wire → aggregate)
	// and cmd/pa-monitor/cli_format.go (dirSessionCounts, shared by `status` and
	// `info path:`). Zero hits means the walk found nothing and the guard is inert.
	if total == 0 {
		t.Fatalf("found no DormantN/GetDormantN references under %s — the guard is not scanning anything", root)
	}
	t.Logf("scanned %d hand-written DormantN/GetDormantN reference(s) under %s", total, root)

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("ADR 0024 R8: dormant_n is retired and permanently 0 — it may only be "+
			"folded into idle (idle += IdleN + DormantN), never read as a standalone count: %s", v)
	}
}

// foldedDormantPositions returns the positions of every DormantN/GetDormantN
// identifier that sits inside an addition whose operands also read
// IdleN/GetIdleN — the one approved "fold dormant into idle" shape.
func foldedDormantPositions(f *ast.File) map[token.Pos]bool {
	out := map[token.Pos]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		be, ok := n.(*ast.BinaryExpr)
		if !ok || be.Op != token.ADD {
			return true
		}
		if !containsIdentNamed(be, idleNNames) || !containsIdentNamed(be, dormantNNames) {
			return true
		}
		ast.Inspect(be, func(m ast.Node) bool {
			if id, ok := m.(*ast.Ident); ok && dormantNNames[id.Name] {
				out[id.Pos()] = true
			}
			return true
		})
		return true
	})
	return out
}

// containsIdentNamed reports whether the subtree rooted at n contains an
// identifier whose name is in names.
func containsIdentNamed(n ast.Node, names map[string]bool) bool {
	found := false
	ast.Inspect(n, func(m ast.Node) bool {
		if found {
			return false
		}
		if id, ok := m.(*ast.Ident); ok && names[id.Name] {
			found = true
			return false
		}
		return true
	})
	return found
}

// moduleRoot walks up from the test's working directory to the directory holding
// go.mod, so the guard scans the whole pa-monitor module no matter which package
// it is invoked from (and works unchanged inside the nix build sandbox).
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
