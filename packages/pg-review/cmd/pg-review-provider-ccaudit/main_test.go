package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/schema"
	"github.com/phillipgreenii/x/exitcode"
)

func stubOnPath(t *testing.T, exit string) {
	t.Helper()
	dir := t.TempDir()
	fx, _ := filepath.Abs(filepath.Join("..", "..", "testdata", "provider-ccaudit"))
	script := "#!/bin/sh\n" + exit + "cat \"" + fx + "/$2.json\"\n"
	if err := os.WriteFile(filepath.Join(dir, "pg-ccaudit"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRunSuccessIsSchemaValid(t *testing.T) {
	stubOnPath(t, "")
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"--since", "2026-01-01T19:00:00-05:00", "--until", "2026-01-02T19:00:00-05:00", "--scope", "demo", "--exclude-cwd-prefix", "/srv/neutral"}, &out, &errb)
	if code != exitcode.OK {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if err := schema.Validate("evidence-rows", out.Bytes()); err != nil {
		t.Error(err)
	}
}

func TestRunUsageAndFailureExits(t *testing.T) {
	stubOnPath(t, "")
	var out, errb bytes.Buffer
	if c := run(context.Background(), []string{"--scope", "demo"}, &out, &errb); c != exitcode.Usage {
		t.Errorf("missing bounds: exit %d, want %d", c, exitcode.Usage)
	}
	if c := run(context.Background(), []string{"--since", "2026-01-01", "--until", "2026-01-02T00:00:00Z", "--scope", "demo"}, &out, &errb); c != exitcode.Usage {
		t.Errorf("offset-less bound: exit %d, want %d", c, exitcode.Usage)
	}
	stubOnPath(t, "exit 1\n")
	out.Reset()
	if c := run(context.Background(), []string{"--since", "2026-01-01T00:00:00Z", "--until", "2026-01-02T00:00:00Z", "--scope", "demo"}, &out, &errb); c != exitcode.Error || out.Len() != 0 {
		t.Errorf("failing query: exit %d stdout %q, want %d and empty", c, out.String(), exitcode.Error)
	}
}
