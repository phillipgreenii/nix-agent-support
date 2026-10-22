package session

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// injectedMarkers mirrors INV-POOL-7 (docs/behavior/invariants.md): the env
// markers launchAndWait writes into every session, besides the caller's own
// environment and the blanked child markers. Adding a marker to launchAndWait
// without adding it here (and to INV-POOL-7) fails
// TestInjectedMarkers_matchInvariant; this list is the one place the test holds.
var injectedMarkers = []string{
	"CCPOOL_AUTONOMOUS",
	"CCPOOL_EXTERNAL_ID",
	"CCPOOL_POOL",
	"PA_MONITOR_NO_NUDGE",
}

// launchAndWaitLiteralKeys returns the string-literal keys of every
// env["..."] = ... assignment in launchAndWait, parsed from session.go. The
// child-marker blanking loop writes env[k] with a non-literal key, so it is
// excluded by construction.
func launchAndWaitLiteralKeys(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "session.go", nil, 0)
	if err != nil {
		t.Fatalf("parse session.go: %v", err)
	}
	var keys []string
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "launchAndWait" {
			return true
		}
		found = true
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range as.Lhs {
				ix, ok := lhs.(*ast.IndexExpr)
				if !ok {
					continue
				}
				if id, ok := ix.X.(*ast.Ident); !ok || id.Name != "env" {
					continue
				}
				if lit, ok := ix.Index.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					k, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("unquote %s: %v", lit.Value, err)
					}
					keys = append(keys, k)
				}
			}
			return true
		})
		return false
	})
	if !found {
		t.Fatal("launchAndWait not found in session.go")
	}
	sort.Strings(keys)
	return keys
}

// TestInjectedMarkers_matchInvariant fails when ccpool injects a marker INV-POOL-7
// does not list (or lists one it no longer injects).
func TestInjectedMarkers_matchInvariant(t *testing.T) {
	got := launchAndWaitLiteralKeys(t)
	want := append([]string(nil), injectedMarkers...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("launchAndWait injects markers %v; INV-POOL-7's list is %v.\n"+
			"A new marker is a change to INV-POOL-7 (docs/behavior/invariants.md) first, then to injectedMarkers here.", got, want)
	}
}

// TestInjectedMarkers_invariantNamesEveryMarker keeps the behavior doc and the
// code in step: INV-POOL-7's paragraph names every injected marker and every
// blanked child marker.
func TestInjectedMarkers_invariantNamesEveryMarker(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "behavior", "invariants.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	start := strings.Index(doc, "**`INV-POOL-7`**")
	if start < 0 {
		t.Fatal("INV-POOL-7 not found in docs/behavior/invariants.md")
	}
	para := doc[start+1:]
	if end := strings.Index(para, "- **`INV-"); end >= 0 {
		para = para[:end]
	}
	for _, m := range append(append([]string(nil), injectedMarkers...), claudeChildMarkers...) {
		if !strings.Contains(para, "`"+m+"`") {
			t.Errorf("INV-POOL-7 does not name `%s`", m)
		}
	}
}

// The test above is only as good as its parse: prove it sees a literal key a
// future edit would add, and ignores the non-literal blanking key.
func TestInjectedMarkers_parseSeesLiteralKeysOnly(t *testing.T) {
	got := launchAndWaitLiteralKeys(t)
	if len(got) != len(injectedMarkers) {
		t.Fatalf("parsed %v, want exactly the %d literal keys", got, len(injectedMarkers))
	}
	for _, k := range got {
		for _, c := range claudeChildMarkers {
			if k == c {
				t.Errorf("child marker %s must be excluded (non-literal key)", c)
			}
		}
	}
}
