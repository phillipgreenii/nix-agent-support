package claudecodeadapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectpolicy"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
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

// TestGrepGlobFollowPathAllowlist pins the operator ruling of 2026-10-25
// (bead pg2-8swr9): Grep and Glob are read-only searches, so their verdict
// MUST follow the same path allowlist as Read — approve inside an allowed
// path, ABSTAIN (never approve, never reject) outside it, with no
// unconditional allow. A Grep with no `path` argument searches the cwd, so
// its verdict is decided by the working directory.
//
// The fixture project root is a real directory under /tmp (see fixture);
// "outside" is /etc, a real directory in no configured zone. Everything runs
// through the real claudecodeadapter.Evaluate with the default registry and
// policies (no mocks), the same entry point read_search_test.go's other
// cases use. This is the new engine only: the live hook still runs the
// frozen internal/rules chain (ruling R1, no fixes to it), whose no-path Grep
// approve is unconditional — that gap closes at cutover, not here.
func TestGrepGlobFollowPathAllowlist(t *testing.T) {
	root := fixture(t)
	const outside = "/etc"
	if st, err := os.Stat(outside); err != nil || !st.IsDir() {
		t.Skipf("%s is not a directory on this machine: %v", outside, err)
	}

	// evalAt evaluates tool for a session whose cwd is cwd; the project root is
	// derived from the cwd exactly as the live hook derives it
	// (patheval.DetectProjectRoot), so the no-path cases are genuinely decided
	// by the working directory.
	evalAt := func(t *testing.T, cwd, tool, pattern, path string) evalcontract.Response {
		t.Helper()
		in := searchInput(t, cwd, tool, pattern, path)
		resp, err := Evaluate(in, RequestConfig{ProjectRoot: patheval.DetectProjectRoot(cwd)}, cmddesc.DefaultRegistry(), effectpolicy.DefaultPolicies(), effectpolicy.DefaultGraphPolicies())
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	want := func(t *testing.T, got evalcontract.Response, d evalcontract.Decision) {
		t.Helper()
		if got.Decision != d {
			t.Fatalf("got %s (%s), want %s", got.Decision, got.Reason, d)
		}
	}

	t.Run("grep inside an allowed path approves", func(t *testing.T) {
		want(t, evalAt(t, root, "Grep", "hi", root), evalcontract.Approve)
		want(t, evalAt(t, root, "Grep", "hi", filepath.Join(root, "README.md")), evalcontract.Approve)
	})
	t.Run("grep outside every allowed path abstains", func(t *testing.T) {
		want(t, evalAt(t, root, "Grep", "hi", outside), evalcontract.Abstain)
	})
	t.Run("glob inside an allowed path approves", func(t *testing.T) {
		want(t, evalAt(t, root, "Glob", root+"/**/*.md", ""), evalcontract.Approve)
		want(t, evalAt(t, root, "Glob", "**/*.md", root), evalcontract.Approve)
	})
	t.Run("glob outside every allowed path abstains", func(t *testing.T) {
		want(t, evalAt(t, root, "Glob", outside+"/*", ""), evalcontract.Abstain)
		want(t, evalAt(t, root, "Glob", "*.conf", outside), evalcontract.Abstain)
	})
	t.Run("grep with no path is decided by the working directory", func(t *testing.T) {
		// cwd inside the allowed project root, and a subdirectory of it.
		sub := filepath.Join(root, "sub")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		want(t, evalAt(t, root, "Grep", "hi", ""), evalcontract.Approve)
		want(t, evalAt(t, sub, "Grep", "hi", ""), evalcontract.Approve)
		// cwd "/" is a fabricated project root that covers $HOME: no zone.
		want(t, evalAt(t, "/", "Grep", "hi", ""), evalcontract.Abstain)
		// The same call, same pattern, flips with nothing but the cwd.
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if st, err := os.Stat(home); err == nil && st.IsDir() && !patheval.InGitRepo(home) {
				want(t, evalAt(t, home, "Grep", "hi", ""), evalcontract.Abstain)
			}
		}
	})
	t.Run("glob with no path and a relative pattern is decided by the working directory", func(t *testing.T) {
		want(t, evalAt(t, root, "Glob", "**/*.md", ""), evalcontract.Approve)
		want(t, evalAt(t, "/", "Glob", "**/*.md", ""), evalcontract.Abstain)
	})
}
