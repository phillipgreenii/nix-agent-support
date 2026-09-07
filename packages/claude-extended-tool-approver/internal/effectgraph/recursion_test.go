package effectgraph

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// selfInterpreter returns a shell child whose program is the leaf's own raw
// text (a cycle) or that text plus one more word (unbounded growth), so the
// builder's cycle and depth guards can be exercised without any real command.
type selfInterpreter struct{ grow bool }

func (s selfInterpreter) Interpret(leaf cmdparse.ParsedCommand, _ cmddesc.CommandSchema, _ cmddesc.Context) cmddesc.Interpretation {
	prog := leaf.Raw
	if s.grow {
		prog += " x"
	}
	return cmddesc.Interpretation{Sufficient: true, Children: []cmddesc.ChildInvocation{{Dialect: "shell", Program: prog, Source: "self"}}}
}

func testRegistry() cmddesc.Registry {
	cmddesc.RegisterInterpreter("test-self", selfInterpreter{})
	cmddesc.RegisterInterpreter("test-grow", selfInterpreter{grow: true})
	return cmddesc.NewRegistry(
		cmddesc.CommandSchema{Name: "loopy", Interpreter: "test-self", Positionals: cmddesc.PositionalSpec{Rest: cmddesc.Literal}},
		cmddesc.CommandSchema{Name: "deeper", Interpreter: "test-grow", Positionals: cmddesc.PositionalSpec{Rest: cmddesc.Literal}},
	)
}

func commandNodes(g Graph) []*Node {
	var out []*Node
	for i := range g.Nodes {
		if g.Nodes[i].Kind == NodeCommand {
			out = append(out, &g.Nodes[i])
		}
	}
	return out
}

func edgesOf(g Graph, kind EdgeKind) []Edge {
	var out []Edge
	for _, e := range g.Edges {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// TestRecursionCycle: a child whose program repeats an enclosing one is not
// recursed into; the parent that would have opened it is insufficient.
func TestRecursionCycle(t *testing.T) {
	g := BuildInterpreted(cmdparse.ParseShell("loopy"), testRegistry(), cmddesc.Context{})
	cmds := commandNodes(g)
	// Top-level `loopy` opens child "loopy" (chain empty, allowed); that child
	// opens "loopy" again, which repeats the chain and stops there.
	if len(cmds) != 2 {
		t.Fatalf("command nodes = %d, want 2: %+v", len(cmds), g.Nodes)
	}
	if cmds[0].Mark != MarkUnjudged {
		t.Errorf("top-level mark = %s (%s)", cmds[0].Mark, cmds[0].MarkReason)
	}
	if cmds[1].Mark != MarkInsufficient || !strings.Contains(cmds[1].MarkReason, "repeats an enclosing program") {
		t.Errorf("child mark = %s (%q)", cmds[1].Mark, cmds[1].MarkReason)
	}
	if len(g.Scopes) != 1 || g.Scopes[0].Label != "self" {
		t.Errorf("scopes = %+v", g.Scopes)
	}
	if ex := edgesOf(g, EdgeExecutes); len(ex) != 1 || ex[0].From != cmds[1].ID || ex[0].To != cmds[0].ID || ex[0].Label != "self" {
		t.Errorf("executes edges = %+v", ex)
	}
}

// TestRecursionDepthLimit: an ever-growing chain stops at maxChildDepth with
// the deepest opened parent insufficient and nothing deeper.
func TestRecursionDepthLimit(t *testing.T) {
	g := BuildInterpreted(cmdparse.ParseShell("deeper"), testRegistry(), cmddesc.Context{})
	cmds := commandNodes(g)
	if len(cmds) != maxChildDepth+1 {
		t.Fatalf("command nodes = %d, want %d", len(cmds), maxChildDepth+1)
	}
	for _, n := range cmds[:maxChildDepth] {
		if n.Mark != MarkUnjudged {
			t.Errorf("node %s mark = %s (%s)", n.ID, n.Mark, n.MarkReason)
		}
	}
	last := cmds[maxChildDepth]
	if last.Mark != MarkInsufficient || !strings.Contains(last.MarkReason, "nested deeper than") {
		t.Errorf("deepest mark = %s (%q)", last.Mark, last.MarkReason)
	}
	if len(g.Scopes) != maxChildDepth {
		t.Errorf("scopes = %d, want %d", len(g.Scopes), maxChildDepth)
	}
	deepest := g.Scopes[len(g.Scopes)-1].ID
	if strings.Count(deepest, "/") != maxChildDepth-1 {
		t.Errorf("deepest scope id = %q", deepest)
	}
}

// TestRecursionUnparseableChild: a child program the parser rejects marks the
// PARENT insufficient with the parse reason and opens no scope.
func TestRecursionUnparseableChild(t *testing.T) {
	g := BuildInterpreted(cmdparse.ParseShell(`bash -c "cat 'x"`), cmddesc.DefaultRegistry(), cmddesc.Context{})
	cmds := commandNodes(g)
	if len(cmds) != 1 || len(g.Scopes) != 0 {
		t.Fatalf("nodes = %+v scopes = %+v", g.Nodes, g.Scopes)
	}
	if cmds[0].Mark != MarkInsufficient || !strings.HasPrefix(cmds[0].MarkReason, "child program unparseable") {
		t.Errorf("mark = %s (%q)", cmds[0].Mark, cmds[0].MarkReason)
	}
}

// TestRecursionArgvChild: an argv child is synthesized as one leaf whose
// dynamic operands become dynamic effects, in a scope labelled by Source.
func TestRecursionArgvChild(t *testing.T) {
	g := BuildInterpreted(cmdparse.ParseShell("xargs -I{} cp {} sub"), cmddesc.DefaultRegistry(), cmddesc.Context{})
	cmds := commandNodes(g)
	if len(cmds) != 2 {
		t.Fatalf("command nodes = %d", len(cmds))
	}
	child := cmds[1]
	if child.Scope != "s0" || child.Leaf.Raw != "cp {} sub" || child.Leaf.Executable != "cp" {
		t.Errorf("child = %+v", child)
	}
	var dyn, static int
	for _, e := range child.Effects {
		if e.Kind == cmddesc.EffectPath {
			if e.Dynamic {
				dyn++
			} else {
				static++
			}
		}
	}
	if dyn != 1 || static != 1 {
		t.Errorf("child effects = %+v", child.Effects)
	}
	if g.ScopePath(child.Scope) != "xargs" {
		t.Errorf("scope path = %q", g.ScopePath(child.Scope))
	}
}

// TestScopePathNested: nested `bash -c` scopes render outermost first.
func TestScopePathNested(t *testing.T) {
	g := BuildInterpreted(cmdparse.ParseShell(`bash -c 'sh -c "cat a"'`), cmddesc.DefaultRegistry(), cmddesc.Context{})
	cmds := commandNodes(g)
	if len(cmds) != 3 {
		t.Fatalf("command nodes = %d", len(cmds))
	}
	if got := g.ScopePath(cmds[2].Scope); got != "bash -c > sh -c" {
		t.Errorf("scope path = %q", got)
	}
	if got := g.ScopePath(""); got != "" {
		t.Errorf("top-level scope path = %q", got)
	}
}

// TestFlowEdges: Flow edges are derived from structure plus effects — only
// where the left side emits content and the right side consumes stdin.
func TestFlowEdges(t *testing.T) {
	reg := cmddesc.DefaultRegistry()
	cases := []struct {
		name    string
		command string
		labels  []string
	}{
		{"pipe content to stdin consumer", "cat a | tee b", []string{"stdout->stdin"}},
		{"pipe into a non-stdin consumer", "cat a | rm b", nil},
		{"pipe from a no-stdout producer", "rm a | tee b", nil},
		{"pipe into an unknown command", "cat a | frobnicate", nil},
		{"redirect in", "tee b < a", []string{"file->stdin"}},
		{"redirect in to non-consumer", "rm b < a", nil},
		{"substitution into argv", "cat $(cat a)", []string{"stdout->argv"}},
		{"heredoc", "tee b <<EOF\nx\nEOF\n", []string{"heredoc->stdin"}},
		{"chain", "cat a | tee b | tee c", []string{"stdout->stdin", "stdout->stdin"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := BuildInterpreted(cmdparse.ParseShell(tc.command), reg, cmddesc.Context{})
			var labels []string
			for _, e := range edgesOf(g, EdgeFlow) {
				labels = append(labels, e.Label)
			}
			if strings.Join(labels, ",") != strings.Join(tc.labels, ",") {
				t.Errorf("flow labels = %v, want %v\n%s", labels, tc.labels, Mermaid(g))
			}
		})
	}
}

// TestStdioInheritanceShellChild: a `bash -c` child shares the parent's
// stdin AND stdout, so a pipe feeding the parent's stdin reaches a child
// that consumes stdin, and a pipe reading the parent's stdout is fed by a
// child that emits stdout content — both represented as explicit EdgeFlow
// edges crossing the EdgeExecutes scope boundary (build.go's deriveFlows).
func TestStdioInheritanceShellChild(t *testing.T) {
	reg := cmddesc.DefaultRegistry()

	t.Run("parent stdin flows into a stdin-consuming child", func(t *testing.T) {
		// tee inside bash -c consumes stdin; the outer pipe feeds the
		// parent's stdin, which must now reach the child.
		g := BuildInterpreted(cmdparse.ParseShell("cat a | bash -c 'tee b'"), reg, cmddesc.Context{})
		cmds := commandNodes(g)
		cat, parent, tee := cmds[0], cmds[1], cmds[2]
		flow := edgesOf(g, EdgeFlow)
		mustHave(t, flow, cat.ID, parent.ID, "stdout->stdin")
		mustHave(t, flow, parent.ID, tee.ID, "stdin->child stdin")
	})

	t.Run("child stdout flows out to the parent", func(t *testing.T) {
		// cat inside bash -c emits stdout content; the outer pipe reads the
		// parent's stdout, which must now be fed by the child.
		g := BuildInterpreted(cmdparse.ParseShell("bash -c 'cat a' | tee b"), reg, cmddesc.Context{})
		cmds := commandNodes(g)
		parent, tee, cat := cmds[0], cmds[1], cmds[2]
		flow := edgesOf(g, EdgeFlow)
		mustHave(t, flow, cat.ID, parent.ID, "child stdout->stdout")
		mustHave(t, flow, parent.ID, tee.ID, "stdout->stdin")
	})

	t.Run("fixpoint: two nested bash -c levels", func(t *testing.T) {
		// The innermost cat's stdout must reach all the way out to the
		// outermost parent — a single non-iterative pass over EdgeExecutes
		// would only propagate one level.
		g := BuildInterpreted(cmdparse.ParseShell(`bash -c 'bash -c "cat a"' | tee b`), reg, cmddesc.Context{})
		cmds := commandNodes(g)
		outer, tee, inner, cat := cmds[0], cmds[1], cmds[2], cmds[3]
		flow := edgesOf(g, EdgeFlow)
		mustHave(t, flow, cat.ID, inner.ID, "child stdout->stdout")
		mustHave(t, flow, inner.ID, outer.ID, "child stdout->stdout")
		mustHave(t, flow, outer.ID, tee.ID, "stdout->stdin")
		if up := g.UpstreamVia(tee.ID, EdgeFlow); strings.Join(up, ",") != outer.ID+","+inner.ID+","+cat.ID {
			t.Errorf("upstream from tee = %v, want [%s %s %s]", up, outer.ID, inner.ID, cat.ID)
		}
	})
}

// TestStdioInheritanceXargsChild: an xargs (argv-dialect) child inherits the
// parent's stdout but NOT its stdin — xargs itself declares StdinAlways and
// consumes stdin to build the item list the child's argv is populated from,
// so the child's own reads are not fed by whatever fed xargs's stdin. Only
// the child-stdout->parent edge is added; no stdin->child-stdin edge is.
func TestStdioInheritanceXargsChild(t *testing.T) {
	reg := cmddesc.DefaultRegistry()
	g := BuildInterpreted(cmdparse.ParseShell("cat list | xargs head | tee copy"), reg, cmddesc.Context{})
	cmds := commandNodes(g)
	if len(cmds) != 4 {
		t.Fatalf("command nodes = %d, want 4: %+v", len(cmds), g.Nodes)
	}
	catList, xargsParent, tee, headChild := cmds[0], cmds[1], cmds[2], cmds[3]
	if headChild.Scope == "" {
		t.Fatalf("head child not in a nested scope: %+v", headChild)
	}

	flow := edgesOf(g, EdgeFlow)
	mustHave(t, flow, catList.ID, xargsParent.ID, "stdout->stdin")
	mustHave(t, flow, headChild.ID, xargsParent.ID, "child stdout->stdout")
	mustHave(t, flow, xargsParent.ID, tee.ID, "stdout->stdin")

	for _, e := range flow {
		if e.Label == "stdin->child stdin" {
			t.Errorf("xargs child must not inherit stdin, but got edge %+v", e)
		}
	}
}

// mustHave fails the test unless edges contains one edge exactly matching
// (from, to, label).
func mustHave(t *testing.T, edges []Edge, from, to, label string) {
	t.Helper()
	for _, e := range edges {
		if e.From == from && e.To == to && e.Label == label {
			return
		}
	}
	t.Errorf("missing edge %s -[%s]-> %s in %+v", from, label, to, edges)
}

// TestUpstreamVia: the transitive walk follows only the requested kind and
// never revisits a node.
func TestUpstreamVia(t *testing.T) {
	g := BuildInterpreted(cmdparse.ParseShell("cat a | tee b | tee c"), cmddesc.DefaultRegistry(), cmddesc.Context{})
	cmds := commandNodes(g)
	up := g.UpstreamVia(cmds[2].ID, EdgeFlow)
	if strings.Join(up, ",") != cmds[1].ID+","+cmds[0].ID {
		t.Errorf("upstream = %v", up)
	}
	if got := g.UpstreamVia(cmds[0].ID, EdgeFlow); len(got) != 0 {
		t.Errorf("source has upstream %v", got)
	}
	if got := g.UpstreamVia(cmds[2].ID, EdgeExecutes); len(got) != 0 {
		t.Errorf("wrong kind walked: %v", got)
	}
}

// TestStructuralHasNoFlowOrExecutes: derived edges belong to the interpreted
// graph only.
func TestStructuralHasNoFlowOrExecutes(t *testing.T) {
	g := BuildStructural(cmdparse.ParseShell("cat a | bash -c 'tee b'"))
	if n := len(edgesOf(g, EdgeFlow)) + len(edgesOf(g, EdgeExecutes)); n != 0 {
		t.Errorf("structural graph carries %d derived edges", n)
	}
}
