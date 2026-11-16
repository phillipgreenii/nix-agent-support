package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionAndUsage(t *testing.T) {
	var out, errb bytes.Buffer
	a := newApp(&out, &errb)
	if code := a.run([]string{"version"}); code != 0 || strings.TrimSpace(out.String()) != Version {
		t.Fatalf("version: %d %q", code, out.String())
	}
	if code := a.run(nil); code != 2 {
		t.Fatalf("no args exit %d", code)
	}
	if code := a.run([]string{"status"}); code != 2 {
		t.Fatalf("removed command exit %d", code)
	}
}

func TestServeRejectsNonLoopback(t *testing.T) {
	var out, errb bytes.Buffer
	if code := newApp(&out, &errb).serve([]string{"--addr", "0.0.0.0:0"}, nil); code != 2 {
		t.Fatalf("exit %d", code)
	}
}
