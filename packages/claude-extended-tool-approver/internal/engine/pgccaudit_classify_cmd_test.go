// pg2-tqles: pg-ccaudit's rule approves `classify` as read-only, but classify
// execs whatever program PG_CCAUDIT_CLASSIFY_CMD names, and no other rule in the
// chain screens that name (envvars guards only the injector family, ENV, PATH and
// HOME). The unit rows in internal/rules/pgccaudit prove the rule defers; these
// rows prove it end to end through the REAL composed chain, where the engine
// hands the rule ONE leaf plus RootExpression, so the `export X=...; cmd` form
// only works if the sibling-leaf scope is recovered correctly.
package engine_test

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func TestPgCCAuditClassifyCmd_FullChain(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	cwd := projectRoot
	eng := buildFullEngine(projectRoot, cwd)

	eval := func(command string) hookio.RuleResult {
		return eng.EvaluateHook(&hookio.HookInput{ToolName: "Bash", CWD: cwd, ToolInput: makeBashJSON(command)})
	}

	// Positive control: without the variable the chain still approves through
	// the pg-ccaudit rule, so the rows below fail for the right reason.
	for _, command := range []string{"pg-ccaudit classify", "pg-ccaudit classify status"} {
		got := eval(command)
		if got.Decision != hookio.Approve || got.Module != "pg-ccaudit" {
			t.Errorf("%q got %v by %q (%s); want Approve by pg-ccaudit", command, got.Decision, got.Module, got.Reason)
		}
	}

	for _, command := range []string{
		"export PG_CCAUDIT_CLASSIFY_CMD='rm -rf /tmp/x'; pg-ccaudit classify --classifier cli",
		"export PG_CCAUDIT_CLASSIFY_CMD=x && pg-ccaudit classify",
		"PG_CCAUDIT_CLASSIFY_CMD=x pg-ccaudit classify --classifier cli",
		"env PG_CCAUDIT_CLASSIFY_CMD=x pg-ccaudit classify",
	} {
		t.Run(command, func(t *testing.T) {
			got := eval(command)
			if got.Decision == hookio.Approve {
				t.Errorf("%q got Approve by %q (%s); want anything but Approve — classify execs the program the variable names",
					command, got.Module, got.Reason)
			}
		})
	}
}
