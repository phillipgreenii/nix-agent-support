package effectpolicy

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// TestPgReviewDecisions pins the approval of the unattended analyst's one
// command, `pg-review agent q`, and the refusal of everything broader: a shell
// metacharacter in an argument, `agent file`, and every other subcommand.
func TestPgReviewDecisions(t *testing.T) {
	root, _ := fixture(t)
	reg := cmddesc.DefaultRegistry()
	forms := []struct {
		command string
		want    evalcontract.Decision
		why     string
	}{
		{"pg-review agent q ccaudit error-rate-by-tool", evalcontract.Approve, ""},
		{"pg-review agent q ccaudit top-signatures --tool Bash --limit 20", evalcontract.Approve, ""},
		{"pg-review agent q hook-decisions denied-by-rule --run 7", evalcontract.Approve, ""},
		{"pg-review --output json agent q ccaudit error-rate-by-tool", evalcontract.Approve, ""},
		{"pg-review agent q ccaudit error-rate-by-tool \"two words\"", evalcontract.Approve, ""},

		{"pg-review agent q ccaudit q1; id", evalcontract.Abstain, "a semicolon starts a second command"},
		{"pg-review agent q ccaudit 'a;b'", evalcontract.Abstain, "a quoted semicolon"},
		{"pg-review agent q ccaudit q1 $(id)", evalcontract.Abstain, "command substitution"},
		{"pg-review agent q ccaudit q1 '$(id)'", evalcontract.Abstain, "a quoted dollar-paren"},
		{"pg-review agent q ccaudit q1 `id`", evalcontract.Abstain, "backtick substitution"},
		{"pg-review agent q ccaudit q1 '`id`'", evalcontract.Abstain, "a quoted backtick"},
		{"pg-review agent q ccaudit q1 | sh", evalcontract.Abstain, "a pipe into a shell"},
		{"pg-review agent q ccaudit q1 'a|b'", evalcontract.Abstain, "a quoted pipe"},
		{"pg-review agent q ccaudit q1 && id", evalcontract.Abstain, "an and-list"},
		{"pg-review agent q ccaudit q1 'a&&b'", evalcontract.Abstain, "a quoted and-list"},
		{"pg-review agent q ccaudit q1 'a>b'", evalcontract.Abstain, "a quoted redirect"},
		{"pg-review agent q ccaudit q1 \"a'b\"", evalcontract.Abstain, "a quote inside an argument"},
		{"pg-review agent q ccaudit q1 'a\\b'", evalcontract.Abstain, "a backslash inside an argument"},
		{"pg-review agent q ccaudit q1 \"a\nb\"", evalcontract.Abstain, "a newline inside an argument"},
		{"pg-review agent q Bad_Provider q1", evalcontract.Abstain, "provider outside [a-z0-9-]+"},
		{"pg-review agent q ccaudit", evalcontract.Abstain, "no query name"},
		{"pg-review agent q", evalcontract.Abstain, "no provider"},
		{"pg-review agent q ccaudit q1 $X", evalcontract.Abstain, "a runtime expansion"},

		{"pg-review agent file findings.json", evalcontract.Abstain, "agent file files into the ledger; the unattended analyst does not use it"},
		{"pg-review agent file - --run 3", evalcontract.Abstain, "agent file from stdin"},
		{"pg-review agent context", evalcontract.Abstain, "not on the analyst allowlist"},
		{"pg-review human rule 1 adopted", evalcontract.Abstain, "operator command"},
		{"pg-review human pending", evalcontract.Abstain, "operator command"},
		{"pg-review job run --lens x", evalcontract.Abstain, "starts a run"},
		{"pg-review job maintain", evalcontract.Abstain, "writes the ledger"},
		{"pg-review", evalcontract.Abstain, "no subcommand"},
	}
	for _, f := range forms {
		if f.want != evalcontract.Approve && f.why == "" {
			t.Errorf("%q: a non-Approve row must record why", f.command)
		}
		resp := Evaluate(evalcontract.Request{Command: f.command, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		if resp.Decision != f.want {
			t.Errorf("%q\n  decision = %s, want %s (%s)\n  reason: %s", f.command, resp.Decision, f.want, f.why, resp.Reason)
		}
		_ = strings.TrimSpace
	}
}
