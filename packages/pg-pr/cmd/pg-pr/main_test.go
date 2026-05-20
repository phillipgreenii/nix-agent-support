package main

import (
	"bytes"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"version"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got := stdout.String()
	want := "pg-pr dev\n"
	if got != want {
		t.Fatalf("version output: got %q, want %q", got, want)
	}
}
