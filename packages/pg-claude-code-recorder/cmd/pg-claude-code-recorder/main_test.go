package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"--version"}, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.HasPrefix(out.String(), "pg-claude-code-recorder ") {
		t.Fatalf("unexpected output %q", out.String())
	}
}

func TestRunUsage(t *testing.T) {
	var out bytes.Buffer
	if code := run(nil, &out); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}
