// guard_test.go: pins the two structural guarantees of this backend that no
// behavioral test can: it reaches Mail.app only through the bridge socket
// (no os/exec import and no osascript string literal anywhere in the
// package, tests excluded), and it exposes no delete-shaped operation
// (INV-MAIL-1).
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-mail-osx-bridge/internal"
)

// deleteShaped are substrings no registered op name may contain.
var deleteShaped = []string{"delete", "remove", "trash", "expunge", "purge", "destroy", "discard"}

func TestDispatchTable_RegistersNoDeleteShapedOp(t *testing.T) {
	table := newDispatchTable(internal.New(internal.NewSocketClient()))
	ops := table.Ops()
	if len(ops) == 0 {
		t.Fatal("dispatch table registers no ops")
	}
	for _, op := range ops {
		for _, bad := range deleteShaped {
			if strings.Contains(strings.ToLower(op), bad) {
				t.Errorf("registered op %q is delete-shaped (INV-MAIL-1)", op)
			}
		}
	}
}

func TestDispatchTable_RegistersEveryCapabilityOp(t *testing.T) {
	have := map[string]bool{}
	for _, op := range newDispatchTable(internal.New(internal.NewSocketClient())).Ops() {
		have[op] = true
	}
	for _, op := range []string{
		"list", "show", "search_messages", "mark_read", "mark_unread", "archive", "unarchive", "fetch_attachment",
		"search", "list_attention", "capabilities",
	} {
		if !have[op] {
			t.Errorf("dispatch table missing op %q", op)
		}
	}
	if have["auth_status"] {
		t.Error("dispatch table registers auth_status; the bridge needs no client credential")
	}
}

// TestPackage_NoExecImportAndNoOsascript parses every non-test Go file of
// this package (main and internal) and fails on an os/exec import or any
// string literal mentioning osascript. Comments are not literals, so prose
// documenting the rule does not trip it.
func TestPackage_NoExecImportAndNoOsascript(t *testing.T) {
	fset := token.NewFileSet()
	checked := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Errorf("parse %s: %v", path, perr)
			return nil
		}
		checked++
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == "os/exec" {
				t.Errorf("%s imports os/exec; this package talks only to the bridge socket", path)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.Contains(strings.ToLower(lit.Value), "osascript") {
				t.Errorf("%s: string literal %s mentions osascript", fset.Position(lit.Pos()), lit.Value)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if checked < 3 {
		t.Fatalf("checked only %d source files; the walk is not reaching the package", checked)
	}
}
