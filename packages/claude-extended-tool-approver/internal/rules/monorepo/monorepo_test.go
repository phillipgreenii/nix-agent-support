package monorepo

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestMonorepo_Unknown_Abstain(t *testing.T) {
	pe := patheval.NewWithCWD("/home/user/monorepo", "/home/user/monorepo")
	r := New(pe, configrules.MonorepoConfig{ApprovedCommands: []string{"tc"}})
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:       "/home/user/monorepo",
		ToolInput: mustJSON(map[string]string{"command": "ls -la"}),
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("ls -la: got %s, want abstain", got.Decision)
	}
}

func TestMonorepo_NonBash_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/monorepo")
	r := New(pe, configrules.MonorepoConfig{})
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/tmp/x"}),
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("Read: got %s, want abstain", got.Decision)
	}
}

func TestMonorepo_Name(t *testing.T) {
	pe := patheval.New("/home/user/monorepo")
	r := New(pe, configrules.MonorepoConfig{})
	if got := r.Name(); got != "monorepo" {
		t.Errorf("Name() = %q, want monorepo", got)
	}
}

// TestMonorepo_EmptyConfigAbstains proves an unconfigured monorepo rule defers
// on a command that a configured rule would approve (safe base default).
func TestMonorepo_EmptyConfigAbstains(t *testing.T) {
	pe := patheval.NewWithCWD("/home/user/monorepo", "/home/user/monorepo")
	r := New(pe, configrules.MonorepoConfig{})
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:       "/home/user/monorepo",
		ToolInput: mustJSON(map[string]string{"command": "tc build"}),
	}
	if got := hookio.Verdict(r.Evaluate(input)); got.Decision != hookio.NoOpinion {
		t.Errorf("empty config `tc build`: got %s, want abstain", got.Decision)
	}
}

// TestMonorepo_ConfiguredApprove proves an approved command is approved, and the
// per-wrapper dangerous-env-var deferral withholds approval.
func TestMonorepo_ConfiguredApprove(t *testing.T) {
	pe := patheval.NewWithCWD("/home/user/monorepo", "/home/user/monorepo")
	r := New(pe, configrules.MonorepoConfig{
		ApprovedCommands:      []string{"tc", "uv"},
		DangerousEnvByWrapper: map[string][]string{"tc": {"TC_DANGER"}},
	})
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"approved command", "tc build", hookio.Approve},
		{"second approved command", "uv sync", hookio.Approve},
		{"approved with dangerous env defers", "TC_DANGER=1 tc build", hookio.NoOpinion},
		{"approved with benign env approves", "FOO=1 tc build", hookio.Approve},
		{"unapproved command abstains", "make all", hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/monorepo",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
			}
			if got := hookio.Verdict(r.Evaluate(input)); got.Decision != tt.want {
				t.Errorf("%q: got %s, want %s", tt.command, got.Decision, tt.want)
			}
		})
	}
}

// TestADR0044_Monorepo_DangerousEnvRefuses is the per-rule half of pg2-qxe85's census for
// monorepo: its single site now REFUSES.
//
// This is the site where the floor is most likely to have TEETH on a real consumer config,
// and that is why the relation matters more than the verdict. The basename IS on the
// consumer's approved list — the rule was one branch from approving it — and the dangerous
// env assignment is the whole reason it does not. Reported as not-applicable, the leaf
// could still be cleared by any later rule that happens to know the same wrapper (e.g.
// build-tools, which runs after monorepo). As a refusal that later Approve is demoted to
// abstain, which is the MORE-restrictive direction and the intended effect.
//
// The control row is what bounds it: the same wrapper WITHOUT the dangerous var must still
// approve, so the refusal is scoped to the invocation and not to the wrapper.
func TestADR0044_Monorepo_DangerousEnvRefuses(t *testing.T) {
	pe := patheval.NewWithCWD("/home/user/monorepo", "/home/user/monorepo")
	r := New(pe, configrules.MonorepoConfig{
		ApprovedCommands:      []string{"tc"},
		DangerousEnvByWrapper: map[string][]string{"tc": {"TC_DANGER"}},
	})
	in := func(cmd string) *hookio.HookInput {
		return &hookio.HookInput{ToolName: "Bash", CWD: "/home/user/monorepo", ToolInput: mustJSON(map[string]string{"command": cmd})}
	}

	res, err := r.Evaluate(in("TC_DANGER=1 tc build"))
	if !errors.Is(err, hookio.ErrRefused) {
		t.Fatalf("approved wrapper with a dangerous env var: err=%v res=%+v, want ErrRefused", err, res)
	}
	if res.Decision < hookio.NoOpinion {
		t.Errorf("floor is %s, weaker than NoOpinion", res.Decision)
	}
	if res.Reason == "" || res.Module != r.Name() {
		t.Errorf("floor = %+v, want a reasoned refusal attributed to %q", res, r.Name())
	}
	if !errors.Is(err, hookio.ErrNotApplicable) {
		t.Error("refusal does not match ErrNotApplicable; the engine would file it as a FAILURE")
	}

	res, err = r.Evaluate(in("tc build"))
	if err != nil || res.Decision != hookio.Approve {
		t.Errorf("same wrapper without the dangerous var = %+v (err=%v), want approve", res, err)
	}

	// An unapproved basename stays a genuine not-applicable: this rule has no model for it,
	// so a refusal would claim an examination that did not happen.
	if _, err := r.Evaluate(in("some-other-tool build")); errors.Is(err, hookio.ErrRefused) {
		t.Error("an unapproved basename reported a REFUSAL; it was never examined")
	}
}
