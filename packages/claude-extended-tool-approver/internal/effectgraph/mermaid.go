package effectgraph

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Mermaid renders the graph as a deterministic `flowchart LR`. Node ids are
// `n<i>` by slice index; nodes are emitted in slice order, top level first and
// then one nested `subgraph` per substitution scope (sorted by scope id);
// edges are emitted sorted by (Kind, From, To, Label). Labels are escaped for
// Mermaid. Everything rendered comes from the Graph model, so two equal
// graphs render byte-identically.
func Mermaid(g Graph) string {
	ids := make(map[string]string, len(g.Nodes))
	for i, n := range g.Nodes {
		ids[n.ID] = "n" + strconv.Itoa(i)
	}

	var b strings.Builder
	b.WriteString("flowchart LR\n")
	emitNodes(&b, g, "", "    ")

	scopes := append([]Scope(nil), g.Scopes...)
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].ID < scopes[j].ID })
	for _, s := range scopes {
		if s.Parent == "" {
			emitScope(&b, g, scopes, s, "    ")
		}
	}

	edges := append([]Edge(nil), g.Edges...)
	sort.Slice(edges, func(i, j int) bool {
		a, c := edges[i], edges[j]
		if a.Kind != c.Kind {
			return a.Kind < c.Kind
		}
		if a.From != c.From {
			return nodeIndex(ids[a.From]) < nodeIndex(ids[c.From])
		}
		if a.To != c.To {
			return nodeIndex(ids[a.To]) < nodeIndex(ids[c.To])
		}
		return a.Label < c.Label
	})
	for _, e := range edges {
		label := e.Kind.String()
		if e.Label != "" {
			label += ": " + e.Label
		}
		fmt.Fprintf(&b, "    %s -->|\"%s\"| %s\n", ids[e.From], escape(label), ids[e.To])
	}
	return b.String()
}

func nodeIndex(id string) int {
	i, _ := strconv.Atoi(strings.TrimPrefix(id, "n"))
	return i
}

func emitScope(b *strings.Builder, g Graph, all []Scope, s Scope, indent string) {
	fmt.Fprintf(b, "%ssubgraph %s[\"%s\"]\n", indent, scopeID(s.ID), escape(s.Label))
	emitNodes(b, g, s.ID, indent+"    ")
	for _, c := range all {
		if c.Parent == s.ID {
			emitScope(b, g, all, c, indent+"    ")
		}
	}
	fmt.Fprintf(b, "%send\n", indent)
}

func scopeID(id string) string {
	return "sg_" + strings.ReplaceAll(id, "/", "_")
}

func emitNodes(b *strings.Builder, g Graph, scope, indent string) {
	for i, n := range g.Nodes {
		if n.Scope != scope {
			continue
		}
		open, closing := shape(n.Kind)
		fmt.Fprintf(b, "%sn%d%s\"%s\"%s\n", indent, i, open, nodeLabel(n), closing)
	}
}

// shape picks the Mermaid node shape per kind.
func shape(k NodeKind) (string, string) {
	switch k {
	case NodeFile:
		return "[/", "/]"
	case NodeStdin, NodeStdout:
		return "([", "])"
	case NodeEnv:
		return "{{", "}}"
	default:
		return "[", "]"
	}
}

// nodeLabel joins the node's label, its effects and its mark with <br/>.
func nodeLabel(n Node) string {
	lines := []string{n.Kind.String() + ": " + escape(n.Label)}
	for _, e := range n.Effects {
		lines = append(lines, "- "+escape(e.String()))
	}
	if n.Mark != MarkUnjudged {
		m := "mark=" + n.Mark.String()
		if n.MarkReason != "" {
			m += ": " + n.MarkReason
		}
		lines = append(lines, escape(m))
	}
	for _, f := range n.GraphFindings {
		lines = append(lines, "graph: "+escape(f))
	}
	return strings.Join(lines, "<br/>")
}

var escaper = strings.NewReplacer(
	"&", "#amp;",
	`"`, "#quot;",
	"<", "#lt;",
	">", "#gt;",
	"\n", "\\n",
)

// escape makes text safe inside a quoted Mermaid label.
func escape(s string) string { return escaper.Replace(s) }
