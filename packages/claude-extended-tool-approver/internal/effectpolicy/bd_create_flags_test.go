package effectpolicy

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// TestBdCreateFlags pins bead tc-54be4 (absorbing Bash(bd create:*)). The ruled
// tracker-write `bd create` models its plain-data value flags, so a quoted
// runtime value for one is a flag value rather than a live positional. Free-text
// flags and the title positional stay unmodeled (pg2-i4lbg pin), an unmodeled
// flag still abstains, an unruled verb still abstains, and file flags stay
// path reads.
func TestBdCreateFlags(t *testing.T) {
	root, _ := fixture(t)
	t.Setenv("CETA_EXTRA_READONLY_ROOTS", "")
	t.Setenv("CETA_EXTRA_READWRITE_ROOTS", "")
	t.Setenv("WORKSPACE_ROOT", "")
	reg := cmddesc.DefaultRegistry()
	cases := []struct {
		name string
		cmd  string
		want evalcontract.Decision
	}{
		{"literal_flags", `bd create "T" --type task -p 2 -l a,b --parent tc-1 --deps blocked-by:tc-2`, evalcontract.Approve},
		{"runtime_type_priority", `bd create "T" -t "$2" -p "$3" -l "$4" --actor "$SID" --json`, evalcontract.Approve},
		{"runtime_parent_deps", `bd create "T" --parent "$P" --deps "discovered-from:$X" --assignee "$A" --json`, evalcontract.Approve},
		{"equals_form", `bd create "T" --type=task --priority=2 --labels=x`, evalcontract.Approve},
		{"freetext_desc_live_abstains", `bd create "T" --description "$(date +%F) d" --type task`, evalcontract.Abstain},
		{"freetext_desc_var_abstains", `bd create "T" -d "$DESC" --type task`, evalcontract.Abstain},
		{"title_positional_var_abstains", `bd create "$1" --type task`, evalcontract.Abstain},
		{"unmodeled_flag_value_abstains", `bd create "T" --bogus "$X"`, evalcontract.Abstain},
		{"repo_routing_not_modeled", `bd create "T" --repo "$R"`, evalcontract.Abstain},
		{"unruled_verb_abstains", `bd reopen tc-abc --type "$A"`, evalcontract.Abstain},
		{"body_file_secret_not_approved", `bd create "T" --body-file ~/.ssh/id_rsa`, evalcontract.Reject},
	}
	for _, c := range cases {
		resp := Evaluate(evalcontract.Request{Command: c.cmd, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		if resp.Decision != c.want {
			t.Errorf("%s: %q\n  decision = %s, want %s\n  reason: %s", c.name, c.cmd, resp.Decision, c.want, resp.Reason)
		}
	}
}
