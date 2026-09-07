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
// into a mark, and fold marks into a Decision.
//
// Node fold: any Forbidden finding -> MarkForbidden; else any Unknown finding,
// any EffectOpaque, or a node the builder already marked insufficient ->
// MarkInsufficient; else MarkPermitted. Graph fold: any Forbidden -> Reject;
// else any Insufficient -> Abstain; else Approve. Reason names the first
// deciding node and effect in slice order.
func Evaluate(req evalcontract.Request, reg cmddesc.Registry, policies []Policy) evalcontract.Response {
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
	pctx := PolicyContext{PathEval: patheval.NewWithCWD(root, req.CWD), CWD: req.CWD}
	ctx := cmddesc.Context{CWD: req.CWD, Env: req.Env}

	resp := evalcontract.Response{
		Structural:  effectgraph.BuildStructural(sp),
		Interpreted: effectgraph.BuildInterpreted(sp, reg, ctx),
	}

	commands := 0
	var forbidden, insufficient string
	for i := range resp.Interpreted.Nodes {
		n := &resp.Interpreted.Nodes[i]
		if n.Kind != effectgraph.NodeCommand {
			continue
		}
		commands++
		judgeNode(n, policies, pctx)
		where := fmt.Sprintf("node %s (%s): %s", n.ID, n.Label, n.MarkReason)
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
