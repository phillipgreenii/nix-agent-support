package effectpolicy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
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
// "force-push" and "delete-ref" are Forbidden, "dolt-server" defaults to
// Unknown and is Forbidden only when PolicyContext.RemoteLifecycle configures
// the effect's Resource ("dolt") as "reject" (slice 3u, operator ruling on
// tc-vn5z), and an unrecognised Operation fails closed to Unknown rather than
// guessing. It never returns Permitted.
func TestRemoteMutationPolicy(t *testing.T) {
	remote := func(op string, dynamic bool) cmddesc.Effect {
		return cmddesc.Effect{Kind: cmddesc.EffectRemote, Resource: "origin", Operation: op, Dynamic: dynamic}
	}
	cases := []struct {
		name    string
		e       cmddesc.Effect
		ctx     PolicyContext
		verdict FindingVerdict
	}{
		{"dynamic resource", remote("push", true), PolicyContext{}, Unknown},
		{"dynamic read is still unknown", remote("read", true), PolicyContext{}, Unknown},
		{"read permitted", remote("read", false), PolicyContext{}, Permitted},
		{"push needs consent", remote("push", false), PolicyContext{}, Unknown},
		{"mutate needs consent", remote("mutate", false), PolicyContext{}, Unknown},
		{"force-push forbidden", remote("force-push", false), PolicyContext{}, Forbidden},
		{"delete-ref forbidden", remote("delete-ref", false), PolicyContext{}, Forbidden},
		{"dolt-server abstains by default", remote("dolt-server", false), PolicyContext{}, Unknown},
		{"dolt-server unaffected by an unrelated target's configuration", remote("dolt-server", false), PolicyContext{RemoteLifecycle: map[string]string{"other": "reject"}}, Unknown},
		{"dolt-server forbidden when the operator configures reject", remote("dolt-server", false), PolicyContext{RemoteLifecycle: map[string]string{"origin": "reject"}}, Forbidden},
		{"unrecognised operation fails closed", remote("mirror", false), PolicyContext{}, Unknown},
	}
	for _, tc := range cases {
		f, applies := RemoteMutation{}.Judge(tc.e, tc.ctx)
		if !applies || f.Verdict != tc.verdict {
			t.Errorf("%s: applies=%v verdict=%s (%s), want %s", tc.name, applies, f.Verdict, f.Reason, tc.verdict)
		}
		if f.Verdict == Permitted && tc.e.Operation != "read" {
			t.Errorf("%s: remote-mutation must never permit a non-read operation", tc.name)
		}
	}
	if _, applies := (RemoteMutation{}).Judge(cmddesc.Effect{Kind: cmddesc.EffectPath, Path: "x"}, PolicyContext{}); applies {
		t.Error("applied to a path effect")
	}
	// slice 3y (tc-lc8f item 4f; tc-vn5z item 3): a Family!="" EffectRemote
	// (kubectl's own) is excluded — KubeContextPolicy owns it instead.
	if _, applies := (RemoteMutation{}).Judge(cmddesc.Effect{Kind: cmddesc.EffectRemote, Operation: "read", Resource: "dev", Family: "kubectl"}, PolicyContext{}); applies {
		t.Error("RemoteMutation applied to a Family==\"kubectl\" effect; KubeContextPolicy must own it alone")
	}
}

// TestKubeContextPolicy exercises KubeContextPolicy's own ladder (slice 3y,
// tc-lc8f item 4f; tc-vn5z item 3): a dynamic or unnamed context is Unknown;
// a listed context permits only its own Allow classes and Forbids anything
// else; an unlisted context falls back to KubeContextDefaultAllow (Unknown
// when that default is empty); a DryRun-marked "mutation" is judged as a
// "read". A non-kubectl (Family=="") EffectRemote, and any non-EffectRemote
// effect, does not apply.
func TestKubeContextPolicy(t *testing.T) {
	kube := func(op, context string, dryRun, dynamic bool) cmddesc.Effect {
		return cmddesc.Effect{Kind: cmddesc.EffectRemote, Operation: op, Resource: context, Family: "kubectl", DryRun: dryRun, Dynamic: dynamic}
	}
	ctx := PolicyContext{
		KubeContexts: map[string]evalcontract.KubeContextRule{
			"dev":  {Allow: []string{"read", "mutation", "exec"}},
			"prod": {Allow: []string{"read"}},
		},
	}
	cases := []struct {
		name    string
		e       cmddesc.Effect
		ctx     PolicyContext
		verdict FindingVerdict
	}{
		{"dynamic context", kube("read", "dev", false, true), ctx, Unknown},
		{"no context (empty Resource)", kube("read", "", false, false), ctx, Unknown},
		{"dev read permitted", kube("read", "dev", false, false), ctx, Permitted},
		{"dev mutation permitted", kube("mutation", "dev", false, false), ctx, Permitted},
		{"dev exec permitted", kube("exec", "dev", false, false), ctx, Permitted},
		{"prod read permitted", kube("read", "prod", false, false), ctx, Permitted},
		{"prod mutation forbidden", kube("mutation", "prod", false, false), ctx, Forbidden},
		{"prod exec forbidden", kube("exec", "prod", false, false), ctx, Forbidden},
		{"unlisted context abstains by default", kube("read", "staging", false, false), ctx, Unknown},
		{"unlisted context, unrelated default configured", kube("read", "staging", false, false), PolicyContext{KubeContextDefaultAllow: []string{"exec"}}, Unknown},
		{"unlisted context, matching default configured", kube("read", "staging", false, false), PolicyContext{KubeContextDefaultAllow: []string{"read"}}, Permitted},
		{"prod dry-run mutation judged as read: permitted", kube("mutation", "prod", true, false), ctx, Permitted},
		{"dev dry-run mutation judged as read: still permitted", kube("mutation", "dev", true, false), ctx, Permitted},
	}
	for _, tc := range cases {
		f, applies := KubeContextPolicy{}.Judge(tc.e, tc.ctx)
		if !applies || f.Verdict != tc.verdict {
			t.Errorf("%s: applies=%v verdict=%s (%s), want %s", tc.name, applies, f.Verdict, f.Reason, tc.verdict)
		}
	}
	if _, applies := (KubeContextPolicy{}).Judge(cmddesc.Effect{Kind: cmddesc.EffectRemote, Operation: "read", Resource: "origin"}, ctx); applies {
		t.Error("applied to a Family==\"\" (non-kubectl) remote effect")
	}
	if _, applies := (KubeContextPolicy{}).Judge(cmddesc.Effect{Kind: cmddesc.EffectPath, Path: "x"}, ctx); applies {
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

// TestChdirScopedPolicy: a static cd target is Permitted, a dynamic one (a
// live expansion, or `-`) is Unknown; a non-chdir effect does not apply.
func TestChdirScopedPolicy(t *testing.T) {
	if f, applies := (ChdirScoped{}).Judge(cmddesc.Effect{Kind: cmddesc.EffectChdir, Path: "sub"}, PolicyContext{}); !applies || f.Verdict != Permitted {
		t.Errorf("static: applies=%v verdict=%s", applies, f.Verdict)
	}
	if f, applies := (ChdirScoped{}).Judge(cmddesc.Effect{Kind: cmddesc.EffectChdir, Path: "$D", Dynamic: true}, PolicyContext{}); !applies || f.Verdict != Unknown {
		t.Errorf("dynamic: applies=%v verdict=%s", applies, f.Verdict)
	}
	if f, applies := (ChdirScoped{}).Judge(cmddesc.Effect{Kind: cmddesc.EffectChdir, Path: "-", Dynamic: true, Detail: "previous directory"}, PolicyContext{}); !applies || f.Verdict != Unknown || f.Reason == "" {
		t.Errorf("previous dir: applies=%v verdict=%s reason=%q", applies, f.Verdict, f.Reason)
	}
	if _, applies := (ChdirScoped{}).Judge(cmddesc.Effect{Kind: cmddesc.EffectPath, Path: "x"}, PolicyContext{}); applies {
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

// TestTrustedCheckoutExecPolicy (slice 3x, tc-lc8f item 4e): CWD inside the
// shared fixture (a declared git workspace) is Permitted; CWD in a bare
// temp directory with no git/go marker is Unknown; a non-exec effect does
// not apply.
func TestTrustedCheckoutExecPolicy(t *testing.T) {
	root, _ := fixture(t)
	exec := cmddesc.Effect{Kind: cmddesc.EffectExec, Detail: "trusted checkout code", Source: "go test"}

	if f, applies := (TrustedCheckoutExec{}).Judge(exec, PolicyContext{CWD: root}); !applies || f.Verdict != Permitted {
		t.Errorf("inside git workspace: applies=%v verdict=%s (%s)", applies, f.Verdict, f.Reason)
	}
	if f, applies := (TrustedCheckoutExec{}).Judge(exec, PolicyContext{CWD: t.TempDir()}); !applies || f.Verdict != Unknown {
		t.Errorf("outside any declared workspace: applies=%v verdict=%s (%s)", applies, f.Verdict, f.Reason)
	}
	if f, applies := (TrustedCheckoutExec{}).Judge(exec, PolicyContext{}); !applies || f.Verdict != Unknown {
		t.Errorf("no CWD: applies=%v verdict=%s", applies, f.Verdict)
	}
	if _, applies := (TrustedCheckoutExec{}).Judge(cmddesc.Effect{Kind: cmddesc.EffectPath, Path: "x"}, PolicyContext{CWD: root}); applies {
		t.Error("applied to a path effect")
	}
}

// TestTrustedCheckoutExecPolicy_BuildToolFamily (tc-8og1 item 3 sub-slice
// 3; tc-vn5z Q1-Q5, ruled 2026-09-08) exercises judgeBuildToolVerb's own
// ladder: a Family!="" EffectExec is routed here, not the git/go
// marker-workspace branch, and Permitted requires BOTH an operator
// BuildToolVerbs declaration AND independent confirmation from
// deletable.DiscoveredVerbs (slice 3ag) that the verb is literally
// defined in-project — either alone abstains (Unknown), per Q3's "abstain
// otherwise, never guess".
//
// The "no BuildToolVerbs entries at all" case is this slice's migration-
// safety proof (the "5. SIZING" plan's own "needs a migration-safety test
// like TestBuildtools_EmptyConfig_JustAbstains's sibling" — production's
// analogue in internal/rules/buildtools/buildtools_test.go): an
// absent/empty operator config must leave the safe abstain default
// unchanged, exactly like RemoteLifecycle/KubeContexts/RemotePaths before
// it.
func TestTrustedCheckoutExecPolicy_BuildToolFamily(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "justfile"), []byte("check:\n    echo ok\n\nbuild:\n    echo build\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	noJustfile := t.TempDir()

	exec := func(tool, verb string, dynamic bool) cmddesc.Effect {
		return cmddesc.Effect{Kind: cmddesc.EffectExec, Family: tool, Operation: verb, Dynamic: dynamic}
	}
	justCheck := []evalcontract.VerbScopedApproval{{Tool: "just", Verb: "check"}}

	cases := []struct {
		name    string
		e       cmddesc.Effect
		ctx     PolicyContext
		verdict FindingVerdict
	}{
		{"dynamic verb", exec("just", "$V", true), PolicyContext{CWD: root, BuildToolVerbs: justCheck}, Unknown},
		{"no BuildToolVerbs configured at all (migration safety)", exec("just", "check", false), PolicyContext{CWD: root}, Unknown},
		{"unrelated tool configured, this one absent", exec("just", "check", false), PolicyContext{CWD: root, BuildToolVerbs: []evalcontract.VerbScopedApproval{{Tool: "npm", Verb: "build"}}}, Unknown},
		{"verb not declared for this tool", exec("just", "build", false), PolicyContext{CWD: root, BuildToolVerbs: justCheck}, Unknown},
		{"declared but workspace has no justfile", exec("just", "check", false), PolicyContext{CWD: noJustfile, BuildToolVerbs: justCheck}, Unknown},
		{"declared and default class, workspace confirms: permitted", exec("just", "check", false), PolicyContext{CWD: root, BuildToolVerbs: justCheck}, Permitted},
		{"declared with explicit class=project-tied, workspace confirms: permitted", exec("just", "check", false), PolicyContext{CWD: root, BuildToolVerbs: []evalcontract.VerbScopedApproval{{Tool: "just", Verb: "check", Class: evalcontract.VerbClassProjectTied}}}, Permitted},
		{"declared with an unrecognised/future class: not yet judged", exec("just", "check", false), PolicyContext{CWD: root, BuildToolVerbs: []evalcontract.VerbScopedApproval{{Tool: "just", Verb: "check", Class: "wrapper"}}}, Unknown},
	}
	for _, tc := range cases {
		f, applies := (TrustedCheckoutExec{}).Judge(tc.e, tc.ctx)
		if !applies || f.Verdict != tc.verdict {
			t.Errorf("%s: applies=%v verdict=%s (%s), want %s", tc.name, applies, f.Verdict, f.Reason, tc.verdict)
		}
	}
	// Family=="" is untouched by this branch — the pre-existing git/go
	// marker-workspace ladder still governs, proven by
	// TestTrustedCheckoutExecPolicy above; not re-asserted here.
}

// TestRemotePathGuard (slice 3aa, tc-lc8f item 4g; tc-vn5z item 4): a PATH
// effect tagged Remote abstains by default under every one of the four
// wrapped policies, even where the LOCAL verdict would have been Forbidden
// (a secret, a read-only zone) or Permitted (an ordinary writable path) —
// the guard overrides the wrapped policy's own verdict unconditionally once
// it applies. The categorized-path hook (PolicyContext.RemotePaths)
// overrides that default per RemotePathRule's own proposed taxonomy; an
// unmatched host/prefix, or an unrecognised category, falls back to the
// ordinary abstain. A non-remote effect is untouched (regression: the
// wrapped policy's own verdict rides straight through).
func TestRemotePathGuard(t *testing.T) {
	root, home := fixture(t)
	pe := patheval.NewWithCWD(root, root)
	baseCtx := PolicyContext{PathEval: pe, CWD: root}

	read := func(p, remote string) cmddesc.Effect {
		return cmddesc.Effect{Kind: cmddesc.EffectPath, Access: cmddesc.AccessRead, Path: p, Remote: remote}
	}
	write := func(p, remote string) cmddesc.Effect {
		return cmddesc.Effect{Kind: cmddesc.EffectPath, Access: cmddesc.AccessTruncate, Path: p, Remote: remote}
	}
	del := func(p, remote string) cmddesc.Effect {
		return cmddesc.Effect{Kind: cmddesc.EffectPath, Access: cmddesc.AccessDelete, Path: p, Remote: remote}
	}

	// Local (non-remote) effects are UNCHANGED by the wrap: a secret read is
	// still Forbidden, a read-only-zone write is still Forbidden.
	if f, applies := (remotePathGuard{NoReadOfSecretPath{}}).Judge(read(filepath.Join(home, ".ssh", "id_rsa"), ""), baseCtx); !applies || f.Verdict != Forbidden {
		t.Errorf("local secret read: applies=%v verdict=%s, want Forbidden", applies, f.Verdict)
	}
	if f, applies := (remotePathGuard{NoWriteToReadOnlyPath{}}).Judge(write("/nix/store/x", ""), baseCtx); !applies || f.Verdict != Forbidden {
		t.Errorf("local read-only-zone write: applies=%v verdict=%s, want Forbidden", applies, f.Verdict)
	}

	// Remote effects abstain by DEFAULT, even where the local verdict would
	// have been Forbidden or Permitted.
	remoteCases := []struct {
		name   string
		policy Policy
		e      cmddesc.Effect
	}{
		{"remote secret read no longer forbidden", NoReadOfSecretPath{}, read(filepath.Join(home, ".ssh", "id_rsa"), "host")},
		{"remote read-only-zone write no longer forbidden", NoWriteToReadOnlyPath{}, write("/nix/store/x", "host")},
		{"remote ordinary read no longer permitted", NoReadOfUnreadablePath{}, read("README.md", "host")},
		{"remote delete-of-root no longer forbidden", DeleteAccess{}, del("/", "host")},
	}
	for _, tc := range remoteCases {
		f, applies := (remotePathGuard{tc.policy}).Judge(tc.e, baseCtx)
		if !applies || f.Verdict != Unknown {
			t.Errorf("%s: applies=%v verdict=%s (%s), want Unknown", tc.name, applies, f.Verdict, f.Reason)
		}
	}

	// The categorized-path hook overrides the default per host+prefix,
	// first match wins; a non-matching host/prefix or an unrecognised
	// category falls back to the ordinary abstain.
	hookCtx := baseCtx
	hookCtx.RemotePaths = map[string][]evalcontract.RemotePathRule{
		"host": {
			{Prefix: "/var/log", Category: "read-only"},
			{Prefix: "/srv/data", Category: "writable"},
			{Prefix: "/srv/scratch", Category: "deletable"},
			{Prefix: "/srv/locked", Category: "protected"},
			{Prefix: "/srv/mystery", Category: "not-a-real-category"},
		},
	}
	hookCases := []struct {
		name    string
		policy  Policy
		e       cmddesc.Effect
		verdict FindingVerdict
	}{
		{"read-only: read permitted", NoReadOfUnreadablePath{}, read("/var/log/syslog", "host"), Permitted},
		{"read-only: write forbidden", NoWriteToReadOnlyPath{}, write("/var/log/syslog", "host"), Forbidden},
		{"writable: write permitted", NoWriteToReadOnlyPath{}, write("/srv/data/x", "host"), Permitted},
		{"writable: delete needs consent", DeleteAccess{}, del("/srv/data/x", "host"), Unknown},
		{"deletable: delete permitted", DeleteAccess{}, del("/srv/scratch/x", "host"), Permitted},
		{"protected: read forbidden", NoReadOfUnreadablePath{}, read("/srv/locked/x", "host"), Forbidden},
		{"protected: write forbidden", NoWriteToReadOnlyPath{}, write("/srv/locked/x", "host"), Forbidden},
		{"unrecognised category falls back to abstain", NoReadOfUnreadablePath{}, read("/srv/mystery/x", "host"), Unknown},
		{"non-matching prefix falls back to abstain", NoReadOfUnreadablePath{}, read("/var/lib/other", "host"), Unknown},
		{"non-matching host falls back to abstain", NoReadOfUnreadablePath{}, read("/var/log/syslog", "otherhost"), Unknown},
	}
	for _, tc := range hookCases {
		f, applies := (remotePathGuard{tc.policy}).Judge(tc.e, hookCtx)
		if !applies || f.Verdict != tc.verdict {
			t.Errorf("%s: applies=%v verdict=%s (%s), want %s", tc.name, applies, f.Verdict, f.Reason, tc.verdict)
		}
	}
}
