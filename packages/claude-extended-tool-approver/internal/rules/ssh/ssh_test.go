package ssh

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

func TestSSH_EmptyConfigAbstains(t *testing.T) {
	r := New(Config{})
	// Even a command that a configured rule would DENY must Abstain when the rule
	// is unconfigured (safe WS2 default; WS3 supplies the data).
	for _, cmd := range []string{
		"ssh root@host rm -rf /",
		"ssh host ls",
		"scp host:/etc/passwd .",
		"sshpass -p x ssh host",
		// The -o bypass vectors must ALSO Abstain when the rule is unconfigured.
		"ssh -o User=root host ls",
		"ssh -oUser=root host ls",
		"ssh -oPasswordAuthentication=yes host ls",
		"ssh -oPubkeyAuthentication=no host ls",
	} {
		input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(cmd)}
		if got := r.Evaluate(input).Decision; got != hookio.Abstain {
			t.Errorf("empty config: %q => %v, want Abstain", cmd, got)
		}
	}
}

func TestSSH_Configured(t *testing.T) {
	cfg := Config{
		AllowedUsers:     []string{"tcadmin"},
		ReadonlyCommands: []string{"ls", "cat", "systemctl"},
		ReadonlySubcommands: map[string][]string{
			"systemctl": {"status", "is-active"},
		},
		SecretPathPatterns:   []string{"/etc/shadow", ".env", "id_rsa"},
		PasswordFlagPatterns: []string{"passwordauthentication=yes", "preferredauthentications=password", "pubkeyauthentication=no"},
	}
	r := New(cfg)
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"readonly ssh approved", "ssh host ls -la", hookio.Approve},
		{"allowed user approved", "ssh tcadmin@host cat /tmp/log", hookio.Approve},
		{"readonly subcommand approved", "ssh host systemctl status sshd", hookio.Approve},
		{"non-allowlisted subcommand asks", "ssh host systemctl restart sshd", hookio.Ask},
		{"disallowed user rejected", "ssh root@host ls", hookio.Reject},
		{"disallowed user via -l rejected", "ssh -l root host ls", hookio.Reject},
		{"disallowed user via -o User rejected", "ssh -o User=root host ls", hookio.Reject},
		{"disallowed user via glued -oUser rejected", "ssh -oUser=root host ls", hookio.Reject},
		{"password auth rejected", "ssh -o PasswordAuthentication=yes host ls", hookio.Reject},
		{"password auth via glued -o rejected", "ssh -oPasswordAuthentication=yes host ls", hookio.Reject},
		{"pubkey-disable via glued -o rejected", "ssh -oPubkeyAuthentication=no host ls", hookio.Reject},
		{"sshpass rejected", "sshpass -p secret ssh host ls", hookio.Reject},
		{"interactive ssh asks", "ssh host", hookio.Ask},
		{"unknown remote command asks", "ssh host make install", hookio.Ask},
		{"secret path in remote asks", "ssh host cat /etc/shadow", hookio.Ask},
		{"remote redirect asks", "ssh host 'ls > /tmp/out'", hookio.Ask},
		{"scp download approved", "scp host:/tmp/log.txt .", hookio.Approve},
		{"scp download secret asks", "scp host:/home/u/.env .", hookio.Ask},
		{"scp upload asks", "scp ./local.txt host:/tmp/", hookio.Ask},
		{"non-ssh abstains", "ls -la", hookio.Abstain},
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
