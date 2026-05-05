package safecmds

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestSafecmds_AlwaysSafe_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	commands := []string{
		"echo hello",
		"test -f foo",
		"true",
		"false",
		"printf '%s' foo",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:      "/home/user/project",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestSafecmds_JqWithProjectPath_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "jq . /home/user/project/package.json"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("jq with project path: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_JqWithNoPaths_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "jq ."}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("jq with no paths: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_CatEtcPasswd_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "cat /etc/passwd"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("cat /etc/passwd: got %s, want abstain", got.Decision)
	}
}

func TestSafecmds_Rm_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "rm -rf /"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("rm -rf /: got %s, want abstain", got.Decision)
	}
}

func TestSafecmds_Ls_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "ls"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("ls: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_HeadProjectFile_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "head -20 /home/user/project/README.md"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("head -20 project README: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_Name(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	if got := r.Name(); got != "safe-commands" {
		t.Errorf("Name() = %q, want safe-commands", got)
	}
}

func TestSafecmds_Compound_EchoAndRm_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "echo hello && rm -rf /"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("echo hello && rm -rf /: got %s, want abstain (rm is unknown)", got.Decision)
	}
}

func TestSafecmds_Compound_EchoAndCatEtcPasswd_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "echo hello && cat /etc/passwd"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("echo hello && cat /etc/passwd: got %s, want abstain (cat with unsafe path)", got.Decision)
	}
}

func TestSafecmds_Compound_EchoAndLs_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "echo hello && ls"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("echo hello && ls: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_Compound_JqAndYq_ProjectPaths_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "jq '.name' /home/user/project/file.json && yq '.v' /home/user/project/other.yaml"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("jq+yq with project paths: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_RmProjectPath_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "rm /home/user/project/tmp/file.txt"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("rm project path: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_RmNixStore_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "rm /nix/store/abc123/bin/foo"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("rm nix store (read-only): got %s, want abstain", got.Decision)
	}
}

func TestSafecmds_CatNixStore_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "cat /nix/store/abc123/bin/foo"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("cat nix store (read-only): got %s, want approve", got.Decision)
	}
}

func TestSafecmds_CpReadToWrite_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "cp /nix/store/abc123/file.txt /home/user/project/dest.txt"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("cp read-only source to writable dest: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_CpWriteToReadOnly_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "cp /home/user/project/file.txt /nix/store/abc123/dest.txt"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("cp to read-only dest: got %s, want abstain", got.Decision)
	}
}

func TestSafecmds_MvProjectPath_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "mv /home/user/project/old.txt /home/user/project/new.txt"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("mv within project: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_TouchProjectPath_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "touch /home/user/project/newfile.txt"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("touch project path: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_SedInPlace_WritePath_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "sed -i 's/foo/bar/' /home/user/project/file.txt"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("sed -i project path: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_SedInPlace_ReadOnlyPath_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "sed -i 's/foo/bar/' /nix/store/abc123/file.txt"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("sed -i read-only path: got %s, want abstain", got.Decision)
	}
}

func TestSafecmds_SedNoInPlace_ReadOnlyPath_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "sed 's/foo/bar/' /nix/store/abc123/file.txt"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("sed (no -i) read-only path: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_RmTmpPath_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "rm /tmp/scratch.txt"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("rm /tmp (writable): got %s, want approve", got.Decision)
	}
}

func TestSafecmds_NewCommands(t *testing.T) {
	pe := patheval.New("/tmp/project")
	r := New(pe)
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"df", "df -h", hookio.Approve},
		{"du in project", "du -sh /tmp/project/src", hookio.Approve},
		{"where", "where go", hookio.Approve},
		{"readlink in project", "readlink /tmp/project/link", hookio.Approve},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.command})}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v", got.Decision, tt.want)
			}
		})
	}
}

func TestSafecmds_GrepNixVarProfiles_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": `grep -rn "vscodeProfiles" /nix/var/nix/profiles/system-461-link/user/ 2>/dev/null | head -10`}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("grep on /nix/var/nix/profiles: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_LsNixVarProfiles_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "ls /nix/var/nix/profiles/system-461-link/user/"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("ls on /nix/var/nix/profiles: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_HeadNixVarProfiles_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "head -20 /nix/var/nix/profiles/system-461-link/user/some-file"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("head on /nix/var/nix/profiles: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_Pgrep_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "pgrep -f claude"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("pgrep: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_ReadlinkNixVar_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "readlink /nix/var/nix/profiles/system-461-link"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("readlink on /nix/var: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_RmNixVar_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "rm /nix/var/nix/profiles/some-link"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("rm on /nix/var (read-only): got %s, want abstain", got.Decision)
	}
}

func TestSafecmds_Help_CommandOnly_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	approve := []string{
		"unknowncmd --help",
		"docker --help",
		"nix --help",
		"kubectl --help",
	}
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:      "/home/user/project",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestSafecmds_Help_SubcommandKnown_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	approve := []string{
		"kubectl apply --help",
		"git rebase --help",
		"docker build --help",
		"nix flake --help",
		"gradle build --help",
		"gh pr --help",
		"cargo test --help",
		"npm install --help",
		"bd create --help",
	}
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:      "/home/user/project",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestSafecmds_Help_SubcommandForm_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	approve := []string{
		"go help",                // known command, bare help
		"go help build",          // known command, help + subcommand
		"cargo help test",        // known command, help + subcommand
		"kubectl help apply",     // known command, help + subcommand
		"git help rebase",        // known command, help + subcommand
		"npm help install",       // known command, help + subcommand
		"bd help",                // known command, bare help
	}
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:      "/home/user/project",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestSafecmds_Jq_ArgFlags_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	approve := []string{
		// --arg value should not be treated as a path
		`echo '{}' | jq --arg dir "/app/src" '.additionalIncludes = [$dir]'`,
		// --argjson value should not be treated as a path
		`echo '{}' | jq --argjson count 42 '.count = $count'`,
		// Multiple --arg flags
		`echo '{}' | jq --arg a "/foo" --arg b "/bar" '. + {a: $a, b: $b}'`,
	}
	for _, cmd := range approve {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:       "/home/user/project",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestSafecmds_Help_NotApproved(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	notApproved := []string{
		"unknowncmd help",           // unknown command, help not recognized
		"unknowncmd --help -r",      // --help not last
		"unknowncmd -r --help",      // flag before --help, not a subcommand
		"unknowncmd sub --help -v",  // --help not last
		"unknowncmd sub --help",     // unknown command with subcommand
		"go help -v build",          // help with flag, not clean
	}
	for _, cmd := range notApproved {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:      "/home/user/project",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got approve, want non-approve", cmd)
		}
	}
}

func TestEvaluateCp_Comprehensive(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)

	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"multi-source writable dest", "cp ./a.txt ./b.txt /tmp/dest/", hookio.Approve},
		{"mixed-access sources", "cp /nix/store/foo ./local.txt /tmp/dest/", hookio.Approve},
		{"unknown source", "cp /etc/shadow /tmp/dest/", hookio.Abstain},
		{"-t with multiple sources", "cp -t /tmp/dest/ ./a.txt ./b.txt", hookio.Approve},
		{"--target-directory= style", "cp --target-directory=/tmp/dest/ ./a.txt", hookio.Approve},
		{"-t to non-writable dest", "cp -t /etc/ ./a.txt", hookio.Abstain},
		{"no path-like args", "cp fileA fileB", hookio.Approve},
		{"single path arg", "cp ./only-one", hookio.Approve},
		{"-r flag with directory", "cp -r ./src/ /tmp/dest/", hookio.Approve},
		{"dest is read-only", "cp ./a.txt /nix/store/out", hookio.Abstain},
		{"-a flag recursive", "cp -a /home/user/project/src/ /tmp/backup/", hookio.Approve},
		{"multiple flags then paths", "cp -rv /home/user/project/a.txt /home/user/project/b.txt /tmp/out/", hookio.Approve},
		{"unknown source with -t", "cp -t /tmp/dest/ /etc/shadow", hookio.Abstain},
		{"all sources in project", "cp /home/user/project/a /home/user/project/b /home/user/project/dest/", hookio.Approve},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:      "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
			}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (reason: %s)", got.Decision, tt.want, got.Reason)
			}
		})
	}
}

func TestSafecmds_Sqlite3Removed(t *testing.T) {
	pe := patheval.New("/tmp/project")
	r := New(pe)
	input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": "sqlite3 /tmp/project/test.db 'SELECT 1'"})}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("Decision = %v, want Abstain (sqlite3 removed from safecmds)", got.Decision)
	}
}

func TestSafecmds_Xargs(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"find pipe xargs cat", "find /home/user/project | xargs cat", hookio.Approve},
		{"find pipe xargs ls", "find /home/user/project | xargs ls -la", hookio.Approve},
		{"xargs sh -c echo", "xargs -I {} sh -c 'echo {}'", hookio.Approve},
		{"xargs unknown cmd", "xargs curl http://example.com", hookio.Abstain},
		{"xargs no command", "xargs -I {}", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
			}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (reason: %s)", got.Decision, tt.want, got.Reason)
			}
		})
	}
}

func TestSafecmds_Jar(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"jar tf readable path", "jar tf /home/user/project/lib/file.jar", hookio.Approve},
		{"jar xf readable path", "jar xf /home/user/project/lib/file.jar", hookio.Approve},
		{"jar tf unknown path", "jar tf /etc/secret.jar", hookio.Abstain},
		{"jar create", "jar cf /home/user/project/out.jar", hookio.Abstain},
		{"jar no args", "jar", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
			}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (reason: %s)", got.Decision, tt.want, got.Reason)
			}
		})
	}
}

func TestSafecmds_YqSpecialHandling(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"yq read project file", "yq '.key' /home/user/project/file.yaml", hookio.Approve},
		{"yq read unknown path", "yq '.key' /etc/secret.yaml", hookio.Abstain},
		{"yq -i write project file", "yq -i '.key = \"value\"' /home/user/project/file.yaml", hookio.Approve},
		{"yq -i write read-only path", "yq -i '.key = \"value\"' /nix/store/abc123/file.yaml", hookio.Abstain},
		{"yq --inplace write project file", "yq --inplace '.key = \"value\"' /home/user/project/file.yaml", hookio.Approve},
		{"yq no paths", "yq '.key'", hookio.Approve},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
			}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (reason: %s)", got.Decision, tt.want, got.Reason)
			}
		})
	}
}

func TestSafecmds_Shellcheck_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:       "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "shellcheck script.sh"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("shellcheck: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_Lsof_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:       "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "lsof -i :8080"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("lsof: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_ContainedClaude_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:       "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "contained-claude --version 2>&1"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("contained-claude --version: got %s, want approve", got.Decision)
	}
}

func TestSafecmds_Unzip(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"unzip readable archive in writable cwd", "unzip /home/user/project/archive.zip", hookio.Approve},
		{"unzip -d writable dest", "unzip -d /tmp /home/user/project/archive.zip", hookio.Approve},
		{"unzip -d writable dest reversed args", "unzip /home/user/project/archive.zip -d /tmp", hookio.Approve},
		{"unzip -l list only", "unzip -l /home/user/project/archive.zip", hookio.Approve},
		{"unzip -t test only", "unzip -t /home/user/project/archive.zip", hookio.Approve},
		{"unzip -l list from nix store", "unzip -l /nix/store/abc123/archive.zip", hookio.Approve},
		{"unzip unknown archive", "unzip /etc/secret.zip", hookio.Abstain},
		{"unzip -d unknown dest", "unzip -d /etc/somewhere /home/user/project/archive.zip", hookio.Abstain},
		{"unzip readable archive to nix store", "unzip -d /nix/store/abc123 /home/user/project/archive.zip", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
			}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (reason: %s)", got.Decision, tt.want, got.Reason)
			}
		})
	}
}

func TestSafecmds_BashSyntaxCheck(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"bash -n readable file", "bash -n /home/user/project/script.sh", hookio.Approve},
		{"bash -n readable file with echo", `bash -n /home/user/project/script.sh && echo "OK"`, hookio.Approve},
		{"bash -n nix store file", "bash -n /nix/store/abc123/script.sh", hookio.Approve},
		{"bash -n unknown path", "bash -n /etc/secret.sh", hookio.Abstain},
		{"sh -n readable file", "sh -n /home/user/project/script.sh", hookio.Approve},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
			}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (reason: %s)", got.Decision, tt.want, got.Reason)
			}
		})
	}
}
