package setup

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/engine"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
)

// This file is the EVIDENCE for ADR 0043's error paths.
//
// The corpus replay differential cannot reach them: a replayed row carries a
// well-formed tool_input by construction (it was logged from a real tool call), and
// a resolver subprocess timeout is not reproducible from stored data. ADR 0043
// therefore requires each verdict-producing error site to be covered by a unit test,
// and states plainly that the replay MUST NOT be cited as evidence for them.
//
// It is written against setup.RuleChain rather than rule-by-rule ON PURPOSE, for the
// same reason engine_integration_test.go is (pg2-v94d7): a per-rule copy of this
// table would silently omit the next rule somebody adds, and it is precisely the
// omitted rule whose error path nobody would notice. Driving the real chain means a
// new rule is covered the moment it is registered.
//
// Sites NOT reachable from here — they need an injected failure rather than a
// malformed input — are covered where they live:
//
//	gh's two resolver sites            internal/rules/gh (stub BranchResolver)
//	webfetch's url.Parse site          internal/rules/webfetch (malformed URL)
//	killshell's fail-closed Ask        internal/rules/killshell + the ordering suite

// malformedToolInput is not valid JSON, so every accessor in hookio/input.go
// (BashCommand, FilePath, SearchPath) fails on it. That is the malformed-tool_input
// class ADR 0043 names as unreachable by replay.
var malformedToolInput = json.RawMessage(`{`)

func chainForErrorTests(t *testing.T) []hookio.RuleModule {
	t.Helper()
	const projectRoot = "/Users/testuser/workspace/my-project"
	cfg := configrules.Load(commandBlocksFixture) // configured ssh/vault/curl/monorepo
	pe := patheval.NewWithCWD(projectRoot, projectRoot)
	eng := engine.New()
	eng.SetPathEvaluator(pe)
	chain := RuleChain(eng, pe, cfg, nil)
	eng.RegisterRules(chain...)
	return chain
}

// TestEveryRuleOnMalformedInput_NeverApproves is the SAFETY property, asserted for
// every rule in the production chain and for every tool class that reaches one.
//
// A rule that cannot read its input must never green-light the call. Before ADR 0043
// this was held by every such site folding to the Abstain sentinel; now it is held by
// the error channel, and this is what proves the conversion did not turn any of them
// into an approval (the failure mode that would matter, since Approve is the
// Decision zero value and `RuleResult{}` is what an error site returns).
func TestEveryRuleOnMalformedInput_NeverApproves(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	chain := chainForErrorTests(t)

	tools := []string{"Bash", "Read", "Write", "Edit", "MultiEdit", "Delete", "Glob", "Grep", "KillShell", "WebFetch"}
	for _, rule := range chain {
		for _, tool := range tools {
			in := &hookio.HookInput{
				ToolName:  tool,
				CWD:       "/Users/testuser/workspace/my-project",
				ToolInput: malformedToolInput,
			}
			res, err := rule.Evaluate(in)
			if err == nil && res.Decision == hookio.Approve {
				t.Errorf("%s on malformed %s tool_input returned APPROVE (%q) — a rule that cannot read its input must never approve",
					rule.Name(), tool, res.Reason)
			}
			// And whatever it returns must be a legible outcome: a nil error means the
			// RuleResult IS the verdict, so it must name its module.
			if err == nil && res.Module == "" {
				t.Errorf("%s on malformed %s tool_input returned a verdict (%s) with no Module — "+
					"the engine attributes the decision by Module", rule.Name(), tool, res.Decision)
			}
		}
	}
}

// TestBashErrorSitesReportGenuineFailure pins the SPLIT itself for every rule with a
// Bash-gated error site: on a malformed Bash tool_input the rule must report a
// GENUINE failure, not ErrNotApplicable.
//
// The distinction is the whole point of ADR 0043 and it is invisible to any
// verdict-level assertion — both outcomes continue the chain. Only this test
// separates "I do not govern Bash" from "Bash is mine and I could not read it",
// which is what makes a systematically-failing rule detectable in the metrics sink.
//
// The expected set is stated positively (which rules DO own Bash and DO read the
// command) so that a rule losing its error site fails here rather than silently
// reverting to the conflated behaviour.
func TestBashErrorSitesReportGenuineFailure(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	chain := chainForErrorTests(t)

	// Rules whose Evaluate reads the Bash command and therefore has a genuine-error
	// site. Derived by reading each rule; kept here as the assertion, not as a
	// restatement of the chain.
	wantGenuineError := map[string]bool{
		"config-rules":       true,
		"git-directory":      true,
		"dangerous-commands": true,
		"secrets":            true,
		"env-vars":           true,
		"assume":             true,
		"primary-commit":     true,
		"primary-push":       true,
		"git":                true,
		"gh":                 true,
		"monorepo":           true,
		"nix":                true,
		"docker":             true,
		"curl":               true,
		"ssh":                true,
		"vault":              true,
		"safe-commands":      true,
		"kubectl":            true,
		"build-tools":        true,
		"sqlite3":            true,
	}

	seen := map[string]bool{}
	for _, rule := range chain {
		in := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:       "/Users/testuser/workspace/my-project",
			ToolInput: malformedToolInput,
		}
		_, err := rule.Evaluate(in)
		genuine := err != nil && !errors.Is(err, hookio.ErrNotApplicable)
		seen[rule.Name()] = genuine

		if want := wantGenuineError[rule.Name()]; genuine != want {
			t.Errorf("%s on malformed Bash tool_input: genuine error = %v, want %v (err=%v)",
				rule.Name(), genuine, want, err)
		}
		if genuine && !strings.Contains(err.Error(), rule.Name()) {
			t.Errorf("%s's error %q does not name the rule — the metrics sink keys on the rule name, "+
				"but a human reading stderr needs the message to say it too", rule.Name(), err)
		}
	}
	for name := range wantGenuineError {
		if _, present := seen[name]; !present {
			t.Errorf("rule %q is expected to have a Bash error site but is no longer in the chain — "+
				"either it was removed (drop it here) or RuleChain changed unexpectedly", name)
		}
	}
}

// TestFileAndSearchErrorSitesReportGenuineFailure is the same assertion for the two
// non-Bash accessor families. Only the rules that OWN a file/search tool have a site
// here, which is why the expected sets are much smaller than the Bash one.
func TestFileAndSearchErrorSitesReportGenuineFailure(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	chain := chainForErrorTests(t)

	cases := []struct {
		tool string
		want map[string]bool
	}{
		{"Read", map[string]bool{"git-directory": true, "secrets": true, "path-safety": true}},
		{"Write", map[string]bool{"git-directory": true, "secrets": true, "path-safety": true}},
		{"Grep", map[string]bool{"git-directory": true, "secrets": true, "path-safety": true}},
		{"Glob", map[string]bool{"git-directory": true, "secrets": true, "path-safety": true}},
	}

	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			for _, rule := range chain {
				in := &hookio.HookInput{
					ToolName:  tc.tool,
					CWD:       "/Users/testuser/workspace/my-project",
					ToolInput: malformedToolInput,
				}
				_, err := rule.Evaluate(in)
				genuine := err != nil && !errors.Is(err, hookio.ErrNotApplicable)
				if want := tc.want[rule.Name()]; genuine != want {
					t.Errorf("%s on malformed %s tool_input: genuine error = %v, want %v (err=%v)",
						rule.Name(), tc.tool, genuine, want, err)
				}
			}
		})
	}
}

// TestKillShellKeepsItsFailClosedAsk pins ADR 0043's one named error-policy
// carve-out at CHAIN scope, on the malformed-input shape the ordering suite does not
// cover (it uses `{}`, valid JSON with no shell_id).
//
// Routing this through the error channel would make the chain continue, fall off the
// end, and manufacture the terminal NoOpinion — which emits `{}` and AUTO-APPROVES
// killing an unverifiable background shell in `auto` mode. So the rule must answer
// with a nil error and its own Ask.
func TestKillShellKeepsItsFailClosedAsk(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	chain := chainForErrorTests(t)

	for _, rule := range chain {
		if rule.Name() != "killshell" {
			continue
		}
		for _, ti := range []json.RawMessage{malformedToolInput, json.RawMessage(`{}`), json.RawMessage(`{"shell_id":""}`)} {
			res, err := rule.Evaluate(&hookio.HookInput{ToolName: "KillShell", ToolInput: ti})
			if err != nil {
				t.Fatalf("killshell on %s returned err=%v — it MUST answer with a nil error so the engine "+
					"treats the Ask as HANDLED and stops; continuing would emit {} and auto-approve", ti, err)
			}
			if res.Decision != hookio.Ask {
				t.Errorf("killshell on %s: got %s, want ask (fail closed)", ti, res.Decision)
			}
		}
		return
	}
	t.Fatal("killshell is not in the production chain")
}

// TestPathSafetyAgentConfigWriteIsTerminalNoOpinion pins ADR 0043's ONE
// terminal-NoOpinion conversion, and pins it as a VERDICT rather than as a
// not-applicable — the two are indistinguishable at the chain's output (`{}` either
// way) but completely different in effect, because only the verdict STOPS the chain.
// If this ever became ErrNotApplicable, ADR 0041's control would be one
// later-registered approving rule away from being void.
func TestPathSafetyAgentConfigWriteIsTerminalNoOpinion(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	const projectRoot = "/Users/testuser/workspace/my-project"
	chain := chainForErrorTests(t)

	ti, err := json.Marshal(hookio.FileToolInput{FilePath: projectRoot + "/.claude/settings.local.json"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, rule := range chain {
		if rule.Name() != "path-safety" {
			continue
		}
		res, evalErr := rule.Evaluate(&hookio.HookInput{ToolName: "Write", CWD: projectRoot, ToolInput: ti})
		if evalErr != nil {
			t.Fatalf("agent-config write returned err=%v — it MUST be a terminal NoOpinion VERDICT "+
				"(ADR 0041 requires path-safety itself to stop approving; a continue would let a later "+
				"rule approve)", evalErr)
		}
		if res.Decision != hookio.NoOpinion {
			t.Errorf("agent-config write: got %s, want abstain/NoOpinion", res.Decision)
		}
		if res.Module != "path-safety" {
			t.Errorf("agent-config write Module = %q, want path-safety", res.Module)
		}
		return
	}
	t.Fatal("path-safety is not in the production chain")
}
