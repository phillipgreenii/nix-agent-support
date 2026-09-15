package main

import (
	"strings"
	"testing"
)

func TestRoot_HelpListsAllThreeVerbs(t *testing.T) {
	stdout, _, code := runCLI(t, "--help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, verb := range []string{"changes", "sweep", "list"} {
		if !strings.Contains(stdout, verb) {
			t.Fatalf("--help output missing verb %q:\n%s", verb, stdout)
		}
	}
}

func TestSubcommands_HelpWorksWithoutRequiredFlags(t *testing.T) {
	for _, verb := range []string{"changes", "sweep", "list"} {
		t.Run(verb, func(t *testing.T) {
			stdout, stderr, code := runCLI(t, verb, "--help")
			if code != 0 {
				t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
			}
			if !strings.Contains(stdout, verb) {
				t.Fatalf("--help output missing verb name %q:\n%s", verb, stdout)
			}
		})
	}
}

func TestRoot_VersionFlagWorks(t *testing.T) {
	stdout, stderr, code := runCLI(t, "--version")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stdout, Version) {
		t.Fatalf("--version output = %q, want to contain Version %q", stdout, Version)
	}
}
