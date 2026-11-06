package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/schema"
)

// stubOnPath puts a fake pg-ccaudit on PATH that records its arguments and
// prints the skill-calls fixture; it returns the argument log path.
func stubOnPath(t *testing.T, exit string) string {
	t.Helper()
	dir := t.TempDir()
	fx, _ := filepath.Abs(filepath.Join("..", "..", "testdata", "provider-config-inventory", "skill-calls.json"))
	log := filepath.Join(dir, "args.log")
	script := "#!/bin/sh\necho \"$@\" >> \"" + log + "\"\n" + exit + "cat \"" + fx + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "pg-ccaudit"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func tree(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "testdata", "provider-config-inventory", "tree"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func baseArgs(t *testing.T, extra ...string) []string {
	root := tree(t)
	a := []string{
		"--since", "2029-12-31T19:00:00-05:00", "--until", "2030-01-14T19:00:00-05:00", "--scope", "demo",
		"--scope-prefix", filepath.Join(root, "proj-a"), "--layer", "user", "--layer", "project",
		"--user-dir", filepath.Join(root, "home", ".claude"),
	}
	return append(a, extra...)
}

func TestRunSuccessIsSchemaValidAndPassesExcludeFlag(t *testing.T) {
	log := stubOnPath(t, "")
	var out, errb bytes.Buffer
	code := run(context.Background(), baseArgs(t, "--exclude-cwd-prefix", "/srv/neutral"), &out, &errb)
	if code != exitcode.OK {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if err := schema.Validate("evidence-rows", out.Bytes()); err != nil {
		t.Error(err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "--exclude-cwd-prefix /srv/neutral") || !strings.Contains(string(b), "--since 2030-01-01T00:00:00Z") {
		t.Errorf("pg-ccaudit args %q", b)
	}
}

func TestRunUsageAndFailureExits(t *testing.T) {
	stubOnPath(t, "")
	var out, errb bytes.Buffer
	for name, args := range map[string][]string{
		"missing flags": {"--scope", "demo"},
		"bad layer":     baseArgs(t, "--layer", "nope"),
		"unknown flag":  {"--bogus"},
		"stray arg":     append(baseArgs(t), "extra"),
	} {
		if c := run(context.Background(), args, &out, &errb); c != exitcode.Usage {
			t.Errorf("%s: exit %d, want %d", name, c, exitcode.Usage)
		}
	}
	stubOnPath(t, "exit 1\n")
	out.Reset()
	if c := run(context.Background(), baseArgs(t), &out, &errb); c != exitcode.Failure || out.Len() != 0 {
		t.Errorf("failing query: exit %d stdout %q, want %d and empty", c, out.String(), exitcode.Failure)
	}
}
