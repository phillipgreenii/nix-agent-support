package effectpolicy

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
)

// TestRemoteMutationPolicy: dynamic is Unknown, "push" is Unknown (consent),
// "force-push" and "delete-ref" are Forbidden, and an unrecognised Operation
// fails closed to Unknown rather than guessing. It never returns Permitted.
func TestRemoteMutationPolicy(t *testing.T) {
	remote := func(op string, dynamic bool) cmddesc.Effect {
		return cmddesc.Effect{Kind: cmddesc.EffectRemote, Resource: "origin", Operation: op, Dynamic: dynamic}
	}
	cases := []struct {
		name    string
		e       cmddesc.Effect
		verdict FindingVerdict
	}{
		{"dynamic resource", remote("push", true), Unknown},
		{"push needs consent", remote("push", false), Unknown},
		{"force-push forbidden", remote("force-push", false), Forbidden},
		{"delete-ref forbidden", remote("delete-ref", false), Forbidden},
		{"unrecognised operation fails closed", remote("mirror", false), Unknown},
	}
	for _, tc := range cases {
		f, applies := RemoteMutation{}.Judge(tc.e, PolicyContext{})
		if !applies || f.Verdict != tc.verdict {
			t.Errorf("%s: applies=%v verdict=%s (%s), want %s", tc.name, applies, f.Verdict, f.Reason, tc.verdict)
		}
		if f.Verdict == Permitted {
			t.Errorf("%s: remote-mutation must never permit", tc.name)
		}
	}
	if _, applies := (RemoteMutation{}).Judge(cmddesc.Effect{Kind: cmddesc.EffectPath, Path: "x"}, PolicyContext{}); applies {
		t.Error("applied to a path effect")
	}
}
