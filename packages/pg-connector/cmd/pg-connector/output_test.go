package main

import (
	"strings"
	"testing"
)

func TestOutputModeFor_DefaultIsJSON(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"pr", "show", "x"})
	// Parsing flags without executing RunE is enough to populate the
	// persistent --output flag's default.
	if err := root.ParseFlags([]string{}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	mode, err := outputModeFor(root)
	if err != nil {
		t.Fatalf("outputModeFor: %v", err)
	}
	if mode != OutputJSON {
		t.Fatalf("mode = %q, want %q", mode, OutputJSON)
	}
}

func TestOutputModeFor_ExplicitHuman(t *testing.T) {
	root := newRootCmd()
	if err := root.ParseFlags([]string{"--output", "human"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	mode, err := outputModeFor(root)
	if err != nil {
		t.Fatalf("outputModeFor: %v", err)
	}
	if mode != OutputHuman {
		t.Fatalf("mode = %q, want %q", mode, OutputHuman)
	}
}

func TestOutputModeFor_InvalidValueIsError(t *testing.T) {
	root := newRootCmd()
	if err := root.ParseFlags([]string{"--output", "yaml"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if _, err := outputModeFor(root); err == nil {
		t.Fatal("expected an error for an unrecognized --output value")
	}
}

func TestRun_InvalidOutputFlag_IsGenericFailure(t *testing.T) {
	// An unrecognized --output value is caught before ever dispatching to
	// a backend, so no config/backend is needed; it is the generic exit-1
	// CLI failure path, matching pr.go's own --disposition validation
	// convention.
	writeConfigFor(t, "backend-unused")

	_, code := executePr(t, []string{"--output", "yaml", "pr", "show", "pr-1"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

func TestFormatSourcesTable_Empty(t *testing.T) {
	got := formatSourcesTable(nil)
	if !strings.Contains(got, "no backends registered") {
		t.Fatalf("formatSourcesTable(nil) = %q", got)
	}
}

func TestFormatSourcesTable_RendersEveryRow(t *testing.T) {
	got := formatSourcesTable([]SourceResult{
		{Source: "backend-a", Status: SourceSucceeded, Count: 3},
		{Source: "backend-b", Status: SourceDegraded, Reason: "bad token"},
	})
	if !strings.Contains(got, "backend-a: succeeded count=3") {
		t.Fatalf("formatSourcesTable = %q, missing succeeded row", got)
	}
	if !strings.Contains(got, "backend-b: degraded (bad token)") {
		t.Fatalf("formatSourcesTable = %q, missing degraded row with reason", got)
	}
}
