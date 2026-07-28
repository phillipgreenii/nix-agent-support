package vault

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
)

func mustJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

func TestVault_EmptyConfigAbstains(t *testing.T) {
	r := New(configrules.VaultConfig{})
	for _, cmd := range []string{
		"vault read secret/foo",
		"vault write secret/foo x=1",
		"vault kv put secret/foo x=1",
		"vault status",
	} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(cmd)}
		if got := r.Evaluate(input).Decision; got != hookio.Abstain {
			t.Errorf("empty config: %q => %v, want Abstain", cmd, got)
		}
	}
}

func TestVault_Configured(t *testing.T) {
	cfg := configrules.VaultConfig{
		ReadVerbs:  []string{"read", "status", "version", "kv get", "kv list"},
		WriteVerbs: []string{"write", "delete", "kv put", "kv delete"},
	}
	r := New(cfg)
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"read approved", "vault read secret/foo", hookio.Approve},
		{"status approved", "vault status", hookio.Approve},
		{"kv get approved", "vault kv get secret/foo", hookio.Approve},
		{"kv list approved", "vault kv list secret/", hookio.Approve},
		{"write asks", "vault write secret/foo x=1", hookio.Ask},
		{"delete asks", "vault delete secret/foo", hookio.Ask},
		{"kv put asks", "vault kv put secret/foo x=1", hookio.Ask},
		{"kv delete asks", "vault kv delete secret/foo", hookio.Ask},
		{"unknown subcommand abstains", "vault lease renew abc", hookio.Abstain},
		{"bare kv abstains", "vault kv", hookio.Abstain},
		{"non-vault abstains", "kubectl get pods", hookio.Abstain},
		{"non-bash abstains", "", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := "Bash"
			if tt.command == "" {
				tool = "Read"
			}
			input := &hookio.HookInput{ToolName: tool, ToolInput: mustJSON(tt.command)}
			if got := r.Evaluate(input).Decision; got != tt.want {
				t.Errorf("%q => %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}
