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

// TestStdioIsLocalPolicy: every EffectStdio effect is Permitted; a non-stdio
// effect does not apply.
func TestStdioIsLocalPolicy(t *testing.T) {
	f, applies := StdioIsLocal{}.Judge(cmddesc.Effect{Kind: cmddesc.EffectStdio, Stream: cmddesc.StreamStdout}, PolicyContext{})
	if !applies || f.Verdict != Permitted {
		t.Errorf("stdout: applies=%v verdict=%s", applies, f.Verdict)
	}
	if _, applies := (StdioIsLocal{}).Judge(cmddesc.Effect{Kind: cmddesc.EffectPath, Path: "x"}, PolicyContext{}); applies {
		t.Error("applied to a path effect")
	}
}

// TestProgramInterpretedPolicy: every EffectProgram effect is Permitted; a
// non-program effect does not apply.
func TestProgramInterpretedPolicy(t *testing.T) {
	f, applies := ProgramInterpreted{}.Judge(cmddesc.Effect{Kind: cmddesc.EffectProgram, Dialect: "sed", Program: "s/a/b/"}, PolicyContext{})
	if !applies || f.Verdict != Permitted {
		t.Errorf("program: applies=%v verdict=%s", applies, f.Verdict)
	}
	if _, applies := (ProgramInterpreted{}).Judge(cmddesc.Effect{Kind: cmddesc.EffectPath, Path: "x"}, PolicyContext{}); applies {
		t.Error("applied to a path effect")
	}
}

// TestEnvAssignmentPolicy: reads are always Permitted; a dynamic NAME is
// Unknown; an injector name is Forbidden; an injector-ask/ask name is
// Unknown; any other static name is Permitted. It never returns Forbidden
// for a read, and a non-env effect does not apply.
func TestEnvAssignmentPolicy(t *testing.T) {
	set := func(name string, dynamic bool) cmddesc.Effect {
		return cmddesc.Effect{Kind: cmddesc.EffectEnv, EnvName: name, EnvSet: true, Dynamic: dynamic}
	}
	read := func(name string) cmddesc.Effect {
		return cmddesc.Effect{Kind: cmddesc.EffectEnv, EnvName: name, EnvSet: false}
	}
	cases := []struct {
		name    string
		e       cmddesc.Effect
		verdict FindingVerdict
	}{
		{"read is always permitted", read("LD_PRELOAD"), Permitted},
		{"dynamic name is unknown", set("$NAME", true), Unknown},
		{"injector var forbidden", set("LD_PRELOAD", false), Forbidden},
		{"another injector var forbidden", set("ZDOTDIR", false), Forbidden},
		{"injector-ask var unknown", set("ENV", false), Unknown},
		{"ask var PATH unknown", set("PATH", false), Unknown},
		{"ask var HOME unknown", set("HOME", false), Unknown},
		{"benign name permitted", set("FOO", false), Permitted},
	}
	for _, tc := range cases {
		f, applies := EnvAssignment{}.Judge(tc.e, PolicyContext{})
		if !applies || f.Verdict != tc.verdict {
			t.Errorf("%s: applies=%v verdict=%s (%s), want %s", tc.name, applies, f.Verdict, f.Reason, tc.verdict)
		}
	}
	if _, applies := (EnvAssignment{}).Judge(cmddesc.Effect{Kind: cmddesc.EffectPath, Path: "x"}, PolicyContext{}); applies {
		t.Error("applied to a path effect")
	}
}
