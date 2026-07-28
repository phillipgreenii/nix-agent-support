package engine

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

type mockRule struct {
	name     string
	decision hookio.Decision
	reason   string
}

func (m *mockRule) Name() string { return m.name }
func (m *mockRule) Evaluate(*hookio.HookInput) hookio.RuleResult {
	return hookio.RuleResult{Decision: m.decision, Reason: m.reason, Module: m.name}
}

func TestEngine_FirstNonAbstainWins(t *testing.T) {
	abstain := &mockRule{name: "abstain", decision: hookio.Abstain}
	approve := &mockRule{name: "approve", decision: hookio.Approve, reason: "ok"}
	reject := &mockRule{name: "reject", decision: hookio.Reject, reason: "no"}

	e := New(abstain, approve, reject)
	input := &hookio.HookInput{ToolName: "Bash"}
	got := e.Evaluate(input)

	if got.Decision != hookio.Approve {
		t.Errorf("Decision = %v, want Approve", got.Decision)
	}
	if got.Reason != "ok" {
		t.Errorf("Reason = %q, want ok", got.Reason)
	}
}

func TestEngine_AllAbstainReturnsAbstain(t *testing.T) {
	a1 := &mockRule{name: "a1", decision: hookio.Abstain}
	a2 := &mockRule{name: "a2", decision: hookio.Abstain}

	e := New(a1, a2)
	input := &hookio.HookInput{ToolName: "Bash"}
	got := e.Evaluate(input)

	if got.Decision != hookio.Abstain {
		t.Errorf("Decision = %v, want Abstain", got.Decision)
	}
}

func TestEngine_NoRulesReturnsAbstain(t *testing.T) {
	e := New()
	input := &hookio.HookInput{ToolName: "Bash"}
	got := e.Evaluate(input)

	if got.Decision != hookio.Abstain {
		t.Errorf("Decision = %v, want Abstain", got.Decision)
	}
}

func TestEngine_FirstRuleDecides(t *testing.T) {
	reject := &mockRule{name: "reject", decision: hookio.Reject, reason: "blocked"}
	approve := &mockRule{name: "approve", decision: hookio.Approve, reason: "ok"}

	e := New(reject, approve)
	input := &hookio.HookInput{ToolName: "Bash"}
	got := e.Evaluate(input)

	if got.Decision != hookio.Reject {
		t.Errorf("Decision = %v, want Reject", got.Decision)
	}
	if got.Reason != "blocked" {
		t.Errorf("Reason = %q, want blocked", got.Reason)
	}
}

func TestEngine_LogsToStderr(t *testing.T) {
	approve := &mockRule{name: "logrule", decision: hookio.Approve, reason: "logged"}
	e := New(approve)
	input := &hookio.HookInput{ToolName: "Bash"}

	r, w, _ := os.Pipe()
	oldStderr := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = oldStderr }()

	_ = e.Evaluate(input)
	_ = w.Close()
	buf := make([]byte, 256)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if !strings.Contains(output, "logrule") {
		t.Errorf("stderr should contain rule name, got %q", output)
	}
	if !strings.Contains(output, "approve") {
		t.Errorf("stderr should contain decision, got %q", output)
	}
	if !strings.Contains(output, "logged") {
		t.Errorf("stderr should contain reason, got %q", output)
	}
}

func TestEngine_NonAbstainAlwaysHasReason(t *testing.T) {
	decisions := []struct {
		name     string
		decision hookio.Decision
		reason   string
	}{
		{"approve with reason", hookio.Approve, "ok"},
		{"reject with reason", hookio.Reject, "no"},
		{"ask with reason", hookio.Ask, "confirm"},
	}
	for _, tt := range decisions {
		t.Run(tt.name, func(t *testing.T) {
			rule := &mockRule{name: "test", decision: tt.decision, reason: tt.reason}
			e := New(rule)
			input := &hookio.HookInput{ToolName: "Bash"}
			got := e.Evaluate(input)
			if got.Decision != hookio.Abstain && got.Reason == "" {
				t.Errorf("Decision %v has empty Reason — all non-Abstain results must include a reason", got.Decision)
			}
		})
	}
}

func TestEngine_EmptyReasonOnNonAbstain_Detected(t *testing.T) {
	// Verify that a rule returning a non-Abstain decision with empty reason
	// is still logged (the engine does not silently swallow it).
	// This test documents the current behavior; a future guard could reject empty reasons.
	rule := &mockRule{name: "bad", decision: hookio.Approve, reason: ""}
	e := New(rule)
	input := &hookio.HookInput{ToolName: "Bash"}
	got := e.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Decision = %v, want Approve", got.Decision)
	}
	// Note: Reason is empty — this test exists to flag that the engine
	// does not enforce non-empty reasons. If enforcement is added, update this test.
	if got.Reason != "" {
		t.Errorf("Expected empty reason from bad rule, got %q", got.Reason)
	}
}

type conditionalMockRule struct {
	approvePrefix string
	rejectPrefix  string
	// approveExecs approves any executable matching one of these prefixes (in
	// addition to approvePrefix). Lets a test own more than one leaf — needed
	// once an abstaining leaf correctly demotes its siblings.
	approveExecs []string
}

func (m *conditionalMockRule) Name() string { return "conditional" }
func (m *conditionalMockRule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	cmd, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: m.Name()}
	}
	parsed := cmdparse.Parse(cmd)
	for _, pc := range parsed {
		if m.rejectPrefix != "" && strings.HasPrefix(pc.Executable, m.rejectPrefix) {
			return hookio.RuleResult{Decision: hookio.Reject, Reason: "rejected", Module: m.Name()}
		}
		if m.approvePrefix != "" && strings.HasPrefix(pc.Executable, m.approvePrefix) {
			return hookio.RuleResult{Decision: hookio.Approve, Reason: "approved", Module: m.Name()}
		}
		for _, p := range m.approveExecs {
			if strings.HasPrefix(pc.Executable, p) {
				return hookio.RuleResult{Decision: hookio.Approve, Reason: "approved", Module: m.Name()}
			}
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: m.Name()}
}

func TestEngine_RegisterRules(t *testing.T) {
	e := New()
	approve := &mockRule{name: "approve", decision: hookio.Approve, reason: "ok"}
	e.RegisterRules(approve)
	input := &hookio.HookInput{ToolName: "Bash"}
	got := e.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Decision = %v, want Approve", got.Decision)
	}
}

func TestEngine_EvaluateExpression_Simple(t *testing.T) {
	approve := &mockRule{name: "approve", decision: hookio.Approve, reason: "ok"}
	e := New(approve)
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}
	got := e.EvaluateExpression("echo hello", nil, origin)
	if got.Decision != hookio.Approve {
		t.Errorf("Decision = %v, want Approve", got.Decision)
	}
}

func TestEngine_EvaluateExpression_MostRestrictiveWins(t *testing.T) {
	rule := &conditionalMockRule{approvePrefix: "echo", rejectPrefix: "rm"}
	e := New(rule)
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}
	got := e.EvaluateExpression("echo hello && rm -rf /", nil, origin)
	if got.Decision != hookio.Reject {
		t.Errorf("Decision = %v, want Reject (most restrictive)", got.Decision)
	}
}

func TestEngine_EvaluateHook_BashUsesExpressionFold(t *testing.T) {
	// EvaluateHook MUST route Bash through EvaluateExpression (per-leaf fold),
	// not the whole-string first-match Evaluate. A compound with an abstaining
	// leaf therefore demotes to Abstain — the whole-string Evaluate would have
	// returned the first rule's Approve for `git status`.
	rule := &conditionalMockRule{approvePrefix: "git"}
	e := New(rule)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:       "/tmp/project",
		ToolInput: json.RawMessage(`{"command":"git status && rm -rf /home/user/x"}`),
	}
	got := e.EvaluateHook(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("Decision = %v, want Abstain (Bash must route through the EvaluateExpression fold)", got.Decision)
	}
}

func TestEngine_EvaluateHook_NonBashUsesEvaluate(t *testing.T) {
	approve := &mockRule{name: "approve", decision: hookio.Approve, reason: "ok"}
	e := New(approve)
	input := &hookio.HookInput{ToolName: "Write"}
	got := e.EvaluateHook(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Decision = %v, want Approve (non-Bash routes to Evaluate)", got.Decision)
	}
}

func TestEngine_EvaluateExpression_AbstainDemotesApprove(t *testing.T) {
	// pg2-t4uyx regression guard: an Abstaining leaf MUST demote an approving
	// sibling. A compound is Approve iff EVERY leaf independently approves; any
	// leaf that only abstains (no rule owns it) demotes the whole compound to
	// Abstain so Claude's own prompt re-engages. This is the core fold-order fix
	// (Abstain must outrank Approve in restrictiveness).
	rule := &conditionalMockRule{approvePrefix: "git"} // rm matches nothing -> Abstain
	e := New(rule)
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}

	got := e.EvaluateExpression("git status && rm -rf /home/user/important", nil, origin)
	if got.Decision != hookio.Abstain {
		t.Errorf("Decision = %v, want Abstain (abstaining rm leaf must demote the git approve)", got.Decision)
	}

	// Control: a fully-approving compound stays Approve.
	got2 := e.EvaluateExpression("git status && git log", nil, origin)
	if got2.Decision != hookio.Approve {
		t.Errorf("Decision = %v, want Approve (all leaves approve)", got2.Decision)
	}
}

// envAssignmentMockRule stands in for the env-var rule: it Rejects any leaf that
// assigns rejectVar, Approves any leaf assigning approveVar, and Abstains
// otherwise. It also records every command text the chain handed it.
type envAssignmentMockRule struct {
	rejectVar  string
	approveVar string
	seen       []string
}

func (m *envAssignmentMockRule) Name() string { return "env-assignment-mock" }
func (m *envAssignmentMockRule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	cmd, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: m.Name()}
	}
	m.seen = append(m.seen, cmd)
	for _, pc := range cmdparse.Parse(cmd) {
		for _, ev := range pc.EnvVars {
			if m.rejectVar != "" && ev.Name == m.rejectVar {
				return hookio.RuleResult{Decision: hookio.Reject, Reason: "injector", Module: m.Name()}
			}
			if m.approveVar != "" && ev.Name == m.approveVar {
				return hookio.RuleResult{Decision: hookio.Approve, Reason: "verified safe", Module: m.Name()}
			}
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: m.Name()}
}

// TestEngine_EvaluateExpression_AssignmentOnlyLeafReachesRuleChain is the pg2-mtnmb
// engine half. An assignment-only compound segment parses to a COMMAND-LESS leaf
// (Executable == "", EnvVars set). The command-less branch used to evaluate only
// redirections and `continue`, so the assignments never reached any rule and the
// fold — Approve iff EVERY leaf approves — returned the sibling's verdict alone:
// `LD_PRELOAD=/evil.so && echo hi` auto-approved.
func TestEngine_EvaluateExpression_AssignmentOnlyLeafReachesRuleChain(t *testing.T) {
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}

	// A decisive verdict on the assignment-only leaf must reach the fold.
	for _, cmd := range []string{
		"LD_PRELOAD=/evil.so && echo hi",
		"LD_PRELOAD=/evil.so ; echo hi",
		"LD_PRELOAD=/evil.so\necho hi",
		"echo hi && LD_PRELOAD=/evil.so",
		"LD_PRELOAD=/evil.so",
	} {
		envRule := &envAssignmentMockRule{rejectVar: "LD_PRELOAD"}
		e := New(envRule, &conditionalMockRule{approvePrefix: "echo"})
		got := e.EvaluateExpression(cmd, nil, origin)
		if got.Decision != hookio.Reject {
			t.Errorf("EvaluateExpression(%q) = %v (%s), want Reject (assignment-only leaf must reach the rule chain)",
				cmd, got.Decision, got.Reason)
		}
		if !slices.Contains(envRule.seen, "LD_PRELOAD=/evil.so") {
			t.Errorf("EvaluateExpression(%q): rule chain never saw the assignment-only leaf; saw %q", cmd, envRule.seen)
		}
	}

	// An Approve on the assignment-only leaf is honored (the pg2-0q99a
	// verified-safe-preserve verdict for a command-less leaf).
	envRule := &envAssignmentMockRule{approveVar: "PATH"}
	e := New(envRule, &conditionalMockRule{approvePrefix: "echo"})
	if got := e.EvaluateExpression(`PATH="$PATH:/x" && echo hi`, nil, origin); got.Decision != hookio.Approve {
		t.Errorf(`EvaluateExpression("PATH=... && echo hi") = %v (%s), want Approve`, got.Decision, got.Reason)
	}
}

// TestEngine_EvaluateExpression_AssignmentOnlyLeafIsNeutralWhenNoRuleObjects pins
// the other half of the pg2-mtnmb contract, and it is what keeps the fix from being
// a mass over-ask: an assignment-only leaf EXECUTES NOTHING, so when no rule has a
// decisive opinion about its assignments the leaf must contribute NOTHING to the
// fold — exactly as evaluateRedirections returns Approve for a leaf with no
// redirections. Folding the chain's Abstain instead would demote every
// `count=$(...) && cmd` / `A=1 && cmd` in the corpus from allow to abstain.
func TestEngine_EvaluateExpression_AssignmentOnlyLeafIsNeutralWhenNoRuleObjects(t *testing.T) {
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}
	e := New(&envAssignmentMockRule{rejectVar: "LD_PRELOAD"}, &conditionalMockRule{approvePrefix: "echo"})

	for _, cmd := range []string{
		"A=1 && echo hi",
		"A=1 ; echo hi",
		"echo hi && A=1",
		"A=1 B=2 && echo hi",
	} {
		if got := e.EvaluateExpression(cmd, nil, origin); got.Decision != hookio.Approve {
			t.Errorf("EvaluateExpression(%q) = %v (%s), want Approve (a benign assignment-only leaf must not demote its sibling)",
				cmd, got.Decision, got.Reason)
		}
	}

	// The neutrality is scoped to the assignments: a genuinely unowned COMMAND leaf
	// still demotes (the pg2-t4uyx invariant is untouched).
	if got := e.EvaluateExpression("A=1 && rm -rf /important", nil, origin); got.Decision != hookio.Abstain {
		t.Errorf("EvaluateExpression(%q) = %v, want Abstain (unowned command leaf must still demote)", "A=1 && rm -rf /important", got.Decision)
	}
}

// TestEngine_EvaluateExpression_UnownedAssignmentsOnlyAbstain pins the FLOOR on the
// neutrality above: an expression whose leaves are ALL assignments that no rule owns
// executes nothing and was judged by nobody, so there is no Approve to give — it
// Abstains, exactly as it did when Parse dropped such segments and the expression
// reached zero leaves (pg2-mtnmb must not move anything toward allow).
//
// This is load-bearing beyond tidiness. A parser desync of the pg2-3ggxm class turns
// a real command into a PHANTOM NAME=VALUE (corpus row 142386: the engine's per-line
// comment stripping mangles a multi-line single-quoted value containing `#`, whose
// now-unterminated quote swallows the real `bd update` leaf). Without this floor the
// mangled remnant is a lone unowned assignment leaf and the neutrality promotion
// turns it into `allow` — a parse failure manufacturing an auto-approval. Measured:
// that row moved abstain -> approve before this floor, and is the ONLY row in the
// 204,219-row Bash corpus that did.
func TestEngine_EvaluateExpression_UnownedAssignmentsOnlyAbstain(t *testing.T) {
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}
	e := New(&envAssignmentMockRule{rejectVar: "LD_PRELOAD", approveVar: "PATH"}, &conditionalMockRule{approvePrefix: "echo"})

	for _, cmd := range []string{
		"A=1",
		"A=1 B=2",
		"A=1 && B=2",
		"SUMMARY='mangled remnant that swallowed the real command",
	} {
		if got := e.EvaluateExpression(cmd, nil, origin); got.Decision != hookio.Abstain {
			t.Errorf("EvaluateExpression(%q) = %v (%s), want Abstain (nothing executes and no rule judged it)",
				cmd, got.Decision, got.Reason)
		}
	}

	// A rule that IS decisive about the assignment still owns the verdict — this is
	// what keeps the standalone form's verdict equal to its export/leading/env forms.
	for _, tc := range []struct {
		cmd  string
		want hookio.Decision
	}{
		{`PATH="$PATH:/x"`, hookio.Approve},
		{"LD_PRELOAD=/evil.so", hookio.Reject},
		{`A=1 && PATH="$PATH:/x"`, hookio.Approve},
	} {
		if got := e.EvaluateExpression(tc.cmd, nil, origin); got.Decision != tc.want {
			t.Errorf("EvaluateExpression(%q) = %v (%s), want %v (a decisive rule verdict survives the floor)",
				tc.cmd, got.Decision, got.Reason, tc.want)
		}
	}
}

func TestEngine_EvaluateExpression_StripsComments(t *testing.T) {
	rule := &conditionalMockRule{rejectPrefix: "rm"}
	e := New(rule)
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}
	got := e.EvaluateExpression("safe_cmd # looks fine\nrm -rf /dangerous", nil, origin)
	if got.Decision != hookio.Reject {
		t.Errorf("Decision = %v, want Reject (comment should not hide rm)", got.Decision)
	}
}

func TestEngine_EvaluateExpression_CycleDetection(t *testing.T) {
	approve := &mockRule{name: "approve", decision: hookio.Approve, reason: "ok"}
	e := New(approve)
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp"}

	stack := []hookio.StackFrame{
		{RuleName: "docker", Command: "docker run", Expression: "echo hello"},
	}
	got := e.EvaluateExpression("echo hello", stack, origin)
	if got.Decision != hookio.Abstain {
		t.Errorf("Decision = %v, want Abstain (cycle detected)", got.Decision)
	}
	if !strings.Contains(got.Reason, "cycle") {
		t.Errorf("Reason = %q, want to contain 'cycle'", got.Reason)
	}
}

func TestEngine_EvaluateExpression_NoCycleDeepNesting(t *testing.T) {
	approve := &mockRule{name: "approve", decision: hookio.Approve, reason: "ok"}
	e := New(approve)
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp"}

	stack := make([]hookio.StackFrame, 20)
	for i := range stack {
		stack[i] = hookio.StackFrame{RuleName: "test", Command: "cmd", Expression: "cmd-" + string(rune('a'+i))}
	}
	got := e.EvaluateExpression("echo unique", stack, origin)
	if got.Decision != hookio.Approve {
		t.Errorf("Decision = %v, want Approve (no cycle, deep nesting is fine)", got.Decision)
	}
}

func TestEngine_EvaluateExpression_NearCycleNotBlocked(t *testing.T) {
	approve := &mockRule{name: "approve", decision: hookio.Approve, reason: "ok"}
	e := New(approve)
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp"}

	stack := []hookio.StackFrame{
		{RuleName: "nix", Command: "nix develop", Expression: "echo hello world"},
	}
	got := e.EvaluateExpression("echo hello", stack, origin)
	if got.Decision != hookio.Approve {
		t.Errorf("Decision = %v, want Approve (near-cycle, not exact match)", got.Decision)
	}
}

func TestEngine_EvaluateExpression_ProcessSubstitution(t *testing.T) {
	// The mock owns BOTH diff (outer) and sort (inner). Under the restrictiveness
	// fold an abstaining inner command correctly demotes the outer to Abstain, so
	// "safe process substitution stays Approve" only holds when the inner command
	// is genuinely approved — which in production `sort file` is (safecmds). We
	// model that by having the mock approve sort rather than abstain on it.
	rule := &conditionalMockRule{approvePrefix: "diff", approveExecs: []string{"sort"}, rejectPrefix: "rm"}
	e := New(rule)
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}

	tests := []struct {
		name string
		expr string
		want hookio.Decision
	}{
		{"safe process substitution", "diff <(sort file1) <(sort file2)", hookio.Approve},
		{"dangerous inner command", "diff <(rm -rf /) <(sort file2)", hookio.Reject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.EvaluateExpression(tt.expr, nil, origin)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (%s)", got.Decision, tt.want, got.Reason)
			}
		})
	}
}

func TestEngine_EvaluateExpression_SubstitutionRecursion(t *testing.T) {
	// pg2-1q5i3: a $(...) / `...` / <(...) / >(...) body is re-evaluated through
	// ALL rules and folded most-risky-wins into the outer leaf. The mock owns echo
	// (approve) and rm (reject); an unowned command abstains.
	rule := &conditionalMockRule{approvePrefix: "echo", rejectPrefix: "rm"}
	e := New(rule)
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}

	tests := []struct {
		name string
		expr string
		want hookio.Decision
	}{
		// Approvable inner command keeps the outer approve.
		{"approvable inner cmd sub", "echo $(echo hi)", hookio.Approve},
		{"approvable inner backtick", "echo `echo hi`", hookio.Approve},
		// A rejecting inner command propagates most-risky-wins.
		{"rejecting inner cmd sub", "echo $(rm -rf /)", hookio.Reject},
		{"rejecting inner backtick", "echo `rm -rf /`", hookio.Reject},
		{"rejecting inner process sub", "echo <(rm -rf /)", hookio.Reject},
		// An unowned (abstaining) inner command demotes the outer approve.
		{"abstaining inner demotes", "echo $(unowned thing)", hookio.Abstain},
		// Nested: the inner rm surfaces on re-evaluation of the outer body.
		{"nested rm surfaces", "echo $(cat $(rm -rf /))", hookio.Reject},
		{"process sub nested in cmd sub", "echo $(cat <(rm -rf /))", hookio.Reject},
		// Single-quoted body is literal — no substitution, outer approve stands.
		{"single quoted literal not recursed", "echo '$(rm -rf /)'", hookio.Approve},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.EvaluateExpression(tt.expr, nil, origin)
			if got.Decision != tt.want {
				t.Errorf("EvaluateExpression(%q) = %v (%s), want %v", tt.expr, got.Decision, got.Reason, tt.want)
			}
		})
	}
}

func TestEngine_EvaluateExpression_SubstitutionCycleDetection(t *testing.T) {
	// The substitution recursion MUST push a StackFrame so the existing cycle
	// check fires: a substitution body equal to an ancestor expression yields
	// Abstain, which then demotes the outer approve.
	approve := &mockRule{name: "approve", decision: hookio.Approve, reason: "ok"}
	e := New(approve)
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp"}

	stack := []hookio.StackFrame{
		{RuleName: "docker", Command: "docker run", Expression: "echo hello"},
	}
	got := e.EvaluateExpression("echo $(echo hello)", stack, origin)
	if got.Decision != hookio.Abstain {
		t.Errorf("Decision = %v, want Abstain (substitution body cycles with ancestor)", got.Decision)
	}
}

func TestEngine_EvaluateExpression_SafeDeviceRedirects(t *testing.T) {
	// pg2-9ctmb: a redirect whose target is a standard special device file
	// (/dev/null, /dev/stdout, /dev/stderr, /dev/tty, /dev/fd/<n>) MUST NOT demote
	// an otherwise-approved command. The PathEvaluator does not know these
	// pseudo-files (it returns PathUnknown), which previously demoted the whole
	// command to Abstain — so `bd list 2>/dev/null` abstained while `bd list` allowed.
	approve := &mockRule{name: "approve", decision: hookio.Approve, reason: "ok"}
	e := New(approve)
	pe := patheval.New("/tmp/project")
	e.SetPathEvaluator(pe)
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}

	exprs := []struct {
		name string
		expr string
	}{
		{"stderr to /dev/null", "echo hi 2>/dev/null"},
		{"stdout to /dev/null", "echo hi >/dev/null"},
		{"fd1 to /dev/null", "echo hi 1>/dev/null"},
		{"all to /dev/null", "echo hi &>/dev/null"},
		{"stderr to /dev/stderr", "echo hi 2>/dev/stderr"},
		{"stdout to /dev/stdout", "echo hi >/dev/stdout"},
		{"stderr to /dev/fd/2", "echo hi 2>/dev/fd/2"},
		{"stdin from /dev/null", "cat </dev/null"},
		{"compound with /dev/null redirect", "echo one && echo two 2>/dev/null"},
	}
	for _, tt := range exprs {
		t.Run(tt.name, func(t *testing.T) {
			got := e.EvaluateExpression(tt.expr, nil, origin)
			if got.Decision != hookio.Approve {
				t.Errorf("Decision = %v, want Approve (%s)", got.Decision, got.Reason)
			}
		})
	}
}

func TestEngine_EvaluateExpression_NonDeviceRedirectsStillEvaluated(t *testing.T) {
	// Negative guard for pg2-9ctmb: the safe-device short-circuit MUST be scoped to
	// the standard special files only. Genuine read-only targets still Reject, unknown
	// non-writable targets still Abstain, and an arbitrary /dev/* device is not a free
	// pass.
	approve := &mockRule{name: "approve", decision: hookio.Approve, reason: "ok"}
	e := New(approve)
	pe := patheval.New("/tmp/project")
	e.SetPathEvaluator(pe)
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}

	tests := []struct {
		name string
		expr string
		want hookio.Decision
	}{
		{"write to read-only nix path still rejects", "echo hi > /nix/store/bad.txt", hookio.Reject},
		{"write to unknown path still abstains", "echo hi > /home/other/nope.txt", hookio.Abstain},
		{"non-special /dev device still abstains", "echo hi > /dev/sda", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.EvaluateExpression(tt.expr, nil, origin)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (%s)", got.Decision, tt.want, got.Reason)
			}
		})
	}
}

func TestEngine_EvaluateExpression_CdRelativeRedirection(t *testing.T) {
	// pg2-opclh: a relative redirection target after a `cd` MUST resolve against
	// the cd target, not the original cwd. The base evaluator's cwd is a
	// non-writable location (/etc), so a relative `> ./out` is PathUnknown
	// (Abstain) at origin; after `cd /tmp` (a read-write zone) the same target
	// resolves under /tmp and the compound approves.
	approve := &mockRule{name: "approve", decision: hookio.Approve, reason: "ok"}
	e := New(approve)
	pe := patheval.NewWithCWD("/tmp/project", "/etc")
	e.SetPathEvaluator(pe)
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/etc"}

	tests := []struct {
		name string
		expr string
		want hookio.Decision
	}{
		// Control / pre-fix isolation: without a cd the relative target resolves
		// under the non-writable base cwd and demotes to Abstain.
		{"relative redirect at origin is non-writable", "echo hi > ./out", hookio.Abstain},
		// Fixed behavior: cd into a read-write zone re-roots the relative target.
		{"cd into writable zone re-roots relative redirect", "cd /tmp && echo hi > ./out", hookio.Approve},
		// Relative cd target resolves against the running cwd (origin.CWD=/etc):
		// /etc/../tmp == /tmp, a read-write zone.
		{"relative cd target re-roots relative redirect", "cd ../tmp && echo hi > ./out", hookio.Approve},
		// Control: cd into a non-writable location must NOT approve.
		{"cd into non-writable location does not approve", "cd /usr && echo hi > ./out", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.EvaluateExpression(tt.expr, nil, origin)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (%s)", got.Decision, tt.want, got.Reason)
			}
		})
	}
}

func TestEngine_EvaluateExpression_CdConservativeBranches(t *testing.T) {
	// pg2-trh3z: guard the pg2-opclh conservative `cd` branches (engine.go:230-231).
	// The running cwd advances ONLY for a simple `cd <dir>` with exactly one
	// non-flag, non-`~` argument. `cd -`, `cd <a> <b>`, etc. MUST NOT re-root — a
	// relative redirect after them still resolves against the (non-writable) origin
	// cwd and stays Abstain. Same setup as CdRelativeRedirection above: base cwd is
	// /etc (a non-writable zone), so a relative `> ./out` is Abstain unless a valid
	// re-root moves it into a writable zone.
	approve := &mockRule{name: "approve", decision: hookio.Approve, reason: "ok"}
	e := New(approve)
	pe := patheval.NewWithCWD("/tmp/project", "/etc")
	e.SetPathEvaluator(pe)
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/etc"}

	tests := []struct {
		name string
		expr string
		want hookio.Decision
	}{
		// `cd -` (previous dir) carries a `-`-prefixed arg — the conservative branch
		// refuses to re-root, so ./out stays under /etc (Abstain).
		{"cd - does not re-root", "cd - && echo hi > ./out", hookio.Abstain},
		// Two args — the `len(pc.Args) == 1` guard refuses to re-root (Abstain).
		{"cd two relative args does not re-root", "cd a b && echo hi > ./out", hookio.Abstain},
		// Discriminating two-arg case: the FIRST arg IS a writable zone (/tmp). If the
		// multi-arg guard were removed and the code re-rooted on pc.Args[0], ./out
		// would resolve under /tmp and Approve — so this case flips to Approve the
		// moment the guard breaks, proving the guard is exercised.
		{"cd two args first writable does not re-root", "cd /tmp extra && echo hi > ./out", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.EvaluateExpression(tt.expr, nil, origin)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (%s)", got.Decision, tt.want, got.Reason)
			}
		})
	}
}

func TestEngine_TraceEnabled_CollectsAllRules(t *testing.T) {
	a1 := &mockRule{name: "rule-a", decision: hookio.Abstain, reason: "not relevant"}
	a2 := &mockRule{name: "rule-b", decision: hookio.Abstain, reason: "also not relevant"}
	winner := &mockRule{name: "rule-c", decision: hookio.Approve, reason: "matched"}
	after := &mockRule{name: "rule-d", decision: hookio.Reject, reason: "should not appear"}

	e := New(a1, a2, winner, after)
	e.SetTrace(true)
	input := &hookio.HookInput{ToolName: "Bash"}
	got := e.Evaluate(input)

	if got.Decision != hookio.Approve {
		t.Fatalf("Decision = %v, want Approve", got.Decision)
	}
	if got.Trace == nil {
		t.Fatal("Trace should not be nil when tracing is enabled")
	}
	if len(got.Trace) != 3 {
		t.Fatalf("Trace has %d entries, want 3 (2 abstains + 1 winner)", len(got.Trace))
	}
	if got.Trace[0].RuleName != "rule-a" || got.Trace[0].Decision != hookio.Abstain {
		t.Errorf("Trace[0] = %+v, want rule-a/Abstain", got.Trace[0])
	}
	if got.Trace[1].RuleName != "rule-b" || got.Trace[1].Decision != hookio.Abstain {
		t.Errorf("Trace[1] = %+v, want rule-b/Abstain", got.Trace[1])
	}
	if got.Trace[2].RuleName != "rule-c" || got.Trace[2].Decision != hookio.Approve {
		t.Errorf("Trace[2] = %+v, want rule-c/Approve", got.Trace[2])
	}
	if got.Trace[2].Reason != "matched" {
		t.Errorf("Trace[2].Reason = %q, want 'matched'", got.Trace[2].Reason)
	}
}

func TestEngine_TraceEnabled_AllAbstains(t *testing.T) {
	a1 := &mockRule{name: "rule-a", decision: hookio.Abstain, reason: "nope"}
	a2 := &mockRule{name: "rule-b", decision: hookio.Abstain, reason: "also nope"}

	e := New(a1, a2)
	e.SetTrace(true)
	input := &hookio.HookInput{ToolName: "Bash"}
	got := e.Evaluate(input)

	if got.Decision != hookio.Abstain {
		t.Fatalf("Decision = %v, want Abstain", got.Decision)
	}
	if got.Trace == nil {
		t.Fatal("Trace should not be nil when tracing is enabled")
	}
	if len(got.Trace) != 2 {
		t.Fatalf("Trace has %d entries, want 2", len(got.Trace))
	}
}

func TestEngine_TraceDisabled_NilTrace(t *testing.T) {
	approve := &mockRule{name: "rule-a", decision: hookio.Approve, reason: "ok"}
	e := New(approve)
	input := &hookio.HookInput{ToolName: "Bash"}
	got := e.Evaluate(input)

	if got.Trace != nil {
		t.Errorf("Trace should be nil when tracing is disabled, got %d entries", len(got.Trace))
	}
}

func TestEngine_TraceEnabled_LogsToStderr(t *testing.T) {
	a1 := &mockRule{name: "rule-a", decision: hookio.Abstain, reason: "skip"}
	winner := &mockRule{name: "rule-b", decision: hookio.Approve, reason: "ok"}

	e := New(a1, winner)
	e.SetTrace(true)
	input := &hookio.HookInput{ToolName: "Bash"}

	r, w, _ := os.Pipe()
	oldStderr := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = oldStderr }()

	_ = e.Evaluate(input)
	_ = w.Close()
	buf := make([]byte, 1024)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if !strings.Contains(output, "TRACE rule-a -> abstain") {
		t.Errorf("stderr should contain TRACE for rule-a, got %q", output)
	}
	if !strings.Contains(output, "TRACE rule-b -> approve") {
		t.Errorf("stderr should contain TRACE for rule-b, got %q", output)
	}
}

func TestEngine_EvaluateExpression_RedirectionPaths(t *testing.T) {
	approve := &mockRule{name: "approve", decision: hookio.Approve, reason: "ok"}
	e := New(approve)
	pe := patheval.New("/tmp/project")
	e.SetPathEvaluator(pe)
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}

	tests := []struct {
		name string
		expr string
		want hookio.Decision
	}{
		{"stdin from nix store", "docker load < /nix/store/image.tar.gz", hookio.Approve},
		{"stdout to project", "cmd > /tmp/project/out.txt", hookio.Approve},
		{"stdout to readonly", "cmd > /nix/store/bad.txt", hookio.Reject},
		{"stdin from unknown", "cmd < /home/other/file", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.EvaluateExpression(tt.expr, nil, origin)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (%s)", got.Decision, tt.want, got.Reason)
			}
		})
	}
}
