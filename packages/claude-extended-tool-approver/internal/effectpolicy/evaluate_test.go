package effectpolicy

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectgraph"
)

// TestJudgeNodeFailsClosedOnUnjudgedEffect proves the slice 3f fix directly
// at the judgeNode level: an effect of a kind NO policy in DefaultPolicies
// (or any other policy slice) applies to must fold the node to
// MarkInsufficient, never to MarkPermitted by default. cmddesc.EffectKind(99)
// is a throwaway kind value no real Effect construction site ever produces
// and no policy's Judge ever recognises — it stands in for "a future effect
// kind this policy set has not been taught yet", the exact shape the
// `export FOO=bar` defect (slice 3e) turned out to be for EffectEnv.
func TestJudgeNodeFailsClosedOnUnjudgedEffect(t *testing.T) {
	n := &effectgraph.Node{
		Mark:    effectgraph.MarkUnjudged,
		Effects: []cmddesc.Effect{{Kind: cmddesc.EffectKind(99)}},
	}
	judgeNode(n, DefaultPolicies(), PolicyContext{})
	if n.Mark != effectgraph.MarkInsufficient {
		t.Fatalf("mark = %s, want insufficient (reason: %q)", n.Mark, n.MarkReason)
	}
	if !strings.Contains(n.MarkReason, "no policy judges this effect") {
		t.Errorf("reason = %q, want it to name the unjudged effect", n.MarkReason)
	}
}

// TestJudgeNodeNoEffectsIsPermitted: the fail-closed fold only fires on an
// effect no policy applies to — a node with zero effects at all (nothing to
// fail closed on) still folds to Permitted, exactly as before this slice.
func TestJudgeNodeNoEffectsIsPermitted(t *testing.T) {
	n := &effectgraph.Node{Mark: effectgraph.MarkUnjudged}
	judgeNode(n, DefaultPolicies(), PolicyContext{})
	if n.Mark != effectgraph.MarkPermitted {
		t.Fatalf("mark = %s, want permitted", n.Mark)
	}
}

// TestJudgeNodeForbiddenOutranksUnjudged: a Forbidden finding on one effect
// still wins the node even when another effect on the same node is
// unjudged — the fail-closed fold does not change Forbidden's precedence.
func TestJudgeNodeForbiddenOutranksUnjudged(t *testing.T) {
	n := &effectgraph.Node{
		Mark: effectgraph.MarkUnjudged,
		Effects: []cmddesc.Effect{
			{Kind: cmddesc.EffectKind(99)},
			{Kind: cmddesc.EffectRemote, Resource: "origin", Operation: "force-push"},
		},
	}
	judgeNode(n, DefaultPolicies(), PolicyContext{})
	if n.Mark != effectgraph.MarkForbidden {
		t.Fatalf("mark = %s, want forbidden (reason: %q)", n.Mark, n.MarkReason)
	}
}
