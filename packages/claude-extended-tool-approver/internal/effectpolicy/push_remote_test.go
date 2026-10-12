package effectpolicy

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/targetspec"
)

// TestPushRemoteResolution (packet tc-o14i5.3.10, R6/D8/R11): push is judged
// by the R6 rules only against a configured remote NAME or no remote
// argument; a URL or filesystem path remote abstains whatever the rest of the
// command says.
func TestPushRemoteResolution(t *testing.T) {
	root, _ := fixture(t)
	reg := cmddesc.DefaultRegistry()
	cases := []struct {
		name, command string
		want          evalcontract.Decision
	}{
		{"named remote, plain ref", "git push origin main", evalcontract.Approve},
		{"named remote, lease", "git push --force-with-lease origin main", evalcontract.Approve},
		{"named remote, force", "git push --force origin main", evalcontract.Reject},
		{"no remote argument", "git push", evalcontract.Approve},
		{"no remote argument, lease", "git push --force-with-lease", evalcontract.Approve},
		{"no remote argument, force", "git push --force", evalcontract.Reject},
		{"URL remote", "git push https://example.invalid/r.git main", evalcontract.Abstain},
		{"URL remote, lease", "git push --force-with-lease https://example.invalid/r.git main", evalcontract.Abstain},
		{"scp-style URL remote", "git push git@example.invalid:r.git main", evalcontract.Abstain},
		{"path remote", "git push ../other main", evalcontract.Abstain},
		{"absolute path remote", "git push /srv/repo.git main", evalcontract.Abstain},
		{"URL remote, --all", "git push --all https://example.invalid/r.git", evalcontract.Abstain},
		{"URL remote, delete of non-primary", "git push https://example.invalid/r.git --delete feature", evalcontract.Abstain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := Evaluate(evalcontract.Request{Command: tc.command, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
			if resp.Decision != tc.want {
				t.Errorf("%q: decision = %s, want %s (%s)", tc.command, resp.Decision, tc.want, resp.Reason)
			}
		})
	}
}

// TestPushRemoteTargetSpec: the K4 target-spec lookup is consulted for a
// push remote NAME. A production-classed git-remote rejects; a listed
// trusted-dev or an unlisted remote defers to the R6 rules; with no target
// spec adopted (nil) nothing changes.
func TestPushRemoteTargetSpec(t *testing.T) {
	prod := targetspec.TargetClassProduction
	dev := targetspec.TargetClassTrustedDev
	effect := cmddesc.Effect{Kind: cmddesc.EffectRemote, Resource: "origin", Operation: "push"}
	for _, tc := range []struct {
		name        string
		class       *targetspec.TargetClass
		wantApplies bool
		wantVerdict FindingVerdict
	}{
		{"production remote", &prod, true, Forbidden},
		{"trusted-dev remote defers", &dev, false, 0},
		{"unlisted remote defers", nil, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := []targetLookupEntry{{"other-remote", targetspec.TargetKindGitRemote, targetspec.TargetClassProduction}}
			if tc.class != nil {
				entries = append(entries, targetLookupEntry{"origin", targetspec.TargetKindGitRemote, *tc.class})
			}
			ctx := PolicyContext{TargetSpec: buildTargetLookup(t, entries)}
			f, applies := TargetSpecPolicy{}.Judge(effect, ctx)
			if applies != tc.wantApplies || (applies && f.Verdict != tc.wantVerdict) {
				t.Errorf("applies=%v verdict=%s, want applies=%v verdict=%s", applies, f.Verdict, tc.wantApplies, tc.wantVerdict)
			}
		})
	}
	if _, applies := (TargetSpecPolicy{}).Judge(effect, PolicyContext{}); applies {
		t.Error("nil TargetSpec must keep the policy a no-op")
	}
}
