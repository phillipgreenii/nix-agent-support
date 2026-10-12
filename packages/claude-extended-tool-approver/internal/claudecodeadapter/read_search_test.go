package claudecodeadapter

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectpolicy"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func readInput(t *testing.T, cwd, path string) *hookio.HookInput {
	t.Helper()
	raw, err := json.Marshal(hookio.FileToolInput{FilePath: path})
	if err != nil {
		t.Fatal(err)
	}
	return &hookio.HookInput{CWD: cwd, ToolName: "Read", ToolInput: raw}
}

func searchInput(t *testing.T, cwd, tool, pattern, path string) *hookio.HookInput {
	t.Helper()
	raw, err := json.Marshal(hookio.SearchToolInput{Pattern: pattern, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	return &hookio.HookInput{CWD: cwd, ToolName: tool, ToolInput: raw}
}

func evalIn(t *testing.T, root string, in *hookio.HookInput) evalcontract.Response {
	t.Helper()
	resp, err := Evaluate(in, RequestConfig{ProjectRoot: root}, cmddesc.DefaultRegistry(), effectpolicy.DefaultPolicies(), effectpolicy.DefaultGraphPolicies())
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestReadGlobGrepAdapters(t *testing.T) {
	root := fixture(t)
	readme := filepath.Join(root, "README.md")
	dotenv := filepath.Join(root, ".env")

	t.Run("read ordinary approves", func(t *testing.T) {
		if r := evalIn(t, root, readInput(t, root, readme)); r.Decision != evalcontract.Approve {
			t.Fatalf("got %s: %s", r.Decision, r.Reason)
		}
	})
	t.Run("read secret rejects", func(t *testing.T) {
		if r := evalIn(t, root, readInput(t, root, dotenv)); r.Decision != evalcontract.Reject {
			t.Fatalf("got %s: %s", r.Decision, r.Reason)
		}
	})
	t.Run("grep secret path rejects", func(t *testing.T) {
		if r := evalIn(t, root, searchInput(t, root, "Grep", "S=", dotenv)); r.Decision != evalcontract.Reject {
			t.Fatalf("got %s: %s", r.Decision, r.Reason)
		}
	})
	t.Run("grep ordinary dir approves, default path is cwd", func(t *testing.T) {
		for _, p := range []string{root, ""} {
			if r := evalIn(t, root, searchInput(t, root, "Grep", "hi", p)); r.Decision != evalcontract.Approve {
				t.Fatalf("path %q: got %s: %s", p, r.Decision, r.Reason)
			}
		}
	})
	t.Run("glob absolute literal secret judged as literal read", func(t *testing.T) {
		g := evalIn(t, root, searchInput(t, root, "Glob", dotenv, ""))
		l := evalIn(t, root, readInput(t, root, dotenv))
		if g.Decision != evalcontract.Reject || g.Decision != l.Decision {
			t.Fatalf("glob=%s literal=%s (%s)", g.Decision, l.Decision, g.Reason)
		}
	})
	t.Run("glob absolute prefix judged as equivalent literal dir", func(t *testing.T) {
		g := evalIn(t, root, searchInput(t, root, "Glob", root+"/**/*.md", ""))
		l := evalIn(t, root, readInput(t, root, root))
		if g.Decision != l.Decision || g.Decision != evalcontract.Approve {
			t.Fatalf("glob=%s literal=%s (%s)", g.Decision, l.Decision, g.Reason)
		}
	})
	t.Run("glob dotdot pattern judged as resolved literal", func(t *testing.T) {
		g := evalIn(t, root, searchInput(t, root, "Glob", "sub/../.env", root))
		l := evalIn(t, root, readInput(t, root, dotenv))
		if g.Decision != l.Decision || g.Decision != evalcontract.Reject {
			t.Fatalf("glob=%s literal=%s (%s)", g.Decision, l.Decision, g.Reason)
		}
	})
	t.Run("glob dotdot escaping the root judged like literal", func(t *testing.T) {
		esc := root + "/../../../../../../../../etc"
		g := evalIn(t, root, searchInput(t, root, "Glob", esc+"/*", ""))
		l := evalIn(t, root, readInput(t, root, esc))
		if g.Decision != l.Decision {
			t.Fatalf("glob=%s literal=%s", g.Decision, l.Decision)
		}
	})
	t.Run("glob wildcard before dotdot abstains", func(t *testing.T) {
		g := evalIn(t, root, searchInput(t, root, "Glob", "*/../.env", root))
		if g.Decision != evalcontract.Abstain {
			t.Fatalf("got %s: %s", g.Decision, g.Reason)
		}
	})
	t.Run("relative glob without path uses cwd", func(t *testing.T) {
		_, g, err := BuildReadToolGraph(searchInput(t, root, "Glob", "src/**/*.go", ""))
		if err != nil {
			t.Fatal(err)
		}
		if got := g.Nodes[0].Effects[0]; got.Path != "./src" || got.Access != cmddesc.AccessRead || got.Dynamic {
			t.Fatalf("effect = %+v", got)
		}
	})
	t.Run("unmapped tool still errors", func(t *testing.T) {
		in := searchInput(t, root, "WebSearch", "x", "")
		if _, err := Evaluate(in, RequestConfig{ProjectRoot: root}, cmddesc.DefaultRegistry(), effectpolicy.DefaultPolicies(), effectpolicy.DefaultGraphPolicies()); err == nil || !strings.Contains(err.Error(), "unsupported tool") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestGlobLiteralPrefix(t *testing.T) {
	cases := []struct {
		in      string
		prefix  string
		dynamic bool
	}{
		{"**/*.go", "", false},
		{"src/**/*.go", "src", false},
		{"/etc/*", "/etc", false},
		{"/*.go", "/", false},
		{"../../x/*", "../../x", false},
		{"a/*/../b", "a", true},
		{"{a,b}/c", "", false},
		{"a/b.txt", "a/b.txt", false},
	}
	for _, c := range cases {
		p, d := globLiteralPrefix(c.in)
		if p != c.prefix || d != c.dynamic {
			t.Errorf("%q = (%q,%v), want (%q,%v)", c.in, p, d, c.prefix, c.dynamic)
		}
	}
}
