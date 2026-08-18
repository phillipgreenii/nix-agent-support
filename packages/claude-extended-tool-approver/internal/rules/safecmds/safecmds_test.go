package safecmds

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
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
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s, want abstain (dynamic write path)", cmd, got.Decision)
		}
	}

	// A literal in-project write path (no expansion) must be unchanged (Approve),
	// proving the guard is scoped to dynamic args, not all writes.
	input := &hookio.HookInput{ToolName: "Bash", CWD: "/home/user/project", ToolInput: mustJSON(map[string]string{"command": "rm -rf ./build"})}
	if got := hookio.Verdict(r.Evaluate(input)); got.Decision != hookio.Approve {
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
		{"xxd /etc/shadow", hookio.NoOpinion},
		// log: show/stream/stats are read-only; erase/config/collect mutate
		{"log show --last 5m", hookio.Approve},
		{"log stream --level debug", hookio.Approve},
		{"log stats", hookio.Approve},
		{"log erase --all", hookio.NoOpinion},
		{"log config --status", hookio.NoOpinion},
		{"log collect", hookio.NoOpinion},
	}
	for _, c := range cases {
		input := &hookio.HookInput{ToolName: "Bash", CWD: "/home/user/project", ToolInput: mustJSON(map[string]string{"command": c.cmd})}
		if got := hookio.Verdict(r.Evaluate(input)); got.Decision != c.want {
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
		got := hookio.Verdict(r.Evaluate(input))
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
		got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
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
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
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
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
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
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
			got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
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
		got := hookio.Verdict(r.Evaluate(input))
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
		got := hookio.Verdict(r.Evaluate(input))
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
		got := hookio.Verdict(r.Evaluate(input))
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
		got := hookio.Verdict(r.Evaluate(input))
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
		got := hookio.Verdict(r.Evaluate(input))
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
		{"unknown source", "cp /etc/shadow /tmp/dest/", hookio.NoOpinion},
		{"-t with multiple sources", "cp -t /tmp/dest/ ./a.txt ./b.txt", hookio.Approve},
		{"--target-directory= style", "cp --target-directory=/tmp/dest/ ./a.txt", hookio.Approve},
		{"-t to non-writable dest", "cp -t /etc/ ./a.txt", hookio.NoOpinion},
		{"no path-like args", "cp fileA fileB", hookio.Approve},
		{"single path arg", "cp ./only-one", hookio.Approve},
		{"-r flag with directory", "cp -r ./src/ /tmp/dest/", hookio.Approve},
		{"dest is read-only", "cp ./a.txt /nix/store/out", hookio.NoOpinion},
		{"-a flag recursive", "cp -a /home/user/project/src/ /tmp/backup/", hookio.Approve},
		{"multiple flags then paths", "cp -rv /home/user/project/a.txt /home/user/project/b.txt /tmp/out/", hookio.Approve},
		{"unknown source with -t", "cp -t /tmp/dest/ /etc/shadow", hookio.NoOpinion},
		{"all sources in project", "cp /home/user/project/a /home/user/project/b /home/user/project/dest/", hookio.Approve},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
			}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (reason: %s)", got.Decision, tt.want, got.Reason)
			}
		})
	}
}

// TestEvaluateCp_TargetDirectoryGluedQuoteParity is pg2-6f2gu's relation-fixture
// requirement for the cp side: `--target-directory='X'` (glued AND quoted) must
// reach the SAME verdict as `--target-directory=X` (glued, unquoted) and
// `-t X` (space-separated) — all three are identical to the shell.
//
// /etc/ is the existing out-of-zone fixture this file already uses ("-t to
// non-writable dest" above; also internal/patheval's own zone model treats it
// as outside every writable zone for this evaluator). /home/user/project/dest/
// is the in-zone control, so the relation is checked in BOTH directions: a
// quoted glued destination must not gain a bypass it shouldn't have, and must
// not lose an approval it should keep.
func TestEvaluateCp_TargetDirectoryGluedQuoteParity(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)

	cases := []struct {
		dest string
		want hookio.Decision
	}{
		{"/etc/", hookio.NoOpinion},
		{"/home/user/project/dest/", hookio.Approve},
	}
	for _, c := range cases {
		spaced := "cp ./a.txt -t " + c.dest
		gluedPlain := "cp ./a.txt --target-directory=" + c.dest
		gluedQuoted := "cp ./a.txt --target-directory='" + c.dest + "'"

		verdictFor := func(cmd string) hookio.RuleResult {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": cmd}),
			}
			return hookio.Verdict(r.Evaluate(input))
		}
		sv, gv, qv := verdictFor(spaced), verdictFor(gluedPlain), verdictFor(gluedQuoted)

		if gv.Decision != sv.Decision {
			t.Errorf("GLUED-SPELLING DISAGREEMENT: %q is %s but %q is %s (pg2-wxbr9)",
				gluedPlain, gv.Decision, spaced, sv.Decision)
		}
		if qv.Decision != sv.Decision {
			t.Errorf("GLUED-QUOTE DISAGREEMENT: %q is %s (%s) but %q is %s (%s) — both are identical to the shell and MUST reach the same verdict (pg2-6f2gu)",
				gluedQuoted, qv.Decision, qv.Reason, spaced, sv.Decision, sv.Reason)
		}
		for _, got := range []hookio.RuleResult{sv, gv, qv} {
			if got.Decision != c.want {
				t.Errorf("dest %q: got %s (%s), want %s", c.dest, got.Decision, got.Reason, c.want)
			}
		}
	}
}

// TestEvaluateCp_TargetDirectoryMalformedGluedQuotingAbstains pins pg2-mp9oq's
// fail-closed fix on top of pg2-6f2gu: a `--target-directory=` value whose
// quoting is MALFORMED (UnwrapGluedQuotes declines it — returns it unchanged)
// must no longer fall through to the unconditional Approve at the end of the
// -t/--target-directory branch.
//
// BEFORE this fix (still on this tree immediately after pg2-6f2gu), the
// multi-segment, adjacent-glued-quote, and double-wrapped forms below all
// APPROVED regardless of destination: a declined value stays quote-wrapped,
// so it never starts with an unquoted `/`/`./`/`../`/`~`, looksLikePath is
// false, and the destination zone/writability check was skipped entirely —
// the same "unclassifiable value defaults to the wrong thing" shape as
// pg2-9zgso/pg2-6f2gu, just for the malformed subset UnwrapGluedQuotes itself
// declines to touch. MEASURED on this tree before this fix: with cwd
// /home/user/project, the multi-segment form
// (`cp ./a.txt --target-directory='/etc/'x'/etc/'`), the double-single-quote
// wrapped form (the "double-wrapped" case below), and the adjacent-glued-quote
// form (`cp ./a.txt --target-directory='/etc'"'"'/passwd'`) all APPROVED.
//
// NOTE ON THIS COMMENT: gofumpt's doc-comment reformatter rewrites a literal
// pair of adjacent single-quote characters in DOC COMMENT prose into a curly
// Unicode quote (even inside a backtick code span), silently corrupting the
// exact byte sequence a "double-wrapped" example needs to show — which is why
// that one case is described in prose above instead of spelled out with
// backticks. The test table below is a Go STRING LITERAL, not a comment, so
// gofumpt leaves it untouched and it is authoritative.
//
// AFTER this fix they Abstain (NoOpinion) — the same "can't tell, won't clear
// it" verdict this file already uses for "destination is not writable" one
// line below and throughout readPathIssue.
//
// The genuinely UNTERMINATED case is different in kind and is UNCHANGED by
// this fix: a single quote with no closing pair ANYWHERE in the command is
// not a value-shaping quirk, it is invalid shell syntax (real bash would keep
// reading for the missing close), so cmdparse cannot even tokenize a `cp`
// invocation out of it — the whole command abstains via NotApplicable before
// evaluateCp (and this fix's check) ever runs.
func TestEvaluateCp_TargetDirectoryMalformedGluedQuotingAbstains(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)

	tests := []struct {
		name string
		cmd  string
		want hookio.Decision
	}{
		{
			"interior contains the wrapper character (multi-segment concatenation)",
			"cp ./a.txt --target-directory='/etc/'x'/etc/'",
			hookio.NoOpinion,
		},
		{
			"interior contains the wrapper character (adjacent glued quotes, no literal middle)",
			`cp ./a.txt --target-directory='/etc'"'"'/passwd'`,
			hookio.NoOpinion,
		},
		{
			"unterminated single quote makes the whole command unparseable",
			"cp ./a.txt --target-directory='/etc/",
			hookio.NoOpinion,
		},
		{
			"double-wrapped: outer pair around an already-quoted inner value",
			"cp ./a.txt --target-directory=''/etc/''",
			hookio.NoOpinion,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": tt.cmd}),
			}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != tt.want {
				t.Errorf("cmd %q: got %s (%s), want %s", tt.cmd, got.Decision, got.Reason, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// pg2-1xq3m: LONG-FLAG ABBREVIATION AWARENESS, WIDENED BEYOND `git` (pg2-os1kq).
// ---------------------------------------------------------------------------
//
// pg2-os1kq's bug class: an exact-token long-flag test is bypassable whenever
// the underlying program accepts unambiguous PREFIX abbreviations, and the
// bypass direction is toward Approve. This bead MEASURED which of that class's
// two suspected sites in THIS package are actually reachable on a real binary
// — see the doc comments on isSedInPlace, isYqInPlace,
// minAbbrevTargetDirectory, and (in the curl package) effectiveMethod for the
// verbatim measurements. The upshot: cp's `--target-directory` (fixed below,
// GNU coreutils 9.11) and sed's `--in-place` (fixed, GNU sed 4.9) are real;
// yq's `--inplace` and curl's `--request` are NOT (both parsers reject every
// abbreviation outright).
//
// DECISION: NOT GENERALIZING git's THREE-LAYER AST-GUARD HERE (pg2-1xq3m). git's
// TestGit_LongFlagTests_AreAbbreviationAware walks git.go's OWN AST for every
// exact-token long-flag test, because git.go gates DOZENS of long flags across
// an actively-edited ~3000-line file — the guard's value is catching a FUTURE
// exact-token regression among many. safecmds.go and curl.go, by contrast, gate
// exactly ONE long-flag test whose value is load-bearing
// (`--target-directory`) and two boolean ones (`--in-place`, `--inplace`) —
// there is no comparable population of existing gates for a mechanical AST
// walk to protect, and building the three-layer machinery (an exemptions list,
// a "which matcher for which flag" pinning test, and a package-scoped AST
// parser) for a single call site is disproportionate to what it would catch
// beyond what the behavioral tests below already pin. If a THIRD long-flag
// gate is added to safecmds.go or curl.go — or if either file's flag-testing
// surface grows toward git.go's scale — this decision should be revisited: at
// that point the AST guard's cost (one parser, one exemptions list) is repaid
// by the same "can't silently regress" property it gives git.go today.
// ---------------------------------------------------------------------------

// TestEvaluateCp_TargetDirectoryAbbrev_NeverApproved pins pg2-1xq3m's fix: every
// abbreviated spelling of `--target-directory` GNU coreutils accepts (measured
// on cp (GNU coreutils) 9.11 — see minAbbrevTargetDirectory) must be recognised
// and its VALUE checked for writability, exactly like the full spelling.
//
// /etc/ is not writable under this evaluator's zone model. BEFORE this fix, the
// abbreviated rows below fell through to "standard mode" (the last path-like
// arg is destination): `/etc/` was scanned as an ordinary positional, still
// caught the write check by accident in this single-destination shape, so the
// live bug this test actually forecloses is the standard-mode MISATTRIBUTION
// case pinned separately below
// (TestEvaluateCp_TargetDirectoryAbbrev_MisattributionWithoutFix) — this test's
// job is to pin that the intended CODE PATH (the -t/--target-directory arm,
// not the standard-mode fallback) is what fires for every spelling, which the
// reason string demonstrates.
func TestEvaluateCp_TargetDirectoryAbbrev_NeverApproved(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)

	spellings := []string{"--t", "--ta", "--tar", "--target-d", "--target-director", "--target-directory"}
	for _, flag := range spellings {
		for _, form := range []string{
			flag + " /etc/",   // separate-token
			flag + "=/etc/",   // glued
			flag + "='/etc/'", // glued and quoted
		} {
			cmd := "cp ./a.txt " + form
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": cmd}),
			}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != hookio.NoOpinion {
				t.Errorf("cmd %q: got %s (%s), want abstain — /etc/ is not writable, and this spelling is a real GNU coreutils abbreviation of --target-directory", cmd, got.Decision, got.Reason)
			}
			if !strings.Contains(got.Reason, "target directory") {
				t.Errorf("cmd %q: reason %q does not mention the target directory — the abbreviation may not have been recognised as --target-directory at all (fell through to a different code path)", cmd, got.Reason)
			}
		}
	}
}

// TestEvaluateCp_TargetDirectoryAbbrev_MisattributionWithoutFix pins the
// SPECIFIC live bypass pg2-1xq3m measured: with the pre-fix exact-token test,
// an abbreviated `--target-directory` whose value is NOT the last path-like
// token gets treated as an ordinary positional by "standard mode"'s
// last-path-arg-is-destination heuristic — attributing the WRITE check to the
// wrong argument and approving a write to an unwritable zone.
//
// `cp --t /etc/cron.d /home/user/project/payload` really means (GNU coreutils,
// abbreviation accepted): copy `payload` INTO `/etc/cron.d`. Pre-fix, the
// exact-token test never recognised `--t`, so evaluateCp's fallback treated
// `/home/user/project/payload` (the LAST path-like arg, and NOT a real
// destination at all) as the destination — writable, so it approved — while
// `/etc/cron.d` (the REAL destination, and unwritable under this zone model)
// was checked only for READABILITY, which /etc/ typically passes. That is an
// APPROVE for a write GNU coreutils actually sends to an unwritable path — the
// same "exact-token miss defaults toward Approve" shape as pg2-os1kq's `git
// reset --har`.
func TestEvaluateCp_TargetDirectoryAbbrev_MisattributionWithoutFix(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)

	cmd := "cp --t /etc/cron.d /home/user/project/payload"
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:       "/home/user/project",
		ToolInput: mustJSON(map[string]string{"command": cmd}),
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision == hookio.Approve {
		t.Errorf("cmd %q: got APPROVE (%s) — GNU coreutils reads --t as --target-directory and would write payload into /etc/cron.d, which this zone model must not approve", cmd, got.Reason)
	}
	if got.Decision != hookio.NoOpinion {
		t.Errorf("cmd %q: got %s (%s), want abstain (destination not writable)", cmd, got.Decision, got.Reason)
	}
}

// TestSafecmds_SedInPlaceAbbrev_ReadOnlyPath_Abstain is the sed twin of the cp
// abbreviation fix (pg2-1xq3m). BEFORE the fix, an abbreviated --in-place spelling
// was invisible to isSedInPlace, so the command fell through to the READ-ONLY
// branch (readPathIssue, which checks READABILITY, not writability) — and
// /nix/store/abc123/file.txt IS readable under this evaluator's zone model, so
// this would have APPROVED a command GNU sed actually executes as an in-place
// EDIT of a file this zone model correctly refuses to let anything WRITE to
// (see TestSafecmds_SedInPlace_ReadOnlyPath_Abstain for the exact-spelling
// twin this mirrors).
func TestSafecmds_SedInPlaceAbbrev_ReadOnlyPath_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)

	for _, flag := range []string{"--i", "--in", "--in-plac", "--in-place"} {
		cmd := "sed " + flag + " 's/foo/bar/' /nix/store/abc123/file.txt"
		input := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:       "/home/user/project",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got APPROVE (%s) — GNU sed 4.9 performs the in-place edit for this spelling (measured down to --i), so it must not be treated as a read", cmd, got.Reason)
		}
		if got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want abstain (write path not writable)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestSafecmds_SedInPlaceAbbrev_WritePath_Approve is the positive control: an
// abbreviated --in-place spelling targeting a genuinely writable path must
// still Approve, exactly like the full spelling does
// (TestSafecmds_SedInPlace_WritePath_Approve) — the fix must not turn every
// abbreviation into a blanket refusal.
func TestSafecmds_SedInPlaceAbbrev_WritePath_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)

	for _, flag := range []string{"--i", "--in", "--in-plac", "--in-place"} {
		cmd := "sed " + flag + " 's/foo/bar/' /home/user/project/file.txt"
		input := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:       "/home/user/project",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve (writable path)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestSafecmds_YqInplaceAbbrev_NotRecognised pins the NOT-AFFECTED measurement
// for yq (see isYqInPlace's doc): mikefarah/yq's pflag-based parser rejects every
// abbreviation of --inplace outright, so an abbreviated spelling here is not a
// live bypass — it is simply an argument yq itself would refuse, and this rule
// correctly does not treat it as in-place (it falls to the ordinary read path,
// which is where it belongs since yq would error out and touch nothing).
func TestSafecmds_YqInplaceAbbrev_NotRecognised(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)

	// The distinguishing case is a READ-ONLY path. If an abbreviation were
	// wrongly recognised as --inplace, this would Abstain (write check) instead
	// of Approve (read check) — but yq itself rejects every abbreviation of
	// --inplace, so there is nothing to recognise, and this remains a plain
	// read regardless of spelling.
	for _, flag := range []string{"--i", "--in", "--inplac"} {
		cmd := "yq " + flag + " '.a = 2' /nix/store/abc123/file.yaml"
		input := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:       "/home/user/project",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve — a read-only path is fine for a plain READ; if this abstained, the abbreviation was wrongly recognised as --inplace", cmd, got.Decision, got.Reason)
		}
	}
}

func TestSafecmds_Sqlite3Removed(t *testing.T) {
	pe := patheval.New("/tmp/project")
	r := New(pe)
	input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": "sqlite3 /tmp/project/test.db 'SELECT 1'"})}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
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
		{"xargs unknown cmd", "xargs curl http://example.com", hookio.NoOpinion},
		{"xargs no command", "xargs -I {}", hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
			}
			got := hookio.Verdict(r.Evaluate(input))
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
		{"jar tf unknown path", "jar tf /etc/secret.jar", hookio.NoOpinion},
		{"jar create", "jar cf /home/user/project/out.jar", hookio.NoOpinion},
		{"jar no args", "jar", hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
			}
			got := hookio.Verdict(r.Evaluate(input))
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
		{"yq read unknown path", "yq '.key' /etc/secret.yaml", hookio.NoOpinion},
		{"yq -i write project file", "yq -i '.key = \"value\"' /home/user/project/file.yaml", hookio.Approve},
		{"yq -i write read-only path", "yq -i '.key = \"value\"' /nix/store/abc123/file.yaml", hookio.NoOpinion},
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
			got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
	got := hookio.Verdict(r.Evaluate(input))
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
		{"unzip unknown archive", "unzip /etc/secret.zip", hookio.NoOpinion},
		{"unzip -d unknown dest", "unzip -d /etc/somewhere /home/user/project/archive.zip", hookio.NoOpinion},
		{"unzip readable archive to nix store", "unzip -d /nix/store/abc123 /home/user/project/archive.zip", hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
			}
			got := hookio.Verdict(r.Evaluate(input))
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
		{"gofmt -d out-of-zone path", "gofmt -d /etc/shadow", hookio.NoOpinion},
		// Any -w write invocation is NOT approved — deferred to normal flow.
		{"gofmt -w write relative", "gofmt -w main.go", hookio.NoOpinion},
		{"gofmt -w write project path", "gofmt -w /home/user/project/main.go", hookio.NoOpinion},
		{"gofmt -s -w simplify+write", "gofmt -s -w .", hookio.NoOpinion},
		{"gofmt -l -w list+write", "gofmt -l -w .", hookio.NoOpinion},
		{"gofmt --w double-dash write", "gofmt --w main.go", hookio.NoOpinion},
		{"gofmt -w=true explicit write", "gofmt -w=true main.go", hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
			}
			got := hookio.Verdict(r.Evaluate(input))
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
		{"bash -n unknown path", "bash -n /etc/secret.sh", hookio.NoOpinion},
		{"sh -n readable file", "sh -n /home/user/project/script.sh", hookio.Approve},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
			}
			got := hookio.Verdict(r.Evaluate(input))
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
		{"strings aws credentials abstains", "strings ~/.aws/credentials", hookio.NoOpinion},
		// Unknown system path -> Abstain (zone guard, matches cat /etc/passwd).
		{"strings /etc/passwd abstains", "strings /etc/passwd", hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
			}
			got := hookio.Verdict(r.Evaluate(input))
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
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.NoOpinion {
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
		got := hookio.Verdict(r.Evaluate(input))
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
			got := hookio.Verdict(r.Evaluate(input))
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
		{"awk bare var program", "awk $F", hookio.NoOpinion},
		{"sed bare var script", "sed $S /home/user/project/x", hookio.NoOpinion},
		{"jq bare var filter", "jq $Q /home/user/project/x.json", hookio.NoOpinion},
		{"awk subst program", "awk $(cat /home/user/project/prog.awk)", hookio.NoOpinion},
		// A path BUILT from an expansion is refused even in program position.
		{"awk var-rooted path program", "awk $D/prog.awk", hookio.NoOpinion},
		// Program supplied by -f: every positional is a path, so the coarse
		// predicate applies to all of them.
		{"awk -f then dynamic file", "awk -f /home/user/project/p.awk $F", hookio.NoOpinion},
		{"sed -e then dynamic file", "sed -e 's/a/b/' $F", hookio.NoOpinion},
		// A dynamic FILE argument is refused even when a static program precedes it.
		{"awk program then dynamic file", "awk '{print $1}' $F", hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", CWD: "/home/user/project", ToolInput: mustJSON(map[string]string{"command": tt.command})}
			got := hookio.Verdict(r.Evaluate(input))
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
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve (browsing commands are exempt)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestReadPathIssue_IsNeverLooserThanTheStaticSubstitutionSeam is the pg2-zpct4
// reconciliation asserted as a RELATION, from the side that OWNS path readability.
//
// THE RELATION: whenever this rule's readPathIssue refuses a path for reading, cmdparse's
// static safe-substitution seam must NOT clear the same read. Clearance there means
// ExpansionSafeCmd, which skips the substitution recursion entirely — so a cleared body's
// paths never reach this function at all, and the seam's own screen stands in place of the
// whole zone model. That is exactly how `X=$(cat /etc/shadow) echo hi` reached `allow`
// while `cat /etc/shadow` abstained: two path models, and the capture reached the weaker.
//
// Stated as a relation rather than as rows, it survives retuning EITHER model: add a zone
// to `patheval`, add an entry to `secretpath`, or move a command between cmdparse's two
// substitution lists, and the relation still says the one thing that must stay true. A
// verdict table would have to be rewritten by whoever retuned it, which is how the two
// models drifted apart in the first place.
//
// It deliberately does NOT assert the converse. The seam is allowed to be STRICTER than
// this rule — it refuses a `jq` FILTER shaped like a secret path, and it refuses whole
// shapes (pipelines, heredocs, `git show`) this rule would clear — and an equality
// assertion would forbid that safe direction of drift.
func TestReadPathIssue_IsNeverLooserThanTheStaticSubstitutionSeam(t *testing.T) {
	pe := patheval.New("/home/user/project")
	// Content readers only: these are the commands that can emit another file's bytes,
	// and therefore the ones for which "may this path be read" decides anything. The
	// list is the intersection this bead reconciles — every entry is on cmdparse's
	// fileReaderSubstitutions AND on this rule's read path.
	readers := []string{"cat", "head -1", "tail -1", "wc -l", "grep -c x", "jq -r .x", "yq .a"}
	paths := []string{
		// Out of every readable zone: the class the two models disagreed about.
		"/etc/shadow", "/etc/passwd", "/", "/var/log/system.log",
		"/home/other/.aws/credentials", "~someuser/notes.txt",
		// Deny-listed, where they already agreed.
		".env", "/home/user/.ssh/id_rsa", "~/.ssh/config", "secrets/db.yaml",
		// In zone: the relation must hold here too, and it does so by the seam
		// DELEGATING rather than by it guessing "readable".
		"/home/user/project/go.mod", "./go.mod", "../go.mod", "go.mod",
	}
	for _, r := range readers {
		for _, p := range paths {
			args := append(strings.Fields(r)[1:], p)
			if readPathIssue(args, pe, "") == "" {
				continue // this rule clears the read; the seam may do as it likes
			}
			for _, body := range []string{r + " " + p, r + " < " + p} {
				if cmdparse.IsSafeSubstitutionBody(body) {
					t.Errorf("PATH MODELS DISAGREE: readPathIssue refuses %q for %q, but cmdparse.IsSafeSubstitutionBody(%q) = true — the captured spelling would skip this rule entirely",
						p, r, body)
				}
			}
		}
	}
}

// TestSafecmds_GluedQuoteParity_PathCandidate is pg2-52eod's relation-fixture
// requirement, generalizing pg2-6f2gu's TestEvaluateCp_TargetDirectoryGluedQuoteParity
// past cp's bespoke `--target-directory=` extraction to pathCandidate itself — the
// seam every OTHER read/write/reject check in this file shares (readPathIssue,
// hasRejectPath, hasUnsafeWritePath, evaluateCp's other positional loops,
// argsHaveDynamicExpansion). A value glued to an unquoted flag name AND wrapped in a
// shell quote (`--flag='X'`) must reach the SAME verdict as the unquoted glued
// spelling (`--flag=X`) and the space-separated spelling (`--flag X`) — all three are
// identical to the shell.
//
// CONFIRMED LIVE, pre-fix (this bead's brief, reproduced against this tree before the
// change): cat/mkdir/ls each auto-approved the quoted glued spelling of a path
// cmdparse.GluedFlagValue never unwrapped, while their unquoted twins correctly
// abstained/refused. grep is the fourth family here, and it is DELIBERATELY not
// pathCandidate's own caller — it goes through cmdparse.SkipGrepPattern, this bead's
// audited THIRD (in fact fourth, see internal/rules/gitdir) caller of
// cmdparse.GluedFlagValue, so this fixture also proves that seam was fixed.
func TestSafecmds_GluedQuoteParity_PathCandidate(t *testing.T) {
	pe := patheval.New("/home/user/project")
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{
		// A DenyRead zone NOT covered by internal/secretpath's own lexical list, so
		// this exercises hasRejectPath's patheval.PathReject branch specifically
		// rather than re-proving secrets.Rule's independent (and already-fixed)
		// ".ssh"/".env" coverage.
		DenyRead: []string{"/etc/company-secrets"},
	})
	r := New(pe)

	verdictFor := func(cmd string) hookio.RuleResult {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:       "/home/user/project",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		return hookio.Verdict(r.Evaluate(input))
	}

	// Each template has exactly one "%s" — the flag's value slot — filled with the
	// space-separated arg, the glued-unquoted value, and the glued-quoted value in
	// turn, so a case defines its command shape ONCE rather than three times.
	cases := []struct {
		name     string
		template string // e.g. "cat --output %s" — %s is replaced by the value spelling
		space    string // the SPACE form's value token (flag and value as two args)
		glued    string // the flag=value PREFIX for the glued forms, e.g. "--output="
		path     string
		want     hookio.Decision
	}{
		// The three CONFIRMED examples from the bead brief, each a different
		// pathCandidate consumer: readPathIssue (cat, safeReadCmds), hasUnsafeWritePath
		// (mkdir, safeWriteCmds), hasRejectPath (ls, browsingCmds).
		{"cat --output (readPathIssue: unknown path)", "cat %s", "--output /etc/shadow", "--output=", "/etc/shadow", hookio.NoOpinion},
		{"mkdir --mode (hasUnsafeWritePath)", "mkdir %s", "--mode /etc/shadow", "--mode=", "/etc/shadow", hookio.NoOpinion},
		// NOTE: patheval.PathReject is currently unreachable via a plain (non-container)
		// Evaluate() call in this codebase — grep confirms it — so hasRejectPath never
		// actually fires here regardless of quoting, and Approve is the CORRECT,
		// unchanged verdict for all three spellings. It is kept in this table anyway
		// because it is a pathCandidate consumer this bead's brief names explicitly,
		// and the relation (all three spellings agree) is exactly what the fix
		// guarantees even though there is no live discrepancy to observe today.
		{"ls --sort (hasRejectPath consumer; PathReject unreachable here, so all agree on approve)", "ls %s", "--sort /etc/company-secrets/config", "--sort=", "/etc/company-secrets/config", hookio.Approve},
		// A FOURTH command family, deliberately NOT a pathCandidate caller directly:
		// grep's file-flag operand is emitted by cmdparse.SkipGrepPattern, this bead's
		// audited third/fourth caller of cmdparse.GluedFlagValue.
		{"grep --file (SkipGrepPattern file-flag operand)", "grep %s x.log", "--file /etc/shadow", "--file=", "/etc/shadow", hookio.NoOpinion},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spaced := strings.Replace(c.template, "%s", c.space, 1)
			gluedPlain := strings.Replace(c.template, "%s", c.glued+c.path, 1)
			gluedQuoted := strings.Replace(c.template, "%s", c.glued+"'"+c.path+"'", 1)

			sv, gv, qv := verdictFor(spaced), verdictFor(gluedPlain), verdictFor(gluedQuoted)

			if gv.Decision != sv.Decision {
				t.Errorf("GLUED-SPELLING DISAGREEMENT: %q is %s but %q is %s",
					gluedPlain, gv.Decision, spaced, sv.Decision)
			}
			if qv.Decision != sv.Decision {
				t.Errorf("GLUED-QUOTE DISAGREEMENT: %q is %s (%s) but %q is %s (%s) — both are identical to the shell and MUST reach the same verdict (pg2-52eod)",
					gluedQuoted, qv.Decision, qv.Reason, spaced, sv.Decision, sv.Reason)
			}
			for _, got := range []hookio.RuleResult{sv, gv, qv} {
				if got.Decision != c.want {
					t.Errorf("got %s (%s), want %s", got.Decision, got.Reason, c.want)
				}
			}
		})
	}
}

// TestSafecmds_MalformedGluedQuotingAbstains pins pg2-52eod's fail-closed
// requirement, generalizing pg2-mp9oq's evaluateCp-specific
// TestEvaluateCp_TargetDirectoryMalformedGluedQuotingAbstains to every OTHER
// pathCandidate consumer in this file. A glued value whose quoting is MALFORMED
// (cmdparse.UnwrapGluedQuotes declines — returns it unchanged) must not fall through
// to the unconditional Approve at the end of each branch: BEFORE this fix a declined
// value stayed quote-wrapped, so it never started with an unquoted
// `/`/`./`/`../`/`~`, looksLikePath was false, and the corresponding zone/write/reject
// check was skipped entirely.
func TestSafecmds_MalformedGluedQuotingAbstains(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)

	tests := []struct {
		name string
		cmd  string
	}{
		{"cat: double-wrapped", "cat --output=''/etc/shadow''"},
		{"cat: interior wrapper character", "cat --output='/etc/shadow'x'/etc/shadow'"},
		{"mkdir: double-wrapped", "mkdir --mode=''/etc/shadow''"},
		{"mkdir: interior wrapper character", "mkdir --mode='/etc/shadow'x'/etc/shadow'"},
		{"ls: double-wrapped", "ls --sort=''/etc/shadow''"},
		{"grep --file: double-wrapped", "grep --file=''/etc/shadow'' x.log"},
		{"grep --file: interior wrapper character", "grep --file='/etc/shadow'x'/etc/shadow' x.log"},
		// cp's OTHER positional loop (not --target-directory=, which pg2-mp9oq
		// already covers) — the standard-mode source/destination scan, exercised via
		// a GLUED flag this bead's audit found nowhere else in this rule (mkdir/cat
		// above already cover a bare command's glued flag; cp's positional loop is
		// the site, not a new shape of malformed value).
		{"cp standard mode: glued malformed destination", "cp ./a.txt --unknown-flag=''/etc/shadow''"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": tt.cmd}),
			}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != hookio.NoOpinion {
				t.Errorf("cmd %q: got %s (%s), want abstain — malformed glued quoting must fail closed, never approve", tt.cmd, got.Decision, got.Reason)
			}
		})
	}
}

// TestSafecmds_ArgsHaveDynamicExpansion_IndependentOfMalformedQuoting pins
// argsHaveDynamicExpansion's independence (this bead's acceptance criterion 5): it
// must keep Abstaining on a `$`-expansion regardless of this change, AND it must
// ALSO abstain on malformed glued quoting via the SAME pathCandidate seam — the two
// are separate signals folded by the SAME predicate (see argsHaveDynamicExpansion's
// doc), so this test pins both without confusing one for the other.
func TestSafecmds_ArgsHaveDynamicExpansion_IndependentOfMalformedQuoting(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)

	tests := []struct {
		name string
		cmd  string
	}{
		{"dynamic expansion, unrelated to quoting", "rm -rf $HOME/.ssh"},
		{"dynamic expansion via glued flag", "touch --reference=$HOME/.bashrc"},
		{"malformed glued quoting, no expansion at all", "mkdir --mode=''/etc/shadow''"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/project",
				ToolInput: mustJSON(map[string]string{"command": tt.cmd}),
			}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != hookio.NoOpinion {
				t.Errorf("cmd %q: got %s (%s), want abstain", tt.cmd, got.Decision, got.Reason)
			}
		})
	}
}
