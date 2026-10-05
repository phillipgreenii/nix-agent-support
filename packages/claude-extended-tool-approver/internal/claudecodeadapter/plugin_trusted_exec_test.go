package claudecodeadapter

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectpolicy"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestEvaluate_PluginInstructedTrustedCheckoutExec pins the pb-plugin
// (pg2-cjfpy.1) command forms whose approval depends on the working directory
// being inside a real git/go workspace (TrustedCheckoutExec, ADR 0053): the
// pre-commit hook runner, prek, and a local `nix build` of the repo's own
// flake. The golden corpus cannot carry approve rows for them because
// corpus.json fixtures name a synthetic cwd ("/repo") that is not a real
// workspace, so the same forms are pinned here against a fixture with a real
// `.git` marker. Run from a directory that is NOT a workspace they must stay
// abstain (the not-a-workspace subtest), so the approval is never unconditional.
func TestEvaluate_PluginInstructedTrustedCheckoutExec(t *testing.T) {
	forms := []string{
		"pg-hooks run pre-commit a.go b.go",
		"pg-hooks run pre-commit a.go b.go > /tmp/hooks.log 2>&1; echo DONE >> /tmp/hooks.log",
		"pg-hooks fix",
		"prek run --files a.go b.go",
		"nix build .#checks.aarch64-darwin.claude-extended-tool-approver-go-tests",
		"nix build .#pb -o /tmp/pg2-cjfpy1-scratch/pb-result",
	}
	eval := func(t *testing.T, cwd, command string) evalcontract.Response {
		t.Helper()
		raw, err := json.Marshal(hookio.BashToolInput{Command: command})
		if err != nil {
			t.Fatal(err)
		}
		input := &hookio.HookInput{CWD: cwd, ToolName: "Bash", ToolInput: raw}
		resp, err := Evaluate(input, RequestConfig{ProjectRoot: cwd}, cmddesc.DefaultRegistry(), effectpolicy.DefaultPolicies(), effectpolicy.DefaultGraphPolicies())
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	root := fixture(t)
	for _, c := range forms {
		t.Run("workspace/"+c, func(t *testing.T) {
			if resp := eval(t, root, c); resp.Decision != evalcontract.Approve {
				t.Fatalf("decision = %s, want approve — reason: %s", resp.Decision, resp.Reason)
			}
		})
	}
	bare := tempDirUnderRealTmp(t)
	for _, c := range forms {
		t.Run("not-a-workspace/"+c, func(t *testing.T) {
			if resp := eval(t, bare, c); resp.Decision == evalcontract.Approve {
				t.Fatalf("decision = approve outside any workspace, want abstain — reason: %s", resp.Reason)
			}
		})
	}
}
