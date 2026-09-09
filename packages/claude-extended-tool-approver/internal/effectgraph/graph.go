// Package effectgraph builds the EFFECT GRAPH of a parsed shell command: a
// STRUCTURAL graph (nodes and edges from the parse alone) and an INTERPRETED
// graph (the structural graph plus each command node's effects from its
// schema). It renders both to Mermaid deterministically. Policies live
// elsewhere; this package only records their marks.
//
// Import cycle RESOLVED as of the effect-graph spike's slice 3ap (tc-8og1):
// cmdparse used to import internal/hookio for *hookio.HookInput
// (LeavesOf/RootLeavesOf's parameter), so hookio was reached transitively
// through cmdparse. Slice 3ap relocated LeavesOf/RootLeavesOf into hookio
// itself, removing that import, so this package's own cmdparse import no
// longer reaches internal/hookio transitively either. This package does not
// import internal/hookio directly, and — since the effect-graph spike's
// slice 3r moved Redirection out of hookio into the zero-dependency
// internal/hooktypes — it no longer reaches hookio for any TYPE it names.
package effectgraph

import (
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// NodeKind classifies a graph node.
type NodeKind int

const (
	// NodeCommand is one parsed leaf.
	NodeCommand NodeKind = iota
	// NodeFile is a path endpoint (redirect target or path effect).
	NodeFile
	// NodeStdin is a standard-input source (a heredoc).
	NodeStdin
	// NodeStdout is a standard-output sink.
	NodeStdout
	// NodeEnv is an environment variable endpoint.
	NodeEnv
)

// String returns the deterministic kind name.
func (k NodeKind) String() string {
	switch k {
	case NodeCommand:
		return "command"
	case NodeFile:
		return "file"
	case NodeStdin:
		return "stdin"
	case NodeStdout:
		return "stdout"
	case NodeEnv:
		return "env"
	default:
		return "node-invalid"
	}
}

// EdgeKind classifies a graph edge.
type EdgeKind int

const (
	// EdgePipe: From's stdout feeds To's stdin.
	EdgePipe EdgeKind = iota
	// EdgeRedirectOut: From (command) writes to To (file).
	EdgeRedirectOut
	// EdgeRedirectIn: From (file) is read by To (command).
	EdgeRedirectIn
	// EdgeSubstitution: From (a substitution's command) feeds To (the leaf that
	// contains it).
	EdgeSubstitution
	// EdgeAssign: From (command) assigns To (env variable).
	EdgeAssign
	// EdgeHeredoc: From (stdin node) is the heredoc body fed to To (command).
	EdgeHeredoc
	// EdgeReads: From (command) has a read effect on To (file).
	EdgeReads
	// EdgeWrites: From (command) has a write-class effect on To (file).
	EdgeWrites
	// EdgeExecutes: From (a child command) is executed by To (the command whose
	// operand carried it — a `bash -c` program, an xargs argv).
	EdgeExecutes
	// EdgeFlow: CONTENT flows from From to To, derived from structure plus
	// effects (a pipe whose left side emits content and whose right side reads
	// stdin, a redirect-in, a substitution's output, a heredoc). These are the
	// edges a graph-level policy walks.
	EdgeFlow
)

// String returns the deterministic kind name.
func (k EdgeKind) String() string {
	switch k {
	case EdgePipe:
		return "pipe"
	case EdgeRedirectOut:
		return "redirect-out"
	case EdgeRedirectIn:
		return "redirect-in"
	case EdgeSubstitution:
		return "substitution"
	case EdgeAssign:
		return "assign"
	case EdgeHeredoc:
		return "heredoc"
	case EdgeReads:
		return "reads"
	case EdgeWrites:
		return "writes"
	case EdgeExecutes:
		return "executes"
	case EdgeFlow:
		return "flow"
	default:
		return "edge-invalid"
	}
}

// Mark is a policy's judgement of a command node. The builder sets only
// MarkInsufficient (no schema, unmodeled token); every other mark is set by
// the policy fold.
type Mark int

const (
	// MarkUnjudged: no policy has run.
	MarkUnjudged Mark = iota
	// MarkPermitted: every effect was judged permitted.
	MarkPermitted
	// MarkForbidden: at least one effect is forbidden.
	MarkForbidden
	// MarkInsufficient: the model does not fully understand the node.
	MarkInsufficient
)

// String returns the deterministic mark name.
func (m Mark) String() string {
	switch m {
	case MarkUnjudged:
		return "unjudged"
	case MarkPermitted:
		return "permitted"
	case MarkForbidden:
		return "forbidden"
	case MarkInsufficient:
		return "insufficient"
	default:
		return "mark-invalid"
	}
}

// Node is one graph vertex. Leaf is set for NodeCommand only. Scope is the
// substitution or child-invocation scope the node lives in ("" at top level,
// "s0", "s0/s1", ...) and drives Mermaid subgraph nesting. GraphFindings are
// the graph-level policy findings recorded against the node, rendered so a
// flow finding is visible even when the node-level mark already says the
// same thing.
type Node struct {
	ID            string
	Kind          NodeKind
	Label         string
	Scope         string
	Leaf          *cmdparse.ParsedCommand
	Effects       []cmddesc.Effect
	Mark          Mark
	MarkReason    string
	GraphFindings []string
}

// Edge is one directed relation between two node IDs.
type Edge struct {
	From, To string
	Kind     EdgeKind
	Label    string
}

// Scope is one substitution recursion level; Parent is the enclosing scope
// ("" for a top-level substitution). Remote is the host a REMOTE child
// invocation's scope runs on (slice 3aa, tc-lc8f item 4g; tc-vn5z item 4 —
// ssh's own remote command); "" means local. A scope inherits its parent's
// Remote by default (build.go's newScope), so anything nested inside a
// remote scope — a `bash -c` the remote command itself runs, an xargs/find
// argv it reconstructs, a command substitution — stays tagged the SAME
// host, transitively, until a DIFFERENT remote child (a second, nested
// `ssh`) overrides it.
type Scope struct {
	ID     string
	Parent string
	Label  string
	Remote string
}

// Graph is the effect graph model. Node IDs are assigned `n<i>` in slice
// order by the builder; the Mermaid emitter relies on slice order, not on the
// ID text.
type Graph struct {
	Nodes  []Node
	Edges  []Edge
	Scopes []Scope
}

// Node returns a pointer to the node with the given ID, or nil.
func (g *Graph) Node(id string) *Node {
	for i := range g.Nodes {
		if g.Nodes[i].ID == id {
			return &g.Nodes[i]
		}
	}
	return nil
}

// ScopePath renders the labels of a scope and its ancestors, outermost first,
// joined by " > " ("" for the top level). A reason naming a node inside
// `bash -c` prefixes this so the nesting is visible in text, not only in the
// diagram.
func (g *Graph) ScopePath(scopeID string) string {
	if scopeID == "" {
		return ""
	}
	labels := map[string]string{}
	for _, s := range g.Scopes {
		labels[s.ID] = s.Label
	}
	parts := strings.Split(scopeID, "/")
	out := make([]string, 0, len(parts))
	for i := range parts {
		out = append(out, labels[strings.Join(parts[:i+1], "/")])
	}
	return strings.Join(out, " > ")
}

// UpstreamVia returns the IDs of every node from which a chain of edges of
// the given kind reaches id (transitively, excluding id itself), in
// deterministic breadth-first discovery order.
func (g *Graph) UpstreamVia(id string, kind EdgeKind) []string {
	seen := map[string]bool{id: true}
	var out []string
	queue := []string{id}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range g.Edges {
			if e.Kind != kind || e.To != cur || seen[e.From] {
				continue
			}
			seen[e.From] = true
			out = append(out, e.From)
			queue = append(queue, e.From)
		}
	}
	return out
}
