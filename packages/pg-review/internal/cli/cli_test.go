package cli

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"

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
		{"agent", "q", "ccaudit", "some-query"},
		{"job", "maintain"},
		{"job", "doctor"},
		{"job", "status", "--json"},
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

func doctorEnv(t *testing.T, cfg string, stubs ...string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root+"/cfg")
	t.Setenv("XDG_STATE_HOME", root+"/state")
	t.Setenv("XDG_CACHE_HOME", root+"/cache")
	bin := t.TempDir()
	for _, s := range stubs {
		if err := os.WriteFile(bin+"/"+s, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	if cfg != "" {
		if err := os.MkdirAll(root+"/cfg/pg-review", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(root+"/cfg/pg-review/config.toml", []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

var allProviderStubs = []string{"pg-review-provider-ccaudit", "pg-review-provider-hook-decisions", "pg-review-provider-config-inventory"}

func TestDoctorConfigOnlyNoConfigIsValidWithWarning(t *testing.T) {
	doctorEnv(t, "", allProviderStubs...)
	code, out, _ := run(t, "job", "doctor", "--config-only")
	if code != exitcode.OK {
		t.Fatalf("exit %d: %s", code, out)
	}
	if n := strings.Count(out, "warning:"); n != 1 || !strings.Contains(out, "no scope is configured") {
		t.Errorf("output:\n%s", out)
	}
}

func TestDoctorConfigOnlyValidFile(t *testing.T) {
	doctorEnv(t, "[[scope]]\nname=\"w\"\nprefixes=[\"/a\"]\n", allProviderStubs...)
	code, out, _ := run(t, "job", "doctor", "--config-only")
	if code != exitcode.OK || strings.Contains(out, "warning") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestDoctorConfigOnlyRejections(t *testing.T) {
	cases := []struct{ name, cfg, want string }{
		{"unknown provider", "[[lens]]\nid=\"agent-workflow\"\nproviders=[\"nope\"]\n", `lens "agent-workflow": unknown provider "nope"`},
		{"unknown profile", "[[lens]]\nid=\"agent-workflow\"\nprofile=\"nope\"\n", `lens "agent-workflow": unknown profile "nope"`},
		{"empty kinds", "[[lens]]\nid=\"agent-workflow\"\nkinds=[]\n", `lens "agent-workflow": empty kind list`},
		{"provider without command", "[[provider]]\nname=\"newp\"\n", `provider "newp": command is missing`},
		{"scope without prefix", "[[scope]]\nname=\"s\"\n", `scope "s": no prefix`},
		{"duplicate ids", "[[scope]]\nname=\"s\"\nprefixes=[\"/a\"]\n[[scope]]\nname=\"s\"\nprefixes=[\"/a\"]\n", `scope "s": duplicate id`},
		{"missing prompt", "[[lens]]\nid=\"agent-workflow\"\nprompt=\"prompts/gone.md\"\n", `lens "agent-workflow": prompt file`},
		{"profile without model", "[profile.fresh]\neffort=\"low\"\n", `profile "fresh": no model`},
		{"bad effort", "[profile.deep-review]\neffort=\"extreme\"\n", `profile "deep-review": effort "extreme"`},
		{"malformed toml", "[oops", `config.toml`},
		{"bad schema version", "schema_version = 7\n", `unsupported schema_version`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doctorEnv(t, c.cfg, allProviderStubs...)
			code, out, _ := run(t, "job", "doctor", "--config-only")
			if code != exitcode.ValidationRejected || !strings.Contains(out, c.want) {
				t.Errorf("exit %d, want 4 with %q:\n%s", code, c.want, out)
			}
		})
	}
}

func TestDoctorConfigOnlyMissingBuiltinProviderCommand(t *testing.T) {
	doctorEnv(t, "", "pg-review-provider-ccaudit", "pg-review-provider-hook-decisions")
	code, out, _ := run(t, "job", "doctor", "--config-only")
	if code != exitcode.ValidationRejected || !strings.Contains(out, `provider "config-inventory"`) {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

func init() {
	// The prompts packet owns the embedded prompt content; the doctor tests
	// use an in-memory fs holding the built-in prompt paths.
	assetsFS = func() fs.FS {
		return fstest.MapFS{
			"prompts/base.md":           {Data: []byte("b")},
			"prompts/agent-workflow.md": {Data: []byte("a")},
			"prompts/config-health.md":  {Data: []byte("c")},
		}
	}
}
