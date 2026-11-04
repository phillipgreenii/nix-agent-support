package shapes

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
)

const testCWD = "/work/repo"

func bashRow(t *testing.T, cmd string) asklog.DecisionRow {
	t.Helper()
	in, err := json.Marshal(map[string]string{"command": cmd, "description": "d"})
	if err != nil {
		t.Fatal(err)
	}
	return asklog.DecisionRow{ID: 1, ToolName: "Bash", ToolInputJSON: string(in), CWD: testCWD}
}

func toolRow(tool, input string) asklog.DecisionRow {
	return asklog.DecisionRow{ID: 1, ToolName: tool, ToolInputJSON: input, CWD: testCWD}
}

func keyOf(t *testing.T, cmd string) string {
	t.Helper()
	return Normalize(bashRow(t, cmd)).Key()
}

func TestShapeCollapsesRowsDifferingOnlyInLiteralPath(t *testing.T) {
	a, b := keyOf(t, "rm -rf ./build"), keyOf(t, "rm -rf ./dist/out")
	if a != b {
		t.Fatalf("project-zone paths must collapse:\n%s\n%s", a, b)
	}
	if keyOf(t, "rm -rf ./build") == keyOf(t, "rm -rf /") {
		t.Fatal("./build (project) and / (other-absolute) must differ")
	}
	r1 := Normalize(toolRow("Read", `{"file_path":"/work/repo/a.go"}`))
	r2 := Normalize(toolRow("Read", `{"file_path":"/work/repo/sub/b.go"}`))
	if r1 != r2 {
		t.Fatalf("Read of two project files must collapse: %+v %+v", r1, r2)
	}
}

func TestShapeCompoundKeepsOrderedProgramSubcommandPairs(t *testing.T) {
	ls, rm := Normalize(bashRow(t, "cd /repo && ls x")), Normalize(bashRow(t, "cd /repo && rm x"))
	if ls.Key() == rm.Key() {
		t.Fatal("cd && ls and cd && rm must differ")
	}
	if got, want := ls.Programs(), []string{"cd", "ls"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("programs = %v, want %v", got, want)
	}
	if ls.Program != "ls" || ls.Family() != "ls" {
		t.Fatalf("program/family must skip the cd wrapper, got %q/%q", ls.Program, ls.Family())
	}
	if keyOf(t, "ls x && cd /repo") == keyOf(t, "cd /repo && ls x") {
		t.Fatal("order of simple commands must be part of the key")
	}
	p := Normalize(bashRow(t, "cd a; rm -rf ./b | grep x"))
	if got, want := p.Programs(), []string{"cd", "rm", "grep"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("programs = %v, want %v", got, want)
	}
	if p.Program != "rm" {
		t.Fatalf("program = %q, want rm (first non-wrapper)", p.Program)
	}
	if got := p.FlagList(); !reflect.DeepEqual(got, []string{"-f", "-r"}) {
		t.Fatalf("flags = %v", got)
	}
}

func TestShapeWrappersAreInProgramsButNotProgram(t *testing.T) {
	s := Normalize(bashRow(t, "sudo -u root env A=1 ls /etc"))
	if got, want := s.Programs(), []string{"sudo", "env", "ls"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("programs = %v, want %v", got, want)
	}
	if s.Program != "ls" || s.Family() != "ls" {
		t.Fatalf("program/family = %q/%q", s.Program, s.Family())
	}
	if got := s.EnvNameList(); !reflect.DeepEqual(got, []string{"A"}) {
		t.Fatalf("env names = %v", got)
	}
	if got := s.PathClassList(); !reflect.DeepEqual(got, []string{"system"}) {
		t.Fatalf("path classes = %v", got)
	}
	n := Normalize(bashRow(t, "nohup ls x &"))
	if got, want := n.Programs(), []string{"nohup", "ls"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nohup programs = %v, want %v", got, want)
	}
	if got := Normalize(bashRow(t, "FOO=bar ls x")).EnvNameList(); !reflect.DeepEqual(got, []string{"FOO"}) {
		t.Fatalf("prefix assignment env names = %v", got)
	}
}

func TestShapePinnedFlagsContributeValues(t *testing.T) {
	if keyOf(t, "kubectl --context prod delete pod x") == keyOf(t, "kubectl --context dev delete pod x") {
		t.Fatal("kubectl --context prod vs dev must differ")
	}
	if keyOf(t, "curl -X DELETE http://h/x") == keyOf(t, "curl -X GET http://h/x") {
		t.Fatal("curl -X DELETE vs GET must differ")
	}
	k := Normalize(bashRow(t, "kubectl --context prod delete pod x"))
	if k.Subcommand != "delete" {
		t.Fatalf("subcommand = %q, want delete (the flag value must not be the subcommand)", k.Subcommand)
	}
	if got := k.FlagList(); !reflect.DeepEqual(got, []string{"--context=prod"}) {
		t.Fatalf("flags = %v", got)
	}
	// --foo=bar and --foo bar agree for a pinned flag too.
	if keyOf(t, "kubectl --context=prod delete pod x") != keyOf(t, "kubectl --context prod delete pod x") {
		t.Fatal("attached and separate pinned value must give one shape")
	}
	// A non-pinned flag contributes its name only.
	if keyOf(t, "grep --color=always x f") != keyOf(t, "grep --color=never x f") {
		t.Fatal("non-pinned flag values must not split shapes")
	}
	// rm -rf is two bare flags, not a value.
	if got := Normalize(bashRow(t, "rm -rf ./build")).FlagList(); !reflect.DeepEqual(got, []string{"-f", "-r"}) {
		t.Fatalf("rm -rf flags = %v", got)
	}
}

func TestShapePinnedFlagValueRendering(t *testing.T) {
	long := strings.Repeat("a", 100)
	cases := map[string]string{
		"curl -H '" + long + "' http://h":    "-H=" + strings.Repeat("a", 64),
		"curl -H 'X: $TOKEN' http://h":       "-H=<expansion>",
		"curl -H 'X: *glob' http://h":        "-H=<expansion>",
		"curl --header=Accept:json http://h": "--header=Accept:json",
	}
	for cmd, want := range cases {
		got := Normalize(bashRow(t, cmd)).FlagList()
		found := false
		for _, f := range got {
			if f == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: flags %v lack %q", cmd, got, want)
		}
	}
}

func TestShapeGitPushOperandsGetDistinctClasses(t *testing.T) {
	a := keyOf(t, "git push origin :main")
	b := keyOf(t, "git push origin main")
	c := keyOf(t, "git push origin ./main")
	if a == b || b == c || a == c {
		t.Fatalf("three refspec forms must differ:\n%s\n%s\n%s", a, b, c)
	}
	s := Normalize(bashRow(t, "git push origin :main"))
	if s.Subcommand != "push" {
		t.Fatalf("subcommand = %q", s.Subcommand)
	}
	want := []string{"ref-delete", "word"}
	if got := s.PathClassList(); !reflect.DeepEqual(got, want) {
		t.Fatalf("classes = %v, want %v", got, want)
	}
}

func TestShapePathClassOrder(t *testing.T) {
	for _, tc := range []struct{ p, want string }{
		{"https://x/y", "url"},
		{"~/.ssh/id_rsa", "secret-format"},
		{"/work/repo/.env.local", "secret-format"},
		{"cert.pem", "secret-format"},
		{"./.claude/settings.json", "agent-control"},
		{"CLAUDE.md", "agent-control"},
		{"/work/repo/.git/config", "git-internal"},
		{"src/*.go", "expansion"},
		{"$HOME/x", "expansion"},
		{"~/notes", "home"},
		{"/home/alice/x", "home"},
		{"/Users/bob/x", "home"},
		{"/tmp/x", "temp"},
		{"/var/tmp/x", "temp"},
		{"/etc/passwd", "system"},
		{"/nix/store/x", "system"},
		{"../up", "parent-escape"},
		{":main", "ref-delete"},
		{"-x", "flag-like"},
		{"/srv/data", "other-absolute"},
		{"/work/repo/src/a.go", "project"},
		{"/work/repo", "project"},
		{"./build", "project"},
		{"sub/dir", "project"},
		{".hidden", "project"},
		{"main", "word"},
	} {
		if got := PathClass(tc.p, testCWD); got != tc.want {
			t.Errorf("PathClass(%q) = %q, want %q", tc.p, got, tc.want)
		}
	}
	// A path outside the row's CWD is other-absolute, inside it project.
	if got := PathClass("/work/repo2/x", testCWD); got != "other-absolute" {
		t.Errorf("sibling directory must not count as under cwd, got %q", got)
	}
}

func TestShapeRedirectionTargetsAddClassesNotFlags(t *testing.T) {
	s := Normalize(bashRow(t, "ls > /tmp/x 2>&1"))
	if got := s.PathClassList(); !reflect.DeepEqual(got, []string{"temp"}) {
		t.Fatalf("classes = %v, want [temp]", got)
	}
	if got := s.FlagList(); len(got) != 0 {
		t.Fatalf("redirections must add no flag, got %v", got)
	}
	in := Normalize(bashRow(t, "cat < ./in.txt"))
	if got := in.PathClassList(); !reflect.DeepEqual(got, []string{"project"}) {
		t.Fatalf("classes = %v, want [project]", got)
	}
	if got := Normalize(bashRow(t, "ls 2>&1")).PathClassList(); len(got) != 0 {
		t.Fatalf("2>&1 must add nothing, got %v", got)
	}
}

func TestShapeUnparsedBash(t *testing.T) {
	for _, row := range []asklog.DecisionRow{
		bashRow(t, ""),
		bashRow(t, "   "),
		bashRow(t, "ls 'unterminated"),
		toolRow("Bash", `not json`),
		toolRow("Bash", `{"description":"no command"}`),
	} {
		s := Normalize(row)
		if s.Program != Unparsed || s.Family() != Unparsed || s.Subcommand != "" || len(s.Programs()) != 0 {
			t.Errorf("%q: want unparsed, got %+v", row.ToolInputJSON, s)
		}
	}
}

func TestShapeNonBashToolsKeyedByToolAndDiscriminator(t *testing.T) {
	skill := Normalize(toolRow("Skill", `{"skill":"pb:drain-one"}`))
	if skill.Subcommand != "pb:drain-one" || skill.Family() != "Skill" || skill.Program != "" {
		t.Fatalf("Skill: %+v", skill)
	}
	if skill.Key() == Normalize(toolRow("Skill", `{"skill":"other"}`)).Key() {
		t.Fatal("different skills must differ")
	}
	if got := Normalize(toolRow("Agent", `{"subagent_type":"Explore","prompt":"x"}`)).Subcommand; got != "Explore" {
		t.Fatalf("Agent subcommand = %q", got)
	}
	if got := Normalize(toolRow("mcp__HA__x", `{}`)).Subcommand; got != "mcp__HA__x" {
		t.Fatalf("mcp subcommand = %q", got)
	}
	mon := Normalize(toolRow("Monitor", `{"command":"tail -f x"}`))
	if mon.Subcommand != "" || len(mon.FlagList()) != 0 || len(mon.PathClassList()) != 0 || mon.Program != "" {
		t.Fatalf("Monitor must key by tool only: %+v", mon)
	}
	for _, tc := range []struct {
		tool, input, class string
	}{
		{"Read", `{"file_path":"/etc/hosts"}`, "system"},
		{"Write", `{"file_path":"/work/repo/a.txt"}`, "project"},
		{"Edit", `{"file_path":"~/x"}`, "home"},
		{"MultiEdit", `{"file_path":"/work/repo/.git/HEAD"}`, "git-internal"},
		{"Glob", `{"pattern":"*","path":"/tmp"}`, "temp"},
		{"Grep", `{"pattern":"x","path":"../y"}`, "parent-escape"},
	} {
		got := Normalize(toolRow(tc.tool, tc.input)).PathClassList()
		if !reflect.DeepEqual(got, []string{tc.class}) {
			t.Errorf("%s: classes = %v, want [%s]", tc.tool, got, tc.class)
		}
	}
	if got := Normalize(toolRow("Glob", `{"pattern":"*"}`)).PathClassList(); len(got) != 0 {
		t.Fatalf("missing path input must give empty classes, got %v", got)
	}
}

func TestShapeIgnoresRecordedDecision(t *testing.T) {
	row := bashRow(t, "ls x")
	allow, deny := "allow", "deny"
	row.HookDecision, row.Outcome = &allow, asklog.OutcomeApproved
	a := Normalize(row).Key()
	row.HookDecision, row.Outcome = &deny, asklog.OutcomeDenied
	if b := Normalize(row).Key(); a != b {
		t.Fatalf("recorded outcome must not affect the shape:\n%s\n%s", a, b)
	}
}

func TestShapeKeyIsSingleLineAndDeterministic(t *testing.T) {
	s := Normalize(bashRow(t, "curl -H 'a,b|c>d/e%f' http://h"))
	if strings.ContainsAny(s.Key(), "\n\x1f\x1e") {
		t.Fatalf("key must be one printable line: %q", s.Key())
	}
	if s.Key() != Normalize(bashRow(t, "curl -H 'a,b|c>d/e%f' http://h")).Key() {
		t.Fatal("key must be deterministic")
	}
	// An element containing a separator must not collide with two elements.
	if keyOf(t, "curl -H 'a,b' http://h") == keyOf(t, "curl -H a -H b http://h") {
		t.Fatal("escaping must keep a,b distinct from a and b")
	}
}

func TestShapeSubcommandOnlyForSchemaWithSubcommands(t *testing.T) {
	if got := Normalize(bashRow(t, "git status")).Subcommand; got != "status" {
		t.Fatalf("git subcommand = %q", got)
	}
	if got := Normalize(bashRow(t, "ls x")).Subcommand; got != "" {
		t.Fatalf("ls has no subcommands, got %q", got)
	}
	if got := Normalize(bashRow(t, "zzz-unknown-cmd status")).Subcommand; got != "" {
		t.Fatalf("a program without a schema has no subcommand, got %q", got)
	}
}

func TestShapeNonOptionDashTokensAreOperands(t *testing.T) {
	s := Normalize(bashRow(t, "echo --- done ---"))
	if got := s.FlagList(); len(got) != 0 {
		t.Fatalf("banner tokens must not be flags, got %v", got)
	}
	if got := s.PathClassList(); !reflect.DeepEqual(got, []string{"flag-like", "word"}) {
		t.Fatalf("classes = %v", got)
	}
	// After "--" even a real-looking option is an operand.
	if got := Normalize(bashRow(t, "rm -- -rf")).FlagList(); len(got) != 0 {
		t.Fatalf("flags after -- = %v", got)
	}
}
