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
// Node fold: any Forbidden finding -> MarkForbidden; else any Unknown finding,
// any EffectOpaque, or a node the builder already marked insufficient ->
// MarkInsufficient; else MarkPermitted. Graph findings fold into the named
// node with the same precedence. Graph fold: any Forbidden -> Reject; else
// any Insufficient -> Abstain; else Approve. Reason names the first deciding
// node and effect in slice order, prefixed by the node's scope path so a
// verdict inside `bash -c` reads as such.
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
	pctx := PolicyContext{PathEval: patheval.NewWithCWD(root, req.CWD), CWD: req.CWD, VettedHosts: req.VettedHosts}
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
func judgeNode(n *effectgraph.Node, policies []Policy, pctx PolicyContext) {
	var forbidden, unknown string
	for _, e := range n.Effects {
		if e.Kind == cmddesc.EffectOpaque && unknown == "" {
			unknown = e.String()
		}
		for _, p := range policies {
			f, applies := p.Judge(e, pctx)
			if !applies {
				continue
			}
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
