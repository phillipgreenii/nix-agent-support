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

func TestSafecmds_DynamicWritePath_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	// Write commands whose path arg is dynamically expanded ($VAR / $(...) /
	// backtick) hide their real target from path evaluation — defer to prompt
	// (pg2-t4uyx). looksLikePath does not match a bare $VAR.
	abstain := []string{
		"rm -rf $HOME/.ssh",
		"cp secret $HOME/exfil",
		"tee $HOME/.bashrc",
		"mv a ${TARGET}/b",
		"touch $BUILD_DIR/marker",
		"chmod 700 $DIR",
		"mkdir $HOME/x",
	}
	for _, cmd := range abstain {
		input := &hookio.HookInput{ToolName: "Bash", CWD: "/home/user/project", ToolInput: mustJSON(map[string]string{"command": cmd})}
		got := r.Evaluate(input)
		if got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain (dynamic write path)", cmd, got.Decision)
		}
	}

	// A literal in-project write path (no expansion) must be unchanged (Approve),
	// proving the guard is scoped to dynamic args, not all writes.
	input := &hookio.HookInput{ToolName: "Bash", CWD: "/home/user/project", ToolInput: mustJSON(map[string]string{"command": "rm -rf ./build"})}
	if got := r.Evaluate(input); got.Decision != hookio.Approve {
		t.Errorf("literal in-project `rm -rf ./build`: got %s (%s), want approve", got.Decision, got.Reason)
	}
}

func TestSafecmds_Pg2_5k6pu_Commands(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	cases := []struct {
		cmd  string
		want hookio.Decision
	}{
		// sw_vers: read-only macOS version query (alwaysSafe)
		{"sw_vers", hookio.Approve},
		{"sw_vers -productVersion", hookio.Approve},
		// xxd: reads file contents — path in a readable zone approves
		{"xxd /home/user/project/data.bin", hookio.Approve},
		{"xxd -l 64 /home/user/project/data.bin", hookio.Approve},
		// xxd on an out-of-zone path defers
		{"xxd /etc/shadow", hookio.Abstain},
		// log: show/stream/stats are read-only; erase/config/collect mutate
		{"log show --last 5m", hookio.Approve},
		{"log stream --level debug", hookio.Approve},
		{"log stats", hookio.Approve},
		{"log erase --all", hookio.Abstain},
		{"log config --status", hookio.Abstain},
		{"log collect", hookio.Abstain},
	}
	for _, c := range cases {
		input := &hookio.HookInput{ToolName: "Bash", CWD: "/home/user/project", ToolInput: mustJSON(map[string]string{"command": c.cmd})}
		if got := r.Evaluate(input); got.Decision != c.want {
			t.Errorf("cmd %q: got %s (%s), want %s", c.cmd, got.Decision, got.Reason, c.want)
		}
	}
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
			CWD:       "/home/user/project",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

// TestSafecmds_Cd_Approve is the pg2-trh3z regression guard for `cd`'s
// membership in the alwaysSafe set (safecmds.go:21), which was previously
// untested. A bare `cd <abs-path>` MUST Approve — and because `cd` is in NO
// other command map (browsingCmds/safeReadCmds/safeWriteCmds), that Approve can
// only come from the alwaysSafe branch: drop "cd" from alwaysSafe and every case
// here falls through to the "Unknown command" Abstain. The cd target is
// irrelevant (alwaysSafe short-circuits before any path check), so an
// out-of-zone absolute path still Approves.
func TestSafecmds_Cd_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	commands := []string{
		"cd /home/user/project",
		"cd /tmp",
		"cd /some/other/absolute/path",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:       "/home/user/project",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve (cd is alwaysSafe)", cmd, got.Decision, got.Reason)
		}
	}
}

func TestSafecmds_JqWithProjectPath_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "rm -rf /"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("rm -rf /: got %s, want abstain", got.Decision)
	}
}

// TestSafecmds_LooksLikePath_TildeUser is the tc-fielf regression: a "~user"
// argument (tilde + username, no slash) is path-shaped and MUST be classified so
// the zone check runs. Before this bead looksLikePath matched bare "~" and "~/..."
// (tc-sfpto) but NOT "~user", so `rm -rf ~someuser` was never classified and
// slipped through as safe. Bare "~" and "~/..." MUST stay path-shaped too.
func TestSafecmds_LooksLikePath_TildeUser(t *testing.T) {
	tests := []struct {
		arg  string
		want bool
	}{
		{"~someuser", true},        // tc-fielf: the gap
		{"~someuser/config", true}, // ~user with a trailing path
		{"~root", true},
		{"~", true},           // bare ~ (tc-sfpto) still path-shaped
		{"~/x", true},         // ~/... still path-shaped
		{"/etc/passwd", true}, // sanity: absolute path
		{"README.md", false},  // bare relative filename is not path-shaped
	}
	for _, tt := range tests {
		if got := looksLikePath(tt.arg); got != tt.want {
			t.Errorf("looksLikePath(%q) = %v, want %v", tt.arg, got, tt.want)
		}
	}
}

// TestSafecmds_RmTildeUser_Abstain pins the end-to-end tc-fielf behavior: because
// "~someuser" is now path-shaped and resolves to an UNKNOWN zone (unknown user ->
// cleanPath returns "" -> PathUnknown; a known user's home is also outside every
// writable zone), `rm -rf ~someuser` is NOT auto-approved.
func TestSafecmds_RmTildeUser_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:       "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": "rm -rf ~someuser"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("rm -rf ~someuser: got %s (%s), want abstain", got.Decision, got.Reason)
	}
}

func TestSafecmds_Ls_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
		CWD:       "/home/user/project",
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
			CWD:       "/home/user/project",
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
			CWD:       "/home/user/project",
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
		"go help",            // known command, bare help
		"go help build",      // known command, help + subcommand
		"cargo help test",    // known command, help + subcommand
		"kubectl help apply", // known command, help + subcommand
		"git help rebase",    // known command, help + subcommand
		"npm help install",   // known command, help + subcommand
		"bd help",            // known command, bare help
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
		"unknowncmd help",          // unknown command, help not recognized
		"unknowncmd --help -r",     // --help not last
		"unknowncmd -r --help",     // flag before --help, not a subcommand
		"unknowncmd sub --help -v", // --help not last
		"unknowncmd sub --help",    // unknown command with subcommand
		"go help -v build",         // help with flag, not clean
	}
	for _, cmd := range notApproved {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:       "/home/user/project",
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

// TestSafecmds_Gofmt covers pg2-wcsur: read-only gofmt (no -w) Approves, while
// any -w (write-in-place) invocation is NOT approved (Abstain — deferred to the
// normal flow). Read-mode path handling follows the read-command model
// (cat/sed/yq): a path-like arg outside a readable zone Abstains; the common
// `gofmt -l .` / `gofmt -d <name>` cases carry no path-like arg and Approve.
func TestSafecmds_Gofmt(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		// Read-only flags print to stdout only — Approve.
		{"gofmt -l list (dot)", "gofmt -l .", hookio.Approve},
		{"gofmt -d diff relative file", "gofmt -d main.go", hookio.Approve},
		{"gofmt -d diff project path", "gofmt -d /home/user/project/main.go", hookio.Approve},
		{"gofmt bare relative file", "gofmt main.go", hookio.Approve},
		{"gofmt -s -l simplify list", "gofmt -s -l .", hookio.Approve},
		{"gofmt -e all errors", "gofmt -e main.go", hookio.Approve},
		{"gofmt -l project dir path", "gofmt -l /home/user/project", hookio.Approve},
		// Read on an out-of-zone path defers, matching cat/sed read-command model.
		{"gofmt -d out-of-zone path", "gofmt -d /etc/shadow", hookio.Abstain},
		// Any -w write invocation is NOT approved — deferred to normal flow.
		{"gofmt -w write relative", "gofmt -w main.go", hookio.Abstain},
		{"gofmt -w write project path", "gofmt -w /home/user/project/main.go", hookio.Abstain},
		{"gofmt -s -w simplify+write", "gofmt -s -w .", hookio.Abstain},
		{"gofmt -l -w list+write", "gofmt -l -w .", hookio.Abstain},
		{"gofmt --w double-dash write", "gofmt --w main.go", hookio.Abstain},
		{"gofmt -w=true explicit write", "gofmt -w=true main.go", hookio.Abstain},
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

// TestSafecmds_Strings covers `strings` as a read command (pg2-t76k8). Before
// this bead `strings` hit the unknown-command fallthrough and abstained; it now
// belongs to safeReadCmds, so it is routed through the SAME readPathIssue
// path-safety check as cat/head/tail/wc — approved only for readable zones,
// abstaining for unknown/secret-adjacent paths (mirrors
// TestSafecmds_CatEtcPasswd_Abstain / TestSafecmds_HeadProjectFile_Approve).
func TestSafecmds_Strings(t *testing.T) {
	// Pin HOME so `~/.aws` below is genuinely an UNKNOWN zone. It is read once at
	// evaluator construction, so it must be set first, and it must be a fixed
	// non-/nix path: the shared mkGoTest builder exports HOME=$TMPDIR, which on
	// darwin is /nix-rooted, and Evaluate's READ-ONLY /nix rule would then make
	// ~/.aws readable — inverting the assertion below (`phillipg-nix-repo-base`
	// ADR 0021).
	t.Setenv("HOME", "/home/testuser")
	pe := patheval.New("/home/user/project")
	r := New(pe)
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		// In-zone file (project root) -> Approve.
		{"strings project binary", "strings /home/user/project/bin/tool", hookio.Approve},
		// Secret-adjacent path stays protected (~/.aws is an unknown zone, so the
		// read command abstains — the secret is never auto-approved).
		{"strings aws credentials abstains", "strings ~/.aws/credentials", hookio.Abstain},
		// Unknown system path -> Abstain (zone guard, matches cat /etc/passwd).
		{"strings /etc/passwd abstains", "strings /etc/passwd", hookio.Abstain},
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

// TestSafecmds_DynamicReadPath_Abstain is the pg2-2ke04 (P0 SECURITY) guard: a READ
// command whose path argument is dynamically expanded ($VAR / ${VAR} / $(...) /
// backtick) is NOT statically determinable, so it MUST NOT be approved — the same
// refusal TestSafecmds_DynamicWritePath_Abstain pins for writes.
//
// The bypass this closes: because looksLikePath matches only a literal `/`, `./`,
// `../` or `~/` prefix, a `$F` argument was not path-like, no zone check ran, and
// `F=/Users/me/.ssh/id_rsa; cat $F` auto-approved a read of a DENY-LISTED
// credential in every permission mode, while the identical `rm $F` was already
// refused.
func TestSafecmds_DynamicReadPath_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	abstain := []string{
		"cat $F",
		"cat \"$F\"",
		"cat ${F}",
		"head -1 $F",
		"tail -f $LOGFILE",
		"xxd $F",
		"strings $F",
		"wc -l $f",
		"cat $HOME/.ssh/id_rsa",
		"cat $D/id_rsa",
		"cat ~/$REL",
		"cat /tmp/$F",
		"cat ./$F",
		"cat $(echo /Users/me/.ssh/id_rsa)",
		"cat `echo /Users/me/.ssh/id_rsa`",
		"grep x $F",
		"rg x $F",
		"sed -n 1p $F",
		"yq . $F",
		"gofmt -l $F",
		"jar tf $F",
		"bash -n $F",
		"jq . $F",
	}
	for _, cmd := range abstain {
		input := &hookio.HookInput{ToolName: "Bash", CWD: "/home/user/project", ToolInput: mustJSON(map[string]string{"command": cmd})}
		got := r.Evaluate(input)
		if got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s (%s), want abstain (dynamic read path)", cmd, got.Decision, got.Reason)
		}
	}

	// Static in-zone reads are unchanged, proving the guard is scoped to dynamic
	// args rather than gating reads wholesale.
	approve := []string{
		"cat /home/user/project/README.md",
		"head -20 /home/user/project/go.mod",
		"wc -l /home/user/project/main.go",
		"grep -rn foo /home/user/project",
		"jq . /home/user/project/x.json",
		"cat",
	}
	for _, cmd := range approve {
		input := &hookio.HookInput{ToolName: "Bash", CWD: "/home/user/project", ToolInput: mustJSON(map[string]string{"command": cmd})}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve (static in-zone read)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestSafecmds_EverySafeReadCmdGatesDynamicPath enumerates safeReadCmds FROM THE MAP
// ITSELF — not from a hand-copied list — so a member added later cannot silently
// escape the pg2-2ke04 guard. Each member is asked to read a deny-listed credential
// through one variable hop; none may approve.
func TestSafecmds_EverySafeReadCmdGatesDynamicPath(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	if len(safeReadCmds) == 0 {
		t.Fatal("safeReadCmds is empty; the enumeration would assert nothing")
	}
	for cmdName := range safeReadCmds {
		for _, spelling := range []string{cmdName + " $F", cmdName + " \"$F\"", cmdName + " $(echo /Users/me/.ssh/id_rsa)"} {
			input := &hookio.HookInput{ToolName: "Bash", CWD: "/home/user/project", ToolInput: mustJSON(map[string]string{"command": spelling})}
			got := r.Evaluate(input)
			if got.Decision == hookio.Approve {
				t.Errorf("safeReadCmds member %q: %q was APPROVED (%s); want != approve", cmdName, spelling, got.Reason)
			}
		}
	}
}

// TestSafecmds_ProgramOperandRole pins the ROLE split the pg2-2ke04 guard needs
// (programOperand / isDynamicPathOperand): awk's program, sed's script and jq's
// filter are CODE, and code legitimately contains a literal `$` — an awk field
// reference, a sed end-of-line anchor, a jq variable bound by --arg. Those args
// reach this rule POST-UNQUOTE, so the single-quoted `$` the shell never expands is
// textually identical to a live expansion; judging them by the coarse
// contains-a-dollar predicate would gate every `awk '{print $2}' file`.
//
// The program operand is NOT exempt — a program that is ITSELF a bare expansion is
// indistinguishable from a path and still refused.
func TestSafecmds_ProgramOperandRole(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		// Program/filter/script text containing a literal `$` stays approved.
		{"awk field ref", "awk '{print $1}' /home/user/project/x", hookio.Approve},
		{"awk field sep", `awk -F'\t' '{print $2}' /home/user/project/x`, hookio.Approve},
		{"awk assign flag", "awk -v n=1 '{print $n}' /home/user/project/x", hookio.Approve},
		{"sed eol anchor", "sed 's/x$//' /home/user/project/x", hookio.Approve},
		{"jq filter var", "jq '.count = $c' /home/user/project/x.json", hookio.Approve},
		{"jq arg then filter", "jq --arg a b '{a:$a}' /home/user/project/x.json", hookio.Approve},
		// A program operand that IS a bare expansion is still refused.
		{"awk bare var program", "awk $F", hookio.Abstain},
		{"sed bare var script", "sed $S /home/user/project/x", hookio.Abstain},
		{"jq bare var filter", "jq $Q /home/user/project/x.json", hookio.Abstain},
		{"awk subst program", "awk $(cat /home/user/project/prog.awk)", hookio.Abstain},
		// A path BUILT from an expansion is refused even in program position.
		{"awk var-rooted path program", "awk $D/prog.awk", hookio.Abstain},
		// Program supplied by -f: every positional is a path, so the coarse
		// predicate applies to all of them.
		{"awk -f then dynamic file", "awk -f /home/user/project/p.awk $F", hookio.Abstain},
		{"sed -e then dynamic file", "sed -e 's/a/b/' $F", hookio.Abstain},
		// A dynamic FILE argument is refused even when a static program precedes it.
		{"awk program then dynamic file", "awk '{print $1}' $F", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", CWD: "/home/user/project", ToolInput: mustJSON(map[string]string{"command": tt.command})}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (reason: %s)", got.Decision, tt.want, got.Reason)
			}
		})
	}
}

// TestSafecmds_BrowsingCmdsKeepDynamicPaths records a DELIBERATE exclusion from the
// pg2-2ke04 guard: ls/find/du/stat/file/lsof expose names, sizes and timestamps but
// never file CONTENT, so a dynamic path there is not an exfiltration primitive and
// gating it would buy prompts with no security gain.
func TestSafecmds_BrowsingCmdsKeepDynamicPaths(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	for _, cmd := range []string{"ls $d", "ls -la $HOME", "find $d -name x", "du -sh $d", "stat $f"} {
		input := &hookio.HookInput{ToolName: "Bash", CWD: "/home/user/project", ToolInput: mustJSON(map[string]string{"command": cmd})}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve (browsing commands are exempt)", cmd, got.Decision, got.Reason)
		}
	}
}
