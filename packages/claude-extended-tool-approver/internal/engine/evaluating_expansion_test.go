package engine_test

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestEvaluatingParameterExpansions pins the engine's treatment of parameter
// expansions that EVALUATE their operand (bead pg2-ftneaj). It runs the REAL composed
// rule chain (buildFullEngine), not a mock.
//
// `x='$(rm -rf /)'; echo "${x@P}"` was approved by safe-commands because the leaf is
// `echo`: bash runs the command substitution found in x's value when it prompt-expands
// it, yet the source holds no `$(...)` node for the engine's substitution recursion to
// see. Such an expansion now takes the command-substitution refusal (Reject with the
// "command substitution denied" reason), while the inert expansions still approve.
func TestEvaluatingParameterExpansions(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	decide := func(command string) hookio.RuleResult {
		return eng.EvaluateHook(&hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: makeBashJSON(command),
			CWD:       projectRoot,
		})
	}

	const denied = "command substitution denied"
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
		reason  string
	}{
		// --- the bead's case, and every shape that evaluates ---
		{"@P with an unsafe value", `x='$(rm -rf /)'; echo "${x@P}"`, hookio.Reject, denied},
		{"@P alone", `echo "${x@P}"`, hookio.Reject, denied},
		{"@P unquoted", `echo ${x@P}`, hookio.Reject, denied},
		{"@P on another safe-commands leaf", `ls "${x@P}"`, hookio.Reject, denied},
		{"@P nested in a safe substitution", `echo "$(echo "${x@P}")"`, hookio.Reject, denied},
		{"@P in a redirection-only leaf", `> "${x@P}"`, hookio.Reject, denied},
		{"@P in a for word list", `for f in "${x@P}"; do echo hi; done`, hookio.Reject, denied},
		{"@P later in a compound", `echo ok && echo "${x@P}"`, hookio.Reject, denied},
		{"@P in an unquoted heredoc body", "cat <<EOF\n${x@P}\nEOF", hookio.Reject, denied},
		{"@P in an unquoted heredoc file write", "cat > /tmp/zr-x/plan.md <<EOF\n${x@P}\nEOF", hookio.Reject, denied},
		{"indirection", `echo "${!x}"`, hookio.Reject, denied},
		{"variable subscript", `echo "${a[i]}"`, hookio.Reject, denied},
		{"variable length subscript", `echo "${#a[i]}"`, hookio.Reject, denied},
		{"variable slice offset", `echo "${x:n}"`, hookio.Reject, denied},
		{"variable slice length", `echo "${x:0:n}"`, hookio.Reject, denied},

		// --- plain expansions keep approving ---
		{"plain braces", `echo "${x}"`, hookio.Approve, ""},
		{"plain bare", `echo "$x"`, hookio.Approve, ""},
		{"default value", `echo "${x:-default}"`, hookio.Approve, ""},
		{"assign default", `echo "${x:=default}"`, hookio.Approve, ""},
		{"length", `echo "${#x}"`, hookio.Approve, ""},
		{"prefix strip and replace", `echo "${x#pre}" "${x/a/b}"`, hookio.Approve, ""},
		{"case change", `echo "${x^^}"`, hookio.Approve, ""},
		{"literal slice", `echo "${x:1:2}"`, hookio.Approve, ""},
		{"literal index", `echo "${a[0]}" "${a[@]}" "${a[*]}"`, hookio.Approve, ""},
		{"key listing", `echo "${!a[@]}"`, hookio.Approve, ""},
		{"inert @ transforms", `echo "${x@Q}" "${x@E}" "${x@U}" "${x@a}"`, hookio.Approve, ""},
		{"unquoted heredoc with plain expansions", "cat > /tmp/zr-x/plan.md <<EOF\n${x} $x\nEOF", hookio.Approve, ""},
		{"quoted heredoc holding @P text is literal", "cat > /tmp/zr-x/plan.md <<'EOF'\n${x@P}\nEOF", hookio.Approve, ""},
		{"@P inside single quotes is literal", `echo '${x@P}'`, hookio.Approve, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decide(tt.command)
			if got.Decision != tt.want {
				t.Fatalf("EvaluateHook(%q) = %v (%s / %s), want %v", tt.command, got.Decision, got.Module, got.Reason, tt.want)
			}
			if tt.reason != "" && !strings.Contains(got.Reason, tt.reason) {
				t.Errorf("EvaluateHook(%q) reason = %q, want it to contain %q", tt.command, got.Reason, tt.reason)
			}
		})
	}
}
