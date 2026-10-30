package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func testApp(env map[string]string) (*app, *bytes.Buffer, *bytes.Buffer) {
	var out, errw bytes.Buffer
	a := &app{
		out: &out, errw: &errw, stdin: strings.NewReader(""),
		getenv:      func(k string) string { return env[k] },
		launchState: func(string) string { return "loaded, not running" },
		ctx:         context.Background(),
	}
	return a, &out, &errw
}

func TestRunVersion(t *testing.T) {
	a, out, _ := testApp(nil)
	if code := a.run([]string{"--version"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.HasPrefix(out.String(), "pg-claude-code-recorder ") {
		t.Fatalf("unexpected output %q", out.String())
	}
}

func TestRunUsageIsExit2(t *testing.T) {
	a, _, errw := testApp(nil)
	if code := a.run(nil); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errw.String(), "usage:") {
		t.Fatalf("stderr %q", errw.String())
	}
}

func TestUnknownSubcommandsAndMissingArgumentsAreUsageErrors(t *testing.T) {
	for _, sub := range []string{"remove", "where", "extract", "bogus"} {
		a, _, errw := testApp(nil)
		if code := a.run([]string{sub}); code != 2 {
			t.Errorf("%s: exit %d", sub, code)
		}
		if !strings.HasPrefix(errw.String(), "error: usage\n") {
			t.Errorf("%s: stderr %q", sub, errw.String())
		}
	}
}

func TestHelpCarriesExamplesExitTableAndStageTable(t *testing.T) {
	a, out, _ := testApp(nil)
	if code := a.run([]string{"--help"}); code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{"Examples:", "status --short", "9  doctor found a mismatch", "removal_pending", "State machine:", "--client NAME"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help lacks %q", want)
		}
	}
}
