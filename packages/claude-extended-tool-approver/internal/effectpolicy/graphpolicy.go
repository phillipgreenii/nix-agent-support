package effectpolicy

import (
	"fmt"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectgraph"
)

// GraphFinding is a graph-level policy's verdict on one Command node.
type GraphFinding struct {
	NodeID  string
	Verdict FindingVerdict
	Reason  string
}

// GraphPolicy judges the WHOLE interpreted graph — relations between nodes,
// not one effect at a time — after the node-level policies have set every
// node's mark. Its findings fold into the named node's mark with the same
// precedence as node-level findings (Forbidden > Insufficient > Permitted);
// a Permitted graph finding never promotes a node.
type GraphPolicy interface {
	Name() string
	JudgeGraph(g *effectgraph.Graph, ctx PolicyContext) []GraphFinding
}

// DefaultGraphPolicies returns the spike's graph-level policy set.
func DefaultGraphPolicies() []GraphPolicy {
	return []GraphPolicy{NoContentFlowToUnvettedNetwork{}}
}

// NoContentFlowToUnvettedNetwork: for every node with an outbound net effect
// (a sink), walk the Flow edges upstream transitively and collect the path
// reads on the upstream nodes and on the sink itself (`-d @file`, `-T
// file`). A secret read anywhere on that path is Forbidden on the sink
// ("secret content flows to network"); otherwise an insufficient upstream
// node is Unknown; otherwise a vetted sink host is Unknown ("upload of local
// content to a vetted host requires consent" — still Abstain in this slice);
// otherwise Unknown ("content flows to unvetted host"). A sink with no
// upstream flow, no path read and no stdin consumption carries only literal
// content and gets no finding. The policy has no command knowledge: it reads
// effects and edges only.
type NoContentFlowToUnvettedNetwork struct{}

// Name implements GraphPolicy.
func (NoContentFlowToUnvettedNetwork) Name() string { return "no-content-flow-to-unvetted-network" }

// JudgeGraph implements GraphPolicy.
func (p NoContentFlowToUnvettedNetwork) JudgeGraph(g *effectgraph.Graph, ctx PolicyContext) []GraphFinding {
	var out []GraphFinding
	for i := range g.Nodes {
		sink := &g.Nodes[i]
		if sink.Kind != effectgraph.NodeCommand {
			continue
		}
		hosts := outboundHosts(sink.Effects)
		if len(hosts) == 0 {
			continue
		}
		upstream := g.UpstreamVia(sink.ID, effectgraph.EdgeFlow)
		if f, ok := p.judgeSink(g, sink, hosts, upstream, ctx); ok {
			out = append(out, f)
		}
	}
	return out
}

// judgeSink applies the policy's ladder to one sink.
func (NoContentFlowToUnvettedNetwork) judgeSink(g *effectgraph.Graph, sink *effectgraph.Node, hosts []cmddesc.Effect, upstream []string, ctx PolicyContext) (GraphFinding, bool) {
	sources := []*effectgraph.Node{sink}
	for _, id := range upstream {
		if n := g.Node(id); n != nil {
			sources = append(sources, n)
		}
	}
	consumesContent := len(upstream) > 0
	var insufficient *effectgraph.Node
	for _, n := range sources {
		for _, e := range n.Effects {
			switch {
			case e.Kind == cmddesc.EffectPath && e.Access == cmddesc.AccessRead:
				consumesContent = true
				if e.Dynamic {
					continue
				}
				if reason, secret := secretRead(e.Path, ctx); secret {
					return GraphFinding{NodeID: sink.ID, Verdict: Forbidden, Reason: fmt.Sprintf("secret content flows to network: %s on node %s (%s)", e, n.ID, reason)}, true
				}
			case e.Kind == cmddesc.EffectStdio && e.Stream == cmddesc.StreamStdin && n == sink:
				consumesContent = true
			}
		}
		if n != sink && insufficient == nil && n.Mark == effectgraph.MarkInsufficient {
			insufficient = n
		}
	}
	if !consumesContent {
		return GraphFinding{}, false
	}
	if insufficient != nil {
		return GraphFinding{NodeID: sink.ID, Verdict: Unknown, Reason: fmt.Sprintf("upstream node %s (%s) is insufficient", insufficient.ID, insufficient.Label)}, true
	}
	for _, h := range hosts {
		if h.Dynamic || !ctx.HostVetted(h.Host) {
			return GraphFinding{NodeID: sink.ID, Verdict: Unknown, Reason: "content flows to unvetted host " + h.Host}, true
		}
	}
	return GraphFinding{NodeID: sink.ID, Verdict: Unknown, Reason: "upload of local content to a vetted host requires consent"}, true
}

// outboundHosts returns the outbound net effects among effects.
func outboundHosts(effects []cmddesc.Effect) []cmddesc.Effect {
	var out []cmddesc.Effect
	for _, e := range effects {
		if e.Kind == cmddesc.EffectNet && e.Direction == cmddesc.NetOutbound {
			out = append(out, e)
		}
	}
	return out
}
