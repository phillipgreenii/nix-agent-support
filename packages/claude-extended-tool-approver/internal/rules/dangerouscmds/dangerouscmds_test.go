package dangerouscmds

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

func TestDangerousCommands(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
		tool    string
		want    hookio.Decision
	}{
		{"sudo", "sudo rm -rf /", "Bash", hookio.Reject},
		{"su", "su - root", "Bash", hookio.Reject},
		{"doas", "doas whoami", "Bash", hookio.Reject},
		{"dd", "dd if=/dev/zero of=/dev/sda", "Bash", hookio.Reject},
		{"mkfs bare", "mkfs /dev/sdb", "Bash", hookio.Reject},
		{"mkfs.ext4 variant", "mkfs.ext4 /dev/sdb1", "Bash", hookio.Reject},
		{"fdisk", "fdisk -l", "Bash", hookio.Reject},
		{"mount", "mount /dev/sdb /mnt", "Bash", hookio.Reject},
		{"umount", "umount /mnt", "Bash", hookio.Reject},
		{"reboot", "reboot", "Bash", hookio.Reject},
		{"shutdown", "shutdown -h now", "Bash", hookio.Reject},
		{"wget", "wget http://x/y", "Bash", hookio.Reject},
		{"nc", "nc -l 4444", "Bash", hookio.Reject},
		{"ncat", "ncat host 22", "Bash", hookio.Reject},
		{"netcat", "netcat host 22", "Bash", hookio.Reject},
		{"telnet", "telnet host", "Bash", hookio.Reject},
		{"sftp", "sftp user@host", "Bash", hookio.Reject},
		{"full path sudo", "/usr/bin/sudo id", "Bash", hookio.Reject},
		{"dangerous leaf in compound", "git status && sudo rm -rf /", "Bash", hookio.Reject},
		// Not on the denylist / handled elsewhere.
		{"curl not here (curl rule)", "curl https://x", "Bash", hookio.Abstain},
		{"ssh not here (ssh rule)", "ssh host ls", "Bash", hookio.Abstain},
		{"kill not in scope", "kill 1234", "Bash", hookio.Abstain},
		{"safe ls", "ls -la", "Bash", hookio.Abstain},
		{"dangerous as substring arg", "echo sudo", "Bash", hookio.Abstain},
		{"non-bash", "", "Read", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: tt.tool, ToolInput: mustJSON(tt.command)}
			if got := r.Evaluate(input).Decision; got != tt.want {
				t.Errorf("Decision = %v, want %v", got, tt.want)
			}
		})
	}
}
