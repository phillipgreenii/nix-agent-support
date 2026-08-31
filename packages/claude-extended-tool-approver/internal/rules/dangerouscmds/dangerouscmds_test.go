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
		// fdisk is NOT operand-gated like mount, even though `fdisk -l` really is a
		// read-only partition-table listing on util-linux: the flag is
		// platform-divergent (BSD/darwin `fdisk` has no listing `-l`, and bare
		// `fdisk <device>` there is an INTERACTIVE partition editor), so misreading
		// an operand as a listing flag there would be the worst outcome on this
		// list. Operator ruling, pg2-km9ng, 2026-07-30: no change, documented --
		// pinned as an unconditional Reject.
		{"fdisk", "fdisk -l", "Bash", hookio.Reject},
		{"mount", "mount /dev/sdb /mnt", "Bash", hookio.Reject},
		{"umount", "umount /mnt", "Bash", hookio.Reject},
		// umount is NOT operand-gated: it always needs a target, so the bare form is
		// a usage error, not a query worth recovering (pg2-2nm54 audit).
		{"umount bare still rejects", "umount", "Bash", hookio.Reject},
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
		// Command-runner wrappers must not hide the dangerous inner command from
		// the argv[0]-keyed denylist (tc-otuid): cmdparse now unwraps nice/timeout
		// (and nohup/stdbuf) so dd is seen and Rejected.
		{"nice wraps dd", "nice dd if=/dev/zero of=/dev/sda", "Bash", hookio.Reject},
		{"timeout wraps dd", "timeout 5 dd if=/dev/zero of=/dev/sda", "Bash", hookio.Reject},
		// bgrun is the same shape: without unwrapping it past its own literal
		// `--` boundary, `bgrun x -- <anything>` would be a permission-
		// laundering hole (the leaf's Executable would read "bgrun", not the
		// wrapped command). A dangerous payload must still Reject through it,
		// and a safe payload (asserted below, safecmds.go's jurisdiction) must
		// still resolve safe rather than inheriting bgrun's own denial.
		{"bgrun wraps dd", "bgrun x -- dd if=/dev/zero of=/dev/sda", "Bash", hookio.Reject},
		{"bgrun wraps safe command", "bgrun x -- ls -la", "Bash", hookio.NoOpinion},
		// Not on the denylist / handled elsewhere.
		{"curl not here (curl rule)", "curl https://x", "Bash", hookio.NoOpinion},
		{"ssh not here (ssh rule)", "ssh host ls", "Bash", hookio.NoOpinion},
		{"kill not in scope", "kill 1234", "Bash", hookio.NoOpinion},
		{"safe ls", "ls -la", "Bash", hookio.NoOpinion},
		{"dangerous as substring arg", "echo sudo", "Bash", hookio.NoOpinion},
		{"non-bash", "", "Read", hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: tt.tool, ToolInput: mustJSON(tt.command)}
			if got := hookio.Verdict(r.Evaluate(input)).Decision; got != tt.want {
				t.Errorf("Decision = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestMountOperandGate pins BOTH directions of the pg2-2nm54 operand gate: the
// operand-less `mount` listing is no longer hard-denied, and every form that can
// actually mount — or that the rule cannot prove is a listing — still Rejects.
func TestMountOperandGate(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		// --- read-only listing forms: no longer dangerous ---
		{"bare mount", "mount", hookio.NoOpinion},
		{"bare mount piped", "mount | grep acme", hookio.NoOpinion},
		// Row 310193's exact shape: the listing reached the rule only as an
		// assignment-only segment's command substitution (pg2-mtnmb made that
		// position rule-visible), so the position is pinned explicitly.
		{"row 310193 shape: VAR=$(mount | awk)", `DATA_DEV=$(mount | awk '/on \/System\/Volumes\/Data /{print $1; exit}')`, hookio.NoOpinion},
		{"assignment substitution, plain", "X=$(mount)", hookio.NoOpinion},
		{"listing inside a compound", `echo "--- mounts ---" && mount | head -5`, hookio.NoOpinion},
		{"full path bare mount", "/sbin/mount", hookio.NoOpinion},
		{"env-prefixed bare mount", "env mount", hookio.NoOpinion},
		// --- informational flags: allowed ---
		{"show-labels short", "mount -l", hookio.NoOpinion},
		{"show-labels long", "mount --show-labels", hookio.NoOpinion},
		{"verbose short", "mount -v", hookio.NoOpinion},
		{"verbose long", "mount --verbose", hookio.NoOpinion},
		{"version", "mount -V", hookio.NoOpinion},
		{"help", "mount --help", hookio.NoOpinion},
		{"type filter short", "mount -t apfs", hookio.NoOpinion},
		{"type filter long", "mount --types nfs", hookio.NoOpinion},
		{"type filter inline long", "mount --types=nfs", hookio.NoOpinion},
		{"clustered informational shorts", "mount -lv", hookio.NoOpinion},
		{"informational combination", "mount -l -v -t ext4", hookio.NoOpinion},
		// --- operand-bearing / unprovable forms: still Reject ---
		{"device and dir", "mount /dev/sdb /mnt", hookio.Reject},
		{"single fstab operand", "mount /mnt", hookio.Reject},
		{"type plus operands", "mount -t nfs server:/export /mnt", hookio.Reject},
		{"mount all", "mount -a", hookio.Reject},
		{"mount all long", "mount --all", hookio.Reject},
		{"remount options", "mount -o remount,rw /", hookio.Reject},
		{"bind", "mount --bind /src /dst", hookio.Reject},
		{"target in flag form", "mount --target /mnt", hookio.Reject},
		{"source in flag form", "mount --source /dev/sdb", hookio.Reject},
		{"label selects device", "mount -L mydisk", hookio.Reject},
		{"uuid selects device", "mount -U 1234-5678", hookio.Reject},
		{"read-only flag is not informational", "mount -r /dev/sdb /mnt", hookio.Reject},
		{"unresolved expansion operand", "mount $TARGET", hookio.Reject},
		{"quoted expansion operand", `mount "$DEV" "$DIR"`, hookio.Reject},
		{"end-of-options then operand", "mount -- /mnt", hookio.Reject},
		{"bare end-of-options marker", "mount --", hookio.Reject},
		{"type flag missing its value", "mount -t", hookio.Reject},
		{"unknown flag", "mount --no-canonicalize", hookio.Reject},
		{"cluster containing a value flag", "mount -lt", hookio.Reject},
		{"cluster containing a mounting flag", "mount -av", hookio.Reject},
		{"attached short type value", "mount -text4", hookio.Reject},
		{"operand-bearing leaf in a compound", "mount | head -1 && mount /dev/sdb /mnt", hookio.Reject},
		{"sudo prefix still rejects on sudo", "sudo mount", hookio.Reject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(tt.command)}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != tt.want {
				t.Errorf("Evaluate(%q).Decision = %v (%s), want %v", tt.command, got.Decision, got.Reason, tt.want)
			}
		})
	}
}
