package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--version"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), "pg-git-lander ") {
		t.Fatalf("unexpected output %q", out.String())
	}
}

func TestRunNoArgsIsUsageError(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}
