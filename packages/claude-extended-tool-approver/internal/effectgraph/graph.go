// Package effectgraph builds the EFFECT GRAPH of a parsed shell command: a
// STRUCTURAL graph (nodes and edges from the parse alone) and an INTERPRETED
// graph (the structural graph plus each command node's effects from its
// schema). It renders both to Mermaid deterministically. Policies live
// elsewhere; this package only records their marks.
//
// Known import cycle to fix later: cmdparse.ParsedCommand embeds
// hookio.Redirection, so hookio is reached transitively through cmdparse. This
// package does not import internal/hookio directly.
package effectgraph

import (
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
// substitution scope the node lives in ("" at top level, "s0", "s0/s1", ...)
// and drives Mermaid subgraph nesting.
type Node struct {
	ID         string
	Kind       NodeKind
	Label      string
	Scope      string
	Leaf       *cmdparse.ParsedCommand
	Effects    []cmddesc.Effect
	Mark       Mark
	MarkReason string
}

// Edge is one directed relation between two node IDs.
type Edge struct {
	From, To string
	Kind     EdgeKind
	Label    string
}

// Scope is one substitution recursion level; Parent is the enclosing scope
// ("" for a top-level substitution).
type Scope struct {
	ID     string
	Parent string
	Label  string
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
