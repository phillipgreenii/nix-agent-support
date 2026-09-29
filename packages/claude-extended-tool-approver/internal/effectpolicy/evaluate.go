package effectpolicy

import (
	"fmt"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectgraph"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// Evaluate is the composition root behind the evalcontract port: parse once,
// build both graphs, fold policy findings over every Command node's effects
// into a mark, apply the graph-level policies, and fold marks into a
// Decision.
//
// Node fold: any Forbidden finding -> MarkForbidden; else any Unknown
// finding, any EffectOpaque, an effect NO policy applies to, or a node the
// builder already marked insufficient -> MarkInsufficient; else
// MarkPermitted. The spike's rule is "Approve only when every node is fully
// understood and every effect permitted" (slice 3f) — an effect no policy
// has an opinion on is not permitted, so it can no longer pass silently; see
// judgeNode. Graph findings fold into the named node with the same
// precedence. Graph fold: any Forbidden -> Reject; else any Insufficient ->
// Abstain; else Approve. Reason names the first deciding node and effect in
// slice order, prefixed by the node's scope path so a verdict inside
// `bash -c` reads as such.
//
// bash -c / sh -c recursion folds to the WORST child verdict (slice 3v,
// tc-lc8f item 4c; tc-ife3 item 1). Operator ruling (Phillip, 2026-09-07,
// verbatim, recorded on tc-ife3 and tc-vn5z): "bash -c should recurse and
// return worst. ie any rejextion rejects." Normalized: the spike's
// recursion into a bare top-level `bash -c '<script>'` (build.go's
// builder.child, "shell" dialect) is correct and stays; the fold over the
// recursed children must be worst-of — any Forbidden anywhere in the child
// graph -> Reject; else any Insufficient/Unknown -> Abstain; else
// Permitted -> Approve. This is already exactly what the graph fold above
// does, unconditionally, for EVERY Command node regardless of scope: the
// loop over g.Nodes below never filters by scope or nesting depth, so a
// child node produced by recursing into a `bash -c` script folds into the
// SAME forbidden/insufficient/approve variables a top-level node would,
// with no special-casing by dialect or command name. Verified empirically
// (golden_test.go's bash_c_reject_* / bash_c_abstain_* / bash_c_approve_*
// cases, slice 3v): a Reject-producing child wins the fold regardless of
// its position — first, middle, or last statement in a `;` list, inside
// `||`/`&&`, inside a pipeline stage, inside a subshell `( ... )`, or
// nested inside another `bash -c` — and Forbidden always outranks a
// sibling Insufficient. No code change was required for this slice; the
// ruling resolves tc-ife3 item 1 in the spike's favour, and production
// (internal/rules/safecmds) is taught later, not here — see
// agreement_integration_test.go's knownSpikeLooser entries for the bash -c
// rows, which still register against production's current, unrelated gap
// (no rule for a bare top-level `bash -c` at all).
// P1 (fail-closed fold, docket tc-o14i5.3, packet tc-o14i5.3.1): the hook
// MUST NOT emit ask. Evaluate is a composition root a hook calls directly,
// so it is one of the two places (EvaluateGraph is the other) that must
// itself never let a panic escape to its caller — the deferred recover
// below turns any panic raised anywhere in this function's own body
// (cmdparse.ParseShell, effectgraph.BuildStructural/BuildInterpreted, or
// EvaluateGraph and everything it calls) into Abstain, per the shared
// preamble's "panic ... => ABSTAIN" ruling, never a propagated panic.
func Evaluate(req evalcontract.Request, reg cmddesc.Registry, policies []Policy, graphPolicies []GraphPolicy) (resp evalcontract.Response) {
	defer func() {
		if r := recover(); r != nil {
			resp = evalcontract.Response{Decision: evalcontract.Abstain, Reason: fmt.Sprintf("recovered panic: %v", r)}
		}
	}()

	sp := cmdparse.ParseShell(req.Command)
	if sp.Unparseable {
		reason := "unparseable"
		if sp.Reason != "" {
			reason += ": " + sp.Reason
		}
		return evalcontract.Response{Decision: evalcontract.Abstain, Reason: reason}
	}

	ctx := cmddesc.Context{CWD: req.CWD, Env: req.Env}
	structural := effectgraph.BuildStructural(sp)
	interpreted := effectgraph.BuildInterpreted(sp, reg, ctx)

	return EvaluateGraph(structural, interpreted, policies, graphPolicies, BuildPolicyContext(req))
}

// BuildPolicyContext derives a PolicyContext from a Request: the
// PathEvaluator (rooted at req.ProjectRoot, or patheval.DetectProjectRoot's
// guess from req.CWD when ProjectRoot is empty) plus every operator-
// configuration field PolicyContext carries straight through from the
// identically-named Request field. Evaluate itself calls this; it is
// exported so a caller that builds its OWN graph — never going through
// Evaluate's cmdparse.ParseShell(req.Command) step at all — still gets the
// IDENTICAL PolicyContext construction Evaluate uses, rather than a second,
// possibly-drifting copy of this logic. See EvaluateGraph's own doc comment
// for the adapter this exists for (tc-8og1 item 7, the Claude Code
// Write/Edit adapter): it has a Request-shaped bag of operator configuration
// but no Command to parse, so it calls this directly.
func BuildPolicyContext(req evalcontract.Request) PolicyContext {
	root := req.ProjectRoot
	if root == "" {
		root = patheval.DetectProjectRoot(req.CWD)
	}
	return PolicyContext{
		PathEval:                patheval.NewWithCWD(root, req.CWD),
		CWD:                     req.CWD,
		VettedHosts:             req.VettedHosts,
		RemoteLifecycle:         req.RemoteLifecycle,
		KubeContexts:            req.KubeContexts,
		KubeContextDefaultAllow: req.KubeContextDefaultAllow,
		RemotePaths:             req.RemotePaths,
		BuildToolVerbs:          req.BuildToolVerbs,
	}
}

// EvaluateGraph is Evaluate's shared back half, split out (tc-8og1 item 7)
// so a caller that builds its OWN structural/interpreted graph pair —
// never parsing shell text at all — is judged through the EXACT SAME
// node-fold / graph-policy / decision-fold code Evaluate itself uses for a
// parsed shell command's graph, rather than a second, parallel
// implementation that could silently drift from this one.
//
// This is the mechanism that makes file policies (NoWriteToSecretPath,
// NoWriteToReadOnlyPath, DeleteAccess, ...) SINGLE-SOURCED between a shell
// command and Claude Code's own Write/Edit tool calls: the Claude Code
// adapter package (internal/claudecodeadapter) builds a ONE-NODE graph
// directly from a Write/Edit HookInput — one cmddesc.Effect naming the
// tool's file_path, no cmdparse involved since there is no shell text to
// parse — and hands it to THIS function, the identical entry point a
// `cat`/`echo`/`sed -i` command's multi-node graph is folded through. A
// policy therefore cannot tell, and does not care, whether the EffectPath
// it is judging came from a real shell redirect or from a structured tool
// call; there is exactly one path-policy implementation for both.
// P1 (fail-closed fold): EvaluateGraph is the other composition root a
// caller invokes directly (claudecodeadapter's file-tool path never goes
// through Evaluate/cmdparse at all — see this function's own doc comment
// above), so it carries its own independent panic recovery rather than
// relying on Evaluate's: a panic raised anywhere in the node/graph-policy
// walk below (a Policy.Judge or GraphPolicy.JudgeGraph implementation) is
// recovered here and folded to Abstain, never propagated to this
// function's caller.
func EvaluateGraph(structural, interpreted effectgraph.Graph, policies []Policy, graphPolicies []GraphPolicy, pctx PolicyContext) (resp evalcontract.Response) {
	resp = evalcontract.Response{Structural: structural, Interpreted: interpreted}
	g := &resp.Interpreted

	defer func() {
		if r := recover(); r != nil {
			resp.Decision, resp.Reason = evalcontract.Abstain, fmt.Sprintf("recovered panic: %v", r)
		}
	}()

	for i := range g.Nodes {
		if g.Nodes[i].Kind == effectgraph.NodeCommand {
			judgeNode(&g.Nodes[i], policies, pctx)
		}
	}
	for _, gp := range graphPolicies {
		for _, f := range gp.JudgeGraph(g, pctx) {
			if n := g.Node(f.NodeID); n != nil && n.Kind == effectgraph.NodeCommand {
				applyGraphFinding(n, gp.Name(), f)
			}
		}
	}

	resp.Decision, resp.Reason, _, _ = Fold(g)
	return resp
}

// FoldFailure is a hard failure that produced no policy Finding for Fold to
// walk at all: something outside the Forbidden/Unknown/Permitted
// vocabulary broke before or independent of the node walk — a shell parse
// failure, a caller's own deadline already exceeded (P11's bounded-
// evaluation instrumentation is a later packet's job; Fold only guarantees
// what happens once a caller reports one), a tool this adapter does not
// model, or an operator config file that failed to load. See Fold's own
// doc comment for why reporting one can only ever TIGHTEN the outcome,
// never loosen it.
type FoldFailure struct {
	// Kind names the failure class (e.g. "parse", "deadline",
	// "unsupported-tool", "config-load", "panic" — Fold itself never sets
	// "panic": that Kind is EvaluateGraph/Evaluate's own recover()
	// vocabulary, reported via resp.Reason directly rather than routed
	// through a FoldFailure, since a panic aborts the walk Fold would
	// otherwise need to fold).
	Kind string
	// Reason is a human-readable detail (the parser's own message, the
	// unsupported tool's name, the config loader's error text).
	Reason string
}

// String renders "Kind: Reason", or just Kind when Reason is empty.
func (f FoldFailure) String() string {
	if f.Reason == "" {
		return f.Kind
	}
	return f.Kind + ": " + f.Reason
}

// Fold is the P1 fail-closed decision-rule entry point (docket tc-o14i5.3,
// packet tc-o14i5.3.1): the single function that reduces an
// ALREADY-JUDGED interpreted graph — every Command node's Mark/MarkReason
// already set by judgeNode and any GraphPolicy, exactly what EvaluateGraph
// produces before calling this — into one evalcontract.Decision, together
// with any FoldFailure the caller already knows about.
//
// Truth table: REJECT iff some Command node is MarkForbidden (this always
// wins, regardless of any failures reported — Reject is already the
// strictest, safest verdict for that node, so a failure elsewhere cannot
// make it any safer to loosen); otherwise ABSTAIN iff any node is
// MarkInsufficient, OR the graph has zero command nodes, OR the caller
// reported at least one FoldFailure; otherwise APPROVE. This is exactly
// the trichotomy P1 requires: REJECT iff some effect positively violates a
// policy, APPROVE iff every effect of every node is positively permitted,
// otherwise ABSTAIN — with a reported FoldFailure downgrading what would
// otherwise be Approve to Abstain, never the reverse.
//
// findings collects every Forbidden/Insufficient node's "where" string
// (not just the first, unlike reason, which — for backward-compatible
// parity with EvaluateGraph's pre-existing Reason text — still names only
// the first deciding node in slice order) plus a "failure: ..." entry per
// reported FoldFailure, for a caller that wants the full picture rather
// than just the winning reason.
//
// Fold itself never panics: it only reads already-computed Mark/MarkReason
// fields and Kind/Reason strings, so there is nothing here that can raise
// — a nil graph is handled explicitly below rather than dereferenced.
func Fold(g *effectgraph.Graph, failures ...FoldFailure) (decision evalcontract.Decision, reason string, findings []string, err error) {
	if g == nil {
		return evalcontract.Abstain, "nil graph", nil, nil
	}

	commands := 0
	var forbidden, insufficient string
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Kind != effectgraph.NodeCommand {
			continue
		}
		commands++
		label := n.Label
		if sp := g.ScopePath(n.Scope); sp != "" {
			label = sp + ": " + label
		}
		where := fmt.Sprintf("node %s (%s): %s", n.ID, label, n.MarkReason)
		switch n.Mark {
		case effectgraph.MarkForbidden:
			findings = append(findings, where)
			if forbidden == "" {
				forbidden = where
			}
		case effectgraph.MarkInsufficient:
			findings = append(findings, where)
			if insufficient == "" {
				insufficient = where
			}
		}
	}
	for _, f := range failures {
		findings = append(findings, "failure: "+f.String())
	}

	switch {
	case forbidden != "":
		return evalcontract.Reject, forbidden, findings, nil
	case len(failures) > 0:
		return evalcontract.Abstain, failures[0].String(), findings, nil
	case insufficient != "":
		return evalcontract.Abstain, insufficient, findings, nil
	case commands == 0:
		return evalcontract.Abstain, "no command nodes", findings, nil
	default:
		return evalcontract.Approve, fmt.Sprintf("all %d command nodes permitted", commands), findings, nil
	}
}

// judgeNode folds the policies over one Command node's effects and sets its
// mark. A builder-set MarkInsufficient survives unless a Forbidden finding
// outranks it.
//
// Fail-closed fold (slice 3f): an EffectOpaque effect is always unjudged (no
// policy is ever asked, since none ever applies to it — see the doc comment
// on cmddesc.EffectOpaque). For every OTHER effect, if no policy in the
// given slice applies to it (Judge's second return is false for every one),
// the effect is likewise unjudged and reads as "<effect>: no policy judges
// this effect" — the same fail-closed text an EffectOpaque already got.
// Before this slice an unjudged effect contributed NOTHING to the fold, so a
// node whose every effect fell in this gap folded to MarkPermitted exactly
// like a node with zero effects; slice 3e's `export FOO=bar` (an EffectEnv
// no policy in DefaultPolicies judged) is the case that proved the hole. The
// fix does not special-case EffectEnv: it closes the gap for every effect
// kind uniformly, which is also why DefaultPolicies (policy.go) now carries
// StdioIsLocal, ProgramInterpreted and EnvAssignment — kinds that used to
// ride on the same hole and would otherwise regress from Approve to Abstain.
//
// Precedence within one node is unchanged: the first Forbidden finding in
// slice order wins outright; failing that, a builder-set MarkInsufficient
// survives; failing that, the first Unknown-or-unjudged effect in slice
// order sets Insufficient; only when none of those fire does the node land
// Permitted.
func judgeNode(n *effectgraph.Node, policies []Policy, pctx PolicyContext) {
	var forbidden, unknown string
	for _, e := range n.Effects {
		if e.Kind == cmddesc.EffectOpaque {
			if unknown == "" {
				unknown = e.String()
			}
			continue
		}
		applied := false
		for _, p := range policies {
			f, applies := p.Judge(e, pctx)
			if !applies {
				continue
			}
			applied = true
			switch f.Verdict {
			case Forbidden:
				if forbidden == "" {
					forbidden = fmt.Sprintf("%s: %s (%s)", e, p.Name(), f.Reason)
				}
			case Unknown:
				if unknown == "" {
					unknown = fmt.Sprintf("%s: %s (%s)", e, p.Name(), f.Reason)
				}
			}
		}
		if !applied && unknown == "" {
			unknown = fmt.Sprintf("%s: no policy judges this effect", e)
		}
	}
	switch {
	case forbidden != "":
		n.Mark, n.MarkReason = effectgraph.MarkForbidden, forbidden
	case n.Mark == effectgraph.MarkInsufficient:
		// Builder-level reason already recorded.
	case unknown != "":
		n.Mark, n.MarkReason = effectgraph.MarkInsufficient, unknown
	default:
		n.Mark, n.MarkReason = effectgraph.MarkPermitted, ""
	}
}

// applyGraphFinding records a graph-level finding on its node and folds it
// into the mark with the node-level precedence: Forbidden outranks
// everything, Unknown demotes a permitted node to insufficient, Permitted
// changes nothing. The finding text is always recorded so the diagram shows
// it even when the mark already had a reason.
func applyGraphFinding(n *effectgraph.Node, policy string, f GraphFinding) {
	text := fmt.Sprintf("%s: %s (%s)", policy, f.Verdict, f.Reason)
	n.GraphFindings = append(n.GraphFindings, text)
	switch f.Verdict {
	case Forbidden:
		if n.Mark != effectgraph.MarkForbidden {
			n.Mark, n.MarkReason = effectgraph.MarkForbidden, text
		}
	case Unknown:
		if n.Mark == effectgraph.MarkPermitted || n.Mark == effectgraph.MarkUnjudged {
			n.Mark, n.MarkReason = effectgraph.MarkInsufficient, text
		}
	}
}
