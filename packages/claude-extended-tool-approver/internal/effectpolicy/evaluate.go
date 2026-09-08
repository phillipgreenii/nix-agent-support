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
func Evaluate(req evalcontract.Request, reg cmddesc.Registry, policies []Policy, graphPolicies []GraphPolicy) evalcontract.Response {
	sp := cmdparse.ParseShell(req.Command)
	if sp.Unparseable {
		reason := "unparseable"
		if sp.Reason != "" {
			reason += ": " + sp.Reason
		}
		return evalcontract.Response{Decision: evalcontract.Abstain, Reason: reason}
	}

	root := req.ProjectRoot
	if root == "" {
		root = patheval.DetectProjectRoot(req.CWD)
	}
	pctx := PolicyContext{
		PathEval:                patheval.NewWithCWD(root, req.CWD),
		CWD:                     req.CWD,
		VettedHosts:             req.VettedHosts,
		RemoteLifecycle:         req.RemoteLifecycle,
		KubeContexts:            req.KubeContexts,
		KubeContextDefaultAllow: req.KubeContextDefaultAllow,
		RemotePaths:             req.RemotePaths,
	}
	ctx := cmddesc.Context{CWD: req.CWD, Env: req.Env}

	resp := evalcontract.Response{
		Structural:  effectgraph.BuildStructural(sp),
		Interpreted: effectgraph.BuildInterpreted(sp, reg, ctx),
	}
	g := &resp.Interpreted

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
			if forbidden == "" {
				forbidden = where
			}
		case effectgraph.MarkInsufficient:
			if insufficient == "" {
				insufficient = where
			}
		}
	}

	switch {
	case forbidden != "":
		resp.Decision, resp.Reason = evalcontract.Reject, forbidden
	case insufficient != "":
		resp.Decision, resp.Reason = evalcontract.Abstain, insufficient
	case commands == 0:
		resp.Decision, resp.Reason = evalcontract.Abstain, "no command nodes"
	default:
		resp.Decision, resp.Reason = evalcontract.Approve, fmt.Sprintf("all %d command nodes permitted", commands)
	}
	return resp
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
