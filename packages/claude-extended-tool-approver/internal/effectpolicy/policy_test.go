package effectpolicy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// TestDeleteAccessPolicy walks the DeleteAccess ladder (see its doc
// comment) against the shared golden fixture: dynamic and no-evaluator are
// Unknown; a sandbox denyWrite/denyRead hit, a secret path, and a read-only
// zone are Forbidden; a writable-but-tracked path is Unknown (consent); a
// gitignored path is Permitted. A non-delete effect does not apply — and in
// particular NoWriteToReadOnlyPath must NOT apply to a delete any more, so
// each delete effect carries exactly one finding.
func TestDeleteAccessPolicy(t *testing.T) {
	root, home := fixture(t)
	pe := patheval.NewWithCWD(root, root)
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{
		DenyWrite: []string{filepath.Join(root, "sub")},
		DenyRead:  []string{filepath.Join(root, "build", "secretish")},
	})
	if err := os.MkdirAll(filepath.Join(root, "build", "secretish"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := PolicyContext{PathEval: pe, CWD: root}
	del := func(p string, dynamic bool) cmddesc.Effect {
		return cmddesc.Effect{Kind: cmddesc.EffectPath, Access: cmddesc.AccessDelete, Path: p, Dynamic: dynamic}
	}
	cases := []struct {
		name    string
		e       cmddesc.Effect
		ctx     PolicyContext
		verdict FindingVerdict
	}{
		{"dynamic path", del("$D", true), ctx, Unknown},
		{"no evaluator", del("README.md", false), PolicyContext{}, Unknown},
		{"denyWrite wins", del("sub", false), ctx, Forbidden},
		{"denyRead wins even under a gitignored dir", del("build/secretish", false), ctx, Forbidden},
		{"secret path wins even when gitignored", del(".env", false), ctx, Forbidden},
		{"ssh key", del(filepath.Join(home, ".ssh", "id_rsa"), false), ctx, Forbidden},
		{"read-only zone", del("/nix/store/x", false), ctx, Forbidden},
		{"tracked writable needs consent", del("README.md", false), ctx, Unknown},
		{"gitignored file permitted", del("ignored.log", false), ctx, Permitted},
		{"gitignored dir permitted", del("build", false), ctx, Permitted},
		{"un-ignored dir needs consent", del("newsub", false), ctx, Unknown},
		// Workspace declarations (tc-z806.3).
		{"gradle build dir permitted by declaration", del("gradleproj/build", false), ctx, Permitted},
		{"gradle src kept by git", del("gradleproj/src", false), ctx, Unknown},
		{".git protected", del(".git", false), ctx, Forbidden},
		{"home cache deletable though unzoned", del(filepath.Join(home, ".cache", "x"), false), ctx, Permitted},
		// (an unzoned, undeclared path cannot be isolated in this fixture —
		// its HOME sits under a temp root on this machine — so that ladder
		// step is pinned by internal/deletable's TestClassifyDeletableImpliesWritable)
	}
	for _, tc := range cases {
		f, applies := DeleteAccess{}.Judge(tc.e, tc.ctx)
		if !applies || f.Verdict != tc.verdict {
			t.Errorf("%s: applies=%v verdict=%s (%s), want %s", tc.name, applies, f.Verdict, f.Reason, tc.verdict)
		}
	}
	if _, applies := (DeleteAccess{}).Judge(cmddesc.Effect{Kind: cmddesc.EffectPath, Access: cmddesc.AccessModify, Path: "x"}, ctx); applies {
		t.Error("applied to a modify effect")
	}
	if _, applies := (NoWriteToReadOnlyPath{}).Judge(del("README.md", false), ctx); applies {
		t.Error("NoWriteToReadOnlyPath still applies to a delete; a delete must get exactly one finding")
	}
	if f, applies := (NoWriteToReadOnlyPath{}).Judge(cmddesc.Effect{Kind: cmddesc.EffectPath, Access: cmddesc.AccessModify, Path: "README.md"}, ctx); !applies || f.Verdict != Permitted {
		t.Errorf("NoWriteToReadOnlyPath on a modify: applies=%v verdict=%s", applies, f.Verdict)
	}
}

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
