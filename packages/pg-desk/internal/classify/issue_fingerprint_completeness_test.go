package classify

// T-7, field completeness for the issue classifier.
//
// Every top-level issue_show field the issue rules read MUST yield at least
// one classifier record (a specific kind or fields_changed) when only that
// field changes in a normalised payload; a rule that reads a field it then
// ignores would lose a real change. The field list is DERIVED by parsing the
// rule sources, never hand-copied: a field counts as "read" when it appears as
// a string-literal index key on a show map (show["state"], oldShow[...]) or as
// a string element of the issueKindFields residual table, in issue.go or in
// the sync rule source rules.go. Keys the normaliser drops (the volatile
// ones) are skipped: a normalised payload never carries them.

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// issueRuleSources are the files parsed for the read-set.
var issueRuleSources = []string{"issue.go", "../sync/rules.go"}

// issueReadFields parses the rule sources and returns the sorted, distinct
// top-level issue_show keys they read.
func issueReadFields(t *testing.T) []string {
	t.Helper()
	seen := map[string]struct{}{}
	fset := token.NewFileSet()
	for _, src := range issueRuleSources {
		f, err := parser.ParseFile(fset, src, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", src, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.IndexExpr: // show["state"]
				id, ok := x.X.(*ast.Ident)
				if !ok || !strings.HasSuffix(strings.ToLower(id.Name), "show") {
					return true
				}
				if lit, ok := x.Index.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil {
						seen[s] = struct{}{}
					}
				}
			case *ast.ValueSpec: // var issueKindFields = map[Kind][]string{...}
				for i, name := range x.Names {
					if name.Name != "issueKindFields" || i >= len(x.Values) {
						continue
					}
					ast.Inspect(x.Values[i], func(m ast.Node) bool {
						// Only the []string element literals, not the Kind keys.
						if cl, ok := m.(*ast.CompositeLit); ok {
							if _, isArr := cl.Type.(*ast.ArrayType); isArr || cl.Type == nil {
								for _, e := range cl.Elts {
									if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
										if s, err := strconv.Unquote(lit.Value); err == nil {
											seen[s] = struct{}{}
										}
									}
								}
							}
						}
						return true
					})
				}
			}
			return true
		})
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		if _, volatile := issueVolatileFields[k]; volatile {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestIssueFieldCompleteness_EveryReadFieldYieldsARecord(t *testing.T) {
	fields := issueReadFields(t)
	if len(fields) == 0 {
		t.Fatal("no fields derived from the rule sources: the parser found nothing")
	}
	// Self-check of the derivation: it must see at least the fields the
	// classifier is known to branch on (a parser regression would otherwise
	// pass this test vacuously).
	for _, must := range []string{"state", "assignee", "deps"} {
		found := false
		for _, f := range fields {
			found = found || f == must
		}
		if !found {
			t.Fatalf("derived fields %v lack %q: the parser does not see the rule sources", fields, must)
		}
	}

	var base map[string]json.RawMessage
	if err := json.Unmarshal(issueFixture(t, "open").Payload, &base); err != nil {
		t.Fatal(err)
	}
	baseShow := map[string]any{}
	if err := json.Unmarshal(base["issue_show"], &baseShow); err != nil {
		t.Fatal(err)
	}
	// The base is a normalised payload: the volatile keys are removed.
	for k := range issueVolatileFields {
		delete(baseShow, k)
	}
	render := func(show map[string]any) Snapshot {
		b, err := json.Marshal(map[string]any{"issue_show": show})
		if err != nil {
			t.Fatal(err)
		}
		return Snapshot{Type: "issue", ID: "iss-1", Exists: true, Active: true, Payload: b}
	}

	for _, f := range fields {
		for _, owning := range []bool{false, true} {
			mutated := map[string]any{}
			for k, v := range baseShow {
				mutated[k] = v
			}
			if s, ok := baseShow[f].(string); ok {
				mutated[f] = s + "-t7"
			} else {
				mutated[f] = "t7-mutated"
			}
			old, nw := render(baseShow), render(mutated)
			old.OwningBackend, nw.OwningBackend = owning, owning
			if got := Classify(old, nw); len(got) == 0 {
				t.Errorf("mutating only %q (owning=%v) yields no record: a read field is lost", f, owning)
			}
		}
	}
}
