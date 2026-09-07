package effectgraph

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

const complexCommand = "X=1 cat README.md > /etc/hosts | frobnicate $(ls -l) <<EOF\nbody\nEOF\n"

// TestMermaidDeterministic: the same graph renders byte-identically twice,
// and a graph rebuilt from a fresh parse renders identically too.
func TestMermaidDeterministic(t *testing.T) {
	reg := cmddesc.DefaultRegistry()
	ctx := cmddesc.Context{CWD: "/work"}
	sp := cmdparse.ParseShell(complexCommand)
	g := BuildInterpreted(sp, reg, ctx)
	first := Mermaid(g)
	if second := Mermaid(g); second != first {
		t.Fatalf("same graph rendered differently:\n%s\n---\n%s", first, second)
	}
	rebuilt := BuildInterpreted(cmdparse.ParseShell(complexCommand), reg, ctx)
	if third := Mermaid(rebuilt); third != first {
		t.Fatalf("rebuilt graph rendered differently:\n%s\n---\n%s", first, third)
	}
	s1, s2 := Mermaid(BuildStructural(sp)), Mermaid(BuildStructural(cmdparse.ParseShell(complexCommand)))
	if s1 != s2 {
		t.Fatalf("structural graphs differ:\n%s\n---\n%s", s1, s2)
	}
}

// TestStructuralShape: the structural graph carries exactly what cmdparse
// provides — pipe, redirect, assign, heredoc and substitution relations —
// and no effects.
func TestStructuralShape(t *testing.T) {
	g := BuildStructural(cmdparse.ParseShell(complexCommand))
	kinds := map[EdgeKind]int{}
	for _, e := range g.Edges {
		kinds[e.Kind]++
	}
	want := map[EdgeKind]int{EdgePipe: 1, EdgeRedirectOut: 1, EdgeAssign: 1, EdgeHeredoc: 1, EdgeSubstitution: 1}
	for k, n := range want {
		if kinds[k] != n {
			t.Errorf("%s edges = %d, want %d", k, kinds[k], n)
		}
	}
	if kinds[EdgeReads]+kinds[EdgeWrites] != 0 {
		t.Error("structural graph must carry no effect edges")
	}
	for _, n := range g.Nodes {
		if len(n.Effects) != 0 || n.Mark != MarkUnjudged {
			t.Errorf("structural node %s carries effects/marks", n.ID)
		}
	}
	if len(g.Scopes) != 1 || g.Scopes[0].Label != "$(ls -l)" {
		t.Errorf("scopes = %+v", g.Scopes)
	}
	out := Mermaid(g)
	if !strings.Contains(out, "subgraph sg_s0[") || !strings.Contains(out, "    end\n") {
		t.Errorf("expected one subgraph:\n%s", out)
	}
	for _, id := range []string{"n0[", "n1[", "n2", "n3", "n4", "n5"} {
		if !strings.Contains(out, id) {
			t.Errorf("missing %s in:\n%s", id, out)
		}
	}
}

// TestInterpretedMarks: no-schema leaves are insufficient with the documented
// reason; redirections become path effects with the right access class.
func TestInterpretedMarks(t *testing.T) {
	g := BuildInterpreted(cmdparse.ParseShell("cat a >> b 2> c < d; frobnicate; X=1"), cmddesc.DefaultRegistry(), cmddesc.Context{})
	var cat, frob, bare *Node
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Kind != NodeCommand {
			continue
		}
		switch n.Leaf.Executable {
		case "cat":
			cat = n
		case "frobnicate":
			frob = n
		case "":
			bare = n
		}
	}
	if cat == nil || frob == nil || bare == nil {
		t.Fatalf("missing command nodes: %+v", g.Nodes)
	}
	if cat.Mark != MarkUnjudged {
		t.Errorf("cat mark = %s (%s)", cat.Mark, cat.MarkReason)
	}
	access := map[string]cmddesc.PathAccess{}
	for _, e := range cat.Effects {
		if e.Kind == cmddesc.EffectPath {
			access[e.Path] = e.Access
		}
	}
	want := map[string]cmddesc.PathAccess{"a": cmddesc.AccessRead, "b": cmddesc.AccessModify, "c": cmddesc.AccessTruncate, "d": cmddesc.AccessRead}
	for p, a := range want {
		if access[p] != a {
			t.Errorf("%s access = %s, want %s", p, access[p], a)
		}
	}
	if frob.Mark != MarkInsufficient || frob.MarkReason != "no schema for frobnicate" {
		t.Errorf("frobnicate mark = %s (%q)", frob.Mark, frob.MarkReason)
	}
	if len(frob.Effects) != 1 || frob.Effects[0].Kind != cmddesc.EffectOpaque {
		t.Errorf("frobnicate effects = %+v", frob.Effects)
	}
	if bare.Mark != MarkInsufficient {
		t.Errorf("bare assignment mark = %s", bare.Mark)
	}
}

func TestEscape(t *testing.T) {
	if got := escape(`a "b" <c> & d`); got != "a #quot;b#quot; #lt;c#gt; #amp; d" {
		t.Errorf("escape = %q", got)
	}
}
