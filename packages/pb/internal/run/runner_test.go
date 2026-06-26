package run

import (
	"context"
	"testing"
)

func TestCLIRunner_capturesStdoutAndExit(t *testing.T) {
	r := CLIRunner{}
	res, err := r.Run(context.Background(), "sh", []string{"-c", "printf hi; exit 0"}, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stdout != "hi" {
		t.Errorf("Stdout = %q, want %q", res.Stdout, "hi")
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
}

func TestCLIRunner_nonZeroExitReturnsError(t *testing.T) {
	r := CLIRunner{}
	res, err := r.Run(context.Background(), "sh", []string{"-c", "echo boom 1>&2; exit 3"}, Options{})
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
	if res.Stderr == "" {
		t.Error("expected stderr captured")
	}
}

func TestCLIRunner_stdinPiped(t *testing.T) {
	r := CLIRunner{}
	res, err := r.Run(context.Background(), "cat", nil, Options{Stdin: "piped"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stdout != "piped" {
		t.Errorf("Stdout = %q, want piped", res.Stdout)
	}
}

func TestFakeRunner_scriptedAndRecords(t *testing.T) {
	f := NewFakeRunner()
	f.AddResponse("bd", []string{"gate", "list"}, Result{Stdout: `{"data":[]}`}, nil)
	res, err := f.Run(context.Background(), "bd", []string{"gate", "list"}, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stdout != `{"data":[]}` {
		t.Errorf("Stdout = %q", res.Stdout)
	}
	calls := f.Calls()
	if len(calls) != 1 || calls[0].Name != "bd" {
		t.Errorf("Calls = %+v", calls)
	}
}

func TestFakeRunner_unscriptedErrors(t *testing.T) {
	f := NewFakeRunner()
	if _, err := f.Run(context.Background(), "bd", []string{"nope"}, Options{}); err == nil {
		t.Fatal("expected error for unscripted call")
	}
}
