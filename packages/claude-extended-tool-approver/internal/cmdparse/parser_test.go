package cmdparse

import (
	"reflect"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func TestParse_SimpleCommand(t *testing.T) {
	got := Parse("git status")
	if len(got) != 1 {
		t.Fatalf("len(Parse) = %d, want 1", len(got))
	}
	if got[0].Executable != "git" {
		t.Errorf("Executable = %q, want git", got[0].Executable)
	}
	if !reflect.DeepEqual(got[0].Args, []string{"status"}) {
		t.Errorf("Args = %v, want [status]", got[0].Args)
	}
}

func TestParse_QuotedArgs(t *testing.T) {
	got := Parse(`git commit -m "hello world"`)
	if len(got) != 1 {
		t.Fatalf("len(Parse) = %d, want 1", len(got))
	}
	if got[0].Executable != "git" {
		t.Errorf("Executable = %q, want git", got[0].Executable)
	}
	want := []string{"commit", "-m", "hello world"}
	if !reflect.DeepEqual(got[0].Args, want) {
		t.Errorf("Args = %v, want %v", got[0].Args, want)
	}
}

func TestParse_SingleQuotedArgs(t *testing.T) {
	got := Parse(`echo 'hello world'`)
	if len(got) != 1 {
		t.Fatalf("len(Parse) = %d, want 1", len(got))
	}
	if got[0].Args[0] != "hello world" {
		t.Errorf("Args[0] = %q, want hello world", got[0].Args[0])
	}
}

func TestParse_AndChain(t *testing.T) {
	got := Parse("cd /tmp && ls -la")
	if len(got) != 2 {
		t.Fatalf("len(Parse) = %d, want 2", len(got))
	}
	if got[0].Executable != "cd" {
		t.Errorf("got[0].Executable = %q, want cd", got[0].Executable)
	}
	if got[1].Executable != "ls" {
		t.Errorf("got[1].Executable = %q, want ls", got[1].Executable)
	}
}

func TestParse_OrChain(t *testing.T) {
	got := Parse("false || echo ok")
	if len(got) != 2 {
		t.Fatalf("len(Parse) = %d, want 2", len(got))
	}
	if got[0].Executable != "false" {
		t.Errorf("got[0].Executable = %q, want false", got[0].Executable)
	}
	if got[1].Executable != "echo" {
		t.Errorf("got[1].Executable = %q, want echo", got[1].Executable)
	}
}

func TestParse_Semicolons(t *testing.T) {
	got := Parse("echo a; echo b")
	if len(got) != 2 {
		t.Fatalf("len(Parse) = %d, want 2", len(got))
	}
	if got[0].Args[0] != "a" {
		t.Errorf("got[0].Args[0] = %q, want a", got[0].Args[0])
	}
	if got[1].Args[0] != "b" {
		t.Errorf("got[1].Args[0] = %q, want b", got[1].Args[0])
	}
}

func TestParse_Pipes(t *testing.T) {
	got := Parse("cat file | grep foo")
	if len(got) != 2 {
		t.Fatalf("len(Parse) = %d, want 2", len(got))
	}
	if got[0].Executable != "cat" {
		t.Errorf("got[0].Executable = %q, want cat", got[0].Executable)
	}
	if got[1].Executable != "grep" {
		t.Errorf("got[1].Executable = %q, want grep", got[1].Executable)
	}
}

func TestParse_EmptyInput(t *testing.T) {
	got := Parse("")
	if len(got) != 0 {
		t.Errorf("len(Parse(\"\")) = %d, want 0", len(got))
	}
}

func TestParse_EnvOnlySkipsSegment(t *testing.T) {
	got := Parse("FOO=1 BAR=2")
	if len(got) != 0 {
		t.Errorf("len(Parse(\"FOO=1 BAR=2\")) = %d, want 0 (env vars only, no executable)", len(got))
	}
}

func TestParse_EnvVarsExtracted(t *testing.T) {
	tests := []struct {
		input    string
		wantExec string
		wantEnvs []string // "NAME=VALUE" pairs
	}{
		{"FOO=bar git status", "git", []string{"FOO=bar"}},
		{"A=1 B=2 ls", "ls", []string{"A=1", "B=2"}},
		{"FOO= git status", "git", []string{"FOO="}},
		{"PYTHONPATH=/a/b cmd", "cmd", []string{"PYTHONPATH=/a/b"}},
		{"git status", "git", nil},
		{"FOO=1 BAR=2", "", []string{"FOO=1", "BAR=2"}},
	}
	for _, tt := range tests {
		got := Parse(tt.input)
		if tt.wantExec == "" {
			if len(got) != 0 {
				t.Errorf("Parse(%q): got %d commands, want 0", tt.input, len(got))
			}
			continue
		}
		if len(got) != 1 {
			t.Fatalf("Parse(%q): got %d commands, want 1", tt.input, len(got))
		}
		if got[0].Executable != tt.wantExec {
			t.Errorf("Parse(%q).Executable = %q, want %q", tt.input, got[0].Executable, tt.wantExec)
		}
		gotEnvs := make([]string, len(got[0].EnvVars))
		for i, ev := range got[0].EnvVars {
			gotEnvs[i] = ev.Name + "=" + ev.Value
		}
		if tt.wantEnvs == nil && len(gotEnvs) != 0 {
			t.Errorf("Parse(%q).EnvVars = %v, want nil", tt.input, gotEnvs)
		} else if tt.wantEnvs != nil && !reflect.DeepEqual(gotEnvs, tt.wantEnvs) {
			t.Errorf("Parse(%q).EnvVars = %v, want %v", tt.input, gotEnvs, tt.wantEnvs)
		}
	}
}

func TestParse_EnvPrefix(t *testing.T) {
	got := Parse("FOO=bar git status")
	if len(got) != 1 {
		t.Fatalf("len(Parse) = %d, want 1", len(got))
	}
	if got[0].Executable != "git" {
		t.Errorf("Executable = %q, want git (env prefix stripped)", got[0].Executable)
	}
}

func TestParse_RespectsQuotingInSplitters(t *testing.T) {
	got := Parse(`echo "a && b"`)
	if len(got) != 1 {
		t.Fatalf("len(Parse) = %d, want 1 (&& inside quotes)", len(got))
	}
	if got[0].Args[0] != "a && b" {
		t.Errorf("Args[0] = %q, want a && b", got[0].Args[0])
	}
}

func TestNormalizeExecutable_BareCommand(t *testing.T) {
	got := NormalizeExecutable("git", "/project", "/project")
	if got != "git" {
		t.Errorf("NormalizeExecutable(git) = %q, want git", got)
	}
}

func TestNormalizeExecutable_RelativeFromCwd(t *testing.T) {
	got := NormalizeExecutable("./bin/grazr", "/project", "/project")
	if got != "bin/grazr" {
		t.Errorf("NormalizeExecutable(./bin/grazr) = %q, want bin/grazr", got)
	}
}

func TestNormalizeExecutable_AbsoluteInProject(t *testing.T) {
	got := NormalizeExecutable("/project/bin/grazr", "/project", "/project")
	if got != "bin/grazr" {
		t.Errorf("NormalizeExecutable(/project/bin/grazr) = %q, want bin/grazr", got)
	}
}

func TestNormalizeExecutable_OutsideProject(t *testing.T) {
	got := NormalizeExecutable("/usr/bin/git", "/project", "/project")
	if got != "/usr/bin/git" {
		t.Errorf("NormalizeExecutable(/usr/bin/git) = %q, want /usr/bin/git", got)
	}
}

func TestNormalizeExecutable_RelativeNoDot(t *testing.T) {
	got := NormalizeExecutable("bin/grazr", "/project", "/project")
	if got != "bin/grazr" {
		t.Errorf("NormalizeExecutable(bin/grazr) = %q, want bin/grazr", got)
	}
}

func TestNormalizeExecutable_RelativeFromSubdir(t *testing.T) {
	got := NormalizeExecutable("./scripts/test.sh", "/project", "/project/src")
	if got != "src/scripts/test.sh" {
		t.Errorf("NormalizeExecutable(./scripts/test.sh from subdir) = %q, want src/scripts/test.sh", got)
	}
}

func TestParse_SubshellInEnvVar(t *testing.T) {
	got := Parse("FOO=$(echo hello) git status")
	if len(got) != 1 {
		t.Fatalf("len(Parse) = %d, want 1 (subshell in env var)", len(got))
	}
	if got[0].Executable != "git" {
		t.Errorf("Executable = %q, want git", got[0].Executable)
	}
	if len(got[0].EnvVars) != 1 || got[0].EnvVars[0].Name != "FOO" {
		t.Errorf("EnvVars = %v, want [{FOO ...}]", got[0].EnvVars)
	}
}

func TestParse_BacktickInEnvVar(t *testing.T) {
	got := Parse("FOO=`echo hello` git status")
	if len(got) != 1 {
		t.Fatalf("len(Parse) = %d, want 1 (backtick in env var)", len(got))
	}
	if got[0].Executable != "git" {
		t.Errorf("Executable = %q, want git", got[0].Executable)
	}
}

func TestParse_NestedSubshellInEnvVar(t *testing.T) {
	got := Parse("FOO=$(a $(b)) cmd")
	if len(got) != 1 {
		t.Fatalf("len(Parse) = %d, want 1 (nested subshell)", len(got))
	}
	if got[0].Executable != "cmd" {
		t.Errorf("Executable = %q, want cmd", got[0].Executable)
	}
}

func TestParse_ExpansionKind(t *testing.T) {
	tests := []struct {
		input         string
		wantExpansion ExpansionKind
	}{
		{"FOO=bar cmd", ExpansionNone},
		{"FOO=/a/b/c cmd", ExpansionNone},
		{"FOO= cmd", ExpansionNone},
		{"FOO=$HOME cmd", ExpansionVarRef},
		{"FOO=$USER cmd", ExpansionVarRef},
		{"FOO=${VAR:-default} cmd", ExpansionVarRef},
		{"FOO=$((1+2)) cmd", ExpansionArithmetic},
		{"FOO=$(mktemp) cmd", ExpansionSafeCmd},
		{"FOO=$(mktemp -d) cmd", ExpansionSafeCmd},
		{"FOO=$(date +%F) cmd", ExpansionSafeCmd},
		{"FOO=$(whoami) cmd", ExpansionSafeCmd},
		{"FOO=$(id -u) cmd", ExpansionSafeCmd},
		{"FOO=$(pwd) cmd", ExpansionSafeCmd},
		{"FOO=$(basename /a/b) cmd", ExpansionSafeCmd},
		{"FOO=$(dirname /a/b) cmd", ExpansionSafeCmd},
		{"FOO=$(curl evil) cmd", ExpansionUnknown},
		{"FOO=$(rm -rf /) cmd", ExpansionUnknown},
		{"FOO=`date` cmd", ExpansionSafeCmd},
		{"FOO=`curl evil` cmd", ExpansionUnknown},
		// Multiple expressions must be ExpansionUnknown (security: only first is checked otherwise)
		{"FOO=$(mktemp)$(curl evil) cmd", ExpansionUnknown},
		{"FOO=$(date)/$(rm -rf /) cmd", ExpansionUnknown},
		{"FOO=`date``curl evil` cmd", ExpansionUnknown},
		{"FOO=$(mktemp)$HOME cmd", ExpansionUnknown},
	}
	for _, tt := range tests {
		got := Parse(tt.input)
		if len(got) != 1 {
			t.Fatalf("Parse(%q): got %d commands, want 1", tt.input, len(got))
		}
		if len(got[0].EnvVars) != 1 {
			t.Fatalf("Parse(%q): got %d env vars, want 1", tt.input, len(got[0].EnvVars))
		}
		if got[0].EnvVars[0].Expansion != tt.wantExpansion {
			t.Errorf("Parse(%q).EnvVars[0].Expansion = %d, want %d", tt.input, got[0].EnvVars[0].Expansion, tt.wantExpansion)
		}
	}
}

func TestClassifyExpansion_Unclosed(t *testing.T) {
	if got := classifyExpansion("$(incomplete"); got != ExpansionUnknown {
		t.Errorf("classifyExpansion(%q) = %d, want ExpansionUnknown", "$(incomplete", got)
	}
}

func TestParse_CloudflaredAccessCurl(t *testing.T) {
	got := Parse(`cloudflared access curl "https://example.com"`)
	if len(got) != 1 {
		t.Fatalf("len(Parse) = %d, want 1", len(got))
	}
	if got[0].Executable != "curl" {
		t.Errorf("Executable = %q, want curl (cloudflared access unwrapped)", got[0].Executable)
	}
	want := []string{"https://example.com"}
	if !reflect.DeepEqual(got[0].Args, want) {
		t.Errorf("Args = %v, want %v", got[0].Args, want)
	}
}

func TestParse_CloudflaredAccessCurlPipe(t *testing.T) {
	got := Parse(`cloudflared access curl "https://example.zr.org/api" | jq '.'`)
	if len(got) != 2 {
		t.Fatalf("len(Parse) = %d, want 2", len(got))
	}
	if got[0].Executable != "curl" {
		t.Errorf("got[0].Executable = %q, want curl", got[0].Executable)
	}
	if got[1].Executable != "jq" {
		t.Errorf("got[1].Executable = %q, want jq", got[1].Executable)
	}
}

func TestParse_CloudflaredNonAccess(t *testing.T) {
	got := Parse("cloudflared tunnel list")
	if len(got) != 1 {
		t.Fatalf("len(Parse) = %d, want 1", len(got))
	}
	if got[0].Executable != "cloudflared" {
		t.Errorf("Executable = %q, want cloudflared (no unwrap for non-access subcommand)", got[0].Executable)
	}
	want := []string{"tunnel", "list"}
	if !reflect.DeepEqual(got[0].Args, want) {
		t.Errorf("Args = %v, want %v", got[0].Args, want)
	}
}

func TestParse_CloudflaredAccessNoInnerCmd(t *testing.T) {
	got := Parse("cloudflared access")
	if len(got) != 1 {
		t.Fatalf("len(Parse) = %d, want 1", len(got))
	}
	if got[0].Executable != "cloudflared" {
		t.Errorf("Executable = %q, want cloudflared (no inner cmd to unwrap to)", got[0].Executable)
	}
}

func TestExtractComment(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`curl https://api.zr.org # health check`, "health check"},
		{`echo "foo # bar"`, ""},
		{`echo 'foo # bar'`, ""},
		{"cmd #", ""},
		{"cmd", ""},
	}
	for _, tt := range tests {
		got := ExtractComment(tt.input)
		if got != tt.want {
			t.Errorf("ExtractComment(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestStripComment(t *testing.T) {
	got := StripComment(`curl https://api.zr.org # health check`)
	want := "curl https://api.zr.org"
	if got != want {
		t.Errorf("StripComment(%q) = %q, want %q", "curl https://api.zr.org # health check", got, want)
	}
}

func TestStripComment_NixFlakeRef(t *testing.T) {
	got := StripComment("nix build .#myPackage")
	want := "nix build .#myPackage"
	if got != want {
		t.Errorf("StripComment(%q) = %q, want %q", "nix build .#myPackage", got, want)
	}
}

func TestExtractComment_NixFlakeRef(t *testing.T) {
	got := ExtractComment("nix build .#myPackage")
	if got != "" {
		t.Errorf("ExtractComment(%q) = %q, want empty (not a comment)", "nix build .#myPackage", got)
	}
}

func TestParse_NixFlakeRef(t *testing.T) {
	got := Parse("nix build .#myPackage")
	if len(got) != 1 {
		t.Fatalf("len(Parse) = %d, want 1", len(got))
	}
	if got[0].Executable != "nix" {
		t.Errorf("Executable = %q, want nix", got[0].Executable)
	}
	want := []string{"build", ".#myPackage"}
	if !reflect.DeepEqual(got[0].Args, want) {
		t.Errorf("Args = %v, want %v", got[0].Args, want)
	}
}

func TestParse_NixFlakeRefWithComment(t *testing.T) {
	got := Parse("nix build .#myPackage # build the package")
	if len(got) != 1 {
		t.Fatalf("len(Parse) = %d, want 1", len(got))
	}
	want := []string{"build", ".#myPackage"}
	if !reflect.DeepEqual(got[0].Args, want) {
		t.Errorf("Args = %v, want %v", got[0].Args, want)
	}
	if got[0].Comment != "build the package" {
		t.Errorf("Comment = %q, want 'build the package'", got[0].Comment)
	}
}

func TestParse_MultilineWithComment(t *testing.T) {
	got := Parse("# Check the status\ngit status")
	if len(got) != 1 {
		t.Fatalf("len(Parse) = %d, want 1 (comment line stripped, git status remains)", len(got))
	}
	if got[0].Executable != "git" {
		t.Errorf("Executable = %q, want git", got[0].Executable)
	}
}

func TestParse_MultilineMultipleCommands(t *testing.T) {
	got := Parse("echo hello\ngit status")
	if len(got) != 2 {
		t.Fatalf("len(Parse) = %d, want 2", len(got))
	}
	if got[0].Executable != "echo" {
		t.Errorf("got[0].Executable = %q, want echo", got[0].Executable)
	}
	if got[1].Executable != "git" {
		t.Errorf("got[1].Executable = %q, want git", got[1].Executable)
	}
}

func TestParse_CommentOnlyLine(t *testing.T) {
	got := Parse("# just a comment")
	if len(got) != 0 {
		t.Errorf("len(Parse) = %d, want 0 (pure comment should be empty)", len(got))
	}
}

func TestParse_CommentExtraction(t *testing.T) {
	got := Parse("curl https://api.zr.org # health check")
	if len(got) != 1 {
		t.Fatalf("len(Parse) = %d, want 1", len(got))
	}
	if got[0].Comment != "health check" {
		t.Errorf("Comment = %q, want health check", got[0].Comment)
	}
	wantArgs := []string{"https://api.zr.org"}
	if !reflect.DeepEqual(got[0].Args, wantArgs) {
		t.Errorf("Args = %v, want %v (must not include #, health, check)", got[0].Args, wantArgs)
	}
}

func TestParse_Redirections(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		wantExec   string
		wantArgs   []string
		wantRedirs []hookio.Redirection
	}{
		{
			name: "stdin redirect", command: "docker load < /nix/store/image.tar.gz",
			wantExec: "docker", wantArgs: []string{"load"},
			wantRedirs: []hookio.Redirection{{Operator: "<", Path: "/nix/store/image.tar.gz", Kind: hookio.RedirectStdin}},
		},
		{
			name: "stdout redirect", command: "echo hello > /tmp/out.txt",
			wantExec: "echo", wantArgs: []string{"hello"},
			wantRedirs: []hookio.Redirection{{Operator: ">", Path: "/tmp/out.txt", Kind: hookio.RedirectStdout}},
		},
		{
			name: "stderr redirect", command: "cmd 2>/dev/null",
			wantExec: "cmd", wantArgs: []string{},
			wantRedirs: []hookio.Redirection{{Operator: "2>", Path: "/dev/null", Kind: hookio.RedirectStderr}},
		},
		{
			name: "append redirect", command: "echo line >> /tmp/log.txt",
			wantExec: "echo", wantArgs: []string{"line"},
			wantRedirs: []hookio.Redirection{{Operator: ">>", Path: "/tmp/log.txt", Kind: hookio.RedirectStdout}},
		},
		{
			name: "fd duplication ignored", command: "cmd 2>&1",
			wantExec: "cmd", wantArgs: []string{},
			wantRedirs: nil,
		},
		{
			name: "all redirect", command: "cmd &>/tmp/all.log",
			wantExec: "cmd", wantArgs: []string{},
			wantRedirs: []hookio.Redirection{{Operator: "&>", Path: "/tmp/all.log", Kind: hookio.RedirectAll}},
		},
		{
			name: "multiple redirections", command: "cmd < /tmp/in.txt > /tmp/out.txt 2>/tmp/err.txt",
			wantExec: "cmd", wantArgs: []string{},
			wantRedirs: []hookio.Redirection{
				{Operator: "<", Path: "/tmp/in.txt", Kind: hookio.RedirectStdin},
				{Operator: ">", Path: "/tmp/out.txt", Kind: hookio.RedirectStdout},
				{Operator: "2>", Path: "/tmp/err.txt", Kind: hookio.RedirectStderr},
			},
		},
		{
			name: "no redirections unchanged", command: "ls -la /tmp",
			wantExec: "ls", wantArgs: []string{"-la", "/tmp"},
			wantRedirs: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.command)
			if len(got) != 1 {
				t.Fatalf("Parse(%q): got %d commands, want 1", tt.command, len(got))
			}
			pc := got[0]
			if pc.Executable != tt.wantExec {
				t.Errorf("Executable = %q, want %q", pc.Executable, tt.wantExec)
			}
			if tt.wantArgs == nil {
				tt.wantArgs = []string{}
			}
			gotArgs := pc.Args
			if gotArgs == nil {
				gotArgs = []string{}
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("Args = %v, want %v", gotArgs, tt.wantArgs)
			}
			if !reflect.DeepEqual(pc.Redirections, tt.wantRedirs) {
				t.Errorf("Redirections = %v, want %v", pc.Redirections, tt.wantRedirs)
			}
		})
	}
}

func TestParse_BackslashEscapes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantExec string
		wantArgs []string
	}{
		{
			name:     "escaped double quote inside double quotes",
			input:    `echo "hello \"world\""`,
			wantExec: "echo",
			wantArgs: []string{`hello "world"`},
		},
		{
			name:     "escaped backslash inside double quotes",
			input:    `echo "back\\slash"`,
			wantExec: "echo",
			wantArgs: []string{`back\slash`},
		},
		{
			name:     "unrecognized escape passes through",
			input:    `echo "no escape \n"`,
			wantExec: "echo",
			wantArgs: []string{`no escape \n`},
		},
		{
			name:     "single quotes ignore backslash",
			input:    `echo 'hello \"world\"'`,
			wantExec: "echo",
			wantArgs: []string{`hello \"world\"`},
		},
		{
			name:     "backslash at end of double-quoted string",
			input:    `echo "trailing\\"`,
			wantExec: "echo",
			wantArgs: []string{`trailing\`},
		},
		{
			name:     "escaped quote in compound command",
			input:    `echo "a\"b" && echo c`,
			wantExec: "echo",
			wantArgs: []string{`a"b`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.input)
			if len(got) == 0 {
				t.Fatalf("Parse(%q): got 0 commands", tt.input)
			}
			if got[0].Executable != tt.wantExec {
				t.Errorf("Executable = %q, want %q", got[0].Executable, tt.wantExec)
			}
			if !reflect.DeepEqual(got[0].Args, tt.wantArgs) {
				t.Errorf("Args = %v, want %v", got[0].Args, tt.wantArgs)
			}
		})
	}
}

func TestExtractComment_BackslashEscapes(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`echo "escaped \" quote" # real comment`, "real comment"},
		{`echo "no comment # inside quotes \""`, ""},
	}
	for _, tt := range tests {
		got := ExtractComment(tt.input)
		if got != tt.want {
			t.Errorf("ExtractComment(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParse_ProcessSubstitution(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantExec  string
		wantArgs  []string
		wantPSubs []string
	}{
		{
			name:      "diff with two process substitutions",
			input:     "diff <(sort file1) <(sort file2)",
			wantExec:  "diff",
			wantArgs:  []string{"/dev/fd/63", "/dev/fd/63"},
			wantPSubs: []string{"sort file1", "sort file2"},
		},
		{
			name:      "output process substitution",
			input:     "tee >(wc -l) > /tmp/out.txt",
			wantExec:  "tee",
			wantArgs:  []string{"/dev/fd/63"},
			wantPSubs: []string{"wc -l"},
		},
		{
			name:      "no process substitution in double quotes",
			input:     `echo "<(not a procsub)"`,
			wantExec:  "echo",
			wantArgs:  []string{"<(not a procsub)"},
			wantPSubs: nil,
		},
		{
			name:      "process substitution with nested parens",
			input:     "diff <(sort $(cat file)) <(sort file2)",
			wantExec:  "diff",
			wantArgs:  []string{"/dev/fd/63", "/dev/fd/63"},
			wantPSubs: []string{"sort $(cat file)", "sort file2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.input)
			if len(got) == 0 {
				t.Fatalf("Parse(%q): got 0 commands", tt.input)
			}
			pc := got[0]
			if pc.Executable != tt.wantExec {
				t.Errorf("Executable = %q, want %q", pc.Executable, tt.wantExec)
			}
			if !reflect.DeepEqual(pc.Args, tt.wantArgs) {
				t.Errorf("Args = %v, want %v", pc.Args, tt.wantArgs)
			}
			if tt.wantPSubs == nil && len(pc.ProcessSubstitutions) != 0 {
				t.Errorf("ProcessSubstitutions = %v, want nil", pc.ProcessSubstitutions)
			} else if tt.wantPSubs != nil && !reflect.DeepEqual(pc.ProcessSubstitutions, tt.wantPSubs) {
				t.Errorf("ProcessSubstitutions = %v, want %v", pc.ProcessSubstitutions, tt.wantPSubs)
			}
		})
	}
}

func TestParse_SubshellGrouping(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantCount int
		wantExecs []string
	}{
		{
			name:      "simple subshell extracted as segment",
			input:     "(cd /tmp && ls -la)",
			wantCount: 2,
			wantExecs: []string{"cd", "ls"},
		},
		{
			name:      "subshell followed by command",
			input:     "(echo a) && echo b",
			wantCount: 2,
			wantExecs: []string{"echo", "echo"},
		},
		{
			name:      "dollar-paren is NOT subshell grouping",
			input:     "FOO=$(echo hello) cmd",
			wantCount: 1,
			wantExecs: []string{"cmd"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.input)
			if len(got) != tt.wantCount {
				t.Fatalf("Parse(%q): got %d commands, want %d", tt.input, len(got), tt.wantCount)
			}
			for i, wantExec := range tt.wantExecs {
				if got[i].Executable != wantExec {
					t.Errorf("got[%d].Executable = %q, want %q", i, got[i].Executable, wantExec)
				}
			}
		})
	}
}

func TestParse_FindEscapedParens(t *testing.T) {
	// find uses \( and \) for grouping, which must not be treated as subshells
	tests := []struct {
		name      string
		input     string
		wantCount int
		wantExecs []string
	}{
		{
			name:      "find with escaped parens piped to head",
			input:     `find /Users/phillipg -type f \( -name "*help*" -o -name "*.sh" \) | head -10`,
			wantCount: 2,
			wantExecs: []string{"find", "head"},
		},
		{
			name:      "find with escaped parens and redirect",
			input:     `find /tmp -type f \( -name "*.nix" \) 2>/dev/null`,
			wantCount: 1,
			wantExecs: []string{"find"},
		},
		{
			name:      "find with escaped parens piped to xargs grep",
			input:     `find /tmp -name "*.nix" | xargs grep -l "plist\|ProgramArguments" | head -10`,
			wantCount: 3,
			wantExecs: []string{"find", "xargs", "head"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.input)
			if len(got) != tt.wantCount {
				t.Fatalf("Parse(%q): got %d commands, want %d", tt.input, len(got), tt.wantCount)
			}
			for i, wantExec := range tt.wantExecs {
				if got[i].Executable != wantExec {
					t.Errorf("got[%d].Executable = %q, want %q", i, got[i].Executable, wantExec)
				}
			}
		})
	}
}

func TestParse_CommentWithQuotesMultiline(t *testing.T) {
	// pg2-8c2y: quotes inside comments must not desync splitCompound's quote tracking
	tests := []struct {
		name      string
		input     string
		wantCount int
		wantExecs []string
	}{
		{
			name:      "apostrophe in comment before command",
			input:     "# Check if it's auto-created\ngrep -r \"ServiceAccount\" tools/ 2>/dev/null",
			wantCount: 1,
			wantExecs: []string{"grep"},
		},
		{
			name:      "double quotes in comment before command",
			input:     "# Look for \"special\" things\nls /tmp",
			wantCount: 1,
			wantExecs: []string{"ls"},
		},
		{
			name:      "backtick in comment before command",
			input:     "# Run `test` first\necho hello",
			wantCount: 1,
			wantExecs: []string{"echo"},
		},
		{
			name:      "comment with && inside does not split",
			input:     "# step 1 && step 2\ngrep pattern file",
			wantCount: 1,
			wantExecs: []string{"grep"},
		},
		{
			name:      "inline comment with quotes does not affect next pipe segment",
			input:     "echo hello # it's fine | grep foo",
			wantCount: 1, // "| grep foo" is inside the comment (after #)
			wantExecs: []string{"echo"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.input)
			if len(got) != tt.wantCount {
				t.Fatalf("Parse(%q): got %d commands, want %d", tt.input, len(got), tt.wantCount)
			}
			for i, wantExec := range tt.wantExecs {
				if got[i].Executable != wantExec {
					t.Errorf("got[%d].Executable = %q, want %q", i, got[i].Executable, wantExec)
				}
			}
		})
	}
}

func TestParse_ForLoop(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantCount int
		wantExecs []string
	}{
		{
			name:      "simple for loop",
			input:     `for f in *.md; do echo "$f"; done`,
			wantCount: 1,
			wantExecs: []string{"echo"},
		},
		{
			name:      "for loop with multiple body commands",
			input:     `for f in *.md; do echo "$f"; cat "$f"; done`,
			wantCount: 2,
			wantExecs: []string{"echo", "cat"},
		},
		{
			name:      "for loop with pipe in body",
			input:     `for f in *.md; do cat "$f" | grep pattern; done`,
			wantCount: 2,
			wantExecs: []string{"cat", "grep"},
		},
		{
			name:      "for loop followed by other commands",
			input:     `for f in a b; do echo "$f"; done && echo "all done"`,
			wantCount: 2,
			wantExecs: []string{"echo", "echo"},
		},
		{
			name:      "for loop with newline separators",
			input:     "for f in *.md\ndo\n  echo \"$f\"\ndone",
			wantCount: 1,
			wantExecs: []string{"echo"},
		},
		{
			name:      "nested for loops",
			input:     `for x in a b; do for y in 1 2; do echo $x $y; done; done`,
			wantCount: 1,
			wantExecs: []string{"echo"},
		},
		{
			name:      "for loop with && in body",
			input:     `for app in a b; do echo "=== $app ===" && ls "$app"; done`,
			wantCount: 2,
			wantExecs: []string{"echo", "ls"},
		},
		{
			name:      "for loop with redirect on done",
			input:     `for f in a b; do echo "$f"; done 2>/dev/null`,
			wantCount: 1,
			wantExecs: []string{"echo"},
		},
		{
			name:      "incomplete for loop falls through",
			input:     `for f in *.md; do echo "$f"`,
			wantCount: 2, // "for" and "do" as separate commands (no done found)
			wantExecs: []string{"for", "do"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.input)
			if len(got) != tt.wantCount {
				t.Fatalf("Parse(%q): got %d commands, want %d", tt.input, len(got), tt.wantCount)
			}
			for i, wantExec := range tt.wantExecs {
				if got[i].Executable != wantExec {
					t.Errorf("got[%d].Executable = %q, want %q", i, got[i].Executable, wantExec)
				}
			}
		})
	}
}

func TestParse_WhileLoop(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantCount int
		wantExecs []string
	}{
		{
			name:      "while read loop",
			input:     `while read line; do echo "$line"; done`,
			wantCount: 2,
			wantExecs: []string{"read", "echo"},
		},
		{
			name:      "piped while loop",
			input:     `cat file.txt | while read line; do echo "$line"; done`,
			wantCount: 3,
			wantExecs: []string{"cat", "read", "echo"},
		},
		{
			name:      "until loop",
			input:     `until test -f /tmp/ready; do sleep 1; done`,
			wantCount: 2,
			wantExecs: []string{"test", "sleep"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.input)
			if len(got) != tt.wantCount {
				t.Fatalf("Parse(%q): got %d commands, want %d", tt.input, len(got), tt.wantCount)
			}
			for i, wantExec := range tt.wantExecs {
				if got[i].Executable != wantExec {
					t.Errorf("got[%d].Executable = %q, want %q", i, got[i].Executable, wantExec)
				}
			}
		})
	}
}

func TestParse_Heredoc(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{"heredoc", "cat <<EOF\nhello\nEOF", true},
		{"herestring", "cmd <<<'input'", true},
		{"no heredoc", "echo hello > /tmp/out", false},
		{"stdin redirect not heredoc", "cmd < /tmp/in", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.command)
			if len(got) == 0 {
				t.Fatalf("Parse(%q): got 0 commands", tt.command)
			}
			hasHeredoc := false
			for _, pc := range got {
				if pc.HasHeredoc {
					hasHeredoc = true
					break
				}
			}
			if hasHeredoc != tt.want {
				t.Errorf("HasHeredoc = %v, want %v", hasHeredoc, tt.want)
			}
		})
	}
}
