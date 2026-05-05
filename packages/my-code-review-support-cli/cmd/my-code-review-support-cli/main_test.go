package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	rootCmd.SetArgs([]string{"version"})
	if err := rootCmd.Execute(); err != nil {
		os.Stdout = old
		t.Fatalf("version command failed: %v", err)
	}

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	os.Stdout = old

	output := buf.String()
	if !strings.Contains(output, "my-code-review-support-cli") {
		t.Errorf("version output should contain 'my-code-review-support-cli', got: %s", output)
	}
}

func TestPostHelpContainsSchema(t *testing.T) {
	help := postCmd.Long
	checks := []struct {
		needle string
		desc   string
	}{
		{`"comments"`, "post help must document the comments JSON schema"},
		{`"severity"`, "post help must document severity field"},
		{"error", "post help must list error severity"},
		{"warning", "post help must list warning severity"},
		{"suggestion", "post help must list suggestion severity"},
		{"PR-level", "post help must document PR-level comment type"},
		{"File-level", "post help must document file-level comment type"},
		{"Line-level", "post help must document line-level comment type"},
		{"Range", "post help must document range comment type"},
	}
	for _, c := range checks {
		if !strings.Contains(help, c.needle) {
			t.Errorf("%s (missing %q)", c.desc, c.needle)
		}
	}
}

func TestSetupRequiresArg(t *testing.T) {
	rootCmd.SetArgs([]string{"setup"})
	err := rootCmd.Execute()
	if err == nil {
		t.Error("setup without args should fail")
	}
}

func TestCleanupRequiresArg(t *testing.T) {
	rootCmd.SetArgs([]string{"cleanup"})
	err := rootCmd.Execute()
	if err == nil {
		t.Error("cleanup without args should fail")
	}
}

func TestPrInfoRequiresArg(t *testing.T) {
	rootCmd.SetArgs([]string{"pr-info"})
	err := rootCmd.Execute()
	if err == nil {
		t.Error("pr-info without args should fail")
	}
}

func TestPostRequiresArg(t *testing.T) {
	rootCmd.SetArgs([]string{"post"})
	err := rootCmd.Execute()
	if err == nil {
		t.Error("post without args should fail")
	}
}
