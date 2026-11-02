package effectpolicy

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// TestBdUpdateFlags pins bead tc-gx5ui (absorbing Bash(bd update:*)). The
// ruled tracker-write `bd update` takes bd's persistent flags and its own
// value flags after the verb; a runtime value for such a flag is a flag value,
// not a live positional. A runtime id, an unruled verb and secret file reads
// must keep abstaining/rejecting.
func TestBdUpdateFlags(t *testing.T) {
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
		{"claim_actor_var", `bd update tc-abc --claim --actor "$ACTOR" --json 2>&1`, evalcontract.Approve},
		{"release_vars", `bd update tc-abc --status open --assignee "" --actor "$ID"`, evalcontract.Approve},
		{"defer_assignee_var", `bd update tc-abc --defer +7d --status open --assignee "$X" --actor "$ID"`, evalcontract.Approve},
		{"add_label_var_actor", `bd update tc-abc --add-label human --actor "$ID"`, evalcontract.Approve},
		{"equals_form", `bd update tc-abc --status=in_progress`, evalcontract.Approve},
		{"literal_notes", `bd update tc-abc --append-notes "n" --priority 2 -t feature`, evalcontract.Approve},
		{"freetext_note_live_abstains", `bd update tc-abc --append-notes "$(date +%F) n" --actor x`, evalcontract.Abstain},
		{"positional_var_abstains", `bd update "$id" --claim --actor x`, evalcontract.Abstain},
		{"positional_bare_var_abstains", `bd update $ID --claim`, evalcontract.Abstain},
		{"unmodeled_flag_value_abstains", `bd update tc-abc --bogus "$X"`, evalcontract.Abstain},
		{"unruled_verb_abstains", `bd reopen tc-abc --actor "$A"`, evalcontract.Abstain},
		{"body_file_secret_not_approved", `bd update tc-abc --body-file ~/.ssh/id_rsa`, evalcontract.Reject},
		{"db_unknown_zone_abstains", `bd update tc-abc --db /etc/shadow`, evalcontract.Abstain},
	}
	for _, c := range cases {
		resp := Evaluate(evalcontract.Request{Command: c.cmd, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		if resp.Decision != c.want {
			t.Errorf("%s: %q\n  decision = %s, want %s\n  reason: %s", c.name, c.cmd, resp.Decision, c.want, resp.Reason)
		}
	}
}
