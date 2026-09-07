package effectgraph

import (
	"fmt"
	"path"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// BuildStructural builds the graph from the parse ONLY: one Command node per
// leaf in parse order, Pipe edges between stages of one pipeline, File nodes
// and RedirectOut/RedirectIn edges per redirection, Env nodes and Assign
// edges per leading assignment, Stdin nodes and Heredoc edges per heredoc,
// and Substitution edges from each substitution's own leaves (recursively,
// one scope per substitution). No schema, no effects.
func BuildStructural(sp cmdparse.ShellParse) Graph {
	b := newBuilder()
	b.leaves(sp.Leaves, "")
	return b.g
}

// BuildInterpreted builds the structural graph and then, per Command node,
// looks the leaf's basename up in the registry and interprets it. The node's
// Effects are, in order: env assignments (from the parse), the schema
// interpretation's effects, and redirections (from the parse — the schema does
// not know about redirections; the builder does). A leaf without a schema
// gets one EffectOpaque and MarkInsufficient. Every path effect also becomes a
// File node with a Reads/Writes edge. Marks other than insufficient stay
// MarkUnjudged for the policy fold.
func BuildInterpreted(sp cmdparse.ShellParse, reg cmddesc.Registry, ctx cmddesc.Context) Graph {
	b := newBuilder()
	b.leaves(sp.Leaves, "")
	for i := range b.g.Nodes {
		if b.g.Nodes[i].Kind != NodeCommand {
			continue
		}
		b.interpret(i, reg, ctx)
	}
	return b.g
}

type builder struct {
	g        Graph
	files    map[string]string // scope + "\x00" + path -> node ID
	envs     map[string]string // scope + "\x00" + name -> node ID
	scopeSeq int
}

func newBuilder() *builder {
	return &builder{files: map[string]string{}, envs: map[string]string{}}
}

func (b *builder) add(n Node) string {
	n.ID = fmt.Sprintf("n%d", len(b.g.Nodes))
	b.g.Nodes = append(b.g.Nodes, n)
	return n.ID
}

func (b *builder) edge(from, to string, kind EdgeKind, label string) {
	b.g.Edges = append(b.g.Edges, Edge{From: from, To: to, Kind: kind, Label: label})
}

func (b *builder) file(scope, p string) string {
	key := scope + "\x00" + p
	if id, ok := b.files[key]; ok {
		return id
	}
	id := b.add(Node{Kind: NodeFile, Label: p, Scope: scope})
	b.files[key] = id
	return id
}

func (b *builder) env(scope, name string) string {
	key := scope + "\x00" + name
	if id, ok := b.envs[key]; ok {
		return id
	}
	id := b.add(Node{Kind: NodeEnv, Label: name, Scope: scope})
	b.envs[key] = id
	return id
}

func (b *builder) newScope(parent, label string) string {
	id := fmt.Sprintf("s%d", b.scopeSeq)
	b.scopeSeq++
	if parent != "" {
		id = parent + "/" + id
	}
	b.g.Scopes = append(b.g.Scopes, Scope{ID: id, Parent: parent, Label: label})
	return id
}

// leaves adds one Command node per leaf (parse order) inside scope and wires
// the structural relations cmdparse already provides.
func (b *builder) leaves(leaves []cmdparse.ParsedCommand, scope string) {
	lastStage := map[int]string{}
	for i := range leaves {
		leaf := leaves[i]
		id := b.add(Node{Kind: NodeCommand, Label: leaf.Raw, Scope: scope, Leaf: &leaf})

		if leaf.PipelineID >= 0 {
			if prev, ok := lastStage[leaf.PipelineID]; ok {
				b.edge(prev, id, EdgePipe, "")
			}
			lastStage[leaf.PipelineID] = id
		}
		for _, r := range leaf.Redirections {
			f := b.file(scope, r.Path)
			if r.Kind.IsWrite() {
				b.edge(id, f, EdgeRedirectOut, r.Operator)
			} else {
				b.edge(f, id, EdgeRedirectIn, r.Operator)
			}
		}
		for _, e := range leaf.EnvVars {
			b.edge(id, b.env(scope, e.Name), EdgeAssign, e.Raw)
		}
		for _, h := range leaf.Heredocs {
			label := "<<" + h.Delimiter
			if h.Quoted {
				label = "<<'" + h.Delimiter + "'"
			}
			hid := b.add(Node{Kind: NodeStdin, Label: label, Scope: scope})
			b.edge(hid, id, EdgeHeredoc, "")
			for _, s := range h.Substitutions {
				b.substitution(s, id, scope)
			}
		}
		for _, s := range leaf.Substitutions {
			b.substitution(s, id, scope)
		}
	}
}

// substitution recurses into a substitution's pre-lowered leaves in a new
// scope and connects each of them to the containing leaf.
func (b *builder) substitution(s cmdparse.Substitution, parentID, parentScope string) {
	label := substitutionLabel(s)
	child := b.newScope(parentScope, label)
	start := len(b.g.Nodes)
	b.leaves(s.Leaves, child)
	for i := start; i < len(b.g.Nodes); i++ {
		if b.g.Nodes[i].Kind == NodeCommand && b.g.Nodes[i].Scope == child {
			b.edge(b.g.Nodes[i].ID, parentID, EdgeSubstitution, label)
		}
	}
}

func substitutionLabel(s cmdparse.Substitution) string {
	switch s.Kind {
	case cmdparse.SubstCommand:
		return "$(" + s.Body + ")"
	case cmdparse.SubstBacktick:
		return "`" + s.Body + "`"
	case cmdparse.SubstProcessIn:
		return "<(" + s.Body + ")"
	case cmdparse.SubstProcessOut:
		return ">(" + s.Body + ")"
	default:
		return "substitution(" + s.Body + ")"
	}
}

// interpret attaches effects and the builder-level insufficiency mark to the
// Command node at index i.
func (b *builder) interpret(i int, reg cmddesc.Registry, ctx cmddesc.Context) {
	n := &b.g.Nodes[i]
	leaf := *n.Leaf
	insufficient := func(reason string) {
		if n.Mark != MarkInsufficient {
			n.Mark = MarkInsufficient
			n.MarkReason = reason
		}
	}

	var effects []cmddesc.Effect
	for _, e := range leaf.EnvVars {
		effects = append(effects, cmddesc.Effect{Kind: cmddesc.EffectEnv, EnvName: e.Name, EnvSet: true})
	}

	switch {
	case leaf.Executable == "":
		// A bare assignment or an empty leaf: nothing to look up, and the
		// assignment's value may hide an expansion the parse does not lower here.
		effects = append(effects, cmddesc.Effect{Kind: cmddesc.EffectOpaque, Detail: "no executable"})
		insufficient("no executable")
	default:
		base := path.Base(leaf.Executable)
		schema, ok := reg.Lookup(base)
		if !ok {
			effects = append(effects, cmddesc.Effect{Kind: cmddesc.EffectOpaque, Detail: "no schema for " + base})
			insufficient("no schema for " + base)
			break
		}
		in, ok := cmddesc.LookupInterpreter(schema.Interpreter)
		if !ok {
			effects = append(effects, cmddesc.Effect{Kind: cmddesc.EffectOpaque, Detail: "unknown interpreter " + schema.Interpreter})
			insufficient("unknown interpreter " + schema.Interpreter)
			break
		}
		res := in.Interpret(leaf, schema, ctx)
		effects = append(effects, res.Effects...)
		if !res.Sufficient {
			insufficient(res.Insufficiency)
		}
		if len(res.Children) > 0 {
			insufficient("child invocation not recursed in this slice")
		}
	}

	for _, r := range leaf.Redirections {
		effects = append(effects, redirectionEffect(r.Path, r.Operator, r.Kind.IsWrite()))
	}
	n.Effects = effects

	for _, e := range effects {
		if e.Kind != cmddesc.EffectPath {
			continue
		}
		label := e.Path
		if e.Dynamic {
			label += " (dynamic)"
		}
		f := b.file(n.Scope, label)
		if e.Access.IsWrite() {
			b.edge(n.ID, f, EdgeWrites, e.Access.String())
		} else {
			b.edge(n.ID, f, EdgeReads, e.Access.String())
		}
	}
}

// redirectionEffect classifies a redirection into a path effect. Read vs write
// comes from the parser's Kind (IsWrite), never the operator text; the
// operator is consulted only for the write SUB-class the Kind does not carry:
// `>>`/`&>>` append (modify) and `<>` opens read+write (modify), while
// `>`/`>|`/`&>`/`n>` truncate. A target containing an unexpanded `$` or a
// backtick is not statically known and is marked Dynamic (fail-closed).
func redirectionEffect(target, operator string, isWrite bool) cmddesc.Effect {
	access := cmddesc.AccessRead
	if isWrite {
		switch {
		case strings.Contains(operator, ">>"), strings.Contains(operator, "<>"):
			access = cmddesc.AccessModify
		default:
			access = cmddesc.AccessTruncate
		}
	}
	return cmddesc.Effect{
		Kind:    cmddesc.EffectPath,
		Path:    target,
		Access:  access,
		Dynamic: strings.ContainsAny(target, "$`"),
		Source:  "redirect " + operator,
	}
}
