package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
)

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = Execute(context.Background(), "test", args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestRootListsThreeGroups(t *testing.T) {
	code, out, _ := run(t)
	if code != exitcode.OK {
		t.Fatalf("exit %d", code)
	}
	for _, g := range []string{"human", "agent", "job"} {
		if !strings.Contains(out, g) {
			t.Errorf("root help lacks group %s:\n%s", g, out)
		}
	}
}

func TestGroupHelpListsCommands(t *testing.T) {
	want := map[string][]string{
		"human": {"pending", "show", "rule"},
		"agent": {"q", "file", "context"},
		"job":   {"run", "maintain", "doctor", "status", "show-run", "resume"},
	}
	for g, cmds := range want {
		code, out, _ := run(t, g, "--help")
		if code != exitcode.OK {
			t.Fatalf("%s --help: exit %d", g, code)
		}
		for _, c := range cmds {
			if !strings.Contains(out, "  "+c+" ") {
				t.Errorf("%s --help lacks %s:\n%s", g, c, out)
			}
		}
	}
}

func TestNotBuiltCommandsAreAbsent(t *testing.T) {
	for _, c := range [][]string{
		{"human", "evidence"},
		{"human", "defer-all"},
		{"human", "status"},
		{"human", "topics"},
		{"human", "scopes"},
		{"agent", "ask"},
		{"job", "outbox-drain"},
		{"job", "export"},
		{"job", "metrics"},
		{"job", "config-check"},
		{"human", "topic"},
	} {
		if code, _, _ := run(t, c...); code != exitcode.Usage {
			t.Errorf("%v: exit %d, want usage error", c, code)
		}
	}
}

func TestStubsPrintNotImplementedAndExitOne(t *testing.T) {
	cases := [][]string{
		{"human", "pending"},
		{"human", "pending", "--decided"},
		{"human", "show", "3", "--evidence"},
		{"human", "rule", "3", "4", "adopted", "--note", "n", "--reconsider-when", "w", "--useful", "yes"},
		{"agent", "q", "ccaudit", "some-query"},
		{"agent", "file", "f.json", "--dry-run"},
		{"agent", "file", "f.json", "--run", "r1"},
		{"agent", "context"},
		{"job", "run", "lens", "--scope", "s", "--duration", "7d", "--at", "2026-01-08T00:00:00Z", "--profile", "p", "--analyst", "none", "--evidence-only", "--run-key", "k", "--schedule-id", "id"},
		{"job", "run", "lens", "--scope", "s", "--from", "a", "--to", "b"},
		{"job", "maintain"},
		{"job", "doctor", "--config-only"},
		{"job", "status", "--json"},
		{"job", "show-run", "r1"},
		{"job", "resume", "lens"},
	}
	for _, c := range cases {
		code, _, stderr := run(t, c...)
		if code != exitcode.Failure || strings.TrimSpace(stderr) != "not implemented" {
			t.Errorf("%v: exit %d stderr %q", c, code, stderr)
		}
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	cases := [][]string{
		{"nope"},
		{"human", "nope"},
		{"human", "--bogus"},
		{"human", "show"},
		{"human", "rule", "3"},
		{"agent", "file"},
		{"job", "run", "lens"}, // --scope is required
		{"job", "pending"},
		{"--output", "yaml", "agent", "context"},
	}
	for _, c := range cases {
		if code, _, _ := run(t, c...); code != exitcode.Usage {
			t.Errorf("%v: exit %d, want %d", c, code, exitcode.Usage)
		}
	}
}

func TestJobRunStoresFlagsInRunOptions(t *testing.T) {
	saved := runJob
	t.Cleanup(func() { runJob = saved })
	var got RunOptions
	runJob = func(_ *cobra.Command, o *RunOptions) error { got = *o; return nil }
	code, _, stderr := run(t, "job", "run", "lens-a", "--scope", "s1", "--from", "F", "--to", "T",
		"--duration", "7d", "--at", "A", "--profile", "P", "--analyst", "interactive",
		"--evidence-only", "--run-key", "K", "--schedule-id", "ID")
	if code != exitcode.OK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	want := RunOptions{
		Lens: "lens-a", Scope: "s1", From: "F", To: "T", Duration: "7d", At: "A",
		Profile: "P", Analyst: "interactive", EvidenceOnly: true, RunKey: "K", ScheduleID: "ID",
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestOutputDefaultsPerGroupAndOverride(t *testing.T) {
	root := NewRoot("test")
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"human", "pending"}, "text"},
		{[]string{"agent", "context"}, "json"},
		{[]string{"job", "status"}, "text"},
		{[]string{"human", "pending", "--output", "json"}, "json"},
		{[]string{"agent", "context", "--output", "text"}, "text"},
	}
	for _, c := range cases {
		cmd, _, err := root.Find(c.args[:2])
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.ParseFlags(c.args[2:]); err != nil {
			t.Fatal(err)
		}
		// The persistent --output lives on the root; reset it between cases.
		got, err := OutputFormat(cmd)
		if err != nil || got != c.want {
			t.Errorf("%v: got %q, %v; want %q", c.args, got, err, c.want)
		}
		_ = root.PersistentFlags().Set("output", "")
	}
}
