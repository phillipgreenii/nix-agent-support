package effectgraph

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// TestCWDThreading (slice 3o) pins the builder's working-directory rules:
// a cd re-bases later leaves in the same list and in nested subshells and
// bash -c children; a subshell's cd and a pipeline stage's cd do not leak
// out; a dynamic cd (or `cd -`) makes later leaves insufficient with their
// relative paths Dynamic; a bare cd goes to ~; and a leaf at the base CWD
// keeps its paths exactly as written.
func TestCWDThreading(t *testing.T) {
	const base = "/base"
	reg := cmddesc.DefaultRegistry()
	build := func(cmd string) Graph {
		sp := cmdparse.ParseShell(cmd)
		if sp.Unparseable {
			t.Fatalf("unparseable: %s", cmd)
		}
		return BuildInterpreted(sp, reg, cmddesc.Context{CWD: base})
	}
	// pathsOf returns the path effects of the first Command node whose label
	// starts with prefix, as "access path[ (dynamic)]" strings.
	pathsOf := func(g Graph, prefix string) (paths []string, mark Mark, reason string) {
		for _, n := range g.Nodes {
			if n.Kind != NodeCommand || !strings.HasPrefix(n.Label, prefix) {
				continue
			}
			for _, e := range n.Effects {
				if e.Kind != cmddesc.EffectPath {
					continue
				}
				s := e.Access.String() + " " + e.Path
				if e.Dynamic {
					s += " (dynamic)"
				}
				paths = append(paths, s)
			}
			return paths, n.Mark, n.MarkReason
		}
		t.Fatalf("no node labelled %q", prefix)
		return nil, MarkUnjudged, ""
	}

	cases := []struct {
		name, cmd, leaf string
		wantPaths       []string
		wantMark        Mark
	}{
		{"same list re-bases", "cd sub && cat README.md", "cat", []string{"read /base/sub/README.md"}, MarkUnjudged},
		{"dot-dot resolves", "cd sub && cat ../README.md", "cat", []string{"read /base/README.md"}, MarkUnjudged},
		{"absolute cd", "cd /nix/store && cat x", "cat", []string{"read /nix/store/x"}, MarkUnjudged},
		{"absolute operand untouched", "cd sub && cat /etc/hosts", "cat", []string{"read /etc/hosts"}, MarkUnjudged},
		{"tilde operand untouched", "cd sub && cat ~/x", "cat", []string{"read ~/x"}, MarkUnjudged},
		{"base cwd keeps paths as written", "cat README.md", "cat", []string{"read README.md"}, MarkUnjudged},
		{"subshell cd confined", "(cd sub) && cat README.md", "cat", []string{"read README.md"}, MarkUnjudged},
		{"subshell inherits then re-bases inside", "(cd sub && cat x)", "cat", []string{"read /base/sub/x"}, MarkUnjudged},
		{"outer cd reaches later subshell", "cd sub; (cat x)", "cat", []string{"read /base/sub/x"}, MarkUnjudged},
		{"pipeline stage cd confined", "cd sub | cat; cat x", "cat x", []string{"read x"}, MarkUnjudged},
		{"two cds accumulate", "cd a && cd b && cat x", "cat", []string{"read /base/a/b/x"}, MarkUnjudged},
		{"cd's own read is against the old cwd", "cd a && cd b", "cd b", []string{"read /base/a/b"}, MarkUnjudged},
		{"bare cd goes home", "cd && cat x", "cat", []string{"read ~/x"}, MarkUnjudged},
		{"dynamic cd: later relative paths dynamic, insufficient", `cd "$D" && cat x`, "cat", []string{"read x (dynamic)"}, MarkInsufficient},
		{"dynamic cd: later absolute path stays static", `cd "$D" && cat /etc/hosts`, "cat", []string{"read /etc/hosts"}, MarkInsufficient},
		{"cd - is dynamic", "cd - && cat x", "cat", []string{"read x (dynamic)"}, MarkInsufficient},
		{"bash -c child starts at the parent's cwd", "cd sub && bash -c 'cat x'", "cat x", []string{"read /base/sub/x"}, MarkUnjudged},
		{"redirect re-based too", "cd sub && cat README.md > out", "cat", []string{"read /base/sub/README.md", "truncate /base/sub/out"}, MarkUnjudged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := build(tc.cmd)
			paths, mark, reason := pathsOf(g, tc.leaf)
			if strings.Join(paths, "|") != strings.Join(tc.wantPaths, "|") {
				t.Errorf("paths = %v, want %v", paths, tc.wantPaths)
			}
			if mark != tc.wantMark {
				t.Errorf("mark = %v (%s), want %v", mark, reason, tc.wantMark)
			}
		})
	}
}

// TestRebase pins the join rules.
func TestRebase(t *testing.T) {
	for _, c := range []struct{ cwd, p, want string }{
		{"/base", "x", "/base/x"},
		{"/base", "../x", "/x"},
		{"/base", "/abs", "/abs"},
		{"/base", "~/x", "~/x"},
		{"/base", "~", "~"},
		{"~", "x", "~/x"},
		{"", "x", "x"},
		{"/base", "", ""},
	} {
		if got := rebase(c.cwd, c.p); got != c.want {
			t.Errorf("rebase(%q, %q) = %q, want %q", c.cwd, c.p, got, c.want)
		}
	}
}
