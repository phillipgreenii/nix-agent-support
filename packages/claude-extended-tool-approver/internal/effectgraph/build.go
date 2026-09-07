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
// File node with a Reads/Writes edge. Every child invocation the
// interpretation returns is parsed (or synthesized from its argv) into a new
// scope labelled by its Source, its leaves interpreted recursively with the
// same registry, and each of its Command nodes wired to the parent by an
// Executes edge. Finally Flow edges are derived from structure plus effects.
// Marks other than insufficient stay MarkUnjudged for the policy fold.
func BuildInterpreted(sp cmdparse.ShellParse, reg cmddesc.Registry, ctx cmddesc.Context) Graph {
	b := newBuilder()
	b.baseCWD = ctx.CWD
	b.leaves(sp.Leaves, "")
	b.interpretRange(0, len(b.g.Nodes), reg, ctx, nil)
	b.deriveFlows()
	return b.g
}

// maxChildDepth bounds child-invocation nesting: a chain of more than this
// many nested programs marks the parent insufficient.
const maxChildDepth = 8

type builder struct {
	g        Graph
	files    map[string]string // scope + "\x00" + path -> node ID
	envs     map[string]string // scope + "\x00" + name -> node ID
	scopeSeq int
	// execDialect records, per child Command node ID (the From of an
	// EdgeExecutes edge), the ChildInvocation.Dialect that produced it
	// ("shell" for a `bash -c`/`sh -c` program, "argv" for an xargs-style
	// argument vector). deriveFlows reads this to decide whether the child
	// shares the parent's stdin (see its doc comment) — it is the only place
	// outside cmddesc that distinguishes the two child shapes, and it keys on
	// the dialect data, never on a command name.
	execDialect map[string]string
	// baseCWD is the request's working directory. A leaf whose effective
	// CWD (after a preceding cd in its list, slice 3o) differs from it has
	// its relative path effects re-based to absolute paths, so the policies'
	// single request-CWD evaluator judges the right file; a leaf at the base
	// CWD keeps its paths as written.
	baseCWD string
}

// cwdState is the working directory in force for one (scope, subshell)
// position of a list, as threaded by interpretRange: cwd, or unknown when a
// preceding cd's target was a runtime value (reason says which).
type cwdState struct {
	cwd     string
	unknown bool
	reason  string
}

func newBuilder() *builder {
	return &builder{files: map[string]string{}, envs: map[string]string{}, execDialect: map[string]string{}}
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

// interpretRange interprets the Command nodes at indexes [start, end). A
// child invocation appends its nodes beyond end and interprets them itself,
// so the range never sees a node twice. chain is the normalized program text
// of every enclosing child invocation, outermost first.
//
// WORKING-DIRECTORY THREADING (slice 3o, tc-q9ak item 3 "cd"): a leaf that
// emits an EffectChdir changes the working directory of every LATER leaf in
// the same list, and the builder — not any policy, and never by command
// name — is where that is modeled. The state is kept per (scope, subshell
// path): a leaf reads the innermost state defined for its own
// cmdparse.SubshellScope chain (its own subshell, then each enclosing one,
// then the list's base), so a cd inside `( ... )` reaches the subshell's
// later leaves but never the enclosing list's, while an enclosing list's cd
// reaches into a later subshell. A cd that is one stage of a multi-stage
// pipeline runs in its own subshell (bash's default) and changes nothing.
// A cd whose target is a runtime value (`cd "$D"`, `cd -`) puts the
// position into the UNKNOWN state: every later leaf there is marked
// insufficient and its relative path effects are made Dynamic, so no policy
// can reach a verdict against the wrong directory. A `bash -c` child starts
// at its parent leaf's effective CWD (ctx is passed through). Known
// limitation: leaves inside a command substitution ($(...)) are a separate
// scope keyed independently and start from the LIST's base CWD, not from
// the CWD at the substitution's position.
func (b *builder) interpretRange(start, end int, reg cmddesc.Registry, ctx cmddesc.Context, chain []string) {
	states := map[string]cwdState{}
	stages := map[string]int{} // scope + "\x00" + PipelineID -> stage count
	for i := start; i < end; i++ {
		n := b.g.Nodes[i]
		if n.Kind == NodeCommand && n.Leaf != nil && n.Leaf.PipelineID >= 0 {
			stages[n.Scope+"\x00"+fmt.Sprint(n.Leaf.PipelineID)]++
		}
	}
	for i := start; i < end; i++ {
		n := b.g.Nodes[i]
		if n.Kind != NodeCommand {
			continue
		}
		key := n.Scope + "\x00" + subshellKey(n.Leaf.SubshellScope)
		state := lookupCWD(states, n.Scope, n.Leaf.SubshellScope, ctx.CWD)
		nodeCtx := ctx
		nodeCtx.CWD = state.cwd
		chdir := b.interpret(i, reg, nodeCtx, chain, state)
		if chdir == nil {
			continue
		}
		if n.Leaf.PipelineID >= 0 && stages[n.Scope+"\x00"+fmt.Sprint(n.Leaf.PipelineID)] > 1 {
			continue // a pipeline stage's cd is confined to that stage's subshell
		}
		if state.unknown {
			continue // still unknown; a cd cannot recover a lost base
		}
		if chdir.Dynamic {
			why := "working directory unknown after " + n.Label
			if chdir.Detail != "" {
				why += " (" + chdir.Detail + ")"
			}
			states[key] = cwdState{unknown: true, reason: why}
			continue
		}
		states[key] = cwdState{cwd: rebase(state.cwd, chdir.Path)}
	}
}

// subshellKey renders a cmdparse.SubshellScope path as a map key ("" for a
// top-level leaf).
func subshellKey(path []int) string {
	parts := make([]string, len(path))
	for i, p := range path {
		parts[i] = fmt.Sprint(p)
	}
	return strings.Join(parts, "/")
}

// lookupCWD finds the innermost cwdState for a leaf at subshell path within
// scope: its own subshell, then each enclosing one, then the base.
func lookupCWD(states map[string]cwdState, scope string, path []int, base string) cwdState {
	for n := len(path); n >= 0; n-- {
		if s, ok := states[scope+"\x00"+subshellKey(path[:n])]; ok {
			return s
		}
	}
	return cwdState{cwd: base}
}

// rebase resolves a path against cwd the way the shell would for a cd or a
// relative operand: an absolute path, a `~`-prefixed path, or an empty cwd
// leaves the path as written; anything else is joined and cleaned.
func rebase(cwd, p string) string {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") || cwd == "" {
		return p
	}
	return path.Join(cwd, p)
}

// interpret attaches effects and the builder-level insufficiency mark to the
// Command node at index i and recurses into its child invocations. It works
// on locals and writes the node back by index at the end, because recursion
// appends nodes and would invalidate a pointer taken up front. It returns
// the node's EffectChdir, if it emitted one, for interpretRange's
// working-directory threading; cwd is the state the node was judged under.
func (b *builder) interpret(i int, reg cmddesc.Registry, ctx cmddesc.Context, chain []string, cwd cwdState) *cmddesc.Effect {
	id, scope, leaf := b.g.Nodes[i].ID, b.g.Nodes[i].Scope, *b.g.Nodes[i].Leaf
	mark, markReason := MarkUnjudged, ""
	insufficient := func(reason string) {
		if mark != MarkInsufficient {
			mark, markReason = MarkInsufficient, reason
		}
	}

	var effects []cmddesc.Effect
	var children []cmddesc.ChildInvocation
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
		children = res.Children
		if !res.Sufficient {
			insufficient(res.Insufficiency)
		}
	}

	for _, r := range leaf.Redirections {
		effects = append(effects, redirectionEffect(r.Path, r.Operator, r.Kind.IsWrite(), r.Kind.IsReadWrite(), r.LiveExpansion, r.Append))
	}

	// Working-directory threading (see interpretRange): re-base relative
	// path effects against this leaf's effective CWD when it differs from
	// the request's, or make them Dynamic when that CWD is unknown.
	var chdir *cmddesc.Effect
	for j := range effects {
		e := &effects[j]
		if e.Kind == cmddesc.EffectChdir && chdir == nil {
			c := *e
			chdir = &c
		}
		if e.Kind != cmddesc.EffectPath || e.Dynamic {
			continue
		}
		switch {
		case cwd.unknown:
			if rebase("<cwd>", e.Path) != e.Path { // relative: depends on the lost cwd
				e.Dynamic = true
				e.Detail = "working directory unknown"
			}
		case cwd.cwd != b.baseCWD:
			e.Path = rebase(cwd.cwd, e.Path)
		}
	}
	if cwd.unknown {
		insufficient(cwd.reason)
	}

	for _, e := range effects {
		if e.Kind != cmddesc.EffectPath {
			continue
		}
		label := e.Path
		if e.Dynamic {
			label += " (dynamic)"
		}
		f := b.file(scope, label)
		if e.Access.IsWrite() {
			b.edge(id, f, EdgeWrites, e.Access.String())
		} else {
			b.edge(id, f, EdgeReads, e.Access.String())
		}
	}

	// The parent's own effects are judged as usual; the children are separate
	// nodes the verdict fold iterates. Only a child the builder cannot turn
	// into nodes (unparseable, too deep, cyclic) reflects on the parent.
	for _, c := range children {
		if reason := b.child(id, scope, c, reg, ctx, chain); reason != "" {
			insufficient(reason)
		}
	}

	n := &b.g.Nodes[i]
	n.Effects, n.Mark, n.MarkReason = effects, mark, markReason
	return chdir
}

// child turns one child invocation into nodes in a new scope labelled by its
// Source, wires each of its Command nodes to the parent with an Executes
// edge, and interprets them recursively. It returns a non-empty reason when
// the child cannot be modeled, for the parent to record.
func (b *builder) child(parentID, parentScope string, c cmddesc.ChildInvocation, reg cmddesc.Registry, ctx cmddesc.Context, chain []string) string {
	var leaves []cmdparse.ParsedCommand
	var text string
	switch c.Dialect {
	case "shell":
		sp := cmdparse.ParseShell(c.Program)
		if sp.Unparseable {
			reason := "child program unparseable"
			if sp.Reason != "" {
				reason += ": " + sp.Reason
			}
			return reason
		}
		leaves, text = sp.Leaves, normalizeProgram(c.Program)
	case "argv":
		if len(c.Argv) == 0 {
			return "child invocation has an empty argv"
		}
		dynamic := make([]bool, len(c.Argv))
		copy(dynamic, c.ArgvDynamic)
		raw := strings.Join(c.Argv, " ")
		leaves = []cmdparse.ParsedCommand{{Executable: c.Argv[0], Args: c.Argv[1:], ArgLiveExpansion: dynamic[1:], Raw: raw}}
		text = normalizeProgram(raw)
	default:
		return "child invocation in unmodeled dialect " + c.Dialect
	}
	if len(chain) >= maxChildDepth {
		return fmt.Sprintf("child invocation nested deeper than %d", maxChildDepth)
	}
	for _, prev := range chain {
		if prev == text {
			return "child invocation repeats an enclosing program: " + text
		}
	}

	scope := b.newScope(parentScope, c.Source)
	start := len(b.g.Nodes)
	b.leaves(leaves, scope)
	end := len(b.g.Nodes)
	for j := start; j < end; j++ {
		if b.g.Nodes[j].Kind == NodeCommand && b.g.Nodes[j].Scope == scope {
			b.edge(b.g.Nodes[j].ID, parentID, EdgeExecutes, c.Source)
			b.execDialect[b.g.Nodes[j].ID] = c.Dialect
		}
	}
	next := make([]string, 0, len(chain)+1)
	next = append(append(next, chain...), text)
	b.interpretRange(start, end, reg, ctx, next)
	return ""
}

// normalizeProgram collapses whitespace so the cycle check compares program
// text, not its spacing.
func normalizeProgram(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// deriveFlows adds Flow edges from structure plus effects: a Pipe whose left
// side emits stdout CONTENT and whose right side consumes stdin, a RedirectIn
// into a stdin consumer, a Substitution whose leaf emits content (into the
// containing leaf's argv), and a Heredoc into a stdin consumer.
//
// It also accounts for the standard streams a nested scope SHARES with its
// parent, rather than treating a Command node's stdout/stdin as only what
// its own effects say. A child invocation carried in the "shell" dialect
// (`bash -c PROGRAM`, `sh -c PROGRAM`) runs PROGRAM with the parent's own
// stdin and stdout file descriptors: a child that consumes stdin is
// therefore fed by whatever feeds the PARENT's stdin, so the parent counts
// as a stdin consumer too; and a child that emits stdout content is writing
// to the same descriptor the parent's own stdout would go to, so the parent
// counts as emitting stdout content too. An "argv" dialect child (xargs's
// reconstructed argv) shares only the stdout half: xargs itself declares
// StdinAlways and consumes stdin to build the item list the child's argv is
// populated from, so the child's argv does NOT come from whatever fed
// xargs's own stdin — but the child still inherits and writes to xargs's
// stdout descriptor. This distinction is read from ChildInvocation.Dialect
// (via builder.execDialect, recorded when the EdgeExecutes edge was added),
// never from a command name.
//
// The attribution is computed to a FIXPOINT over the EdgeExecutes edges
// before either flow-edge pass runs below, so it propagates through more
// than one level of nesting (a `bash -c` inside a `bash -c`). It is then
// represented explicitly as new EdgeFlow edges — parent->child labelled
// "stdin->child stdin" for every child that consumes stdin (shell dialect
// only), and child->parent labelled "child stdout->stdout" for every child
// that emits stdout content (either dialect) — so an upstream walk from a
// sink fed by the parent's stdout crosses the scope boundary into the
// child's own reads, and a walk from a sink inside the child crosses out to
// whatever feeds the parent's stdin.
func (b *builder) deriveFlows() {
	stdout, stdin := map[string]bool{}, map[string]bool{}
	for _, n := range b.g.Nodes {
		if n.Kind != NodeCommand {
			continue
		}
		for _, e := range n.Effects {
			if e.Kind != cmddesc.EffectStdio {
				continue
			}
			switch {
			case e.Stream == cmddesc.StreamStdout && !e.Metadata:
				stdout[n.ID] = true
			case e.Stream == cmddesc.StreamStdin:
				stdin[n.ID] = true
			}
		}
	}

	type execEdge struct{ child, parent, dialect string }
	var execEdges []execEdge
	for _, e := range b.g.Edges {
		if e.Kind != EdgeExecutes {
			continue
		}
		execEdges = append(execEdges, execEdge{child: e.From, parent: e.To, dialect: b.execDialect[e.From]})
	}
	for changed := true; changed; {
		changed = false
		for _, ee := range execEdges {
			if ee.dialect == "shell" && stdin[ee.child] && !stdin[ee.parent] {
				stdin[ee.parent] = true
				changed = true
			}
			if stdout[ee.child] && !stdout[ee.parent] {
				stdout[ee.parent] = true
				changed = true
			}
		}
	}

	structural := append([]Edge(nil), b.g.Edges...)
	for _, e := range structural {
		switch e.Kind {
		case EdgePipe:
			if stdout[e.From] && stdin[e.To] {
				b.edge(e.From, e.To, EdgeFlow, "stdout->stdin")
			}
		case EdgeRedirectIn:
			if stdin[e.To] {
				b.edge(e.From, e.To, EdgeFlow, "file->stdin")
			}
		case EdgeSubstitution:
			if stdout[e.From] {
				b.edge(e.From, e.To, EdgeFlow, "stdout->argv")
			}
		case EdgeHeredoc:
			if stdin[e.To] {
				b.edge(e.From, e.To, EdgeFlow, "heredoc->stdin")
			}
		}
	}

	for _, ee := range execEdges {
		if ee.dialect == "shell" && stdin[ee.child] {
			b.edge(ee.parent, ee.child, EdgeFlow, "stdin->child stdin")
		}
		if stdout[ee.child] {
			b.edge(ee.child, ee.parent, EdgeFlow, "child stdout->stdout")
		}
	}
}

// redirectionEffect classifies a redirection into a path effect from the
// SEAM's own parser facts — never by re-deriving them from rendered text, per
// hooktypes.Redirection's own doc ("A consumer MUST classify by Kind, never by
// matching this string"):
//
//   - Read vs write comes from Kind.IsWrite().
//   - The write SUB-class comes from isReadWrite (Kind == RedirectReadWrite,
//     bash's `<>`) and appnd (the operator's own ENUM, `>>`/`&>>`/`n>>`/
//     `{fd}>>`) — either one is a Modify; a plain write with neither is a
//     Truncate (`>`, `>|`, `&>`, `n>`).
//   - Dynamic comes from live (hooktypes.Redirection.LiveExpansion), the same
//     AST-level expansion check ordinary arguments use, not a `$`/backtick
//     substring test on the target text — see LiveExpansion's own doc for
//     the two shapes that heuristic gets wrong (a quoted/escaped `$`/“ ` “
//     that is not live, and a live process substitution `<(...)`/`>(...)`
//     that carries neither byte).
//
// operator is used ONLY for the Source label, which is descriptive text for
// a human/diagram reader, not a classification input.
func redirectionEffect(target, operator string, isWrite, isReadWrite, live, appnd bool) cmddesc.Effect {
	access := cmddesc.AccessRead
	if isWrite {
		switch {
		case appnd, isReadWrite:
			access = cmddesc.AccessModify
		default:
			access = cmddesc.AccessTruncate
		}
	}
	return cmddesc.Effect{
		Kind:    cmddesc.EffectPath,
		Path:    target,
		Access:  access,
		Dynamic: live,
		Source:  "redirect " + operator,
	}
}
