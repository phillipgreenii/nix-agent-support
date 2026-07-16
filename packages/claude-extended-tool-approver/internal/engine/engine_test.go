package engine

import (
	"encoding/json"
	"os"
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
