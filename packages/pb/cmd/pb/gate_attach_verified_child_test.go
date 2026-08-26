package main

import (
	"bytes"
	"testing"
)

func TestParseGateSpecs(t *testing.T) {
	specs, err := parseGateSpecs([]string{"repo-a=sha1", "repo-b=sha2"})
	if err != nil {
		t.Fatalf("parseGateSpecs: %v", err)
	}
	if len(specs) != 2 || specs[0].Repo != "repo-a" || specs[0].Commit != "sha1" ||
		specs[1].Repo != "repo-b" || specs[1].Commit != "sha2" {
		t.Errorf("specs = %+v", specs)
	}
	for _, bad := range []string{"", "repo-a", "=sha1", "repo-a="} {
		if _, err := parseGateSpecs([]string{bad}); err == nil {
			t.Errorf("parseGateSpecs(%q): expected error", bad)
		}
	}
}

func TestAttachVerifiedChildCmd_requiredFlags(t *testing.T) {
	cmd := newGateAttachVerifiedChildCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{}) // nothing supplied
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected required-flag error")
	}
}
